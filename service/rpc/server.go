package rpc

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/xos/serverstatus/pkg/ddns"
	"github.com/xos/serverstatus/pkg/geoip"
	"github.com/xos/serverstatus/pkg/grpcx"
	"github.com/xos/serverstatus/pkg/utils"

	"github.com/jinzhu/copier"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"gorm.io/gorm"

	"github.com/xos/serverstatus/model"
	pb "github.com/xos/serverstatus/proto"
	"github.com/xos/serverstatus/service/singleton"
)

// isConnectionError 检查是否为网络连接错误（broken pipe等）
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	
	errStr := err.Error()
	connectionErrors := []string{
		"broken pipe",
		"connection reset by peer",
		"connection refused",
		"network is unreachable",
		"no route to host",
		"connection timed out",
		"EOF",
	}
	
	for _, connErr := range connectionErrors {
		if strings.Contains(strings.ToLower(errStr), connErr) {
			return true
		}
	}
	
	return false
}

var ServerHandlerSingleton *ServerHandler

type ServerHandler struct {
	Auth          *authHandler
	ioStreams     map[string]*ioStreamContext
	ioStreamMutex *sync.RWMutex
}

func NewServerHandler() *ServerHandler {
	return &ServerHandler{
		Auth:          &authHandler{},
		ioStreamMutex: new(sync.RWMutex),
		ioStreams:     make(map[string]*ioStreamContext),
	}
}

func (s *ServerHandler) ReportTask(c context.Context, r *pb.TaskResult) (*pb.Receipt, error) {
	var err error
	var clientID uint64
	if clientID, err = s.Auth.Check(c); err != nil {
		return nil, err
	}
	if r.GetType() == model.TaskTypeCommand {
		// 处理上报的计划任务
		singleton.CronLock.RLock()
		defer singleton.CronLock.RUnlock()
		cr := singleton.Crons[r.GetId()]
		if cr != nil {
			singleton.ServerLock.RLock()
			defer singleton.ServerLock.RUnlock()
			// 保存当前服务器状态信息
			curServer := model.Server{}
			copier.Copy(&curServer, singleton.ServerList[clientID])
			
			// 计算执行时间
			startTime := time.Now().Add(time.Second * -1 * time.Duration(r.GetDelay()))
			endTime := time.Now()
			
			if cr.PushSuccessful && r.GetSuccessful() {
				message := fmt.Sprintf("[%s]\n任务名称: %s\n执行设备: %s (ID:%d)\n开始时间: %s\n结束时间: %s\n执行结果: 成功\n执行详情:\n%s", 
					singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{
						MessageID: "ScheduledTaskExecutedSuccessfully",
					}),
					cr.Name,
					singleton.ServerList[clientID].Name, clientID,
					startTime.Format("2006-01-02 15:04:05"),
					endTime.Format("2006-01-02 15:04:05"),
					r.GetData())
				singleton.SendNotification(cr.NotificationTag, message, nil, &curServer)
			}
			if !r.GetSuccessful() {
				message := fmt.Sprintf("[%s]\n任务名称: %s\n执行设备: %s (ID:%d)\n开始时间: %s\n结束时间: %s\n执行结果: 失败\n错误详情:\n%s", 
					singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{
						MessageID: "ScheduledTaskExecutedFailed",
					}),
					cr.Name,
					singleton.ServerList[clientID].Name, clientID,
					startTime.Format("2006-01-02 15:04:05"),
					endTime.Format("2006-01-02 15:04:05"),
					r.GetData())
				singleton.SendNotification(cr.NotificationTag, message, nil, &curServer)
			}
			singleton.DB.Model(cr).Updates(model.Cron{
				LastExecutedAt: time.Now().Add(time.Second * -1 * time.Duration(r.GetDelay())),
				LastResult:     r.GetSuccessful(),
			})
		}
	} else if model.IsServiceSentinelNeeded(r.GetType()) {
		singleton.ServiceSentinelShared.Dispatch(singleton.ReportData{
			Data:     r,
			Reporter: clientID,
		})
	}
	return &pb.Receipt{Proced: true}, nil
}

