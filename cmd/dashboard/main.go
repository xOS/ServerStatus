package main

import (
	"context"
	"log"

	"github.com/ory/graceful"
	"github.com/xos/serverstatus/cmd/dashboard/controller"
	"github.com/xos/serverstatus/cmd/dashboard/rpc"
	"github.com/xos/serverstatus/service/singleton"
)

func init() {
	// 初始化 dao 包
	singleton.Init()
	singleton.InitConfigFromPath("data/config.yaml")
	singleton.InitDBFromPath("data/sqlite.db")
	initSystem()
}

func initSystem() {
	// 启动 singleton 包下的所有服务
	singleton.LoadSingleton()

	// 每天的3:30 对 监控记录 和 流量记录 进行清理
	if _, err := singleton.Cron.AddFunc("0 30 3 * * *", singleton.CleanMonitorHistory); err != nil {
		panic(err)
	}

	// 每小时对流量记录进行打点
	if _, err := singleton.Cron.AddFunc("0 0 * * * *", singleton.RecordTransferHourlyUsage); err != nil {
		panic(err)
	}
}

func main() {
	singleton.CleanMonitorHistory()
	go rpc.ServeRPC(singleton.Conf.GRPCPort)
	go rpc.DispatchKeepalive()
	go singleton.AlertSentinelStart()
	srv := controller.ServeWeb(singleton.Conf.HTTPPort)
	graceful.Graceful(func() error {
		return srv.ListenAndServe()
	}, func(c context.Context) error {
		log.Println("NG>> Graceful::START")
		singleton.RecordTransferHourlyUsage()
		log.Println("NG>> Graceful::END")
		srv.Shutdown(c)
		return nil
	})
}