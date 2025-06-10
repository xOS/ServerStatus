package controller

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/bytefmt"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/hashicorp/go-uuid"
	"github.com/jinzhu/copier"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sync/singleflight"

	"github.com/xos/serverstatus/db"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/mygin"
	"github.com/xos/serverstatus/pkg/utils"
	"github.com/xos/serverstatus/pkg/websocketx"
	"github.com/xos/serverstatus/proto"
	"github.com/xos/serverstatus/service/rpc"
	"github.com/xos/serverstatus/service/singleton"
)

type commonPage struct {
	r            *gin.Engine
	requestGroup singleflight.Group
}

func (cp *commonPage) serve() {
	cr := cp.r.Group("")
	cr.Use(mygin.Authorize(mygin.AuthorizeOption{}))
	cr.Use(mygin.PreferredTheme)
	cr.POST("/view-password", cp.issueViewPassword)
	cr.GET("/terminal/:id", cp.terminal)
	cr.Use(mygin.ValidateViewPassword(mygin.ValidateViewPasswordOption{
		IsPage:        true,
		AbortWhenFail: true,
	}))
	cr.GET("/", cp.home)
	cr.GET("/service", cp.service)
	// TODO: 界面直接跳转使用该接口
	cr.GET("/network/:id", cp.network)
	cr.GET("/network", cp.network)
	cr.GET("/ws", cp.ws)
	cr.POST("/terminal", cp.createTerminal)
	cr.GET("/file", cp.createFM)
	cr.GET("/file/:id", cp.fm)

	// 新增：流量数据API，未登录用户也可访问
	cr.GET("/api/traffic", cp.apiTraffic)
	// 新增：单个服务器流量数据API
	cr.GET("/api/server/:id/traffic", cp.apiServerTraffic)
}

type viewPasswordForm struct {
	Password string
}

func (p *commonPage) issueViewPassword(c *gin.Context) {
	var vpf viewPasswordForm
	err := c.ShouldBind(&vpf)
	var hash []byte
	if err == nil && vpf.Password != singleton.Conf.Site.ViewPassword {
		err = errors.New(singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{MessageID: "WrongAccessPassword"}))
	}
	if err == nil {
		hash, err = bcrypt.GenerateFromPassword([]byte(vpf.Password), bcrypt.DefaultCost)
	}
	if err != nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code: http.StatusOK,
			Title: singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{
				MessageID: "AnErrorEccurred",
			}),
			Msg: err.Error(),
		}, true)
		c.Abort()
		return
	}
	c.SetCookie(singleton.Conf.Site.CookieName+"-vp", string(hash), 60*60*24, "", "", false, false)
	c.Redirect(http.StatusFound, c.Request.Referer())
}

func (p *commonPage) service(c *gin.Context) {
	res, _, _ := p.requestGroup.Do("servicePage", func() (interface{}, error) {
		// 使用深拷贝确保并发安全
		singleton.AlertsLock.RLock()
		var stats map[uint64]model.ServiceItemResponse
		var statsStore map[uint64]model.CycleTransferStats

		// 安全获取ServiceSentinel的统计数据
		originalStats := singleton.ServiceSentinelShared.LoadStats()
		if originalStats != nil {
			stats = make(map[uint64]model.ServiceItemResponse)
			for k, v := range originalStats {
				if v != nil {
					// 深拷贝每个ServiceItemResponse
					statsCopy := model.ServiceItemResponse{}
					if err := copier.Copy(&statsCopy, v); err == nil {
						stats[k] = statsCopy
					}
				}
			}
		}

		// 深拷贝AlertsCycleTransferStatsStore
		if singleton.AlertsCycleTransferStatsStore != nil {
			statsStore = make(map[uint64]model.CycleTransferStats)
			for cycleID, statData := range singleton.AlertsCycleTransferStatsStore {
				// 深拷贝每个CycleTransferStats
				newStats := model.CycleTransferStats{
					Name: statData.Name,
					Max:  statData.Max,
				}

				// 深拷贝Transfer map
				if statData.Transfer != nil {
					newStats.Transfer = make(map[uint64]uint64)
					for serverID, transfer := range statData.Transfer {
						newStats.Transfer[serverID] = transfer
					}
				}

				// 深拷贝ServerName map
				if statData.ServerName != nil {
					newStats.ServerName = make(map[uint64]string)
					for serverID, name := range statData.ServerName {
						newStats.ServerName[serverID] = name
					}
				}

				statsStore[cycleID] = newStats
			}
		}
		singleton.AlertsLock.RUnlock()
		for k, service := range stats {
			if !service.Monitor.EnableShowInService {
				delete(stats, k)
			}
		}
		return []interface {
		}{
			stats, statsStore,
		}, nil
	})
	c.HTML(http.StatusOK, mygin.GetPreferredTheme(c, "/service"), mygin.CommonEnvironment(c, gin.H{
		"Title":              singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{MessageID: "ServicesStatus"}),
		"Services":           res.([]interface{})[0],
		"CycleTransferStats": res.([]interface{})[1],
	}))
}