func (s *ServerHandler) RequestTask(h *pb.Host, stream pb.ServerService_RequestTaskServer) error {
	var clientID uint64
	var err error
	if clientID, err = s.Auth.Check(stream.Context()); err != nil {
		return err
	}

	// 使用带缓冲的通道避免阻塞
	closeCh := make(chan error, 1)
	// 使用done channel来安全地通知监控goroutine停止
	done := make(chan struct{})

	singleton.ServerLock.RLock()
	if singleton.ServerList[clientID] == nil {
		singleton.ServerLock.RUnlock()
		return fmt.Errorf("server not found: %d", clientID)
	}

	singleton.ServerList[clientID].TaskCloseLock.Lock()
	// 修复不断的请求 task 但是没有 return 导致内存泄漏
	if singleton.ServerList[clientID].TaskClose != nil {
		// 安全关闭旧的通道
		select {
		case singleton.ServerList[clientID].TaskClose <- fmt.Errorf("connection replaced"):
		default:
		}
		close(singleton.ServerList[clientID].TaskClose)
	}
	singleton.ServerList[clientID].TaskStream = stream
	singleton.ServerList[clientID].TaskClose = closeCh
	singleton.ServerList[clientID].TaskCloseLock.Unlock()
	singleton.ServerLock.RUnlock()

	// 创建一个带超时的上下文，增加超时时间以减少频繁超时错误
	ctx, cancel := context.WithTimeout(stream.Context(), 20*time.Minute)
	defer cancel()

	// 监听连接状态，当连接断开时自动清理
	go func() {
		defer func() {
			// 确保监控goroutine退出时进行最终清理
			if r := recover(); r != nil {
				log.Printf("RequestTask监控goroutine panic恢复: %v", r)
			}
		}()
		
		select {
		case <-ctx.Done():
			// 连接断开时清理资源，增强错误处理
			if ctxErr := ctx.Err(); ctxErr != nil {
				// 检查是否为网络连接错误或超时错误
				if isConnectionError(ctxErr) || ctxErr == context.DeadlineExceeded {
					// 静默处理网络连接错误和正常超时，避免日志干扰
					log.Printf("RequestTask连接正常断开: 客户端ID %d", clientID)
				} else {
					log.Printf("RequestTask连接上下文错误: %v", ctxErr)
				}
			}
			
			singleton.ServerLock.RLock()
			if singleton.ServerList[clientID] != nil {
				singleton.ServerList[clientID].TaskCloseLock.Lock()
				if singleton.ServerList[clientID].TaskClose == closeCh {
					// 只有当前连接才清理
					singleton.ServerList[clientID].TaskStream = nil
					singleton.ServerList[clientID].TaskClose = nil
				}
				singleton.ServerList[clientID].TaskCloseLock.Unlock()
			}
			singleton.ServerLock.RUnlock()

			// 安全地发送关闭信号，使用非阻塞发送
			select {
			case closeCh <- ctx.Err():
			case <-done: // 如果主goroutine已经退出，停止发送
				return
			default:
				// 通道可能已关闭或已满，忽略
			}
		case <-done:
			// 主goroutine要求停止监控
			return
		}
	}()

	defer func() {
		close(done) // 确保监控goroutine被通知停止
		// 额外的清理工作确保资源释放
		singleton.ServerLock.RLock()
		if singleton.ServerList[clientID] != nil {
			singleton.ServerList[clientID].TaskCloseLock.Lock()
			if singleton.ServerList[clientID].TaskClose == closeCh {
				singleton.ServerList[clientID].TaskStream = nil
				singleton.ServerList[clientID].TaskClose = nil
			}
			singleton.ServerList[clientID].TaskCloseLock.Unlock()
		}
		singleton.ServerLock.RUnlock()
	}()

	// 等待连接关闭或超时
	select {
	case err := <-closeCh:
		// 检查是否为网络连接错误
		if isConnectionError(err) {
			log.Printf("客户端 %d 网络连接中断: %v", clientID, err)
			return nil // 将网络连接错误视为正常断开
		}
		return err
	case <-ctx.Done():
		// 超时或连接取消，增强错误分类
		if ctx.Err() == context.DeadlineExceeded {
			log.Printf("客户端 %d RequestTask连接超时 (20分钟)", clientID)
			return fmt.Errorf("request task timeout after 20 minutes")
		}
		// 检查是否为网络连接错误
		if isConnectionError(ctx.Err()) {
			log.Printf("客户端 %d 网络连接异常: %v", clientID, ctx.Err())
			return nil // 将网络连接错误视为正常断开
		}
		return ctx.Err()
	}
}

