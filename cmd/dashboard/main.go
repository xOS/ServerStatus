package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/ory/graceful"
	flag "github.com/spf13/pflag"
	"github.com/xos/serverstatus/cmd/dashboard/controller"
	"github.com/xos/serverstatus/cmd/dashboard/rpc"
	"github.com/xos/serverstatus/db"
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

	// 优化：减少同步等待时间，让初始化更快完成
	time.Sleep(1 * time.Second) // 从2秒减少到1秒

	// 延迟执行非关键操作，避免启动时内存峰值
	go func() {
		// 延迟5秒执行流量相关操作
		time.Sleep(5 * time.Second)

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
	}()
}

func main() {
	if dashboardCliParam.Version {
		fmt.Println(singleton.Version)
		os.Exit(0)
	}

	// 避免 cgo getaddrinfo 在某些环境导致的崩溃，强制使用 Go 纯解析器
	// 参见崩溃栈: net._C2func_getaddrinfo -> net.cgoLookupHostIP
	net.DefaultResolver = &net.Resolver{PreferGo: true}

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

		// 获取正确的BadgerDB路径
		badgerPath := "data/badger"
		if dashboardCliParam.DatebaseLocation != "data/sqlite.db" {
			// 用户在命令行指定了路径
			if strings.HasSuffix(dashboardCliParam.DatebaseLocation, ".db") {
				// 如果指定的是SQLite文件路径，转换为BadgerDB目录路径
				dir := filepath.Dir(dashboardCliParam.DatebaseLocation)
				badgerPath = filepath.Join(dir, "badger")
			} else {
				// 如果不是.db文件，假设它是目录路径
				badgerPath = dashboardCliParam.DatebaseLocation
			}
		} else if singleton.Conf.DatabaseLocation != "" {
			// 使用配置文件中的路径，但需要确保它是目录路径
			if strings.HasSuffix(singleton.Conf.DatabaseLocation, ".db") {
				// 如果配置的是SQLite文件路径，转换为BadgerDB目录路径
				dir := filepath.Dir(singleton.Conf.DatabaseLocation)
				badgerPath = filepath.Join(dir, "badger")
			} else {
				// 如果不是.db文件，假设它是目录路径
				badgerPath = singleton.Conf.DatabaseLocation
			}
		}

		log.Printf("BadgerDB将使用目录: %s", badgerPath)
		db.InitBadgerDBFromPath(badgerPath)
	} else {
		// 默认使用SQLite
		log.Println("使用SQLite数据库...")
		singleton.InitDBFromPath(dashboardCliParam.DatebaseLocation)
	}

	singleton.InitLocalizer()

	// 初始化Goroutine池，防止内存泄漏
	singleton.InitGoroutinePools()

	// 创建用于控制所有后台任务的context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = ctx // 用于将来的goroutine控制

	// 【重要】提前启动HTTP服务器，避免前端502错误
	log.Printf("正在提前启动HTTP服务器在端口 %d...", singleton.Conf.HTTPPort)
	srv := controller.ServeWeb(singleton.Conf.HTTPPort)
	log.Printf("HTTP服务器已创建，将在业务初始化完成后启动服务")

	// 启动RPC服务
	go rpc.ServeRPC(singleton.Conf.GRPCPort)
	serviceSentinelDispatchBus := make(chan model.Monitor)

	// 异步初始化系统，避免阻塞HTTP服务器
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("系统初始化goroutine panic恢复: %v", r)
			}
		}()
		initSystem()
		log.Printf("系统初始化完成")

		// 系统初始化完成后，启动依赖singleton.Cron的服务
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("DispatchKeepalive goroutine panic恢复: %v", r)
				}
			}()
			rpc.DispatchKeepalive()
		}()

		// 异步初始化ServiceSentinel，避免阻塞（也依赖Cron）
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("ServiceSentinel初始化goroutine panic恢复: %v", r)
				}
			}()

			log.Printf("正在初始化ServiceSentinel...")
			singleton.NewServiceSentinel(serviceSentinelDispatchBus)
			log.Printf("ServiceSentinel初始化完成")

			// 只在非BadgerDB模式下调用CleanMonitorHistory
			if singleton.Conf.DatabaseType != "badger" {
				singleton.CleanMonitorHistory()
			}
		}()
	}()

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
				log.Printf("AlertSentinelStart goroutine panic恢复: %v", r)
			}
		}()
		singleton.AlertSentinelStart()
	}()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("DispatchReportInfoTask goroutine panic恢复: %v", r)
			}
		}()
		dispatchReportInfoTask()
	}()

	log.Printf("所有服务已启动，HTTP服务器将运行在端口 %d", singleton.Conf.HTTPPort)

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
			defer close(done)
			log.Println("程序正在优雅关闭，保存所有数据...")

			// 1. 首先停止所有定时任务，防止新的goroutine产生
			if singleton.Cron != nil {
				singleton.Cron.Stop()
				log.Println("定时任务已停止")
			}

			// 2. 关闭所有WebSocket连接，防止新的连接
			singleton.ServerLock.Lock()
			for _, server := range singleton.ServerList {
				if server != nil && server.TaskCloseLock != nil {
					server.TaskCloseLock.Lock()
					if server.TaskClose != nil {
						select {
						case server.TaskClose <- fmt.Errorf("server shutting down"):
						default:
						}
						// 不要在这里close，让RequestTask自己处理
						server.TaskClose = nil
					}
					server.TaskStream = nil
					server.TaskCloseLock.Unlock()
				}
			}
			singleton.ServerLock.Unlock()
			log.Println("所有服务器连接已关闭")

			// 3. 清理Goroutine池，防止内存泄漏
			singleton.CleanupGoroutinePools()

			// 4. 保存流量数据
			singleton.RecordTransferHourlyUsage()
			singleton.SaveAllTrafficToDB()

			// 5. 保存所有数据到数据库（BadgerDB模式下特别重要）
			singleton.SaveAllDataToDB()

			// 6. 关闭HTTP服务器
			srv.Shutdown(c)

			log.Println("所有数据已保存，程序关闭完成")
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