func (cp *commonPage) network(c *gin.Context) {
	if singleton.Conf.Debug {
		log.Printf("network: 进入网络页面处理函数")
	}

	var (
		monitorHistory       *model.MonitorHistory
		servers              []*model.Server
		serverIdsWithMonitor []uint64
		monitorInfos         = []byte("[]") // 默认为空数组
		id                   uint64
	)

	// 根据数据库类型选择不同的处理方式
	if singleton.Conf.DatabaseType == "badger" {
		if singleton.Conf.Debug {
			log.Printf("network: 使用BadgerDB模式，跳过GORM查询监控历史")
		}

		// 从监控配置中获取有监控任务的服务器ID
		if singleton.Conf.Debug {
			log.Printf("network: 从监控配置构建监控服务器ID列表")
		}

		// 安全初始化
		if servers == nil {
			servers = []*model.Server{}
		}
		if serverIdsWithMonitor == nil {
			serverIdsWithMonitor = []uint64{}
		}

		// 从监控配置中获取有监控任务的服务器ID
		if singleton.ServiceSentinelShared != nil {
			monitors := singleton.ServiceSentinelShared.Monitors()
			if singleton.Conf.Debug {
				log.Printf("network: 找到 %d 个监控配置", len(monitors))
			}

			for _, monitor := range monitors {
				if monitor != nil {
					if singleton.Conf.Debug {
						log.Printf("network: 处理监控配置 %d: %s", monitor.ID, monitor.Name)
					}
					// 检查哪些服务器被这个监控任务覆盖
					singleton.ServerLock.RLock()
					for serverID, server := range singleton.ServerList {
						if server != nil {
							var shouldInclude bool

							// 根据Cover字段和SkipServers字段判断是否应该包含此服务器
							if monitor.Cover == 0 {
								// Cover=0: 覆盖所有，仅特定服务器不请求
								// 如果服务器不在SkipServers中，则包含
								shouldInclude = monitor.SkipServers == nil || !monitor.SkipServers[serverID]
							} else {
								// Cover=1: 忽略所有，仅通过特定服务器请求
								// 如果服务器在SkipServers中，则包含
								shouldInclude = monitor.SkipServers != nil && monitor.SkipServers[serverID]
							}

							if shouldInclude {
								// 检查是否已经在列表中
								found := false
								for _, existingID := range serverIdsWithMonitor {
									if existingID == serverID {
										found = true
										break
									}
								}
								if !found {
									serverIdsWithMonitor = append(serverIdsWithMonitor, serverID)
									if singleton.Conf.Debug {
										log.Printf("network: 添加服务器 %d (%s) 到监控列表 (Cover=%d)", serverID, server.Name, monitor.Cover)
									}
								}
								// 如果还没有选定ID，或者服务器在线，则将此服务器ID设为当前ID
								if id == 0 || server.IsOnline {
									id = serverID
								}
							}
							// 移除频繁的"服务器被监控跳过"日志输出
						}
					}
					singleton.ServerLock.RUnlock()
				}
			}
		}

		if singleton.Conf.Debug {
			log.Printf("network: 从监控配置获取到 %d 个有监控任务的服务器", len(serverIdsWithMonitor))
		}

		// 如果仍然没有找到ID，并且有排序列表，则使用排序列表中的第一个ID
		singleton.SortedServerLock.RLock()
		if id == 0 && singleton.SortedServerList != nil && len(singleton.SortedServerList) > 0 {
			// 检查第一个元素是否为nil
			if singleton.SortedServerList[0] != nil {
				id = singleton.SortedServerList[0].ID
				if singleton.Conf.Debug {
					log.Printf("network: 使用SortedServerList中第一个服务器ID: %d", id)
				}
			} else {
				log.Printf("network: 警告 - SortedServerList第一个元素为nil")
			}
		}
		singleton.SortedServerLock.RUnlock()

		// 如果依然没有ID，创建一个默认值
		if id == 0 {
			id = 1
			if singleton.Conf.Debug {
				log.Printf("network: 未找到有效服务器ID，使用默认ID: %d", id)
			}
		}
	} else {
		// SQLite 模式，使用 GORM 查询
		if err := singleton.DB.Model(&model.MonitorHistory{}).Select("monitor_id, server_id").
			Where("monitor_id != 0 and server_id != 0").Limit(1).First(&monitorHistory).Error; err != nil {
			mygin.ShowErrorPage(c, mygin.ErrInfo{
				Code:  http.StatusForbidden,
				Title: "请求失败",
				Msg:   "请求参数有误：" + "server monitor history not found",
				Link:  "/",
				Btn:   "返回重试",
			}, true)
			return
		} else {
			if monitorHistory == nil || monitorHistory.ServerID == 0 {
				if len(singleton.SortedServerList) > 0 {
					id = singleton.SortedServerList[0].ID
				}
			} else {
				id = monitorHistory.ServerID
			}
		}

		// 使用GORM获取带有监控的服务器ID列表
		if err := singleton.DB.Model(&model.MonitorHistory{}).
			Select("distinct(server_id)").
			Where("server_id != 0").
			Find(&serverIdsWithMonitor).
			Error; err != nil {
			mygin.ShowErrorPage(c, mygin.ErrInfo{
				Code:  http.StatusForbidden,
				Title: "请求失败",
				Msg:   "请求参数有误：" + "no server with monitor histories",
				Link:  "/",
				Btn:   "返回重试",
			}, true)
			return
		}
	}

	// 检查URL参数中是否指定了服务器ID
	idStr := c.Param("id")
	if idStr != "" {
		var err error
		id, err = strconv.ParseUint(idStr, 10, 64)
		if err != nil {
			mygin.ShowErrorPage(c, mygin.ErrInfo{
				Code:  http.StatusForbidden,
				Title: "请求失败",
				Msg:   "请求参数有误：" + err.Error(),
				Link:  "/",
				Btn:   "返回重试",
			}, true)
			return
		}

		// 确保指定的服务器ID存在
		singleton.ServerLock.RLock()
		_, ok := singleton.ServerList[id]
		singleton.ServerLock.RUnlock()

		if !ok {
			mygin.ShowErrorPage(c, mygin.ErrInfo{
				Code:  http.StatusForbidden,
				Title: "请求失败",
				Msg:   "请求参数有误：" + "server id not found",
				Link:  "/",
				Btn:   "返回重试",
			}, true)
			return
		}
	}

	// 获取监控历史记录
	var monitorHistories interface{}
	if singleton.Conf.DatabaseType == "badger" {
		// BadgerDB 模式，查询监控历史记录
		if singleton.Conf.Debug {
			log.Printf("network: BadgerDB模式，查询监控历史记录")
		}

		// BadgerDB模式，使用BadgerDB查询监控历史记录
		if db.DB != nil && id > 0 {
			// 获取时间范围（最近30天）
			endTime := time.Now()
			startTime := endTime.AddDate(0, 0, -30)

			monitorOps := db.NewMonitorHistoryOps(db.DB)
			allHistories, err := monitorOps.GetAllMonitorHistoriesInRange(startTime, endTime)
			if err != nil {
				log.Printf("network: 从BadgerDB查询监控历史记录失败: %v", err)
				monitorHistories = []model.MonitorHistory{}
			} else {
				// 过滤出指定服务器的记录并转换为值数组
				var filteredHistories []model.MonitorHistory
				for _, h := range allHistories {
					if h != nil && h.ServerID == id {
						filteredHistories = append(filteredHistories, *h)
					}
				}
				monitorHistories = filteredHistories
				// 移除频繁的监控历史记录查询日志
			}
		} else {
			log.Printf("network: BadgerDB未初始化或无效服务器ID，使用空数组")
			monitorHistories = []model.MonitorHistory{}
		}

		// 序列化为JSON
		var err error
		monitorInfos, err = utils.Json.Marshal(monitorHistories)
		if err != nil {
			log.Printf("network: 监控历史记录序列化失败: %v", err)
			monitorInfos = []byte("[]")
		}
	} else {
		// SQLite 模式，使用 MonitorAPI
		if singleton.MonitorAPI != nil {
			monitorHistories = singleton.MonitorAPI.GetMonitorHistories(map[string]any{"server_id": id})
			var err error
			if monitorHistories != nil {
				monitorInfos, err = utils.Json.Marshal(monitorHistories)
				if err != nil {
					log.Printf("network: 监控历史记录序列化失败: %v", err)
					// 在序列化失败时使用空数组
					monitorInfos = []byte("[]")
				}
			} else {
				log.Printf("network: 监控历史记录为nil，使用空数组")
				monitorInfos = []byte("[]")
			}
		} else {
			log.Printf("network: MonitorAPI为nil，使用空数组")
			monitorInfos = []byte("[]")
		}
	}

	// 检查用户是否有权限访问
	_, isMember := c.Get(model.CtxKeyAuthorizedUser)
	_, isViewPasswordVerfied := c.Get(model.CtxKeyViewPasswordVerified)
	authorized := isMember || isViewPasswordVerfied

	// 根据权限过滤服务器列表
	singleton.SortedServerLock.RLock()
	defer singleton.SortedServerLock.RUnlock()

	// 安全检查，确保服务器切片已初始化
	if servers == nil {
		servers = make([]*model.Server, 0)
	}

	if authorized {
		// 有权限的用户可以访问所有服务器
		if singleton.SortedServerList != nil {
			for _, server := range singleton.SortedServerList {
				// 确保服务器不为nil
				if server != nil {
					// 如果serverIdsWithMonitor为空或包含当前服务器ID，则添加
					if len(serverIdsWithMonitor) == 0 {
						servers = append(servers, server)
					} else {
						for _, monitorServerID := range serverIdsWithMonitor {
							if server.ID == monitorServerID {
								servers = append(servers, server)
								break
							}
						}
					}
				}
			}
		} else if singleton.Conf.Debug {
			log.Printf("network: 警告: SortedServerList为nil")
		}
	} else {
		// 访客只能访问非隐藏服务器
		if singleton.SortedServerListForGuest != nil {
			for _, server := range singleton.SortedServerListForGuest {
				// 确保服务器不为nil
				if server != nil {
					// 如果serverIdsWithMonitor为空或包含当前服务器ID，则添加
					if len(serverIdsWithMonitor) == 0 {
						servers = append(servers, server)
					} else {
						for _, monitorServerID := range serverIdsWithMonitor {
							if server.ID == monitorServerID {
								servers = append(servers, server)
								break
							}
						}
					}
				}
			}
		} else if singleton.Conf.Debug {
			log.Printf("network: 警告: SortedServerListForGuest为nil")
		}
	}

	// 确保我们至少有一个服务器
	if len(servers) == 0 {
		if singleton.Conf.Debug {
			log.Printf("network: 未找到任何服务器，创建演示服务器")
		}
		demoServer := &model.Server{
			Common: model.Common{
				ID: 1,
			},
			Name:     "演示服务器",
			Host:     &model.Host{},
			State:    &model.HostState{},
			IsOnline: true,
		}
		servers = append(servers, demoServer)
	}

	// 序列化数据以便在前端使用
	data := Data{
		Now:     time.Now().Unix() * 1000,
		Servers: servers,
	}

	serversBytes, err := utils.Json.Marshal(data)
	if err != nil {
		log.Printf("network: 服务器数据序列化失败: %v", err)
		// 在序列化失败时使用空对象
		serversBytes = []byte(`{"now":` + fmt.Sprintf("%d", time.Now().Unix()*1000) + `,"servers":[]}`)
	}

	// 渲染页面
	c.HTML(http.StatusOK, mygin.GetPreferredTheme(c, "/network"), mygin.CommonEnvironment(c, gin.H{
		"Servers":         string(serversBytes),
		"MonitorInfos":    string(monitorInfos),
		"MaxTCPPingValue": singleton.Conf.MaxTCPPingValue,
	}))
}

