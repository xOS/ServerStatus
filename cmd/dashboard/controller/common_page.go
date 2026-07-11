package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/hashicorp/go-uuid"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sync/singleflight"

	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/mygin"
	"github.com/xos/serverstatus/pkg/utils"
	"github.com/xos/serverstatus/pkg/websocketx"
	"github.com/xos/serverstatus/service/singleton"
)

var (
	// 添加字节格式化缓存，减少重复计算
	byteFmtCache = make(map[uint64]string)
	byteFmtMutex sync.RWMutex
	// bytes.Buffer 池，用于减少 JSON 序列化时的内存分配
	bytesPool = sync.Pool{New: func() interface{} { return &bytes.Buffer{} }}
)

// cachedByteSize 带缓存的字节格式化函数
func cachedByteSize(bytes uint64) string {
	// 对于0值直接返回，避免缓存开销
	if bytes == 0 {
		return "0 B"
	}

	// 读锁检查缓存
	byteFmtMutex.RLock()
	if cached, exists := byteFmtCache[bytes]; exists {
		byteFmtMutex.RUnlock()
		return cached
	}
	byteFmtMutex.RUnlock()

	// 计算格式化结果
	result := formatByteSize(bytes)

	// 写锁更新缓存（限制缓存大小避免内存泄漏）
	byteFmtMutex.Lock()
	// 双重检查：在获取写锁后再次检查缓存，避免重复计算
	if cached, exists := byteFmtCache[bytes]; exists {
		byteFmtMutex.Unlock()
		return cached
	}
	if len(byteFmtCache) < 10000 { // 限制缓存条目数量
		byteFmtCache[bytes] = result
	}
	byteFmtMutex.Unlock()

	return result
}

func formatByteSize(size uint64) string {
	if size == 0 {
		return "0 B"
	}
	units := [...]string{"B", "K", "M", "G", "T", "P", "E"}
	value := float64(size)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	formatted := strconv.FormatFloat(value, 'f', 1, 64)
	return strings.TrimSuffix(formatted, ".0") + units[unit]
}

func monthlyTransferForServer(server *model.Server, currentMonthStart time.Time) uint64 {
	if server == nil || server.LastActive.IsZero() || !server.LastActive.After(currentMonthStart) {
		return 0
	}
	return model.TotalTransfer(server.CumulativeNetInTransfer, server.CumulativeNetOutTransfer)
}

type commonPage struct {
	r            *gin.Engine
	requestGroup singleflight.Group
}

func (cp *commonPage) serve() {
	cr := cp.r.Group("")
	cr.Use(mygin.Authorize(mygin.AuthorizeOption{}))
	cr.POST("/view-password", cp.issueViewPassword)
	cr.Use(mygin.ValidateViewPassword(mygin.ValidateViewPasswordOption{
		IsPage:        true,
		AbortWhenFail: true,
	}))
	cr.GET("/", cp.home)
	cr.GET("/service", cp.service)
	// TODO: 界面直接跳转使用该接口
	cr.GET("/network/:id", cp.network)
	cr.GET("/network", cp.network)
	cr.GET(apiV1Prefix+"/ws", cp.ws)

	// 新增：流量数据 API，未登录用户也可访问
	cr.GET(apiV1Prefix+"/traffic", cp.apiTraffic)
	// 新增：单个服务器流量数据 API
	cr.GET(apiV1Prefix+"/server/:id/traffic", cp.apiServerTraffic)

}

type viewPasswordForm struct {
	Password string
}

func (p *commonPage) issueViewPassword(c *gin.Context) {
	var vpf viewPasswordForm
	err := c.ShouldBind(&vpf)
	var hash []byte
	if err == nil && vpf.Password != singleton.Conf.Site.ViewPassword {
		err = errors.New("访问密码错误")
	}
	if err == nil {
		hash, err = bcrypt.GenerateFromPassword([]byte(vpf.Password), bcrypt.DefaultCost)
	}
	if err != nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusOK,
			Title: "发生错误",
			Msg:   err.Error(),
		}, true)
		c.Abort()
		return
	}
	setSecureCookie(c, singleton.Conf.Site.CookieName+"-vp", string(hash), 60*60*24)
	c.Redirect(http.StatusFound, c.Request.Referer())
}