func (s *ServerHandler) ReportSystemState(c context.Context, r *pb.State) (*pb.Receipt, error) {
	var clientID uint64
	var err error
	if clientID, err = s.Auth.Check(c); err != nil {
		return nil, err
	}
	state := model.PB2State(r)
	singleton.ServerLock.RLock()
	defer singleton.ServerLock.RUnlock()

	// 更新服务器在线状态
	singleton.ServerList[clientID].IsOnline = true
	singleton.ServerList[clientID].LastActive = time.Now()

	// 保存原始流量数据用于增量计算
	originalNetInTransfer := state.NetInTransfer
	originalNetOutTransfer := state.NetOutTransfer

	// 检查周期流量重置
	checkAndResetCycleTraffic(clientID)

	// 检查是否是服务器重启或网络接口重置
	isRestart := false
	if singleton.ServerList[clientID].Host != nil && singleton.ServerList[clientID].State != nil {
		// 获取之前显示的累计流量值
		prevDisplayIn := singleton.ServerList[clientID].State.NetInTransfer
		prevDisplayOut := singleton.ServerList[clientID].State.NetOutTransfer

		// 修改流量回退检测逻辑，增加容错范围
		// 如果当前原始流量小于之前的显示流量，但差值在合理范围内（不超过10%），认为是正常波动
		if (prevDisplayIn > 0 && originalNetInTransfer < prevDisplayIn && float64(prevDisplayIn-originalNetInTransfer)/float64(prevDisplayIn) < 0.1) ||
			(prevDisplayOut > 0 && originalNetOutTransfer < prevDisplayOut && float64(prevDisplayOut-originalNetOutTransfer)/float64(prevDisplayOut) < 0.1) {
			// 正常波动，不认为是重启
			isRestart = false
		} else if (prevDisplayIn > 0 && originalNetInTransfer < prevDisplayIn/2) ||
			(prevDisplayOut > 0 && originalNetOutTransfer < prevDisplayOut/2) {
			// 流量显著减少，认为是重启
			isRestart = true
		}
	}

	// 更新状态中的累计流量
	if singleton.ServerList[clientID].LastActive.IsZero() || isRestart {
		// 首次上线或重启，只在首次时从数据库读取累计流量
		if singleton.ServerList[clientID].LastActive.IsZero() {
			var server model.Server
			if err := singleton.DB.First(&server, clientID).Error; err == nil {
				singleton.ServerList[clientID].CumulativeNetInTransfer = server.CumulativeNetInTransfer
				singleton.ServerList[clientID].CumulativeNetOutTransfer = server.CumulativeNetOutTransfer
			}
		}

		// 重置基准点为当前原始流量值
		singleton.ServerList[clientID].PrevTransferInSnapshot = int64(originalNetInTransfer)
		singleton.ServerList[clientID].PrevTransferOutSnapshot = int64(originalNetOutTransfer)

		// 显示的流量 = 累计流量（不加原始流量，避免重复计算）
		state.NetInTransfer = singleton.ServerList[clientID].CumulativeNetInTransfer
		state.NetOutTransfer = singleton.ServerList[clientID].CumulativeNetOutTransfer
	} else {
		// 正常增量更新
		var increaseIn, increaseOut uint64

		// 计算增量（仅在流量增长时计算）
		// 确保基准点已初始化，避免初次计算时的异常
		if singleton.ServerList[clientID].PrevTransferInSnapshot == 0 {
			singleton.ServerList[clientID].PrevTransferInSnapshot = int64(originalNetInTransfer)
		}
		if singleton.ServerList[clientID].PrevTransferOutSnapshot == 0 {
			singleton.ServerList[clientID].PrevTransferOutSnapshot = int64(originalNetOutTransfer)
		}

		// 使用uint64进行所有计算，避免int64溢出
		prevIn := uint64(singleton.ServerList[clientID].PrevTransferInSnapshot)
		prevOut := uint64(singleton.ServerList[clientID].PrevTransferOutSnapshot)

		// 修改流量回退检测逻辑，增加容错范围
		if originalNetInTransfer < prevIn {
			// 检查是否是正常波动（差值不超过10%）
			if float64(prevIn-originalNetInTransfer)/float64(prevIn) < 0.1 {
				// 正常波动，不更新基准点，也不增加累计流量
				increaseIn = 0
			} else {
				// 流量回退，更新基准点但不增加累计流量
				singleton.ServerList[clientID].PrevTransferInSnapshot = int64(originalNetInTransfer)
			}
		} else {
			// 正常增量
			increaseIn = originalNetInTransfer - prevIn
			// 检查是否会发生溢出
			if singleton.ServerList[clientID].CumulativeNetInTransfer > 0 &&
				increaseIn > ^uint64(0)-singleton.ServerList[clientID].CumulativeNetInTransfer {
				// 如果会发生溢出，保持当前值不变
				log.Printf("警告：服务器 %d 入站流量累计值即将溢出，保持当前值", clientID)
			} else {
				singleton.ServerList[clientID].CumulativeNetInTransfer += increaseIn
			}
			singleton.ServerList[clientID].PrevTransferInSnapshot = int64(originalNetInTransfer)
		}

		if originalNetOutTransfer < prevOut {
			// 检查是否是正常波动（差值不超过10%）
			if float64(prevOut-originalNetOutTransfer)/float64(prevOut) < 0.1 {
				// 正常波动，不更新基准点，也不增加累计流量
				increaseOut = 0
			} else {
				// 流量回退，更新基准点但不增加累计流量
				singleton.ServerList[clientID].PrevTransferOutSnapshot = int64(originalNetOutTransfer)
			}
		} else {
			// 正常增量
			increaseOut = originalNetOutTransfer - prevOut
			// 检查是否会发生溢出
			if singleton.ServerList[clientID].CumulativeNetOutTransfer > 0 &&
				increaseOut > ^uint64(0)-singleton.ServerList[clientID].CumulativeNetOutTransfer {
				// 如果会发生溢出，保持当前值不变
				log.Printf("警告：服务器 %d 出站流量累计值即将溢出，保持当前值", clientID)
			} else {
				singleton.ServerList[clientID].CumulativeNetOutTransfer += increaseOut
			}
			singleton.ServerList[clientID].PrevTransferOutSnapshot = int64(originalNetOutTransfer)
		}

		// 显示的流量 = 累计流量（不加原始流量，避免重复计算）
		state.NetInTransfer = singleton.ServerList[clientID].CumulativeNetInTransfer
		state.NetOutTransfer = singleton.ServerList[clientID].CumulativeNetOutTransfer

		// 定期保存到数据库（5分钟间隔），添加查询时间监控
		if time.Since(singleton.ServerList[clientID].LastFlowSaveTime).Minutes() > 5 {
			go func() {
				startTime := time.Now()
				updateSQL := "UPDATE servers SET cumulative_net_in_transfer = ?, cumulative_net_out_transfer = ? WHERE id = ?"
				if err := singleton.DB.Exec(updateSQL, singleton.ServerList[clientID].CumulativeNetInTransfer, singleton.ServerList[clientID].CumulativeNetOutTransfer, clientID).Error; err == nil {
					singleton.ServerList[clientID].LastFlowSaveTime = time.Now()
				}
				queryDuration := time.Since(startTime)
				if queryDuration > 200*time.Millisecond {
					log.Printf("慢SQL查询警告: 更新服务器 %d 流量数据耗时 %v", clientID, queryDuration)
				}
			}()
		}
	}

	// 保存当前状态
	singleton.ServerList[clientID].State = &state

	// 同步到前端显示
	updateTrafficDisplay(clientID, singleton.ServerList[clientID].CumulativeNetInTransfer, singleton.ServerList[clientID].CumulativeNetOutTransfer)

	// 保存最后状态，用于离线后显示
	lastState := model.HostState{}
	copier.Copy(&lastState, &state)
	singleton.ServerList[clientID].LastStateBeforeOffline = &lastState

	// 也将当前状态保存到数据库中的LastStateJSON字段，用于面板重启后恢复离线机器状态
	lastStateJSON, err := utils.Json.Marshal(lastState)
	if err == nil {
		singleton.ServerList[clientID].LastStateJSON = string(lastStateJSON)
		singleton.ServerList[clientID].LastOnline = singleton.ServerList[clientID].LastActive

		// 批量更新策略：只有在状态数据有显著变化或距离上次更新超过5分钟时才写入数据库
		now := time.Now()
		server := singleton.ServerList[clientID]
		shouldUpdate := false
		
		// 检查是否需要更新数据库：
		// 1. 首次更新 2. 超过5分钟 3. 服务器状态有重大变化
		if server.LastDBUpdateTime.IsZero() || now.Sub(server.LastDBUpdateTime) > 5*time.Minute {
			shouldUpdate = true
		} else if server.State != nil {
			// 检查关键状态变化：CPU、内存、磁盘使用率等
			prevState := server.State
			if state.CPU != prevState.CPU || 
			   state.MemUsed != prevState.MemUsed ||
			   state.SwapUsed != prevState.SwapUsed ||
			   state.DiskUsed != prevState.DiskUsed ||
			   state.Load1 != prevState.Load1 {
				// 状态有显著变化时立即更新（但限制频率为最多每分钟一次）
				if now.Sub(server.LastDBUpdateTime) > time.Minute {
					shouldUpdate = true
				}
			}
		}
		
		if shouldUpdate {
			// 使用安全的异步更新，增加重试机制和超时控制
			go func() {
				maxRetries := 3
				baseDelay := 100 * time.Millisecond
				
				for retry := 0; retry < maxRetries; retry++ {
					startTime := time.Now()
					
					// 使用事务和超时控制
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					
					err := singleton.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
						return tx.Model(server).Updates(map[string]interface{}{
							"last_state_json": server.LastStateJSON,
							"last_online":     server.LastOnline,
						}).Error
					})
					
					cancel()
					queryDuration := time.Since(startTime)
					
					if err == nil {
						// 成功更新
						if queryDuration > 200*time.Millisecond {
							log.Printf("慢SQL查询警告: 更新服务器 %d 状态耗时 %v", clientID, queryDuration)
						}
						server.LastDBUpdateTime = time.Now()
						break
					}
					
					// 检查是否为数据库锁错误
					if strings.Contains(err.Error(), "database is locked") ||
					   strings.Contains(err.Error(), "SQL statements in progress") ||
					   strings.Contains(err.Error(), "cannot commit transaction") {
						if retry < maxRetries-1 {
							delay := baseDelay * time.Duration(1<<retry) // 指数退避
							log.Printf("数据库忙碌，%v 后重试更新服务器 %d 状态 (%d/%d)", delay, clientID, retry+1, maxRetries)
							time.Sleep(delay)
							continue
						}
					}
					
					log.Printf("更新服务器 %d 状态到数据库失败: %v", clientID, err)
					break
				}
			}()
		}
	} else {
		// 静默处理序列化失败，避免日志干扰
	}

	// 确保PrevTransferSnapshot值被正确初始化
	// 这些值用于计算每小时的增量流量
	if singleton.ServerList[clientID].PrevTransferInSnapshot == 0 || singleton.ServerList[clientID].PrevTransferOutSnapshot == 0 {
		singleton.ServerList[clientID].PrevTransferInSnapshot = int64(originalNetInTransfer)
		singleton.ServerList[clientID].PrevTransferOutSnapshot = int64(originalNetOutTransfer)
	}

	return &pb.Receipt{Proced: true}, nil
}