type Data struct {
	Now         int64           `json:"now,omitempty"`
	Servers     []*model.Server `json:"servers,omitempty"`
	TrafficData interface{}     `json:"trafficData,omitempty"`
}

func (cp *commonPage) getServerStat(c *gin.Context, withPublicNote bool) ([]byte, error) {
	_, isMember := c.Get(model.CtxKeyAuthorizedUser)
	_, isViewPasswordVerfied := c.Get(model.CtxKeyViewPasswordVerified)
	authorized := isMember || isViewPasswordVerfied
	v, err, _ := cp.requestGroup.Do(fmt.Sprintf("serverStats::%t", authorized), func() (interface{}, error) {
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

		// 修复：检查服务器列表是否为空或未初始化
		if serverList == nil || len(serverList) == 0 {
			if singleton.Conf.Debug {
				log.Printf("getServerStat: 服务器列表为空，尝试从 ServerList 提取数据")
			}

			// 从 ServerList 中提取服务器
			singleton.ServerLock.RLock()
			// 检查ServerList是否初始化
			if singleton.ServerList != nil {
				for _, server := range singleton.ServerList {
					if server != nil {
						// 为所有用户展示所有服务器，或者仅对授权用户显示
						if authorized || !server.HideForGuest {
							// 确保服务器对象完整
							if server.Host == nil {
								server.Host = &model.Host{}
								server.Host.Initialize()
							}
							if server.State == nil {
								server.State = &model.HostState{}
							}
							serverList = append(serverList, server)
						}
					}
				}
			} else {
				log.Printf("getServerStat: ServerList未初始化")
			}
			singleton.ServerLock.RUnlock()

			if singleton.Conf.Debug {
				log.Printf("getServerStat: 从 ServerList 提取到 %d 台服务器", len(serverList))
			}

			// 如果服务器列表仍然为空，创建一个默认的演示服务器
			if singleton.Conf.Debug && (serverList == nil || len(serverList) == 0) {
				log.Printf("getServerStat: 创建演示服务器数据用于调试")
				now := time.Now()
				demoServer := &model.Server{
					Common: model.Common{
						ID: 1,
					},
					Name:         "演示服务器",
					Tag:          "演示",
					DisplayIndex: 1,
					Host: &model.Host{
						Platform:        "linux",
						PlatformVersion: "Ubuntu 20.04",
						CPU:             []string{"Intel Core i7-10700K"},
						MemTotal:        16777216,      // 16GB
						DiskTotal:       1099511627776, // 1TB
					},
					State: &model.HostState{
						CPU:            5.2,
						MemUsed:        4096000,
						DiskUsed:       107374182400,
						NetInTransfer:  1073741824,
						NetOutTransfer: 536870912,
						NetInSpeed:     1048576,
						NetOutSpeed:    524288,
						Uptime:         86400,
					},
					IsOnline:   true,
					LastActive: now,
					LastOnline: now,
				}
				serverList = append(serverList, demoServer)
			}
		}

		var servers []*model.Server
		for _, server := range serverList {
			if server == nil {
				continue
			}

			item := *server
			if !withPublicNote {
				item.PublicNote = ""
			}

			// 确保Host和State不为nil
			if item.Host == nil {
				item.Host = &model.Host{}
				item.Host.Initialize()
			}
			if item.State == nil {
				item.State = &model.HostState{}
			}

			servers = append(servers, &item)
		}

		// 组装 trafficData，逻辑与 home handler 保持一致
		// 使用深拷贝确保并发安全
		var trafficData []map[string]interface{}

		// 在锁保护下进行深拷贝
		singleton.AlertsLock.RLock()
		var statsStore map[uint64]model.CycleTransferStats
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

		// 现在可以安全地访问深拷贝的数据
		if statsStore != nil {
			for cycleID, stats := range statsStore {
				if stats.Transfer != nil {
					for serverID, transfer := range stats.Transfer {
						serverName := ""
						if stats.ServerName != nil {
							if name, exists := stats.ServerName[serverID]; exists {
								serverName = name
							}
						}

						usedPercent := float64(0)
						if stats.Max > 0 {
							usedPercent = (float64(transfer) / float64(stats.Max)) * 100
							if usedPercent > 100 {
								usedPercent = 100
							}
							if usedPercent < 0 {
								usedPercent = 0
							}
						}

						trafficItem := map[string]interface{}{
							"server_id":       serverID,
							"server_name":     serverName,
							"max_bytes":       stats.Max,
							"used_bytes":      transfer,
							"max_formatted":   bytefmt.ByteSize(stats.Max),
							"used_formatted":  bytefmt.ByteSize(transfer),
							"used_percent":    math.Round(usedPercent*100) / 100,
							"cycle_name":      stats.Name,
							"cycle_id":        strconv.FormatUint(cycleID, 10),
							"is_bytes_source": true, // 标识这是字节数据源
						}
						trafficData = append(trafficData, trafficItem)
					}
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
			var monthlyTransfer uint64

			// 如果服务器有最后活跃时间记录，且在当月内，使用累积流量
			if !server.LastActive.IsZero() && server.LastActive.After(currentMonthStart) {
				monthlyTransfer = server.CumulativeNetInTransfer + server.CumulativeNetOutTransfer
			} else {
				// 如果服务器在本月开始前就不活跃，或者没有记录，流量从0开始
				monthlyTransfer = 0
			}

			// 计算使用百分比
			usedPercent := float64(0)
			if defaultQuota > 0 {
				usedPercent = (float64(monthlyTransfer) / float64(defaultQuota)) * 100
				usedPercent = math.Max(0, math.Min(100, usedPercent))
			}

			// 构建默认流量数据项，显示月度配额
			trafficItem := map[string]interface{}{
				"server_id":       serverID,
				"server_name":     server.Name,
				"max_bytes":       defaultQuota,
				"used_bytes":      monthlyTransfer,
				"max_formatted":   bytefmt.ByteSize(defaultQuota),
				"used_formatted":  bytefmt.ByteSize(monthlyTransfer),
				"used_percent":    math.Round(usedPercent*100) / 100,
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

		return utils.Json.Marshal(data)
	})

	if err != nil && singleton.Conf.Debug {
		log.Printf("getServerStat: 数据序列化错误: %v", err)
	}

	return v.([]byte), err
}

func (cp *commonPage) home(c *gin.Context) {
	stat, err := cp.getServerStat(c, true)

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

	// 预处理流量数据
	var trafficData []map[string]interface{}
	if statsStore != nil {
		for cycleID, stats := range statsStore {
			if stats.Transfer != nil {
				for serverID, transfer := range stats.Transfer {
					serverName := ""
					if stats.ServerName != nil {
						if name, exists := stats.ServerName[serverID]; exists {
							serverName = name
						}
					}

					usedPercent := float64(0)
					if stats.Max > 0 {
						usedPercent = (float64(transfer) / float64(stats.Max)) * 100
						if usedPercent > 100 {
							usedPercent = 100
						}
						if usedPercent < 0 {
							usedPercent = 0
						}
					}

					trafficItem := map[string]interface{}{
						"server_id":       serverID,
						"server_name":     serverName,
						"max_bytes":       stats.Max,
						"used_bytes":      transfer,
						"max_formatted":   bytefmt.ByteSize(stats.Max),
						"used_formatted":  bytefmt.ByteSize(transfer),
						"used_percent":    math.Round(usedPercent*100) / 100,
						"cycle_name":      stats.Name,
						"cycle_id":        strconv.FormatUint(cycleID, 10),
						"is_bytes_source": true, // 标识这是字节数据源
					}
					trafficData = append(trafficData, trafficItem)
				}
			}
		}
	}

	// 回退机制：为没有警报规则的服务器创建默认流量数据（10TB月配额）
	if len(trafficData) == 0 {
		// 获取当前用户的权限状态
		_, authorized := c.Get(model.CtxKeyAuthorizedUser)
		_, isViewPasswordVerified := c.Get(model.CtxKeyViewPasswordVerified)
		isAuthorized := authorized || isViewPasswordVerified

		// 根据权限获取服务器列表
		var serverList []*model.Server
		if isAuthorized {
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
				var monthlyTransfer uint64

				// 如果服务器有最后活跃时间记录，且在当月内，使用累积流量
				if !actualServer.LastActive.IsZero() && actualServer.LastActive.After(currentMonthStart) {
					monthlyTransfer = actualServer.CumulativeNetInTransfer + actualServer.CumulativeNetOutTransfer
				} else {
					// 如果服务器在本月开始前就不活跃，或者没有记录，流量从0开始
					monthlyTransfer = 0
				}

				// 计算使用百分比
				usedPercent := float64(0)
				if defaultQuota > 0 {
					usedPercent = (float64(monthlyTransfer) / float64(defaultQuota)) * 100
					usedPercent = math.Max(0, math.Min(100, usedPercent))
				}

				// 构建默认流量数据项，显示月度配额
				trafficItem := map[string]interface{}{
					"server_id":       serverID,
					"server_name":     actualServer.Name,
					"max_bytes":       defaultQuota,
					"used_bytes":      monthlyTransfer,
					"max_formatted":   bytefmt.ByteSize(defaultQuota),
					"used_formatted":  bytefmt.ByteSize(monthlyTransfer),
					"used_percent":    math.Round(usedPercent*100) / 100,
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

	trafficDataJSON, _ := utils.Json.Marshal(trafficData)

	if err != nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code: http.StatusInternalServerError,
			Title: singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{
				MessageID: "SystemError",
			}),
			Msg:  "服务器状态获取失败",
			Link: "/",
			Btn:  "返回首页",
		}, true)
		return
	}
	c.HTML(http.StatusOK, mygin.GetPreferredTheme(c, "/home"), mygin.CommonEnvironment(c, gin.H{
		"Servers":            string(stat),
		"CycleTransferStats": statsStore,
		"TrafficData":        string(trafficDataJSON),
	}))
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  32768,
	WriteBufferSize: 32768,
}

func (cp *commonPage) ws(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code: http.StatusInternalServerError,
			Title: singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{
				MessageID: "NetworkError",
			}),
			Msg:  "Websocket协议切换失败",
			Link: "/",
			Btn:  "返回首页",
		}, true)
		return
	}
	defer func() {
		conn.Close()
		// 强制触发GC清理连接相关内存
		runtime.GC()
	}()

	// 设置连接超时和大小限制
	conn.SetReadLimit(512 * 1024) // 512KB 限制
	conn.SetPongHandler(func(appData string) error {
		// 延长读取超时时间，增强容错性
		conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		return nil
	})

	// 使用context控制连接生命周期，增加超时时间
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Minute)
	defer cancel()

	count := 0
	ticker := time.NewTicker(2 * time.Second) // 从1秒改为2秒，减少推送频率
	defer ticker.Stop()

	// 使用ticker而非sleep循环，减少goroutine占用
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 获取服务器状态数据
			stat, err := cp.getServerStat(c, false)
			if err != nil {
				continue
			}

			// 增强数据：添加最新流量信息
			var data Data
			if err := utils.Json.Unmarshal(stat, &data); err == nil {
				// 添加流量数据
				data.TrafficData = buildTrafficData() // 获取最新周期性流量数据

				// 重新序列化
				enhancedStat, enhancedErr := utils.Json.Marshal(data)
				if enhancedErr == nil {
					stat = enhancedStat
				}
			}

			if err := conn.WriteMessage(websocket.TextMessage, stat); err != nil {
				// 检查是否为网络连接错误
				if strings.Contains(err.Error(), "broken pipe") ||
					strings.Contains(err.Error(), "connection reset") ||
					strings.Contains(err.Error(), "use of closed network connection") {
					// 静默处理网络连接错误，客户端可能已断开
					return
				}
				log.Printf("WebSocket写入错误: %v", err)
				return
			}

			count += 1
			if count%4 == 0 {
				err = conn.WriteMessage(websocket.PingMessage, []byte{})
				if err != nil {
					// 检查是否为网络连接错误
					if strings.Contains(err.Error(), "broken pipe") ||
						strings.Contains(err.Error(), "connection reset") ||
						strings.Contains(err.Error(), "use of closed network connection") {
						// 静默处理网络连接错误
						return
					}
					log.Printf("WebSocket Ping错误: %v", err)
					return
				}
			}

			// 定期强制GC，清理连接相关内存
			if count%60 == 0 { // 每分钟清理一次
				runtime.GC()
			}
		}
	}
}