func (p *commonPage) service(c *gin.Context) {
	c.Redirect(http.StatusFound, "/")
}

func (cp *commonPage) network(c *gin.Context) {
	serveSPA(c)
}

type Data struct {
	Now         int64           `json:"now,omitempty"`
	Servers     []*model.Server `json:"servers,omitempty"`
	TrafficData interface{}     `json:"trafficData,omitempty"`
}

func cloneServersForFrontend(serverList []*model.Server, withPublicNote bool) []*model.Server {
	servers := make([]*model.Server, 0, len(serverList))
	for _, server := range serverList {
		if item := cloneServerForFrontend(server, withPublicNote); item != nil {
			servers = append(servers, item)
		}
	}
	return servers
}

func cloneServerForFrontend(server *model.Server, withPublicNote bool) *model.Server {
	if server == nil {
		return nil
	}

	item := *server
	if !withPublicNote {
		item.PublicNote = ""
	}

	if server.Host != nil {
		host := *server.Host
		host.CPU = append([]string(nil), server.Host.CPU...)
		host.GPU = append([]string(nil), server.Host.GPU...)
		item.Host = &host
	}

	if server.State != nil {
		state := *server.State
		state.Temperatures = append([]model.SensorTemperature(nil), server.State.Temperatures...)
		item.State = &state
	}

	return &item
}

