package rpc

import (
	"context"
	"fmt"
	"log"
	"math"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/jinzhu/copier"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/xos/serverstatus/model"
	pb "github.com/xos/serverstatus/proto"
	"github.com/xos/serverstatus/pkg/ddns"
	"github.com/xos/serverstatus/pkg/geoip"
	"github.com/xos/serverstatus/pkg/grpcx"
	"github.com/xos/serverstatus/pkg/utils"
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
	
	// 快速获取服务器信息，立即释放锁
	var serverCopy *model.Server
	var isFirstReport bool
	var prevState *model.HostState
	
	singleton.ServerLock.RLock()
	if server := singleton.ServerList[clientID]; server != nil {
		// 创建服务器信息的副本，避免长时间持锁
		serverCopy = &model.Server{}
		copier.Copy(serverCopy, server)
		isFirstReport = server.LastActive.IsZero()
		if server.State != nil {
			prevState = &model.HostState{}
			copier.Copy(prevState, server.State)
		}
	}
	singleton.ServerLock.RUnlock()
	
	if serverCopy == nil {
		return nil, fmt.Errorf("服务器 %d 未找到", clientID)
	}
	
	// 在锁外处理数据，减少锁竞争
	return s.processServerStateWithoutLock(clientID, serverCopy, &state, isFirstReport, prevState)
}

// processServerStateWithoutLock 在不持有锁的情况下处理服务器状态
func (s *ServerHandler) processServerStateWithoutLock(clientID uint64, serverCopy *model.Server, state *model.HostState, isFirstReport bool, prevState *model.HostState) (*pb.Receipt, error) {
	now := time.Now()
	
	// 保存原始流量数据用于增量计算
	originalNetInTransfer := state.NetInTransfer
	originalNetOutTransfer := state.NetOutTransfer

	// 检查周期流量重置
	checkAndResetCycleTraffic(clientID)

	// 检查是否是服务器重启或网络接口重置
	isRestart := false
	if serverCopy.Host != nil && prevState != nil {
		// 获取之前显示的累计流量值
		prevDisplayIn := prevState.NetInTransfer
		prevDisplayOut := prevState.NetOutTransfer

		// 修改流量回退检测逻辑，增加容错范围
		if (prevDisplayIn > 0 && originalNetInTransfer < prevDisplayIn && float64(prevDisplayIn-originalNetInTransfer)/float64(prevDisplayIn) < 0.1) ||
			(prevDisplayOut > 0 && originalNetOutTransfer < prevDisplayOut && float64(prevDisplayOut-originalNetOutTransfer)/float64(prevDisplayOut) < 0.1) {
			isRestart = false
		} else if (prevDisplayIn > 0 && originalNetInTransfer < prevDisplayIn/2) ||
			(prevDisplayOut > 0 && originalNetOutTransfer < prevDisplayOut/2) {
			isRestart = true
		}
	}

	// 准备数据库更新数据
	dbUpdates := make(map[string]interface{})
	
	// 获取锁快速更新内存状态
	singleton.ServerLock.Lock()
	server := singleton.ServerList[clientID]
	
	// 更新基本状态
	server.IsOnline = true
	server.LastActive = now
	
	// 处理流量计算
	if isFirstReport || isRestart {
		// 首次上线或重启，从数据库读取累计流量
		if isFirstReport {
			var dbServer model.Server
			if err := singleton.DB.First(&dbServer, clientID).Error; err == nil {
				server.CumulativeNetInTransfer = dbServer.CumulativeNetInTransfer
				server.CumulativeNetOutTransfer = dbServer.CumulativeNetOutTransfer
			}
		}
		
		server.PrevTransferInSnapshot = int64(originalNetInTransfer)
		server.PrevTransferOutSnapshot = int64(originalNetOutTransfer)
		state.NetInTransfer = server.CumulativeNetInTransfer
		state.NetOutTransfer = server.CumulativeNetOutTransfer
	} else {
		// 正常增量更新
		s.updateTrafficIncremental(server, state, originalNetInTransfer, originalNetOutTransfer)
	}
	
	// 保存当前状态
	server.State = state
	
	// 准备状态数据保存
	lastState := model.HostState{}
	copier.Copy(&lastState, state)
	server.LastStateBeforeOffline = &lastState
	
	// 检查是否需要数据库更新
	shouldUpdateDB := false
	if server.LastDBUpdateTime.IsZero() || now.Sub(server.LastDBUpdateTime) > 15*time.Minute {
		shouldUpdateDB = true
	} else if prevState != nil {
		// 检查关键状态变化
		if math.Abs(float64(state.CPU-prevState.CPU)) > 25 ||
		   math.Abs(float64(state.MemUsed-prevState.MemUsed)) > (3*1024*1024*1024) ||
		   math.Abs(float64(state.SwapUsed-prevState.SwapUsed)) > (1536*1024*1024) ||
		   math.Abs(float64(state.DiskUsed-prevState.DiskUsed)) > (12*1024*1024*1024) ||
		   math.Abs(float64(state.Load1-prevState.Load1)) > 2.5 {
			if now.Sub(server.LastDBUpdateTime) > 5*time.Minute {
				shouldUpdateDB = true
			}
		}
	}
	
	// 准备数据库更新数据
	if shouldUpdateDB {
		lastStateJSON, err := utils.Json.Marshal(lastState)
		if err == nil {
			server.LastStateJSON = string(lastStateJSON)
			server.LastOnline = server.LastActive
			server.LastDBUpdateTime = now
			
			dbUpdates["last_state_json"] = server.LastStateJSON
			dbUpdates["last_online"] = server.LastOnline
		}
	}
	
	// 检查是否需要保存流量数据
	if time.Since(server.LastFlowSaveTime).Minutes() > 10 {
		server.LastFlowSaveTime = now
		dbUpdates["cumulative_net_in_transfer"] = server.CumulativeNetInTransfer
		dbUpdates["cumulative_net_out_transfer"] = server.CumulativeNetOutTransfer
	}
	
	singleton.ServerLock.Unlock()
	
	// 异步数据库更新，避免阻塞
	if len(dbUpdates) > 0 {
		// 延迟执行，错开不同服务器的更新时间
		delay := time.Duration(clientID%10) * 50 * time.Millisecond
		go func() {
			time.Sleep(delay)
			singleton.AsyncDBUpdate(clientID, "servers", dbUpdates, func(err error) {
				if err != nil {
					log.Printf("异步更新服务器 %d 状态失败: %v", clientID, err)
				}
			})
		}()
	}

	// 同步到前端显示
	updateTrafficDisplay(clientID, server.CumulativeNetInTransfer, server.CumulativeNetOutTransfer)

	// 确保PrevTransferSnapshot值被正确初始化
	if server.PrevTransferInSnapshot == 0 || server.PrevTransferOutSnapshot == 0 {
		singleton.ServerLock.Lock()
		server.PrevTransferInSnapshot = int64(originalNetInTransfer)
		server.PrevTransferOutSnapshot = int64(originalNetOutTransfer)
		singleton.ServerLock.Unlock()
	}

	return &pb.Receipt{Proced: true}, nil
}