func (cp *commonPage) terminal(c *gin.Context) {
	streamId := c.Param("id")
	if _, err := rpc.ServerHandlerSingleton.GetStream(streamId); err != nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusForbidden,
			Title: "无权访问",
			Msg:   "终端会话不存在",
			Link:  "/",
			Btn:   "返回首页",
		}, true)
		return
	}
	defer rpc.ServerHandlerSingleton.CloseStream(streamId)

	wsConn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code: http.StatusInternalServerError,
			Title: singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{
				MessageID: "NetworkError",
			}),
			Msg:  "Websocket协议切换失败",
			Link: "/",
			Btn:  "返回首页",
		}, true)
		return
	}
	defer wsConn.Close()
	conn := websocketx.NewConn(wsConn)

	// 使用 context 控制 PING 保活 goroutine 的生命周期
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	go func() {
		// PING 保活
		ticker := time.NewTicker(time.Second * 10)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.PingMessage, []byte{}); err != nil {
					return
				}
			}
		}
	}()

	if err = rpc.ServerHandlerSingleton.UserConnected(streamId, conn); err != nil {
		return
	}

	rpc.ServerHandlerSingleton.StartStream(streamId, time.Second*10)
}

type createTerminalRequest struct {
	Host     string
	Protocol string
	ID       uint64
}