func (cp *commonPage) getServerStat(c *gin.Context, withPublicNote bool) ([]byte, error) {
	_, isMember := c.Get(model.CtxKeyAuthorizedUser)
	_, isViewPasswordVerfied := c.Get(model.CtxKeyViewPasswordVerified)
	authorized := isMember || isViewPasswordVerfied

	// 优化：使用带超时的singleflight来避免长时间阻塞
	start := time.Now()

	v, err, _ := cp.requestGroup.Do(fmt.Sprintf("serverStats::%t", authorized), func() (interface{}, error) {
		// 使用defer确保锁的正确释放
		singleton.SortedServerLock.RLock()
		defer singleton.SortedServerLock.RUnlock()

		var serverList []*model.Server
		if authorized {
			serverList = singleton.SortedServerList
		} else {
			serverList = singleton.SortedServerListForGuest
		}

		// 仅在极度调试模式下输出日志
		if singleton.Conf.Debug && len(serverList) == 0 {
			log.Printf("getServerStat: 警告 - 服务器列表为空, 授权状态: %v", authorized)
		}

		// 修复：安全地检查服务器列表
		if len(serverList) == 0 {
			if singleton.Conf.Debug {
				log.Printf("getServerStat: 服务器列表为空，尝试从 ServerList 提取数据")
			}

			// 安全地从 ServerList 中提取服务器
			func() {
				singleton.ServerLock.RLock()
				defer singleton.ServerLock.RUnlock()

				// 检查ServerList是否初始化
				if singleton.ServerList != nil {
					for _, server := range singleton.ServerList {
						// 安全检查：确保server不为nil
						if server == nil {
							continue
						}

						// 为所有用户展示所有服务器，或者仅对授权用户显示
						if authorized || !server.HideForGuest {
							if safeServer := cloneServerForFrontend(server, withPublicNote); safeServer != nil {
								serverList = append(serverList, safeServer)
							}
						}
					}
				} else {
					if singleton.Conf.Debug {
						log.Printf("getServerStat: ServerList未初始化")
					}
				}
			}()

			if singleton.Conf.Debug {
				log.Printf("getServerStat: 从 ServerList 提取到 %d 台服务器", len(serverList))
			}

		}
		servers := cloneServersForFrontend(serverList, withPublicNote)

		// 组装 trafficData，逻辑与 home handler 保持一致
		// 使用深拷贝确保并发安全
		var trafficData []map[string]interface{}

		// 紧急修复：使用更安全的深拷贝，防止concurrent map iteration and map write
		singleton.AlertsLock.RLock()
		var statsStore map[uint64]model.CycleTransferStats
		if singleton.AlertsCycleTransferStatsStore != nil {
			statsStore = make(map[uint64]model.CycleTransferStats)

			// 先获取所有cycleID，避免在遍历过程中map被修改
			var cycleIDs []uint64
			for cycleID := range singleton.AlertsCycleTransferStatsStore {
				cycleIDs = append(cycleIDs, cycleID)
			}

			// 然后安全地复制每个条目
			for _, cycleID := range cycleIDs {
				if stats, exists := singleton.AlertsCycleTransferStatsStore[cycleID]; exists {
					// 深拷贝每个CycleTransferStats
					newStats := model.CycleTransferStats{
						Name: stats.Name,
						Max:  stats.Max,
					}

					// 深拷贝Transfer map
					if stats.Transfer != nil {
						newStats.Transfer = make(map[uint64]uint64)
						// 先获取所有serverID，避免在遍历过程中map被修改
						var serverIDs []uint64
						for serverID := range stats.Transfer {
							serverIDs = append(serverIDs, serverID)
						}
						for _, serverID := range serverIDs {
							if transfer, exists := stats.Transfer[serverID]; exists {
								newStats.Transfer[serverID] = transfer
							}
						}
					}

					// 深拷贝ServerName map
					if stats.ServerName != nil {
						newStats.ServerName = make(map[uint64]string)
						// 先获取所有serverID，避免在遍历过程中map被修改
						var serverIDs []uint64
						for serverID := range stats.ServerName {
							serverIDs = append(serverIDs, serverID)
						}
						for _, serverID := range serverIDs {
							if name, exists := stats.ServerName[serverID]; exists {
								newStats.ServerName[serverID] = name
							}
						}
					}

					statsStore[cycleID] = newStats
				}
			}
		}
		singleton.AlertsLock.RUnlock()

		// 现在可以安全地访问深拷贝的数据
		for cycleID, stats := range statsStore {
			if stats.Transfer != nil {
				for serverID, transfer := range stats.Transfer {
					serverName := ""
					if stats.ServerName != nil {
						if name, exists := stats.ServerName[serverID]; exists {
							serverName = name
						}
					}

					usedPercent := model.TrafficUsagePercent(transfer, stats.Max)

					trafficItem := map[string]interface{}{
						"server_id":       serverID,
						"server_name":     serverName,
						"max_bytes":       stats.Max,
						"used_bytes":      transfer,
						"max_formatted":   cachedByteSize(stats.Max),
						"used_formatted":  cachedByteSize(transfer),
						"used_percent":    usedPercent,
						"cycle_name":      stats.Name,
						"cycle_id":        strconv.FormatUint(cycleID, 10),
						"is_bytes_source": true, // 标识这是字节数据源
					}
					trafficData = append(trafficData, trafficItem)
				}
			}
		}

		// 补充机制：为没有被警报规则覆盖的服务器创建默认流量数据（10TB月配额）
		// 获取所有已被警报规则覆盖的服务器ID
		coveredServerIDs := make(map[uint64]bool)
		if statsStore != nil {
			// 使用锁保护，避免并发读写
			singleton.AlertsLock.RLock()
			for _, stats := range statsStore {
				if stats.Transfer != nil {
					for serverID := range stats.Transfer {
						coveredServerIDs[serverID] = true
					}
				}
			}
			singleton.AlertsLock.RUnlock()
		}

		// 为未覆盖的在线服务器创建默认配额
		singleton.ServerLock.RLock()
		for serverID, server := range singleton.ServerList {
			if server == nil || !server.IsOnline {
				continue
			}

			// 如果服务器已被警报规则覆盖，跳过
			if coveredServerIDs[serverID] {
				continue
			}

			// 检查当前用户是否有权限查看此服务器
			serverAuthorized := authorized
			if !authorized {
				// 对于未授权用户，检查服务器是否在guest列表中
				for _, guestServer := range serverList {
					if guestServer.ID == serverID {
						serverAuthorized = true
						break
					}
				}
			}

			if !serverAuthorized {
				continue
			}

			// 创建默认的月流量配额（10TB = 10 * 1024^4 bytes）
			defaultQuota := uint64(10 * 1024 * 1024 * 1024 * 1024) // 10TB

			// 计算当前月的开始和结束时间（每月1号开始）
			now := time.Now()
			currentMonthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
			nextMonthStart := currentMonthStart.AddDate(0, 1, 0)

			// 计算当月累积流量（模拟月度重置）
			monthlyTransfer := monthlyTransferForServer(server, currentMonthStart)

			// 计算使用百分比
			usedPercent := model.TrafficUsagePercent(monthlyTransfer, defaultQuota)

			// 构建默认流量数据项，显示月度配额
			trafficItem := map[string]interface{}{
				"server_id":       serverID,
				"server_name":     server.Name,
				"max_bytes":       defaultQuota,
				"used_bytes":      monthlyTransfer,
				"max_formatted":   cachedByteSize(defaultQuota),
				"used_formatted":  cachedByteSize(monthlyTransfer),
				"used_percent":    usedPercent,
				"cycle_name":      "默认月流量配额",
				"cycle_id":        "default-monthly",
				"cycle_start":     currentMonthStart.Format(time.RFC3339),
				"cycle_end":       nextMonthStart.Format(time.RFC3339),
				"cycle_unit":      "month",
				"cycle_interval":  1,
				"is_bytes_source": true,
			}
			trafficData = append(trafficData, trafficItem)
		}
		singleton.ServerLock.RUnlock()

		// 确保返回正确的数据结构，即使没有服务器数据
		if servers == nil {
			servers = make([]*model.Server, 0)
		}

		data := Data{
			Now:         time.Now().Unix() * 1000,
			Servers:     servers,
			TrafficData: trafficData,
		}

		// 仅在出现异常时输出日志
		if len(servers) == 0 && singleton.Conf.Debug {
			log.Printf("getServerStat: 警告 - 返回空服务器列表")
		}

		// 使用缓冲池减少内存分配
		buf := bytesPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bytesPool.Put(buf)

		// 优化：使用更高效的JSON编码器配置
		encoder := utils.Json.NewEncoder(buf)
		encoder.SetEscapeHTML(false) // 禁用HTML转义，减少处理开销

		if err := encoder.Encode(data); err != nil {
			return nil, err
		}

		// 优化：直接返回buffer字节，避免额外的内存拷贝
		// 由于使用了对象池，这个操作是安全的
		result := make([]byte, buf.Len())
		copy(result, buf.Bytes())
		return result, nil
	})

	if err != nil && singleton.Conf.Debug {
		log.Printf("getServerStat: 数据序列化错误: %v", err)
	}

	// 监控singleflight性能
	elapsed := time.Since(start)
	if elapsed > 3*time.Second && singleton.Conf.Debug {
		log.Printf("getServerStat: singleflight处理时间过长: %v", elapsed)
	}

	return v.([]byte), err
}

