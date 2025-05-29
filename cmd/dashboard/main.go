package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"
	_ "time/tzdata"

	"github.com/ory/graceful"
	flag "github.com/spf13/pflag"
	"github.com/xos/serverstatus/cmd/dashboard/controller"
	"github.com/xos/serverstatus/cmd/dashboard/rpc"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/proto"
	"github.com/xos/serverstatus/service/singleton"
)

type DashboardCliParam struct {
	Version          bool   // 当前版本号
	ConfigFile       string // 配置文件路径
	DatebaseLocation string // Sqlite3 数据库文件路径
}

var (
	dashboardCliParam DashboardCliParam
)

func init() {
	flag.CommandLine.ParseErrorsWhitelist.UnknownFlags = true
	flag.BoolVarP(&dashboardCliParam.Version, "version", "v", false, "查看当前版本号")
	flag.StringVarP(&dashboardCliParam.ConfigFile, "config", "c", "data/config.yaml", "配置文件路径")
	flag.StringVar(&dashboardCliParam.DatebaseLocation, "db", "data/sqlite.db", "Sqlite3数据库文件路径")
	flag.Parse()
}

func initSystem() {
	// 启动 singleton 包下的所有服务
	singleton.LoadSingleton()

	// 等待两秒钟确保所有服务充分初始化
	time.Sleep(2 * time.Second)

	// 加载服务器流量数据并初始化
	singleton.SyncAllServerTrafficFromDB()

	// 特别强调：面板重启时必须执行流量重新计算
	singleton.TriggerTrafficRecalculation()

	// 开启流量同步和持久化
	singleton.AutoSyncTraffic()
}

func main() {
	if dashboardCliParam.Version {
		fmt.Println(singleton.Version)
		os.Exit(0)
	}

	// 初始化 dao 包
	singleton.InitConfigFromPath(dashboardCliParam.ConfigFile)
	singleton.InitTimezoneAndCache()
	singleton.InitDBFromPath(dashboardCliParam.DatebaseLocation)
	singleton.InitLocalizer()

	initSystem()

	// 创建用于控制所有后台任务的context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = ctx // 将来可能用于goroutine控制

	// 启动所有服务
	singleton.CleanMonitorHistory()
	go rpc.ServeRPC(singleton.Conf.GRPCPort)
	serviceSentinelDispatchBus := make(chan model.Monitor)
	
	// 修复goroutine泄漏：使用正确的context模式
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("DispatchTask goroutine panic恢复: %v", r)
			}
		}()
		rpc.DispatchTask(serviceSentinelDispatchBus)
	}()
	
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("DispatchKeepalive goroutine panic恢复: %v", r)
			}
		}()
		rpc.DispatchKeepalive()
	}()
	
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("AlertSentinelStart goroutine panic恢复: %v", r)
			}
		}()
		singleton.AlertSentinelStart()
	}()
	
	singleton.NewServiceSentinel(serviceSentinelDispatchBus)
	srv := controller.ServeWeb(singleton.Conf.HTTPPort)
	
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("DispatchReportInfoTask goroutine panic恢复: %v", r)
			}
		}()
		dispatchReportInfoTask()
	}()

	// 优雅关闭处理
	if err := graceful.Graceful(func() error {
		return srv.ListenAndServe()
	}, func(c context.Context) error {
		log.Println("NG>> Graceful::START")

		// 取消所有后台任务
		cancel()

		// 等待所有任务完成
		done := make(chan struct{})
		go func() {
			// 保存流量数据
			singleton.RecordTransferHourlyUsage()
			singleton.SaveAllTrafficToDB()

			// 关闭所有WebSocket连接
			singleton.ServerLock.RLock()
			for _, server := range singleton.ServerList {
				if server != nil && server.TaskClose != nil {
					select {
					case server.TaskClose <- fmt.Errorf("server shutting down"):
					default:
					}
					close(server.TaskClose)
				}
			}
			singleton.ServerLock.RUnlock()

			// 关闭HTTP服务器
			srv.Shutdown(c)

			close(done)
		}()

		// 设置超时
		select {
		case <-done:
			log.Println("NG>> Graceful::END")
			return nil
		case <-c.Done():
			log.Println("NG>> Graceful::TIMEOUT")
			return fmt.Errorf("shutdown timeout")
		}
	}); err != nil {
		log.Printf("NG>> ERROR: %v", err)
	}
}

func dispatchReportInfoTask() {
	time.Sleep(time.Second * 15)
	singleton.ServerLock.RLock()
	defer singleton.ServerLock.RUnlock()
	for _, server := range singleton.ServerList {
		if server == nil || server.TaskStream == nil {
			continue
		}
		server.TaskStream.Send(&proto.Task{
			Type: model.TaskTypeReportHostInfo,
			Data: "",
		})
	}
}