func (cp *commonPage) createTerminal(c *gin.Context) {
	if _, authorized := c.Get(model.CtxKeyAuthorizedUser); !authorized {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusForbidden,
			Title: "无权访问",
			Msg:   "用户未登录",
			Link:  "/login",
			Btn:   "去登录",
		}, true)
		return
	}
	var createTerminalReq createTerminalRequest
	if err := c.ShouldBind(&createTerminalReq); err != nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusForbidden,
			Title: "请求失败",
			Msg:   "请求参数有误：" + err.Error(),
			Link:  "/server",
			Btn:   "返回重试",
		}, true)
		return
	}

	streamId, err := uuid.GenerateUUID()
	if err != nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code: http.StatusInternalServerError,
			Title: singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{
				MessageID: "SystemError",
			}),
			Msg:  "生成会话ID失败",
			Link: "/server",
			Btn:  "返回重试",
		}, true)
		return
	}

	rpc.ServerHandlerSingleton.CreateStream(streamId)

	singleton.ServerLock.RLock()
	server := singleton.ServerList[createTerminalReq.ID]
	singleton.ServerLock.RUnlock()
	if server == nil || server.TaskStream == nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusForbidden,
			Title: "请求失败",
			Msg:   "服务器不存在或处于离线状态",
			Link:  "/server",
			Btn:   "返回重试",
		}, true)
		return
	}

	terminalData, _ := utils.Json.Marshal(&model.TerminalTask{
		StreamID: streamId,
	})
	if err := server.TaskStream.Send(&proto.Task{
		Type: model.TaskTypeTerminalGRPC,
		Data: string(terminalData),
	}); err != nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusForbidden,
			Title: "请求失败",
			Msg:   "Agent信令下发失败",
			Link:  "/server",
			Btn:   "返回重试",
		}, true)
		return
	}

	c.HTML(http.StatusOK, "dashboard-"+singleton.Conf.Site.DashboardTheme+"/terminal", mygin.CommonEnvironment(c, gin.H{
		"SessionID":  streamId,
		"ServerName": server.Name,
		"ServerID":   server.ID,
	}))
}