func (cp *commonPage) home(c *gin.Context) {
	serveSPA(c)
}

func (cp *commonPage) ws(c *gin.Context) {
	upgrader := websocket.Upgrader{
		CheckOrigin:      isAllowedWebSocketOrigin,
		ReadBufferSize:   8192,
		WriteBufferSize:  8192,
		HandshakeTimeout: 10 * time.Second,
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WebSocket升级失败: %v", err)
		return
	}

	// 生成唯一的连接ID用于日志跟踪
	connID, _ := uuid.GenerateUUID()
	log.Printf("WebSocket连接建立: %s", connID)

	// 使用Context来统一控制此连接所有Goroutine的生命周期
	ctx, cancel := context.WithCancel(c.Request.Context()) // #nosec G118 -- cancel is deferred below

	// defer确保在函数退出时，无论任何原因，都能调用cancel()来清理所有goroutine
	defer func() {
		cancel()
		conn.Close()
		log.Printf("WebSocket连接关闭: %s", connID)
	}()

	// 设置连接基本参数
	conn.SetReadLimit(32768) // 32KB读取限制
	// 设置Pong处理器，用于响应底层的Ping帧，保持连接活跃
	conn.SetPongHandler(func(string) error {
		// 收到Pong后，延长读超时
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	safeConn := websocketx.NewConn(conn)
	var wg sync.WaitGroup
	wg.Add(2) // 对应读、写两个goroutine

	// “读”goroutine：主要职责是监听客户端消息和检测连接断开
	go func() {
		defer wg.Done()
		// 当“读”goroutine退出时，它必须通知“写”goroutine也退出
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				// 增加堆栈信息，方便排查问题
				log.Printf("WebSocket读取goroutine panic恢复 %s: %v\n%s", connID, r, debug.Stack())
			}
		}()

		// 初始化读超时
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))

		for {
			// ReadMessage是一个阻塞调用，如果连接关闭、超时或发生其他错误，它会返回error
			msgType, message, err := safeConn.ReadMessage()
			if err != nil {
				// 正常关闭连接的错误不需要记录日志，以减少噪音
				if !websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure, websocket.CloseNormalClosure) {
					log.Printf("WebSocket读取错误 %s: %v", connID, err)
				}
				// 任何来自ReadMessage的错误都意味着连接已失效，必须退出
				return
			}

			// 处理客户端自定义的ping消息
			if msgType == websocket.TextMessage && string(message) == `{"type":"ping"}` {
				conn.SetReadDeadline(time.Now().Add(60 * time.Second))
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := safeConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"pong"}`)); err != nil {
					log.Printf("发送pong失败 %s: %v", connID, err)
					return // 写入失败也意味着连接失效
				}
			}
		}
	}()

	// “写”goroutine：主要职责是定时向客户端推送数据和发送心跳
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("WebSocket写入goroutine panic恢复 %s: %v\n%s", connID, r, debug.Stack())
			}
		}()

		dataTicker := time.NewTicker(1 * time.Second)
		defer dataTicker.Stop()

		// 心跳必须比读超时（60秒）短
		pingTicker := time.NewTicker(25 * time.Second)
		defer pingTicker.Stop()

		if stat, err := cp.getServerStat(c, false); err == nil {
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err = safeConn.WriteMessage(websocket.TextMessage, stat); err != nil {
				log.Printf("发送首帧数据失败 %s: %v", connID, err)
				return
			}
		} else {
			log.Printf("获取首帧服务器状态失败 %s: %v", connID, err)
		}

		for {
			select {
			// 这是最关键的一步：如果context被取消，说明“读”goroutine已退出，
			// “写”goroutine必须立即停止，以防止泄漏。
			case <-ctx.Done():
				return

			case <-dataTicker.C:
				stat, err := cp.getServerStat(c, false)
				if err != nil {
					log.Printf("获取服务器状态失败 %s: %v", connID, err)
					continue // 获取数据失败不应导致连接断开，记录日志并等待下一次
				}

				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err = safeConn.WriteMessage(websocket.TextMessage, stat); err != nil {
					log.Printf("发送数据失败 %s: %v", connID, err)
					// 写入失败意味着连接已失效，也需要退出
					return
				}

			case <-pingTicker.C:
				// 发送标准的WebSocket Ping帧，由另一端的Pong处理器响应
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					log.Printf("发送ping失败 %s: %v", connID, err)
					return
				}
			}
		}
	}()

	// 等待两个goroutine都执行完毕
	wg.Wait()
}

// apiTraffic 返回和首页相同结构的流量数据。
func (cp *commonPage) apiTraffic(c *gin.Context) {
	// 支持用户登录或view password验证
	_, isMember := c.Get(model.CtxKeyAuthorizedUser)
	_, isViewPasswordVerified := c.Get(model.CtxKeyViewPasswordVerified)

	if !isMember && !isViewPasswordVerified {
		WriteJSON(c, http.StatusForbidden, gin.H{
			"code":    403,
			"message": "请先登录或输入访问密码",
		})
		return
	}

	// 使用深拷贝确保并发安全
	var statsStore map[uint64]model.CycleTransferStats
	singleton.AlertsLock.RLock()
	if singleton.AlertsCycleTransferStatsStore != nil {
		statsStore = make(map[uint64]model.CycleTransferStats)
		for cycleID, stats := range singleton.AlertsCycleTransferStatsStore {
			// 深拷贝每个CycleTransferStats
			newStats := model.CycleTransferStats{
				Name: stats.Name,
				Max:  stats.Max,
			}

			// 深拷贝Transfer map
			if stats.Transfer != nil {
				newStats.Transfer = make(map[uint64]uint64)
				for serverID, transfer := range stats.Transfer {
					newStats.Transfer[serverID] = transfer
				}
			}

			// 深拷贝ServerName map
			if stats.ServerName != nil {
				newStats.ServerName = make(map[uint64]string)
				for serverID, name := range stats.ServerName {
					newStats.ServerName[serverID] = name
				}
			}

			statsStore[cycleID] = newStats
		}
	}
	singleton.AlertsLock.RUnlock()

	var trafficData []map[string]interface{}
	for cycleID, stats := range statsStore {
		if stats.Transfer != nil {
			for serverID, transfer := range stats.Transfer {
				serverName := ""
				if stats.ServerName != nil {
					if name, exists := stats.ServerName[serverID]; exists {
						serverName = name
					}
				}
				usedPercent := model.TrafficUsagePercent(transfer, stats.Max)
				trafficItem := map[string]interface{}{
					"server_id":       serverID,
					"server_name":     serverName,
					"max_bytes":       stats.Max,
					"used_bytes":      transfer,
					"max_formatted":   cachedByteSize(stats.Max),
					"used_formatted":  cachedByteSize(transfer),
					"used_percent":    usedPercent,
					"cycle_name":      stats.Name,
					"cycle_id":        strconv.FormatUint(cycleID, 10),
					"is_bytes_source": true, // 标识这是字节数据源
				}
				trafficData = append(trafficData, trafficItem)
			}
		}
	}

	// 回退机制：为没有警报规则的服务器创建默认流量数据（10TB月配额）
	if len(trafficData) == 0 {
		// 根据权限获取服务器列表
		var serverList []*model.Server
		if isMember || isViewPasswordVerified {
			serverList = singleton.SortedServerList
		} else {
			serverList = singleton.SortedServerListForGuest
		}

		singleton.ServerLock.RLock()
		for _, server := range serverList {
			if server == nil || !server.IsOnline {
				continue
			}

			serverID := server.ID
			// 检查服务器是否在ServerList中存在
			if actualServer := singleton.ServerList[serverID]; actualServer != nil {
				// 创建默认的月流量配额（10TB = 10 * 1024^4 bytes）
				defaultQuota := uint64(10 * 1024 * 1024 * 1024 * 1024) // 10TB

				// 计算当前月的开始和结束时间（每月1号开始）
				now := time.Now()
				currentMonthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
				nextMonthStart := currentMonthStart.AddDate(0, 1, 0)

				// 计算当月累积流量（模拟月度重置）
				monthlyTransfer := monthlyTransferForServer(actualServer, currentMonthStart)

				// 计算使用百分比
				usedPercent := model.TrafficUsagePercent(monthlyTransfer, defaultQuota)

				// 构建默认流量数据项，显示月度配额
				trafficItem := map[string]interface{}{
					"server_id":       serverID,
					"server_name":     actualServer.Name,
					"max_bytes":       defaultQuota,
					"used_bytes":      monthlyTransfer,
					"max_formatted":   cachedByteSize(defaultQuota),
					"used_formatted":  cachedByteSize(monthlyTransfer),
					"used_percent":    usedPercent,
					"cycle_name":      "默认月流量配额",
					"cycle_id":        "default-monthly",
					"cycle_start":     currentMonthStart.Format(time.RFC3339),
					"cycle_end":       nextMonthStart.Format(time.RFC3339),
					"cycle_unit":      "month",
					"cycle_interval":  1,
					"is_bytes_source": true,
				}
				trafficData = append(trafficData, trafficItem)
			}
		}
		singleton.ServerLock.RUnlock()
	}

	WriteJSON(c, http.StatusOK, gin.H{
		"code": 200,
		"data": trafficData,
	})
}

// apiServerTraffic 返回单个服务器的流量数据。
func (cp *commonPage) apiServerTraffic(c *gin.Context) {
	// 支持用户登录或view password验证
	_, isMember := c.Get(model.CtxKeyAuthorizedUser)
	_, isViewPasswordVerified := c.Get(model.CtxKeyViewPasswordVerified)

	if !isMember && !isViewPasswordVerified {
		WriteJSON(c, http.StatusForbidden, gin.H{
			"code":    403,
			"message": "请先登录或输入访问密码",
		})
		return
	}

	// 获取服务器ID
	serverID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		WriteJSON(c, http.StatusBadRequest, gin.H{
			"code":    400,
			"message": "无效的服务器ID",
		})
		return
	}

	// 检查服务器是否存在
	singleton.ServerLock.RLock()
	server := singleton.ServerList[serverID]
	singleton.ServerLock.RUnlock()
	if server == nil {
		WriteJSON(c, http.StatusNotFound, gin.H{
			"code":    404,
			"message": "服务器不存在",
		})
		return
	}

	// 使用深拷贝确保并发安全
	var statsStore map[uint64]model.CycleTransferStats
	singleton.AlertsLock.RLock()
	if singleton.AlertsCycleTransferStatsStore != nil {
		statsStore = make(map[uint64]model.CycleTransferStats)
		for cycleID, stats := range singleton.AlertsCycleTransferStatsStore {
			// 深拷贝每个CycleTransferStats
			newStats := model.CycleTransferStats{
				Name: stats.Name,
				Max:  stats.Max,
			}

			// 深拷贝Transfer map
			if stats.Transfer != nil {
				newStats.Transfer = make(map[uint64]uint64)
				for sID, transfer := range stats.Transfer {
					newStats.Transfer[sID] = transfer
				}
			}

			// 深拷贝ServerName map
			if stats.ServerName != nil {
				newStats.ServerName = make(map[uint64]string)
				for sID, name := range stats.ServerName {
					newStats.ServerName[sID] = name
				}
			}

			statsStore[cycleID] = newStats
		}
	}
	singleton.AlertsLock.RUnlock()

	var trafficData []map[string]interface{}
	for cycleID, stats := range statsStore {
		if stats.Transfer != nil {
			if transfer, exists := stats.Transfer[serverID]; exists {
				serverName := ""
				if stats.ServerName != nil {
					if name, exists := stats.ServerName[serverID]; exists {
						serverName = name
					}
				}
				usedPercent := model.TrafficUsagePercent(transfer, stats.Max)
				trafficItem := map[string]interface{}{
					"server_id":       serverID,
					"server_name":     serverName,
					"max_bytes":       stats.Max,
					"used_bytes":      transfer,
					"max_formatted":   cachedByteSize(stats.Max),
					"used_formatted":  cachedByteSize(transfer),
					"used_percent":    usedPercent,
					"cycle_name":      stats.Name,
					"cycle_id":        strconv.FormatUint(cycleID, 10),
					"is_bytes_source": true, // 标识这是字节数据源
				}
				trafficData = append(trafficData, trafficItem)
			}
		}
	}

	// 回退机制：为没有警报规则的服务器创建默认流量数据（10TB月配额）
	if len(trafficData) == 0 && server != nil {
		// 创建默认的月流量配额（10TB = 10 * 1024^4 bytes）
		defaultQuota := uint64(10 * 1024 * 1024 * 1024 * 1024) // 10TB

		// 计算当前月的开始和结束时间（每月1号开始）
		now := time.Now()
		currentMonthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		nextMonthStart := currentMonthStart.AddDate(0, 1, 0)

		// 计算当月累积流量（模拟月度重置）
		monthlyTransfer := monthlyTransferForServer(server, currentMonthStart)

		// 计算使用百分比
		usedPercent := model.TrafficUsagePercent(monthlyTransfer, defaultQuota)

		// 构建默认流量数据项，显示月度配额
		trafficItem := map[string]interface{}{
			"server_id":       serverID,
			"server_name":     server.Name,
			"max_bytes":       defaultQuota,
			"used_bytes":      monthlyTransfer,
			"max_formatted":   cachedByteSize(defaultQuota),
			"used_formatted":  cachedByteSize(monthlyTransfer),
			"used_percent":    usedPercent,
			"cycle_name":      "默认月流量配额",
			"cycle_id":        "default-monthly",
			"cycle_start":     currentMonthStart.Format(time.RFC3339),
			"cycle_end":       nextMonthStart.Format(time.RFC3339),
			"cycle_unit":      "month",
			"cycle_interval":  1,
			"is_bytes_source": true,
		}
		trafficData = append(trafficData, trafficItem)
	}

	WriteJSON(c, http.StatusOK, gin.H{
		"code": 200,
		"data": trafficData,
	})
}