func (s *ServerHandler) ReportSystemInfo(c context.Context, r *pb.Host) (*pb.Receipt, error) {
	var clientID uint64
	var err error
	if clientID, err = s.Auth.Check(c); err != nil {
		return nil, err
	}
	host := model.PB2Host(r)
	singleton.ServerLock.RLock()
	defer singleton.ServerLock.RUnlock()

	// 检查并更新DDNS
	if singleton.ServerList[clientID].EnableDDNS && host.IP != "" &&
		(singleton.ServerList[clientID].Host == nil || singleton.ServerList[clientID].Host.IP != host.IP) {
		ipv4, ipv6, _ := utils.SplitIPAddr(host.IP)
		providers, err := singleton.GetDDNSProvidersFromProfilesWithServer(
			singleton.ServerList[clientID].DDNSProfiles,
			&ddns.IP{Ipv4Addr: ipv4, Ipv6Addr: ipv6},
			singleton.ServerList[clientID].Name,
			clientID,
		)
		if err == nil {
			for _, provider := range providers {
				go func(provider *ddns.Provider) {
					provider.UpdateDomain(context.Background())
				}(provider)
			}
		} else {
			log.Printf("获取DDNS配置时发生错误: %v", err)
		}
	}

	// 发送IP变动通知（带节流机制）
	if singleton.ServerList[clientID].Host != nil && singleton.Conf.EnableIPChangeNotification &&
		((singleton.Conf.Cover == model.ConfigCoverAll && !singleton.Conf.IgnoredIPNotificationServerIDs[clientID]) ||
			(singleton.Conf.Cover == model.ConfigCoverIgnoreAll && singleton.Conf.IgnoredIPNotificationServerIDs[clientID])) &&
		singleton.ServerList[clientID].Host.IP != "" &&
		host.IP != "" &&
		singleton.ServerList[clientID].Host.IP != host.IP {

		// 使用IP变更通知的静音标签来实现节流
		muteLabel := singleton.NotificationMuteLabel.IPChanged(clientID)
		changeTime := time.Now().Format("2006-01-02 15:04:05")
		singleton.SendNotification(singleton.Conf.IPChangeNotificationTag,
			fmt.Sprintf(
				"[%s] %s (ID:%d), %s => %s\n变更时间: %s",
				singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{
					MessageID: "IPChanged",
				}),
				singleton.ServerList[clientID].Name, clientID, singleton.IPDesensitize(singleton.ServerList[clientID].Host.IP),
				singleton.IPDesensitize(host.IP), changeTime,
			),
			muteLabel)
	}

	/**
	 * 这里的 singleton 中的数据都是关机前的旧数据
	 * 当 agent 重启时，bootTime 变大，agent 端会先上报 host 信息，然后上报 state 信息
	 * 这是可以借助上报顺序的空档，标记服务器为重启状态，表示从该节点开始累计流量
	 */
	if singleton.ServerList[clientID].Host != nil && singleton.ServerList[clientID].Host.BootTime < host.BootTime {
		// 服务器重启时保持累计流量不变，只重置上次记录点
		singleton.ServerList[clientID].PrevTransferInSnapshot = 0
		singleton.ServerList[clientID].PrevTransferOutSnapshot = 0

		// 确保从数据库读取最新的累计流量值（只在重启时读取一次）
		var server model.Server
		if err := singleton.DB.First(&server, clientID).Error; err == nil {
			singleton.ServerList[clientID].CumulativeNetInTransfer = server.CumulativeNetInTransfer
			singleton.ServerList[clientID].CumulativeNetOutTransfer = server.CumulativeNetOutTransfer
		}
	}

	// 不要冲掉国家码
	if singleton.ServerList[clientID].Host != nil {
		host.CountryCode = singleton.ServerList[clientID].Host.CountryCode
	}

	// 保存完整Host信息到数据库，用于重启后恢复
	hostJSON, err := utils.Json.Marshal(host)
	if err == nil {
		// 更新servers表中的host_json字段
		if err := singleton.DB.Exec("UPDATE servers SET host_json = ? WHERE id = ?",
			string(hostJSON), clientID).Error; err != nil {
			// 静默处理保存失败，避免日志干扰
		}
	}

	singleton.ServerList[clientID].Host = &host
	return &pb.Receipt{Proced: true}, nil
}