func (cp *commonPage) fm(c *gin.Context) {
	streamId := c.Param("id")
	if _, err := rpc.ServerHandlerSingleton.GetStream(streamId); err != nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusForbidden,
			Title: "无权访问",
			Msg:   "FM会话不存在",
			Link:  "/",
			Btn:   "返回首页",
		}, true)
		return
	}
	defer func() {
		rpc.ServerHandlerSingleton.CloseStream(streamId)
		runtime.GC() // 强制GC清理连接内存
	}()

	wsConn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code: http.StatusInternalServerError,
			Title: singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{
				MessageID: "NetworkError",
			}),
			Msg:  "Websocket协议切换失败",
			Link: "/",
			Btn:  "返回首页",
		}, true)
		return
	}
	defer wsConn.Close()

	// 设置连接限制和超时
	wsConn.SetReadLimit(1024 * 1024) // 1MB 限制
	wsConn.SetReadDeadline(time.Now().Add(60 * time.Second))
	wsConn.SetWriteDeadline(time.Now().Add(10 * time.Second))

	conn := websocketx.NewConn(wsConn)

	// 使用 context 控制 PING 保活 goroutine 的生命周期
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("FM ping goroutine panic: %v", r)
			}
		}()
		// PING 保活
		ticker := time.NewTicker(time.Second * 10)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.PingMessage, []byte{}); err != nil {
					return
				}
			}
		}
	}()

	if err = rpc.ServerHandlerSingleton.UserConnected(streamId, conn); err != nil {
		return
	}

	rpc.ServerHandlerSingleton.StartStream(streamId, time.Second*10)
}

