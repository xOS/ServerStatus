package rpc

import (
	"context"
	"fmt"
	"log"
	"math"
	"net"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/xos/serverstatus/db"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/ddns"
	"github.com/xos/serverstatus/pkg/geoip"
	"github.com/xos/serverstatus/pkg/grpcx"
	"github.com/xos/serverstatus/pkg/utils"
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

// GetGoroutineStats 获取 goroutine 统计信息
func GetGoroutineStats() (total int, requestTasks int64) {
	return runtime.NumGoroutine(), activeRequestTaskGoroutines
}

// ForceCleanupStaleConnections 强制清理僵尸连接（紧急情况使用）
func ForceCleanupStaleConnections() int {
	cleaned := 0
	singleton.ServerLock.Lock()
	defer singleton.ServerLock.Unlock()

	// 当goroutine数量过多时，更激进地清理连接
	totalGoroutines := runtime.NumGoroutine()
	cleanupThreshold := 10 * time.Minute

	if totalGoroutines > 400 {
		cleanupThreshold = 5 * time.Minute // 更激进的清理
	}
	if totalGoroutines > 450 {
		cleanupThreshold = 2 * time.Minute // 非常激进的清理
	}

	for serverID, server := range singleton.ServerList {
		if server != nil && server.TaskClose != nil {
			// 检查连接是否长时间无活动
			if time.Since(server.LastActive) > cleanupThreshold {
				server.TaskCloseLock.Lock()
				if server.TaskClose != nil {
					// 强制关闭僵尸连接
					select {
					case server.TaskClose <- fmt.Errorf("force cleanup stale connection"):
					default:
					}
					server.TaskClose = nil
					server.TaskStream = nil
					cleaned++
					log.Printf("强制清理服务器 %d 的僵尸连接（无活动时间: %v）", serverID, time.Since(server.LastActive))
				}
				server.TaskCloseLock.Unlock()
			}
		}
	}

	if cleaned > 0 {
		log.Printf("强制清理了 %d 个僵尸连接，当前goroutine数量: %d", cleaned, runtime.NumGoroutine())
	}

	return cleaned
}

var ServerHandlerSingleton *ServerHandler

// goroutine 计数器，用于监控 RequestTask goroutine 数量
var (
	activeRequestTaskGoroutines int64
	maxRequestTaskGoroutines    int64 = 50 // 大幅降低最大允许的 RequestTask goroutine 数量

	// goroutine清理控制变量
	lastGoroutineCleanupTime time.Time
	lastCleanupMutex         sync.Mutex

	// 周期流量检测节流：避免在高频上报路径中频繁获取 AlertsLock
	cycleCheckMu   sync.Mutex
	lastCycleCheck = make(map[uint64]time.Time)
)

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
			if server := singleton.ServerList[clientID]; server != nil {
				// 手动复制关键字段，避免并发安全问题
				curServer.ID = server.ID
				curServer.Name = server.Name
				curServer.Tag = server.Tag
				curServer.Note = server.Note
				curServer.PublicNote = server.PublicNote
				curServer.IsOnline = server.IsOnline
				curServer.LastActive = server.LastActive
			}

			// 计算执行时间
			startTime := time.Now().Add(time.Second * -1 * time.Duration(r.GetDelay()))
			endTime := time.Now()

			if cr.PushSuccessful && r.GetSuccessful() {
				message := fmt.Sprintf("[%s]\n任务名称: %s\n执行设备: %s (ID:%d)\n开始时间: %s\n结束时间: %s\n执行结果: 成功",
					singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{
						MessageID: "ScheduledTaskExecutedSuccessfully",
					}),
					cr.Name,
					singleton.ServerList[clientID].Name, clientID,
					startTime.Format("2006-01-02 15:04:05"),
					endTime.Format("2006-01-02 15:04:05"))

				singleton.SafeSendNotification(cr.NotificationTag, message, nil, &curServer)
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
			// 根据数据库类型选择不同的更新方式
			if singleton.Conf.DatabaseType == "badger" {
				// 使用BadgerDB更新任务执行时间
				if db.DB != nil {
					cronOps := db.NewCronOps(db.DB)
					cr.LastExecutedAt = time.Now().Add(time.Second * -1 * time.Duration(r.GetDelay()))
					cr.LastResult = r.GetSuccessful()
					err := cronOps.SaveCron(cr)
					if err != nil {
						log.Printf("BadgerDB更新任务执行时间失败: %v", err)
					} else {
						log.Printf("任务 %s 执行完成，结束时间: %s", cr.Name, cr.LastExecutedAt.Format("2006-01-02 15:04:05"))
					}
				}
			} else if singleton.DB != nil {
				// 使用SQLite更新任务执行时间
				singleton.DB.Model(cr).Updates(model.Cron{
					LastExecutedAt: time.Now().Add(time.Second * -1 * time.Duration(r.GetDelay())),
					LastResult:     r.GetSuccessful(),
				})
			}
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

	// 检查 goroutine 数量限制，防止 goroutine 泄漏导致程序崩溃
	currentGoroutines := func() int64 {
		// 使用原子操作获取当前活跃的 RequestTask goroutine 数量
		current := activeRequestTaskGoroutines
		total := int64(runtime.NumGoroutine())

		// 修复根本问题后，使用更合理的阈值
		if total > 500 { // 适中的阈值
			// 使用包级变量控制清理频率，避免每次连接都清理
			lastCleanupMutex.Lock()
			now := time.Now()
			// 至少间隔1分钟才进行一次清理
			if now.Sub(lastGoroutineCleanupTime) > 1*time.Minute {
				lastGoroutineCleanupTime = now
				lastCleanupMutex.Unlock()

				log.Printf("警告：总 goroutine 数量过多 (%d)，尝试强制清理", total)
				cleaned := ForceCleanupStaleConnections()
				if cleaned > 0 {
					log.Printf("强制清理了 %d 个连接，当前 goroutine 数量: %d", cleaned, runtime.NumGoroutine())
				}

				// 只有在极端情况下才拒绝连接，避免数据丢失
				if runtime.NumGoroutine() > 1000 {
					log.Printf("清理后 goroutine 数量仍过多 (%d)，暂时拒绝新的 RequestTask 连接", runtime.NumGoroutine())
					return -1
				}
			} else {
				lastCleanupMutex.Unlock()
				// 只有在极端情况下才拒绝连接
				if total > 1000 {
					return -1
				}
			}
		}

		// 如果 RequestTask goroutine 数量超过限制，拒绝新连接
		if current >= maxRequestTaskGoroutines {
			log.Printf("警告：RequestTask goroutine 数量达到限制 (%d/%d)，拒绝新连接", current, maxRequestTaskGoroutines)
			return -1
		}

		return current
	}()

	if currentGoroutines == -1 {
		return fmt.Errorf("服务器负载过高，请稍后重试")
	}

	// 增加活跃 goroutine 计数
	activeRequestTaskGoroutines++
	defer func() {
		// 确保在函数退出时减少计数
		activeRequestTaskGoroutines--
	}()

	// 使用带缓冲的通道避免阻塞
	closeCh := make(chan error, 1)

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

	// 创建一个带超时的上下文，确保所有goroutine都能正确退出
	// 修复：使用更合理的超时时间，避免正常连接被误杀
	ctx, cancel := context.WithTimeout(stream.Context(), 5*time.Minute)
	defer cancel()

	// 根本修复：不再创建额外的监控goroutine
	// 所有逻辑都在主goroutine中处理，避免goroutine泄漏

	defer func() {
		// 清理资源
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

	// 根本修复：简化为单一的等待逻辑，避免复杂的goroutine交互
	select {
	case err := <-closeCh:
		// 检查是否为网络连接错误
		if isConnectionError(err) {
			// 只在Debug模式下记录连接断开日志，减少日志噪音
			if singleton.Conf.Debug {
				log.Printf("服务器 %d 连接断开: %v", clientID, err)
			}
			return nil // 将网络连接错误视为正常断开
		}
		return err
	case <-ctx.Done():
		// 连接取消或超时，正常退出
		return nil
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
		serverCopy = &model.Server{
			Common: model.Common{
				ID:        server.ID,
				CreatedAt: server.CreatedAt,
				UpdatedAt: server.UpdatedAt,
			},
			Name:                     server.Name,
			Tag:                      server.Tag,
			Secret:                   server.Secret,
			Note:                     server.Note,
			PublicNote:               server.PublicNote,
			DisplayIndex:             server.DisplayIndex,
			HideForGuest:             server.HideForGuest,
			EnableDDNS:               server.EnableDDNS,
			DDNSProfiles:             append([]uint64(nil), server.DDNSProfiles...),
			IsOnline:                 server.IsOnline,
			LastActive:               server.LastActive,
			LastOnline:               server.LastOnline,
			CumulativeNetInTransfer:  server.CumulativeNetInTransfer,
			CumulativeNetOutTransfer: server.CumulativeNetOutTransfer,
			LastDBUpdateTime:         server.LastDBUpdateTime,
		}

		// 复制Host信息
		if server.Host != nil {
			serverCopy.Host = &model.Host{
				Platform:        server.Host.Platform,
				PlatformVersion: server.Host.PlatformVersion,
				CPU:             append([]string(nil), server.Host.CPU...),
				MemTotal:        server.Host.MemTotal,
				DiskTotal:       server.Host.DiskTotal,
				SwapTotal:       server.Host.SwapTotal,
				Arch:            server.Host.Arch,
				Virtualization:  server.Host.Virtualization,
				BootTime:        server.Host.BootTime,
				IP:              server.Host.IP,
				CountryCode:     server.Host.CountryCode,
				Version:         server.Host.Version,
			}
		}

		isFirstReport = server.LastActive.IsZero()
		if server.State != nil {
			prevState = &model.HostState{
				CPU:            server.State.CPU,
				MemUsed:        server.State.MemUsed,
				SwapUsed:       server.State.SwapUsed,
				DiskUsed:       server.State.DiskUsed,
				NetInTransfer:  server.State.NetInTransfer,
				NetOutTransfer: server.State.NetOutTransfer,
				NetInSpeed:     server.State.NetInSpeed,
				NetOutSpeed:    server.State.NetOutSpeed,
				Uptime:         server.State.Uptime,
				Load1:          server.State.Load1,
				Load5:          server.State.Load5,
				Load15:         server.State.Load15,
				TcpConnCount:   server.State.TcpConnCount,
				UdpConnCount:   server.State.UdpConnCount,
				ProcessCount:   server.State.ProcessCount,
			}
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

	// 检查是否是服务器重启或网络接口重置 - 优化频繁重启场景
	isRestart := false
	if serverCopy.Host != nil && prevState != nil {
		// 获取之前显示的累计流量值
		prevDisplayIn := prevState.NetInTransfer
		prevDisplayOut := prevState.NetOutTransfer

		// 优化重启检测逻辑，减少频繁重启时的误判
		// 只有在流量大幅回退（超过80%）且上次活跃时间超过5分钟时才认为是重启
		timeSinceLastActive := now.Sub(serverCopy.LastActive)

		if timeSinceLastActive > 5*time.Minute {
			// 长时间离线后重新上线，检查流量回退
			if (prevDisplayIn > 0 && originalNetInTransfer < prevDisplayIn/5) ||
				(prevDisplayOut > 0 && originalNetOutTransfer < prevDisplayOut/5) {
				isRestart = true
			}
		}
		// 短时间内的重连不认为是重启，继续累加流量
	}

	// 准备数据库更新数据
	dbUpdates := make(map[string]interface{})

	// 第一阶段：读取必要数据（短暂持读锁）
	var needDBQuery bool
	var dbCumulativeIn, dbCumulativeOut uint64

	singleton.ServerLock.RLock()
	if server, exists := singleton.ServerList[clientID]; exists {
		needDBQuery = isFirstReport && (server.CumulativeNetInTransfer == 0 && server.CumulativeNetOutTransfer == 0)
	}
	singleton.ServerLock.RUnlock()

	// 第二阶段：数据库查询（在锁外执行）
	if needDBQuery && singleton.DB != nil {
		var dbServer model.Server
		if err := singleton.DB.First(&dbServer, clientID).Error; err == nil {
			dbCumulativeIn = dbServer.CumulativeNetInTransfer
			dbCumulativeOut = dbServer.CumulativeNetOutTransfer
		}
	}

	// 第三阶段：获取写锁快速更新内存状态
	singleton.ServerLock.Lock()
	server := singleton.ServerList[clientID]

	// 更新基本状态
	server.IsOnline = true
	server.LastActive = now

	// 处理流量计算
	if isFirstReport || isRestart {
		// 首次上线或重启，使用之前查询的累计流量
		if isFirstReport && needDBQuery {
			server.CumulativeNetInTransfer = dbCumulativeIn
			server.CumulativeNetOutTransfer = dbCumulativeOut
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

	// 准备状态数据保存 - 手动复制避免并发安全问题
	lastState := model.HostState{
		CPU:            state.CPU,
		MemUsed:        state.MemUsed,
		SwapUsed:       state.SwapUsed,
		DiskUsed:       state.DiskUsed,
		NetInTransfer:  state.NetInTransfer,
		NetOutTransfer: state.NetOutTransfer,
		NetInSpeed:     state.NetInSpeed,
		NetOutSpeed:    state.NetOutSpeed,
		Uptime:         state.Uptime,
		Load1:          state.Load1,
		Load5:          state.Load5,
		Load15:         state.Load15,
		TcpConnCount:   state.TcpConnCount,
		UdpConnCount:   state.UdpConnCount,
		ProcessCount:   state.ProcessCount,
	}
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
		// 检测到流量回退
		backwardAmount := prevIn - originalNetInTransfer
		backwardPercent := float64(backwardAmount) / float64(prevIn+1)

		if backwardPercent < 0.02 { // 进一步降低到2%，减少频繁重启时的误判
			// 极小幅度回退，可能是统计误差，不计入增量
			increaseIn = 0
		} else if backwardPercent < 0.5 {
			// 中等幅度回退，可能是频繁重启，保持累计流量，只重置基准点
			server.PrevTransferInSnapshot = int64(originalNetInTransfer)
			increaseIn = 0
		} else {
			// 大幅度回退，确实是重启，重置基准点
			server.PrevTransferInSnapshot = int64(originalNetInTransfer)
			increaseIn = 0
		}
	} else {
		// 正常增量
		increaseIn = originalNetInTransfer - prevIn
		// 检查增量是否合理（防止异常大值）- 调整阈值为10TB，更适合高流量服务器
		if increaseIn > 10*1024*1024*1024*1024 { // 增量超过10TB，可能是异常值
			log.Printf("警告：服务器 %d 入站流量增量异常大 (%d)，可能是统计错误，本次不计入", server.ID, increaseIn)
			increaseIn = 0
		} else if server.CumulativeNetInTransfer > 0 &&
			increaseIn > ^uint64(0)-server.CumulativeNetInTransfer {
			// 溢出保护
			log.Printf("警告：服务器 %d 入站流量累计值即将溢出，保持当前值", server.ID)
		} else {
			// 正常累加
			server.CumulativeNetInTransfer += increaseIn
		}
		server.PrevTransferInSnapshot = int64(originalNetInTransfer)
	}

	// 处理出站流量
	if originalNetOutTransfer < prevOut {
		// 检测到流量回退
		backwardAmount := prevOut - originalNetOutTransfer
		backwardPercent := float64(backwardAmount) / float64(prevOut+1)

		if backwardPercent < 0.02 { // 进一步降低到2%，减少频繁重启时的误判
			// 极小幅度回退，可能是统计误差，不计入增量
			increaseOut = 0
		} else if backwardPercent < 0.5 {
			// 中等幅度回退，可能是频繁重启，保持累计流量，只重置基准点
			server.PrevTransferOutSnapshot = int64(originalNetOutTransfer)
			increaseOut = 0
		} else {
			// 大幅度回退，确实是重启，重置基准点
			server.PrevTransferOutSnapshot = int64(originalNetOutTransfer)
			increaseOut = 0
		}
	} else {
		// 正常增量
		increaseOut = originalNetOutTransfer - prevOut
		// 检查增量是否合理（防止异常大值）- 调整阈值为10TB，更适合高流量服务器
		if increaseOut > 10*1024*1024*1024*1024 { // 增量超过10TB，可能是异常值
			log.Printf("警告：服务器 %d 出站流量增量异常大 (%d)，可能是统计错误，本次不计入", server.ID, increaseOut)
			increaseOut = 0
		} else if server.CumulativeNetOutTransfer > 0 &&
			increaseOut > ^uint64(0)-server.CumulativeNetOutTransfer {
			// 溢出保护
			log.Printf("警告：服务器 %d 出站流量累计值即将溢出，保持当前值", server.ID)
		} else {
			// 正常累加
			server.CumulativeNetOutTransfer += increaseOut
		}
		server.PrevTransferOutSnapshot = int64(originalNetOutTransfer)
	}

	// 显示的流量 = 累计流量
	state.NetInTransfer = server.CumulativeNetInTransfer
	state.NetOutTransfer = server.CumulativeNetOutTransfer

	// 仅在异常大流量增量时记录警告日志
	if increaseIn > 100*1024*1024*1024 || increaseOut > 100*1024*1024*1024 {
		log.Printf("注意：服务器 %d 流量增量较大 (入站=%d, 出站=%d)", server.ID, increaseIn, increaseOut)
	}
}

func (s *ServerHandler) ReportSystemInfo(c context.Context, r *pb.Host) (*pb.Receipt, error) {
	var clientID uint64
	var err error
	if clientID, err = s.Auth.Check(c); err != nil {
		return nil, err
	}
	host := model.PB2Host(r)
	// 只在需要时短暂读取当前服务器的只读快照，避免重复加锁
	singleton.ServerLock.RLock()
	server := singleton.ServerList[clientID]
	if server == nil {
		singleton.ServerLock.RUnlock()
		return &pb.Receipt{Proced: true}, nil
	}
	enableDDNS := server.EnableDDNS
	ddnsProfiles := append([]uint64(nil), server.DDNSProfiles...)
	serverName := server.Name
	var serverHostIP string
	if server.Host != nil {
		serverHostIP = server.Host.IP
	}
	singleton.ServerLock.RUnlock()

	if enableDDNS {
		if host.IP == "" {
			log.Printf("服务器 %s (ID:%d) DDNS已启用但IP为空，跳过更新", serverName, clientID)
		} else if len(ddnsProfiles) == 0 {
			log.Printf("服务器 %s (ID:%d) DDNS已启用但未配置DDNS配置文件", serverName, clientID)
		} else if serverHostIP == host.IP {
			// IP 没有变化，跳过更新（但不记录日志，避免频繁输出）
		} else {
			// IP 发生变化或首次设置，触发 DDNS 更新
			ipv4, ipv6, _ := utils.SplitIPAddr(host.IP)
			providers, err := singleton.GetDDNSProvidersFromProfilesWithServer(
				ddnsProfiles,
				&ddns.IP{Ipv4Addr: ipv4, Ipv6Addr: ipv6},
				serverName,
				clientID,
			)
			if err == nil {
				log.Printf("服务器 %s (ID:%d) IP变化 (%s -> %s)，触发DDNS更新，配置数量: %d",
					server.Name, clientID,
					func() string {
						if server.Host != nil {
							return server.Host.IP
						} else {
							return "无"
						}
					}(),
					host.IP, len(providers))
				for _, provider := range providers {
					go func(provider *ddns.Provider) {
						provider.UpdateDomain(context.Background())
					}(provider)
				}
			} else {
				log.Printf("服务器 %s (ID:%d) 获取DDNS配置时发生错误: %v", server.Name, clientID, err)
			}
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
				"[%s]\n服务器: %s (ID:%d)\nIP变更: %s => %s\n变更时间: %s",
				singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{
					MessageID: "IPChanged",
				}),
				serverName, clientID, singleton.IPDesensitize(serverHostIP),
				singleton.IPDesensitize(host.IP), changeTime,
			),
			muteLabel)
	}

	/**
	 * 这里的 singleton 中的数据都是关机前的旧数据
	 * 当 agent 重启时，bootTime 变大，agent 端会先上报 host 信息，然后上报 state 信息
	 * 优化：只有在 BootTime 显著变化时才认为是真正的重启
	 */
	if singleton.ServerList[clientID].Host != nil {
		oldBootTime := singleton.ServerList[clientID].Host.BootTime
		bootTimeDiff := host.BootTime - oldBootTime

		// 只有在 BootTime 显著增加（超过1小时）或出现回退时才认为是重启
		// 注意：bootTimeDiff 为无符号，不能与 0 比较，回退用 host.BootTime < oldBootTime 判断
		if bootTimeDiff > 3600 || host.BootTime < oldBootTime {
			// 真正的重启：保持累计流量不变，只重置上次记录点
			singleton.ServerList[clientID].PrevTransferInSnapshot = 0
			singleton.ServerList[clientID].PrevTransferOutSnapshot = 0

			// 确保从数据库读取最新的累计流量值（只在重启时读取一次）
			if singleton.Conf.DatabaseType == "badger" {
				// 使用BadgerDB读取累计流量
				if db.DB != nil {
					serverOps := db.NewServerOps(db.DB)
					if server, err := serverOps.GetServer(clientID); err == nil && server != nil {
						singleton.ServerList[clientID].CumulativeNetInTransfer = server.CumulativeNetInTransfer
						singleton.ServerList[clientID].CumulativeNetOutTransfer = server.CumulativeNetOutTransfer
					}
				}
			} else {
				// 使用SQLite读取累计流量
				if singleton.DB != nil {
					var server model.Server
					if err := singleton.DB.First(&server, clientID).Error; err == nil {
						singleton.ServerList[clientID].CumulativeNetInTransfer = server.CumulativeNetInTransfer
						singleton.ServerList[clientID].CumulativeNetOutTransfer = server.CumulativeNetOutTransfer
					}
				}
			}
		}
		// 小幅度的 BootTime 变化不认为是重启，继续正常累加流量
	}

	// 不要冲掉国家码
	if singleton.ServerList[clientID].Host != nil {
		host.CountryCode = singleton.ServerList[clientID].Host.CountryCode
	}

	// 保存完整Host信息到数据库，用于重启后恢复（在锁外序列化）
	hostJSON, err := utils.Json.Marshal(host)
	if err == nil {
		// 根据数据库类型选择不同的保存方式
		if singleton.Conf.DatabaseType == "badger" {
			// 使用BadgerDB保存Host信息
			if db.DB != nil {
				serverOps := db.NewServerOps(db.DB)
				if server, err := serverOps.GetServer(clientID); err == nil && server != nil {
					server.HostJSON = string(hostJSON)
					if err := serverOps.SaveServer(server); err != nil {
						// 静默处理保存失败，避免日志干扰
					}
				}
			}
		} else {
			// 使用SQLite保存Host信息
			if singleton.DB != nil {
				if err := singleton.DB.Exec("UPDATE servers SET host_json = ? WHERE id = ?",
					string(hostJSON), clientID).Error; err != nil {
					// 静默处理保存失败，避免日志干扰
				}
			}
		}
	}

	// 最后一次快速写入 Host 指针
	singleton.ServerLock.Lock()
	if s := singleton.ServerList[clientID]; s != nil {
		s.Host = &host
	}
	singleton.ServerLock.Unlock()
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
	// 节流：同一服务器30秒内只检查一次，避免高频上报导致锁竞争
	cycleCheckMu.Lock()
	if last, ok := lastCycleCheck[clientID]; ok {
		if time.Since(last) < 30*time.Second {
			cycleCheckMu.Unlock()
			return
		}
	}
	lastCycleCheck[clientID] = time.Now()
	cycleCheckMu.Unlock()

	// 1) 快照读取 Alerts 与匹配的规则（读锁极短持有）
	singleton.AlertsLock.RLock()
	var matchingAlert *model.AlertRule
	var transferRule *model.Rule
	for _, alert := range singleton.Alerts {
		if !alert.Enabled() {
			continue
		}
		for i := range alert.Rules {
			rule := &alert.Rules[i]
			if !rule.IsTransferDurationRule() {
				continue
			}
			// 覆盖范围匹配
			if rule.Cover == model.RuleCoverAll {
				if rule.Ignore[clientID] {
					continue
				}
			} else if rule.Cover == model.RuleCoverIgnoreAll {
				if !rule.Ignore[clientID] {
					continue
				}
			}
			matchingAlert = alert
			transferRule = rule
			break
		}
		if matchingAlert != nil {
			break
		}
	}

	// 若无匹配规则，尽早释放锁并返回
	if matchingAlert == nil || transferRule == nil {
		singleton.AlertsLock.RUnlock()
		return
	}

	currentCycleStart := transferRule.GetTransferDurationStart()
	currentCycleEnd := transferRule.GetTransferDurationEnd()

	// 读取上次重置参考时间（仍在读锁下，随后立即释放）
	lastResetTime := time.Time{}
	hasNextUpdate := false
	if stats, exists := singleton.AlertsCycleTransferStatsStore[matchingAlert.ID]; exists && stats != nil {
		if nu, has := stats.NextUpdate[clientID]; has {
			hasNextUpdate = true
			// 如果NextUpdate在当前周期开始之前，说明是上个周期的记录，可以作为重置参考
			if nu.Before(currentCycleStart) {
				lastResetTime = nu
			}
		}
	}
	singleton.AlertsLock.RUnlock()

	// 2) 判断是否需要重置（锁外计算）
	needReset := false
	now := time.Now()

	// 重置条件：有NextUpdate记录且lastResetTime在当前周期之前
	if hasNextUpdate && now.After(currentCycleStart) && !lastResetTime.IsZero() && lastResetTime.Before(currentCycleStart) {
		needReset = true
	}

	if !needReset {
		return
	}

	// 3) 重置累计流量（写锁仅包裹修改内存状态）
	singleton.ServerLock.Lock()
	server := singleton.ServerList[clientID]
	if server == nil {
		singleton.ServerLock.Unlock()
		return
	}
	oldInTransfer := server.CumulativeNetInTransfer
	oldOutTransfer := server.CumulativeNetOutTransfer
	serverName := server.Name
	serverIP := ""
	if server.Host != nil {
		serverIP = server.Host.IP
	}

	server.CumulativeNetInTransfer = 0
	server.CumulativeNetOutTransfer = 0
	server.PrevTransferInSnapshot = 0
	server.PrevTransferOutSnapshot = 0
	singleton.ServerLock.Unlock()

	// 4) 持久化到数据库（锁外）
	if singleton.Conf.DatabaseType == "badger" {
		if db.DB != nil {
			serverOps := db.NewServerOps(db.DB)
			if dbServer, err := serverOps.GetServer(clientID); err == nil && dbServer != nil {
				dbServer.CumulativeNetInTransfer = 0
				dbServer.CumulativeNetOutTransfer = 0
				_ = serverOps.SaveServer(dbServer) // 静默处理错误，避免打扰热路径
			}
		}
	} else {
		if singleton.DB != nil {
			_ = singleton.DB.Exec("UPDATE servers SET cumulative_net_in_transfer = 0, cumulative_net_out_transfer = 0 WHERE id = ?", clientID).Error
		}
	}

	// 5) 更新周期统计存储（需要写锁）
	singleton.AlertsLock.Lock()
	if stats, exists := singleton.AlertsCycleTransferStatsStore[matchingAlert.ID]; exists && stats != nil {
		if stats.NextUpdate == nil {
			stats.NextUpdate = make(map[uint64]time.Time)
		}
		if stats.Transfer == nil {
			stats.Transfer = make(map[uint64]uint64)
		}
		stats.NextUpdate[clientID] = now
		stats.Transfer[clientID] = 0
		stats.From = currentCycleStart
		stats.To = currentCycleEnd
	}
	singleton.AlertsLock.Unlock()

	// 6) 发送通知（锁外）
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
	totalOldTraffic := oldInTransfer + oldOutTransfer
	resetMessage := fmt.Sprintf(
		"流量重置通知\n服务器 %s [%s] 的周期流量已重置\n上个周期累计流量: %s (入站=%s, 出站=%s)\n新周期: %s 到 %s\n事件规则: %s",
		serverName,
		singleton.IPDesensitize(serverIP),
		formatTraffic(totalOldTraffic),
		formatTraffic(oldInTransfer),
		formatTraffic(oldOutTransfer),
		currentCycleStart.Format("2006-01-02 15:04:05"),
		currentCycleEnd.Format("2006-01-02 15:04:05"),
		matchingAlert.Name,
	)
	resetMuteLabel := fmt.Sprintf("traffic-reset-%d-%d", matchingAlert.ID, clientID)
	singleton.SafeSendNotification(matchingAlert.NotificationTag, resetMessage, &resetMuteLabel, nil)
}

// GetConnectionStats 获取连接统计信息
func GetConnectionStats() (int, int, error) {
	activeConns := int(activeRequestTaskGoroutines)
	maxConns := int(maxRequestTaskGoroutines)
	return activeConns, maxConns, nil
}
