package rpc

import (
	"context"
	"fmt"
	"time"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/xos/serverstatus/model"
	pb "github.com/xos/serverstatus/proto"
	"github.com/xos/serverstatus/service/singleton"
)

type ServerHandler struct {
	Auth *AuthHandler
}

func (s *ServerHandler) RequestTask(h *pb.Host, stream pb.ServerService_RequestTaskServer) error {
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

func (s *ServerHandler) ReportSystemState(c context.Context, r *pb.State) (*pb.Receipt, error) {
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
	if singleton.Conf.EnableIPChangeNotification &&
		((singleton.Conf.Cover == model.ConfigCoverAll && !singleton.Conf.IgnoredIPNotificationServerIDs[clientID]) ||
			(singleton.Conf.Cover == model.ConfigCoverIgnoreAll && singleton.Conf.IgnoredIPNotificationServerIDs[clientID])) &&
		singleton.ServerList[clientID].Host != nil &&
		singleton.ServerList[clientID].Host.IP != "" &&
		host.IP != "" &&
		singleton.ServerList[clientID].Host.IP != host.IP {
		singleton.SendNotification(singleton.Conf.IPChangeNotificationTag, fmt.Sprintf(
			"#%s"+"\n"+"[%s]"+"\n"+"%s"+"\n"+"%s %s"+"\n"+"%s %s",
			singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{
				MessageID: "Notify",
			}),
			singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{
				MessageID: "IPChanged",
			}),
			singleton.ServerList[clientID].Name,
			singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{
				MessageID: "OldIP",
			}),
			singleton.IPDesensitize(singleton.ServerList[clientID].Host.IP),
			singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{
				MessageID: "NewIP",
			}),
			singleton.IPDesensitize(host.IP),
		), true)
	}

	singleton.ServerList[clientID].Host = &host
	return &pb.Receipt{Proced: true}, nil
}