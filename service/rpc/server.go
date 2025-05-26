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

	// 检查是否是服务器重启
	isRestart := false
	if singleton.ServerList[clientID].Host != nil && singleton.ServerList[clientID].State != nil {
		// 获取之前的流量值（原始值，不包含累计）
		prevNetIn := singleton.ServerList[clientID].State.NetInTransfer - singleton.ServerList[clientID].CumulativeNetInTransfer
		prevNetOut := singleton.ServerList[clientID].State.NetOutTransfer - singleton.ServerList[clientID].CumulativeNetOutTransfer

		// 检查是否有流量回退 - 任何流量回退都视为重启
		// 1. 如果有明显回退（入站或出站任意一个显著减少），直接识别为重启
		// 2. 如果出现轻微回退（可能是探针重启但流量统计未完全重置），也识别为重启
		if originalNetInTransfer < prevNetIn || originalNetOutTransfer < prevNetOut {
			// 计算回退量，用于日志记录
			netInDiff := float64(0)
			netOutDiff := float64(0)
			netInPercent := float64(0)
			netOutPercent := float64(0)

			if prevNetIn > 0 && originalNetInTransfer < prevNetIn {
				netInDiff = float64(prevNetIn - originalNetInTransfer)
				netInPercent = netInDiff / float64(prevNetIn) * 100
			}

			if prevNetOut > 0 && originalNetOutTransfer < prevNetOut {
				netOutDiff = float64(prevNetOut - originalNetOutTransfer)
				netOutPercent = netOutDiff / float64(prevNetOut) * 100
			}

			// 任何流量回退都认定为重启
			isRestart = true

			// 记录日志
			if netInDiff > 0 || netOutDiff > 0 {
				log.Printf("检测到服务器 %s 重启: 入站回退=%.2fMB(%.1f%%), 出站回退=%.2fMB(%.1f%%)",
					singleton.ServerList[clientID].Name,
					netInDiff/(1024*1024), netInPercent,
					netOutDiff/(1024*1024), netOutPercent)
			} else {
				log.Printf("检测到服务器 %s 重启: 流量值回退", singleton.ServerList[clientID].Name)
			}
		}
	}

	// 更新状态中的累计流量
	if singleton.ServerList[clientID].LastActive.IsZero() || isRestart {
		// 首次上线或重启，使用当前流量加上数据库中的累计流量
		// 注意：确保每次都从数据库读取最新的累计值，避免使用可能过时的内存值
		var server model.Server
		if err := singleton.DB.First(&server, clientID).Error; err == nil {
			// 使用数据库中的累计值，确保不会丢失数据
			singleton.ServerList[clientID].CumulativeNetInTransfer = server.CumulativeNetInTransfer
			singleton.ServerList[clientID].CumulativeNetOutTransfer = server.CumulativeNetOutTransfer
			log.Printf("从数据库加载服务器 %s 累计流量数据: 入站=%d, 出站=%d",
				singleton.ServerList[clientID].Name,
				server.CumulativeNetInTransfer,
				server.CumulativeNetOutTransfer)

			// 重置基准点，避免后续错误计算
			singleton.ServerList[clientID].PrevTransferInSnapshot = int64(originalNetInTransfer)
			singleton.ServerList[clientID].PrevTransferOutSnapshot = int64(originalNetOutTransfer)
		}

		// 然后再加上当前新上报的流量值 - 探针重启后的原始流量作为新的基准点
		state.NetInTransfer = originalNetInTransfer + singleton.ServerList[clientID].CumulativeNetInTransfer
		state.NetOutTransfer = originalNetOutTransfer + singleton.ServerList[clientID].CumulativeNetOutTransfer

		// 根据状态选择日志消息
		statusMsg := "首次上线"
		if isRestart {
			statusMsg = "重启"
		}

		log.Printf("服务器 %s %s，使用当前流量+累计流量: 当前入站=%d, 当前出站=%d, 累计入站=%d, 累计出站=%d",
			singleton.ServerList[clientID].Name,
			statusMsg,
			originalNetInTransfer,
			originalNetOutTransfer,
			singleton.ServerList[clientID].CumulativeNetInTransfer,
			singleton.ServerList[clientID].CumulativeNetOutTransfer)

		// 将累计流量写入数据库，确保重启后数据不丢失
		// 使用直接的SQL语句来更新，确保数据被写入
		updateSQL := fmt.Sprintf("UPDATE servers SET cumulative_net_in_transfer = %d, cumulative_net_out_transfer = %d WHERE id = %d",
			singleton.ServerList[clientID].CumulativeNetInTransfer,
			singleton.ServerList[clientID].CumulativeNetOutTransfer,
			clientID)

		if err := singleton.DB.Exec(updateSQL).Error; err != nil {
			log.Printf("更新服务器 %s 累计流量到数据库失败: %v", singleton.ServerList[clientID].Name, err)
		} else {
			// 验证数据是否确实保存到了数据库
			var savedValue struct {
				CumulativeNetInTransfer  uint64
				CumulativeNetOutTransfer uint64
			}
			if err := singleton.DB.Raw("SELECT cumulative_net_in_transfer, cumulative_net_out_transfer FROM servers WHERE id = ?", clientID).
				Scan(&savedValue).Error; err == nil {
				log.Printf("服务器 %s 累计流量成功保存到数据库并验证: 入站=%d, 出站=%d",
					singleton.ServerList[clientID].Name,
					savedValue.CumulativeNetInTransfer,
					savedValue.CumulativeNetOutTransfer)

				// 确保内存中的值与数据库一致
				singleton.ServerList[clientID].CumulativeNetInTransfer = savedValue.CumulativeNetInTransfer
				singleton.ServerList[clientID].CumulativeNetOutTransfer = savedValue.CumulativeNetOutTransfer
			} else {
				log.Printf("验证服务器 %s 累计流量保存失败: %v", singleton.ServerList[clientID].Name, err)
			}
		}
	} else {
		// 正常更新，使用增量计算并更新累计流量
		// 先从数据库读取最新的累计流量值，确保使用最准确的数据
		var server model.Server
		if err := singleton.DB.First(&server, clientID).Error; err == nil {
			// 使用数据库中的最新累计值
			singleton.ServerList[clientID].CumulativeNetInTransfer = server.CumulativeNetInTransfer
			singleton.ServerList[clientID].CumulativeNetOutTransfer = server.CumulativeNetOutTransfer
		}

		// 更新累计流量 - 加上新增的流量
		// 通过与上次报告的原始流量进行比较，计算增量
		if singleton.ServerList[clientID].PrevTransferInSnapshot > 0 &&
			int64(originalNetInTransfer) > singleton.ServerList[clientID].PrevTransferInSnapshot {
			// 如果当前流量大于上次记录的原始流量，增加累计流量
			increase := uint64(int64(originalNetInTransfer) - singleton.ServerList[clientID].PrevTransferInSnapshot)
			singleton.ServerList[clientID].CumulativeNetInTransfer += increase
			log.Printf("服务器 %s 入站流量增加: +%d, 累计=%d",
				singleton.ServerList[clientID].Name,
				increase,
				singleton.ServerList[clientID].CumulativeNetInTransfer)
		}

		if singleton.ServerList[clientID].PrevTransferOutSnapshot > 0 &&
			int64(originalNetOutTransfer) > singleton.ServerList[clientID].PrevTransferOutSnapshot {
			// 如果当前流量大于上次记录的原始流量，增加累计流量
			increase := uint64(int64(originalNetOutTransfer) - singleton.ServerList[clientID].PrevTransferOutSnapshot)
			singleton.ServerList[clientID].CumulativeNetOutTransfer += increase
			log.Printf("服务器 %s 出站流量增加: +%d, 累计=%d",
				singleton.ServerList[clientID].Name,
				increase,
				singleton.ServerList[clientID].CumulativeNetOutTransfer)
		}

		// 更新基准点为当前值
		singleton.ServerList[clientID].PrevTransferInSnapshot = int64(originalNetInTransfer)
		singleton.ServerList[clientID].PrevTransferOutSnapshot = int64(originalNetOutTransfer)

		// 设置State中的总流量（原始流量+累计流量）
		state.NetInTransfer = originalNetInTransfer + singleton.ServerList[clientID].CumulativeNetInTransfer
		state.NetOutTransfer = originalNetOutTransfer + singleton.ServerList[clientID].CumulativeNetOutTransfer

		log.Printf("服务器 %s 正常更新流量: 当前入站=%d, 当前出站=%d, 累计入站=%d, 累计出站=%d",
			singleton.ServerList[clientID].Name,
			originalNetInTransfer,
			originalNetOutTransfer,
			singleton.ServerList[clientID].CumulativeNetInTransfer,
			singleton.ServerList[clientID].CumulativeNetOutTransfer)

		// 定期更新累计流量到数据库（按3分钟间隔）
		if time.Since(singleton.ServerList[clientID].LastFlowSaveTime).Minutes() > 3 {
			// 使用直接的SQL语句来更新，确保数据被写入
			updateSQL := fmt.Sprintf("UPDATE servers SET cumulative_net_in_transfer = %d, cumulative_net_out_transfer = %d WHERE id = %d",
				singleton.ServerList[clientID].CumulativeNetInTransfer,
				singleton.ServerList[clientID].CumulativeNetOutTransfer,
				clientID)

			if err := singleton.DB.Exec(updateSQL).Error; err != nil {
				log.Printf("定期更新服务器 %s 累计流量到数据库失败: %v", singleton.ServerList[clientID].Name, err)
			} else {
				// 验证数据是否确实保存到了数据库
				var savedValue struct {
					CumulativeNetInTransfer  uint64
					CumulativeNetOutTransfer uint64
				}
				if err := singleton.DB.Raw("SELECT cumulative_net_in_transfer, cumulative_net_out_transfer FROM servers WHERE id = ?", clientID).
					Scan(&savedValue).Error; err == nil {
					log.Printf("服务器 %s 定期累计流量成功保存并验证: 入站=%d, 出站=%d",
						singleton.ServerList[clientID].Name,
						savedValue.CumulativeNetInTransfer,
						savedValue.CumulativeNetOutTransfer)

					// 确保内存中的值与数据库一致
					singleton.ServerList[clientID].CumulativeNetInTransfer = savedValue.CumulativeNetInTransfer
					singleton.ServerList[clientID].CumulativeNetOutTransfer = savedValue.CumulativeNetOutTransfer
				}
			}
			// 更新最后保存时间
			singleton.ServerList[clientID].LastFlowSaveTime = time.Now()
		}
	}

	// 保存当前状态
	singleton.ServerList[clientID].State = &state

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
		log.Printf("序列化服务器 %s 的最后状态失败: %v", singleton.ServerList[clientID].Name, err)
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
		log.Printf("检测到服务器 %s 重启，重置流量计数基准点", singleton.ServerList[clientID].Name)

		// 服务器重启时保持累计流量不变，只重置上次记录点
		singleton.ServerList[clientID].PrevTransferInSnapshot = 0
		singleton.ServerList[clientID].PrevTransferOutSnapshot = 0

		// 从数据库读取最新的累计流量值
		var server model.Server
		if err := singleton.DB.First(&server, clientID).Error; err == nil {
			singleton.ServerList[clientID].CumulativeNetInTransfer = server.CumulativeNetInTransfer
			singleton.ServerList[clientID].CumulativeNetOutTransfer = server.CumulativeNetOutTransfer
			log.Printf("重启时从数据库加载服务器 %s 累计流量数据: 入站=%d, 出站=%d",
				singleton.ServerList[clientID].Name,
				server.CumulativeNetInTransfer,
				server.CumulativeNetOutTransfer)
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
			log.Printf("保存服务器ID:%d (%s) 的Host配置失败: %v", clientID, singleton.ServerList[clientID].Name, err)
		}
	} else {
		log.Printf("序列化服务器 %s 的Host信息失败: %v", singleton.ServerList[clientID].Name, err)
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