func (s *ServerHandler) IOStream(stream pb.ServerService_IOStreamServer) error {
	if _, err := s.Auth.Check(stream.Context()); err != nil {
		return err
	}
	id, err := stream.Recv()
	if err != nil {
		return err
	}
	if id == nil || len(id.Data) < 4 || (id.Data[0] != 0xff && id.Data[1] != 0x05 && id.Data[2] != 0xff && id.Data[3] == 0x05) {
		return fmt.Errorf("invalid stream id")
	}

	streamId := string(id.Data[4:])

	if _, err := s.GetStream(streamId); err != nil {
		return err
	}
	iw := grpcx.NewIOStreamWrapper(stream)
	if err := s.AgentConnected(streamId, iw); err != nil {
		return err
	}
	iw.Wait()
	return nil
}

func (s *ServerHandler) LookupGeoIP(c context.Context, r *pb.GeoIP) (*pb.GeoIP, error) {
	var clientID uint64
	var err error
	if clientID, err = s.Auth.Check(c); err != nil {
		return nil, err
	}

	// 根据内置数据库查询 IP 地理位置
	record := &geoip.IPInfo{}
	ip := r.GetIp()
	netIP := net.ParseIP(ip)
	location, err := geoip.Lookup(netIP, record)
	if err != nil {
		return nil, err
	}

	// 将地区码写入到 Host
	singleton.ServerLock.RLock()
	defer singleton.ServerLock.RUnlock()
	if singleton.ServerList[clientID].Host == nil {
		return nil, fmt.Errorf("host not found")
	}
	singleton.ServerList[clientID].Host.CountryCode = location

	return &pb.GeoIP{Ip: ip, CountryCode: location}, nil
}

