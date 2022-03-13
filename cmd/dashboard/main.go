package main

import (
	"context"
	"log"
	"time"

	"github.com/ory/graceful"
	"github.com/patrickmn/go-cache"
	"github.com/robfig/cron/v3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/xos/probe-lite/cmd/dashboard/controller"
	"github.com/xos/probe-lite/cmd/dashboard/rpc"
	"github.com/xos/probe-lite/model"
	"github.com/xos/probe-lite/service/singleton"
)

func init() {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		panic(err)
	}

	// 初始化 dao 包
	singleton.Conf = &model.Config{}
	singleton.Cron = cron.New(cron.WithSeconds(), cron.WithLocation(shanghai))
	singleton.ServerList = make(map[uint64]*model.Server)
	singleton.SecretToID = make(map[string]uint64)

	err = singleton.Conf.Read("data/config.yaml")
	if err != nil {
		panic(err)
	}
	singleton.DB, err = gorm.Open(sqlite.Open("data/sqlite.db"), &gorm.Config{
		CreateBatchSize: 200,
	})
	if err != nil {
		panic(err)
	}
	if singleton.Conf.Debug {
		singleton.DB = singleton.DB.Debug()
	}
	if singleton.Conf.GRPCPort == 0 {
		singleton.Conf.GRPCPort = 2222
	}
	singleton.Cache = cache.New(5*time.Minute, 10*time.Minute)

	initSystem()
}

func initSystem() {
	singleton.DB.AutoMigrate(model.Server{}, model.User{},
		model.Notification{}, model.AlertRule{}, model.Cron{}, model.Transfer{})

	singleton.LoadNotifications()
	loadServers() //加载服务器列表
}

func recordTransferHourlyUsage() {
	singleton.ServerLock.Lock()
	defer singleton.ServerLock.Unlock()
	now := time.Now()
	nowTrimSeconds := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, time.Local)
	var txs []model.Transfer
	for id, server := range singleton.ServerList {
		tx := model.Transfer{
			ServerID: id,
			In:       server.State.NetInTransfer - uint64(server.PrevHourlyTransferIn),
			Out:      server.State.NetOutTransfer - uint64(server.PrevHourlyTransferOut),
		}
		if tx.In == 0 && tx.Out == 0 {
			continue
		}
		server.PrevHourlyTransferIn = int64(server.State.NetInTransfer)
		server.PrevHourlyTransferOut = int64(server.State.NetOutTransfer)
		tx.CreatedAt = nowTrimSeconds
		txs = append(txs, tx)
	}
	if len(txs) == 0 {
		return
	}
	log.Println("NG>> Cron 流量统计入库", len(txs), singleton.DB.Create(txs).Error)
}

func loadServers() {
	var servers []model.Server
	singleton.DB.Find(&servers)
	for _, s := range servers {
		innerS := s
		innerS.Host = &model.Host{}
		innerS.State = &model.HostState{}
		singleton.ServerList[innerS.ID] = &innerS
		singleton.SecretToID[innerS.Secret] = innerS.ID
	}
	singleton.ReSortServer()
}

func main() {
	go rpc.ServeRPC(singleton.Conf.GRPCPort)
	go rpc.DispatchKeepalive()
	go singleton.AlertSentinelStart()
	srv := controller.ServeWeb(singleton.Conf.HTTPPort)
	graceful.Graceful(func() error {
		return srv.ListenAndServe()
	}, func(c context.Context) error {
		log.Println("NG>> Graceful::START")
		recordTransferHourlyUsage()
		log.Println("NG>> Graceful::END")
		srv.Shutdown(c)
		return nil
	})
}