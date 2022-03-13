package rpc

import (
	"context"
	"fmt"
	"time"

	"github.com/xos/probe-lite/model"
	pb "github.com/xos/probe-lite/proto"
	"github.com/xos/probe-lite/service/singleton"
)

type ProbeHandler struct {
	Auth *AuthHandler
}

func (s *ProbeHandler) ReportTask(c context.Context, r *pb.TaskResult) (*pb.Receipt, error) {
	return &pb.Receipt{Proced: true}, nil
}

func (s *ProbeHandler) RequestTask(h *pb.Host, stream pb.ProbeService_RequestTaskServer) error {
	var clientID uint64
	var err error
	if clientID, err = s.Auth.Check(stream.Context()); err != nil {
		return err
	}
	closeCh := make(chan error)
	singleton.ServerLock.RLock()
	// 修复不断的请求 task 但是没有 return 导致内存泄漏
	if singleton.ServerList[clientID].TaskClose != nil {
		close(singleton.ServerList[clientID].TaskClose)
	}
	singleton.ServerList[clientID].TaskStream = stream
	singleton.ServerList[clientID].TaskClose = closeCh
	singleton.ServerLock.RUnlock()
	return <-closeCh
}

func (s *ProbeHandler) ReportSystemState(c context.Context, r *pb.State) (*pb.Receipt, error) {
	var clientID uint64
	var err error
	if clientID, err = s.Auth.Check(c); err != nil {
		return nil, err
	}
	state := model.PB2State(r)
	singleton.ServerLock.RLock()
	defer singleton.ServerLock.RUnlock()
	singleton.ServerList[clientID].LastActive = time.Now()
	singleton.ServerList[clientID].State = &state

	// 如果从未记录过，先打点，等到小时时间点时入库
	if singleton.ServerList[clientID].PrevHourlyTransferIn == 0 || singleton.ServerList[clientID].PrevHourlyTransferOut == 0 {
		singleton.ServerList[clientID].PrevHourlyTransferIn = int64(state.NetInTransfer)
		singleton.ServerList[clientID].PrevHourlyTransferOut = int64(state.NetOutTransfer)
	}

	return &pb.Receipt{Proced: true}, nil
}

func (s *ProbeHandler) ReportSystemInfo(c context.Context, r *pb.Host) (*pb.Receipt, error) {
	var clientID uint64
	var err error
	if clientID, err = s.Auth.Check(c); err != nil {
		return nil, err
	}
	host := model.PB2Host(r)
	singleton.ServerLock.RLock()
	defer singleton.ServerLock.RUnlock()
	if singleton.Conf.EnableIPChangeNotification &&
		((singleton.Conf.Cover == model.ConfigCoverAll && !singleton.Conf.IgnoredIPNotificationServerIDs[clientID]) ||
			(singleton.Conf.Cover == model.ConfigCoverIgnoreAll && singleton.Conf.IgnoredIPNotificationServerIDs[clientID])) &&
		singleton.ServerList[clientID].Host != nil &&
		singleton.ServerList[clientID].Host.IP != "" &&
		host.IP != "" &&
		singleton.ServerList[clientID].Host.IP != host.IP {
		singleton.SendNotification(fmt.Sprintf(
			"#探针通知" + "\n" + "[IP 变更]" + "\n" + "%s " + "\n" + "旧 IP：%s" + "\n" + "新 IP：%s",
			singleton.ServerList[clientID].Name, singleton.IPDesensitize(singleton.ServerList[clientID].Host.IP), singleton.IPDesensitize(host.IP)), true)
	}

	// 判断是否是机器重启，如果是机器重启要录入最后记录的流量里面
	if singleton.ServerList[clientID].Host.BootTime < host.BootTime {
		singleton.ServerList[clientID].PrevHourlyTransferIn = singleton.ServerList[clientID].PrevHourlyTransferIn - int64(singleton.ServerList[clientID].State.NetInTransfer)
		singleton.ServerList[clientID].PrevHourlyTransferOut = singleton.ServerList[clientID].PrevHourlyTransferOut - int64(singleton.ServerList[clientID].State.NetOutTransfer)
	}

	singleton.ServerList[clientID].Host = &host
	return &pb.Receipt{Proced: true}, nil
}