// 以下代码必须保留，以确保前端功能正常
func updateTrafficDisplay(serverID uint64, inTransfer, outTransfer uint64) {
	// 确保同步到前端显示
	singleton.UpdateTrafficStats(serverID, inTransfer, outTransfer)
}

// checkAndResetCycleTraffic 检查并重置周期流量
// 根据AlertRule中定义的transfer_all_cycle规则重置累计流量
func checkAndResetCycleTraffic(clientID uint64) {
	singleton.AlertsLock.RLock()
	defer singleton.AlertsLock.RUnlock()

	// 遍历所有启用的报警规则
	for _, alert := range singleton.Alerts {
		if !alert.Enabled() {
			continue
		}

		// 检查规则是否包含此服务器
		shouldMonitorServer := false
		var transferRule *model.Rule

		for i := range alert.Rules {
			rule := &alert.Rules[i]
			if !rule.IsTransferDurationRule() {
				continue
			}

			// 检查规则覆盖范围
			if rule.Cover == model.RuleCoverAll {
				// 监控全部服务器，但排除了此服务器
				if rule.Ignore[clientID] {
					continue
				}
			} else if rule.Cover == model.RuleCoverIgnoreAll {
				// 忽略全部服务器，但指定监控了此服务器
				if !rule.Ignore[clientID] {
					continue
				}
			}

			shouldMonitorServer = true
			transferRule = rule
			break
		}

		if !shouldMonitorServer || transferRule == nil {
			continue
		}

		// 获取当前周期的开始时间
		currentCycleStart := transferRule.GetTransferDurationStart()
		currentCycleEnd := transferRule.GetTransferDurationEnd()

		// 检查周期是否已经发生变化（新周期开始）
		server := singleton.ServerList[clientID]
		lastResetTime := time.Time{}

		// 从CycleTransferStats获取上次重置时间的记录
		if stats, exists := singleton.AlertsCycleTransferStatsStore[alert.ID]; exists {
			if nextUpdate, hasUpdate := stats.NextUpdate[clientID]; hasUpdate {
				// 使用NextUpdate时间作为参考，判断是否进入新周期
				if nextUpdate.Before(currentCycleStart) {
					lastResetTime = nextUpdate
				}
			}
		}

		// 检查是否需要重置：当前时间已经进入新周期，且之前没有在这个周期重置过
		needReset := false
		now := time.Now()

		if lastResetTime.IsZero() {
			// 第一次运行，不需要重置，只记录时间
			// 首次检查周期流量，静默处理
		} else if now.After(currentCycleStart) && lastResetTime.Before(currentCycleStart) {
			// 当前时间已过周期开始时间，且上次重置在当前周期开始之前
			needReset = true
		}

		if needReset {
			// 重置累计流量
			oldInTransfer := server.CumulativeNetInTransfer
			oldOutTransfer := server.CumulativeNetOutTransfer

			server.CumulativeNetInTransfer = 0
			server.CumulativeNetOutTransfer = 0

			// 重置基准点
			server.PrevTransferInSnapshot = 0
			server.PrevTransferOutSnapshot = 0

			// 周期流量重置完成，静默处理

			// 立即保存到数据库
			updateSQL := "UPDATE servers SET cumulative_net_in_transfer = ?, cumulative_net_out_transfer = ? WHERE id = ?"
			if err := singleton.DB.Exec(updateSQL, 0, 0, clientID).Error; err != nil {
				log.Printf("保存服务器 %s 周期重置流量到数据库失败: %v", server.Name, err)
			}

			// 更新AlertsCycleTransferStatsStore中的重置时间记录
			if stats, exists := singleton.AlertsCycleTransferStatsStore[alert.ID]; exists {
				stats.NextUpdate[clientID] = now
				stats.Transfer[clientID] = 0 // 重置显示的流量

				// 更新周期时间信息
				stats.From = currentCycleStart
				stats.To = currentCycleEnd

				// 已更新AlertsCycleTransferStatsStore中的重置记录
			}

			// 发送流量重置通知
			// 格式化流量为人性化显示
			formatTraffic := func(bytes uint64) string {
				const unit = 1024
				if bytes < unit {
					return fmt.Sprintf("%d B", bytes)
				}
				div, exp := uint64(unit), 0
				for n := bytes / unit; n >= unit; n /= unit {
					div *= unit
					exp++
				}
				return fmt.Sprintf("%.2f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
			}

			// 计算上个周期累计流量
			totalOldTraffic := oldInTransfer + oldOutTransfer

			resetMessage := fmt.Sprintf("流量重置通知\n服务器 %s [%s] 的周期流量已重置\n上个周期累计流量: %s (入站=%s, 出站=%s)\n新周期: %s 到 %s\n报警规则: %s",
				server.Name,
				singleton.IPDesensitize(server.Host.IP),
				formatTraffic(totalOldTraffic),
				formatTraffic(oldInTransfer),
				formatTraffic(oldOutTransfer),
				currentCycleStart.Format("2006-01-02 15:04:05"),
				currentCycleEnd.Format("2006-01-02 15:04:05"),
				alert.Name)

			// 创建流量重置通知的静音标签，避免短时间内重复发送
			resetMuteLabel := fmt.Sprintf("traffic-reset-%d-%d", alert.ID, clientID)

			// 使用安全的通知发送方式，防止Goroutine泄漏
			singleton.SafeSendNotification(alert.NotificationTag, resetMessage, &resetMuteLabel, server)
		}

		// 只处理第一个匹配的规则
		break
	}
}
