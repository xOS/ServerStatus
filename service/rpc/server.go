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

		// 检查是否是真实的流量回退
		// 避免因数值溢出导致的错误检测
		if originalNetInTransfer < prevNetIn || originalNetOutTransfer < prevNetOut {
			// 设置重启检测阈值：
			// 1. 流量回退超过1GB且回退幅度超过50%
			// 2. 或者两个方向的流量都大幅回退超过500MB
			const restartThresholdBytes = 1024 * 1024 * 1024 // 1GB
			const restartThresholdSmall = 500 * 1024 * 1024  // 500MB
			const restartPercentThreshold = 0.5              // 50%

			if prevNetIn > 0 && prevNetOut > 0 { // 确保有历史数据
				netInDiff := float64(prevNetIn - originalNetInTransfer)
				netOutDiff := float64(prevNetOut - originalNetOutTransfer)

				// 确保差值合理，防止整数溢出导致的异常大数值
				if netInDiff >= 0 && netOutDiff >= 0 && netInDiff < 1e15 && netOutDiff < 1e15 {
					netInPercent := netInDiff / float64(prevNetIn)
					netOutPercent := netOutDiff / float64(prevNetOut)

					// 重启条件：大幅流量回退
					largeRollback := (netInDiff > restartThresholdBytes && netInPercent > restartPercentThreshold) ||
						(netOutDiff > restartThresholdBytes && netOutPercent > restartPercentThreshold)

					// 或者两个方向都有明显回退
					bothRollback := netInDiff > restartThresholdSmall && netOutDiff > restartThresholdSmall

					if largeRollback || bothRollback {
						isRestart = true
						log.Printf("检测到服务器 %s 可能重启: 入站回退=%.2fMB(%.1f%%), 出站回退=%.2fMB(%.1f%%)",
							singleton.ServerList[clientID].Name,
							netInDiff/(1024*1024), netInPercent*100,
							netOutDiff/(1024*1024), netOutPercent*100)
					}
				}
			}
		}
	}

	// 更新状态中的累计流量
	if singleton.ServerList[clientID].LastActive.IsZero() || isRestart {
		// 首次上线或重启，使用当前流量加上数据库中的累计流量
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
	} else {
		// 正常更新，使用增量计算
		state.NetInTransfer = originalNetInTransfer + singleton.ServerList[clientID].CumulativeNetInTransfer
		state.NetOutTransfer = originalNetOutTransfer + singleton.ServerList[clientID].CumulativeNetOutTransfer
		log.Printf("服务器 %s 正常更新流量: 当前入站=%d, 当前出站=%d, 累计入站=%d, 累计出站=%d",
			singleton.ServerList[clientID].Name,
			originalNetInTransfer,
			originalNetOutTransfer,
			singleton.ServerList[clientID].CumulativeNetInTransfer,
			singleton.ServerList[clientID].CumulativeNetOutTransfer)
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
		log.Printf("检测到服务器 %s 重启，重置流量计数", singleton.ServerList[clientID].Name)

		// 服务器重启时保持累计流量不变，只重置上次记录点
		singleton.ServerList[clientID].PrevTransferInSnapshot = 0
		singleton.ServerList[clientID].PrevTransferOutSnapshot = 0
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