func (cp *commonPage) createFM(c *gin.Context) {
	IdString := c.Query("id")
	if _, authorized := c.Get(model.CtxKeyAuthorizedUser); !authorized {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusForbidden,
			Title: "无权访问",
			Msg:   "用户未登录",
			Link:  "/login",
			Btn:   "去登录",
		}, true)
		return
	}

	streamId, err := uuid.GenerateUUID()
	if err != nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code: http.StatusInternalServerError,
			Title: singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{
				MessageID: "SystemError",
			}),
			Msg:  "生成会话ID失败",
			Link: "/server",
			Btn:  "返回重试",
		}, true)
		return
	}

	rpc.ServerHandlerSingleton.CreateStream(streamId)

	serverId, err := strconv.Atoi(IdString)
	if err != nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusForbidden,
			Title: "请求失败",
			Msg:   "请求参数有误：" + err.Error(),
			Link:  "/server",
			Btn:   "返回重试",
		}, true)
		return
	}

	singleton.ServerLock.RLock()
	server := singleton.ServerList[uint64(serverId)]
	singleton.ServerLock.RUnlock()
	if server == nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusForbidden,
			Title: "请求失败",
			Msg:   "服务器不存在或处于离线状态",
			Link:  "/server",
			Btn:   "返回重试",
		}, true)
		return
	}

	fmData, _ := utils.Json.Marshal(&model.TaskFM{
		StreamID: streamId,
	})
	if err := server.TaskStream.Send(&proto.Task{
		Type: model.TaskTypeFM,
		Data: string(fmData),
	}); err != nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusForbidden,
			Title: "请求失败",
			Msg:   "Agent信令下发失败",
			Link:  "/server",
			Btn:   "返回重试",
		}, true)
		return
	}

	c.HTML(http.StatusOK, "dashboard-"+singleton.Conf.Site.DashboardTheme+"/file", mygin.CommonEnvironment(c, gin.H{
		"SessionID": streamId,
	}))
}

