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
	DatabaseType     string // 数据库类型：sqlite 或 badger
}

var (
	dashboardCliParam DashboardCliParam
)

func init() {
	flag.CommandLine.ParseErrorsWhitelist.UnknownFlags = true
	flag.BoolVarP(&dashboardCliParam.Version, "version", "v", false, "查看当前版本号")
	flag.StringVarP(&dashboardCliParam.ConfigFile, "config", "c", "data/config.yaml", "配置文件路径")
	flag.StringVar(&dashboardCliParam.DatebaseLocation, "db", "data/sqlite.db", "Sqlite3数据库文件路径")
	flag.StringVar(&dashboardCliParam.DatabaseType, "dbtype", "", "数据库类型：sqlite 或 badger，默认使用配置文件中的设置")
	flag.Parse()
}

func initSystem() {
	// 启动 singleton 包下的所有服务
	singleton.LoadSingleton()

	// 等待两秒钟确保所有服务充分初始化
	time.Sleep(2 * time.Second)

	// 加载服务器流量数据并初始化 - 仅在非BadgerDB模式下执行
	if singleton.Conf != nil && singleton.Conf.DatabaseType != "badger" {
		singleton.SyncAllServerTrafficFromDB()
	} else {
		log.Println("使用BadgerDB，执行BadgerDB监控历史清理...")
		count, err := singleton.CleanMonitorHistory()
		if err != nil {
			log.Printf("BadgerDB监控历史清理失败: %v", err)
		} else {
			log.Printf("BadgerDB监控历史清理完成，清理了%d条记录", count)
		}
	}

	// 特别强调：面板重启时必须执行流量重新计算
	singleton.TriggerTrafficRecalculation()

	// 开启流量同步和持久化 - 在BadgerDB模式下使用空实现
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

	// 如果命令行指定了数据库类型，则覆盖配置文件中的设置
	if dashboardCliParam.DatabaseType != "" {
		singleton.Conf.DatabaseType = dashboardCliParam.DatabaseType
	}

	// 根据配置选择数据库类型
	if singleton.Conf.DatabaseType == "badger" {
		// 使用BadgerDB
		log.Println("使用BadgerDB数据库...")

		// 如果命令行没有指定BadgerDB路径，使用默认路径
		badgerPath := "data/badger"
		if dashboardCliParam.DatebaseLocation != "data/sqlite.db" {
			// 用户在命令行指定了路径
			badgerPath = dashboardCliParam.DatebaseLocation
		} else if singleton.Conf.DatabaseLocation != "" {
			// 使用配置文件中的路径
			badgerPath = singleton.Conf.DatabaseLocation
		}

		singleton.InitBadgerDBFromPath(badgerPath)
	} else {
		// 默认使用SQLite
		log.Println("使用SQLite数据库...")
		singleton.InitDBFromPath(dashboardCliParam.DatebaseLocation)
	}

	singleton.InitLocalizer()

	// 初始化Goroutine池，防止内存泄漏
	singleton.InitGoroutinePools()

	initSystem()

	// 创建用于控制所有后台任务的context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = ctx // 将来可能用于goroutine控制

	// 启动所有服务
	// 只在非BadgerDB模式下调用CleanMonitorHistory，BadgerDB模式下已经在initSystem中调用
	if singleton.Conf.DatabaseType != "badger" {
		singleton.CleanMonitorHistory()
	}

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

			// 清理Goroutine池，防止内存泄漏
			singleton.CleanupGoroutinePools()

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
