package rpc

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/xos/serverstatus/pkg/ddns"
	"github.com/xos/serverstatus/pkg/geoip"
	"github.com/xos/serverstatus/pkg/grpcx"
	"github.com/xos/serverstatus/pkg/utils"

	"github.com/jinzhu/copier"
	"github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/xos/serverstatus/model"
	pb "github.com/xos/serverstatus/proto"
	"github.com/xos/serverstatus/service/singleton"
)

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
			if cr.PushSuccessful && r.GetSuccessful() {
				singleton.SendNotification(cr.NotificationTag, fmt.Sprintf("[%s] %s, %s\n%s", singleton.Localizer.MustLocalize(
					&i18n.LocalizeConfig{
						MessageID: "ScheduledTaskExecutedSuccessfully",
					},
				), cr.Name, singleton.ServerList[clientID].Name, r.GetData()), nil, &curServer)
			}
			if !r.GetSuccessful() {
				singleton.SendNotification(cr.NotificationTag, fmt.Sprintf("[%s] %s, %s\n%s", singleton.Localizer.MustLocalize(
					&i18n.LocalizeConfig{
						MessageID: "ScheduledTaskExecutedFailed",
					},
				), cr.Name, singleton.ServerList[clientID].Name, r.GetData()), nil, &curServer)
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
	closeCh := make(chan error)
	singleton.ServerLock.RLock()
	singleton.ServerList[clientID].TaskCloseLock.Lock()
	// 修复不断的请求 task 但是没有 return 导致内存泄漏
	if singleton.ServerList[clientID].TaskClose != nil {
		close(singleton.ServerList[clientID].TaskClose)
	}
	singleton.ServerList[clientID].TaskStream = stream
	singleton.ServerList[clientID].TaskClose = closeCh
	singleton.ServerList[clientID].TaskCloseLock.Unlock()
	singleton.ServerLock.RUnlock()
	return <-closeCh
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
		// 检查是否有显著的流量回退（任何流量显著减少都视为重启/重置）
		// 获取之前显示的累计流量值
		prevDisplayIn := singleton.ServerList[clientID].State.NetInTransfer
		prevDisplayOut := singleton.ServerList[clientID].State.NetOutTransfer

		// 如果当前原始流量远小于之前的显示流量，说明发生了重启
		// 使用更严格的判断：任何明显的流量回退都认为是重启
		if (prevDisplayIn > 0 && originalNetInTransfer < prevDisplayIn/2) ||
			(prevDisplayOut > 0 && originalNetOutTransfer < prevDisplayOut/2) {
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

		if int64(originalNetInTransfer) > singleton.ServerList[clientID].PrevTransferInSnapshot {
			increaseIn = uint64(int64(originalNetInTransfer) - singleton.ServerList[clientID].PrevTransferInSnapshot)
			singleton.ServerList[clientID].CumulativeNetInTransfer += increaseIn
		}

		if singleton.ServerList[clientID].PrevTransferOutSnapshot == 0 {
			singleton.ServerList[clientID].PrevTransferOutSnapshot = int64(originalNetOutTransfer)
		}

		if int64(originalNetOutTransfer) > singleton.ServerList[clientID].PrevTransferOutSnapshot {
			increaseOut = uint64(int64(originalNetOutTransfer) - singleton.ServerList[clientID].PrevTransferOutSnapshot)
			singleton.ServerList[clientID].CumulativeNetOutTransfer += increaseOut
		}

		// 更新基准点
		singleton.ServerList[clientID].PrevTransferInSnapshot = int64(originalNetInTransfer)
		singleton.ServerList[clientID].PrevTransferOutSnapshot = int64(originalNetOutTransfer)

		// 显示的流量 = 累计流量（不加原始流量，避免重复计算）
		state.NetInTransfer = singleton.ServerList[clientID].CumulativeNetInTransfer
		state.NetOutTransfer = singleton.ServerList[clientID].CumulativeNetOutTransfer

		// 定期保存到数据库（5分钟间隔）
		if time.Since(singleton.ServerList[clientID].LastFlowSaveTime).Minutes() > 5 {
			updateSQL := "UPDATE servers SET cumulative_net_in_transfer = ?, cumulative_net_out_transfer = ? WHERE id = ?"
			if err := singleton.DB.Exec(updateSQL, singleton.ServerList[clientID].CumulativeNetInTransfer, singleton.ServerList[clientID].CumulativeNetOutTransfer, clientID).Error; err == nil {
				singleton.ServerList[clientID].LastFlowSaveTime = time.Now()
			}
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

		// 立即更新到数据库
		singleton.DB.Model(singleton.ServerList[clientID]).Updates(map[string]interface{}{
			"last_state_json": singleton.ServerList[clientID].LastStateJSON,
			"last_online":     singleton.ServerList[clientID].LastOnline,
		})
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
		providers, err := singleton.GetDDNSProvidersFromProfiles(singleton.ServerList[clientID].DDNSProfiles, &ddns.IP{Ipv4Addr: ipv4, Ipv6Addr: ipv6})
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

	// 发送IP变动通知
	if singleton.ServerList[clientID].Host != nil && singleton.Conf.EnableIPChangeNotification &&
		((singleton.Conf.Cover == model.ConfigCoverAll && !singleton.Conf.IgnoredIPNotificationServerIDs[clientID]) ||
			(singleton.Conf.Cover == model.ConfigCoverIgnoreAll && singleton.Conf.IgnoredIPNotificationServerIDs[clientID])) &&
		singleton.ServerList[clientID].Host.IP != "" &&
		host.IP != "" &&
		singleton.ServerList[clientID].Host.IP != host.IP {

		singleton.SendNotification(singleton.Conf.IPChangeNotificationTag,
			fmt.Sprintf(
				"[%s] %s, %s => %s",
				singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{
					MessageID: "IPChanged",
				}),
				singleton.ServerList[clientID].Name, singleton.IPDesensitize(singleton.ServerList[clientID].Host.IP),
				singleton.IPDesensitize(host.IP),
			),
			nil)
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
			log.Printf("服务器 %s：首次检查周期流量，当前周期: %s 到 %s",
				server.Name,
				currentCycleStart.Format("2006-01-02 15:04:05"),
				currentCycleEnd.Format("2006-01-02 15:04:05"))
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

			log.Printf("服务器 %s：周期流量重置完成 [%s规则]",
				server.Name, alert.Name)
			log.Printf("  - 重置前累计流量: 入站=%d, 出站=%d", oldInTransfer, oldOutTransfer)
			log.Printf("  - 重置后累计流量: 入站=%d, 出站=%d",
				server.CumulativeNetInTransfer, server.CumulativeNetOutTransfer)
			log.Printf("  - 当前周期: %s 到 %s",
				currentCycleStart.Format("2006-01-02 15:04:05"),
				currentCycleEnd.Format("2006-01-02 15:04:05"))

			// 立即保存到数据库
			updateSQL := "UPDATE servers SET cumulative_net_in_transfer = ?, cumulative_net_out_transfer = ? WHERE id = ?"
			if err := singleton.DB.Exec(updateSQL, 0, 0, clientID).Error; err != nil {
				log.Printf("保存服务器 %s 周期重置流量到数据库失败: %v", server.Name, err)
			} else {
				log.Printf("服务器 %s 周期重置流量已保存到数据库", server.Name)
			}

			// 更新AlertsCycleTransferStatsStore中的重置时间记录
			if stats, exists := singleton.AlertsCycleTransferStatsStore[alert.ID]; exists {
				stats.NextUpdate[clientID] = now
				stats.Transfer[clientID] = 0 // 重置显示的流量

				// 更新周期时间信息
				stats.From = currentCycleStart
				stats.To = currentCycleEnd

				log.Printf("服务器 %s：已更新AlertsCycleTransferStatsStore中的重置记录", server.Name)
			}
		}

		// 只处理第一个匹配的规则
		break
	}
}