// updateTrafficIncremental 增量更新流量数据
func (s *ServerHandler) updateTrafficIncremental(server *model.Server, state *model.HostState, originalNetInTransfer, originalNetOutTransfer uint64) {
	var increaseIn, increaseOut uint64

	// 计算增量（仅在流量增长时计算）
	if server.PrevTransferInSnapshot == 0 {
		server.PrevTransferInSnapshot = int64(originalNetInTransfer)
	}
	if server.PrevTransferOutSnapshot == 0 {
		server.PrevTransferOutSnapshot = int64(originalNetOutTransfer)
	}

	prevIn := uint64(server.PrevTransferInSnapshot)
	prevOut := uint64(server.PrevTransferOutSnapshot)

	// 处理入站流量
	if originalNetInTransfer < prevIn {
		if float64(prevIn-originalNetInTransfer)/float64(prevIn) < 0.1 {
			increaseIn = 0
		} else {
			server.PrevTransferInSnapshot = int64(originalNetInTransfer)
		}
	} else {
		increaseIn = originalNetInTransfer - prevIn
		if server.CumulativeNetInTransfer > 0 &&
			increaseIn > ^uint64(0)-server.CumulativeNetInTransfer {
			log.Printf("警告：服务器 %d 入站流量累计值即将溢出，保持当前值", server.ID)
		} else {
			server.CumulativeNetInTransfer += increaseIn
		}
		server.PrevTransferInSnapshot = int64(originalNetInTransfer)
	}

	// 处理出站流量
	if originalNetOutTransfer < prevOut {
		if float64(prevOut-originalNetOutTransfer)/float64(prevOut) < 0.1 {
			increaseOut = 0
		} else {
			server.PrevTransferOutSnapshot = int64(originalNetOutTransfer)
		}
	} else {
		increaseOut = originalNetOutTransfer - prevOut
		if server.CumulativeNetOutTransfer > 0 &&
			increaseOut > ^uint64(0)-server.CumulativeNetOutTransfer {
			log.Printf("警告：服务器 %d 出站流量累计值即将溢出，保持当前值", server.ID)
		} else {
			server.CumulativeNetOutTransfer += increaseOut
		}
		server.PrevTransferOutSnapshot = int64(originalNetOutTransfer)
	}

	// 显示的流量 = 累计流量
	state.NetInTransfer = server.CumulativeNetInTransfer
	state.NetOutTransfer = server.CumulativeNetOutTransfer
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
