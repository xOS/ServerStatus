package main

import (
	"context"

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
}

func main() {
	go rpc.ServeRPC(singleton.Conf.GRPCPort)
	go singleton.AlertSentinelStart()
	srv := controller.ServeWeb(singleton.Conf.HTTPPort)
	graceful.Graceful(func() error {
		return srv.ListenAndServe()
	}, func(c context.Context) error {
		srv.Shutdown(c)
		return nil
	})
}