// 新增：/api/traffic handler，返回和首页相同结构的流量数据
func (cp *commonPage) apiTraffic(c *gin.Context) {
	// 支持用户登录或view password验证
	_, isMember := c.Get(model.CtxKeyAuthorizedUser)
	_, isViewPasswordVerified := c.Get(model.CtxKeyViewPasswordVerified)

	if !isMember && !isViewPasswordVerified {
		c.JSON(http.StatusForbidden, gin.H{
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
	if statsStore != nil {
		for cycleID, stats := range statsStore {
			if stats.Transfer != nil {
				for serverID, transfer := range stats.Transfer {
					serverName := ""
					if stats.ServerName != nil {
						if name, exists := stats.ServerName[serverID]; exists {
							serverName = name
						}
					}
					usedPercent := float64(0)
					if stats.Max > 0 {
						usedPercent = (float64(transfer) / float64(stats.Max)) * 100
						if usedPercent > 100 {
							usedPercent = 100
						}
						if usedPercent < 0 {
							usedPercent = 0
						}
					}
					trafficItem := map[string]interface{}{
						"server_id":       serverID,
						"server_name":     serverName,
						"max_bytes":       stats.Max,
						"used_bytes":      transfer,
						"max_formatted":   bytefmt.ByteSize(stats.Max),
						"used_formatted":  bytefmt.ByteSize(transfer),
						"used_percent":    math.Round(usedPercent*100) / 100,
						"cycle_name":      stats.Name,
						"cycle_id":        strconv.FormatUint(cycleID, 10),
						"is_bytes_source": true, // 标识这是字节数据源
					}
					trafficData = append(trafficData, trafficItem)
				}
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
				var monthlyTransfer uint64

				// 如果服务器有最后活跃时间记录，且在当月内，使用累积流量
				if !actualServer.LastActive.IsZero() && actualServer.LastActive.After(currentMonthStart) {
					monthlyTransfer = actualServer.CumulativeNetInTransfer + actualServer.CumulativeNetOutTransfer
				} else {
					// 如果服务器在本月开始前就不活跃，或者没有记录，流量从0开始
					monthlyTransfer = 0
				}

				// 计算使用百分比
				usedPercent := float64(0)
				if defaultQuota > 0 {
					usedPercent = (float64(monthlyTransfer) / float64(defaultQuota)) * 100
					usedPercent = math.Max(0, math.Min(100, usedPercent))
				}

				// 构建默认流量数据项，显示月度配额
				trafficItem := map[string]interface{}{
					"server_id":       serverID,
					"server_name":     actualServer.Name,
					"max_bytes":       defaultQuota,
					"used_bytes":      monthlyTransfer,
					"max_formatted":   bytefmt.ByteSize(defaultQuota),
					"used_formatted":  bytefmt.ByteSize(monthlyTransfer),
					"used_percent":    math.Round(usedPercent*100) / 100,
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
	c.JSON(http.StatusOK, gin.H{
		"code": 200,
		"data": trafficData,
	})
}

// 新增：/api/server/:id/traffic handler，返回单个服务器的流量数据
func (cp *commonPage) apiServerTraffic(c *gin.Context) {
	// 支持用户登录或view password验证
	_, isMember := c.Get(model.CtxKeyAuthorizedUser)
	_, isViewPasswordVerified := c.Get(model.CtxKeyViewPasswordVerified)

	if !isMember && !isViewPasswordVerified {
		c.JSON(http.StatusForbidden, gin.H{
			"code":    403,
			"message": "请先登录或输入访问密码",
		})
		return
	}

	// 获取服务器ID
	serverID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
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
		c.JSON(http.StatusNotFound, gin.H{
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
	if statsStore != nil {
		for cycleID, stats := range statsStore {
			if stats.Transfer != nil {
				if transfer, exists := stats.Transfer[serverID]; exists {
					serverName := ""
					if stats.ServerName != nil {
						if name, exists := stats.ServerName[serverID]; exists {
							serverName = name
						}
					}
					usedPercent := float64(0)
					if stats.Max > 0 {
						usedPercent = (float64(transfer) / float64(stats.Max)) * 100
						if usedPercent > 100 {
							usedPercent = 100
						}
						if usedPercent < 0 {
							usedPercent = 0
						}
					}
					trafficItem := map[string]interface{}{
						"server_id":       serverID,
						"server_name":     serverName,
						"max_bytes":       stats.Max,
						"used_bytes":      transfer,
						"max_formatted":   bytefmt.ByteSize(stats.Max),
						"used_formatted":  bytefmt.ByteSize(transfer),
						"used_percent":    math.Round(usedPercent*100) / 100,
						"cycle_name":      stats.Name,
						"cycle_id":        strconv.FormatUint(cycleID, 10),
						"is_bytes_source": true, // 标识这是字节数据源
					}
					trafficData = append(trafficData, trafficItem)
				}
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
		var monthlyTransfer uint64

		// 如果服务器有最后活跃时间记录，且在当月内，使用累积流量
		if !server.LastActive.IsZero() && server.LastActive.After(currentMonthStart) {
			monthlyTransfer = server.CumulativeNetInTransfer + server.CumulativeNetOutTransfer
		} else {
			// 如果服务器在本月开始前就不活跃，或者没有记录，流量从0开始
			monthlyTransfer = 0
		}

		// 计算使用百分比
		usedPercent := float64(0)
		if defaultQuota > 0 {
			usedPercent = (float64(monthlyTransfer) / float64(defaultQuota)) * 100
			usedPercent = math.Max(0, math.Min(100, usedPercent))
		}

		// 构建默认流量数据项，显示月度配额
		trafficItem := map[string]interface{}{
			"server_id":       serverID,
			"server_name":     server.Name,
			"max_bytes":       defaultQuota,
			"used_bytes":      monthlyTransfer,
			"max_formatted":   bytefmt.ByteSize(defaultQuota),
			"used_formatted":  bytefmt.ByteSize(monthlyTransfer),
			"used_percent":    math.Round(usedPercent*100) / 100,
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

	c.JSON(http.StatusOK, gin.H{
		"code": 200,
		"data": trafficData,
	})
}
