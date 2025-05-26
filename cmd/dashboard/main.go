package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"
	_ "time/tzdata"

	"github.com/ory/graceful"
	"github.com/robfig/cron/v3"
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
	ResetTraffic     bool   // 重置所有服务器的累计流量数据
}

var (
	dashboardCliParam DashboardCliParam
)

func init() {
	flag.CommandLine.ParseErrorsWhitelist.UnknownFlags = true
	flag.BoolVarP(&dashboardCliParam.Version, "version", "v", false, "查看当前版本号")
	flag.StringVarP(&dashboardCliParam.ConfigFile, "config", "c", "data/config.yaml", "配置文件路径")
	flag.StringVar(&dashboardCliParam.DatebaseLocation, "db", "data/sqlite.db", "Sqlite3数据库文件路径")
	flag.BoolVar(&dashboardCliParam.ResetTraffic, "reset-traffic", false, "重置所有服务器的累计流量数据")
	flag.Parse()

	// 设置定时任务
	singleton.Cron = cron.New()

	// 每分钟保存一次流量数据
	if _, err := singleton.Cron.AddFunc("0 * * * * *", singleton.RecordTransferHourlyUsage); err != nil {
		panic(err)
	}

	// 每天凌晨3点清理30天前的数据
	if _, err := singleton.Cron.AddFunc("0 0 3 * * *", func() {
		singleton.CleanCumulativeTransferData(30)
	}); err != nil {
		panic(err)
	}

	// 启动定时任务
	singleton.Cron.Start()
}

// syncAllServerTrafficFromDB 从数据库同步所有服务器的累计流量数据到内存
func syncAllServerTrafficFromDB() {
	log.Println("正在从数据库同步所有服务器的累计流量数据...")

	// 先检查 ServerList 是否已初始化
	if singleton.ServerList == nil {
		log.Println("ServerList 未初始化，跳过流量同步")
		return
	}

	singleton.ServerLock.Lock()
	defer singleton.ServerLock.Unlock()

	count := 0
	for _, server := range singleton.ServerList {
		// 跳过 nil 服务器
		if server == nil {
			continue
		}

		// 从数据库读取最新值
		var dbServer model.Server
		if err := singleton.DB.First(&dbServer, server.ID).Error; err == nil {
			// 只有当数据库中的累计值大于内存中的值时才更新
			if dbServer.CumulativeNetInTransfer > server.CumulativeNetInTransfer ||
				dbServer.CumulativeNetOutTransfer > server.CumulativeNetOutTransfer {

				log.Printf("同步服务器 [%s] 的累计流量: 入站 %d → %d, 出站 %d → %d",
					server.Name,
					server.CumulativeNetInTransfer, dbServer.CumulativeNetInTransfer,
					server.CumulativeNetOutTransfer, dbServer.CumulativeNetOutTransfer)

				server.CumulativeNetInTransfer = dbServer.CumulativeNetInTransfer
				server.CumulativeNetOutTransfer = dbServer.CumulativeNetOutTransfer

				// 同时更新状态中的流量数据
				if server.State != nil {
					// 计算原始流量值（可能为负值）
					var originalNetInTransfer, originalNetOutTransfer uint64

					// 确保不会发生整数下溢
					if server.State.NetInTransfer > server.CumulativeNetInTransfer {
						originalNetInTransfer = server.State.NetInTransfer - server.CumulativeNetInTransfer
					}

					if server.State.NetOutTransfer > server.CumulativeNetOutTransfer {
						originalNetOutTransfer = server.State.NetOutTransfer - server.CumulativeNetOutTransfer
					}

					// 应用新的累计值
					server.State.NetInTransfer = originalNetInTransfer + dbServer.CumulativeNetInTransfer
					server.State.NetOutTransfer = originalNetOutTransfer + dbServer.CumulativeNetOutTransfer
				}

				count++
			}
		}
	}

	if count > 0 {
		log.Printf("成功同步了 %d 个服务器的累计流量数据", count)
	} else {
		log.Println("所有服务器的累计流量数据已是最新")
	}
}

func initSystem() {
	// 启动 singleton 包下的所有服务
	singleton.LoadSingleton()

	// 等待一秒钟确保所有初始化完成
	time.Sleep(time.Second)

	// 从数据库同步流量数据到内存
	syncAllServerTrafficFromDB()

	// 每天的3:30 对 监控记录 和 流量记录 进行清理
	if _, err := singleton.Cron.AddFunc("0 30 3 * * *", singleton.CleanMonitorHistory); err != nil {
		panic(err)
	}

	// 每小时对流量记录进行打点
	if _, err := singleton.Cron.AddFunc("0 0 * * * *", singleton.RecordTransferHourlyUsage); err != nil {
		panic(err)
	}

	// 每10分钟同步一次所有服务器的累计流量
	if _, err := singleton.Cron.AddFunc("0 */10 * * * *", syncAllServerTrafficFromDB); err != nil {
		panic(err)
	}
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

	// 处理重置流量命令
	if dashboardCliParam.ResetTraffic {
		resetAllServerTraffic()
		return
	}

	initSystem()

	// TODO 使用 cmux 在同一端口服务 HTTP 和 gRPC
	singleton.CleanMonitorHistory()
	go rpc.ServeRPC(singleton.Conf.GRPCPort)
	serviceSentinelDispatchBus := make(chan model.Monitor) // 用于传递服务监控任务信息的channel
	go rpc.DispatchTask(serviceSentinelDispatchBus)
	go rpc.DispatchKeepalive()
	go singleton.AlertSentinelStart()
	singleton.NewServiceSentinel(serviceSentinelDispatchBus)
	srv := controller.ServeWeb(singleton.Conf.HTTPPort)
	go dispatchReportInfoTask()
	if err := graceful.Graceful(func() error {
		return srv.ListenAndServe()
	}, func(c context.Context) error {
		log.Println("NG>> Graceful::START")
		
		// 保存流量数据
		singleton.RecordTransferHourlyUsage()
		
		// 优雅关闭流量管理器
		tm := singleton.GetTrafficManager()
		if err := tm.Shutdown(); err != nil {
			log.Printf("流量管理器关闭失败: %v", err)
		}
		
		log.Println("NG>> Graceful::END")
		srv.Shutdown(c)
		return nil
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

// resetAllServerTraffic 重置所有服务器的累计流量数据
func resetAllServerTraffic() {
	fmt.Println("正在重置所有服务器的累计流量数据...")

	// 首先清空数据库中的累计流量数据
	err := singleton.DB.Model(&model.Server{}).Updates(map[string]interface{}{
		"cumulative_net_in_transfer":  0,
		"cumulative_net_out_transfer": 0,
	}).Error

	if err != nil {
		fmt.Printf("重置数据库中的累计流量失败: %v\n", err)
		return
	}

	// 加载服务器列表
	singleton.LoadSingleton()

	// 重置内存中的累计流量数据
	singleton.ServerLock.Lock()
	defer singleton.ServerLock.Unlock()

	count := 0
	for _, server := range singleton.ServerList {
		if server.CumulativeNetInTransfer > 0 || server.CumulativeNetOutTransfer > 0 {
			fmt.Printf("重置服务器 [%s] 的累计流量: 入站 %d → 0, 出站 %d → 0\n",
				server.Name,
				server.CumulativeNetInTransfer,
				server.CumulativeNetOutTransfer)

			server.CumulativeNetInTransfer = 0
			server.CumulativeNetOutTransfer = 0

			// 同时重置状态中的流量数据
			if server.State != nil {
				server.State.NetInTransfer = 0
				server.State.NetOutTransfer = 0
			}

			// 重置增量计算的基准点
			server.PrevTransferInSnapshot = 0
			server.PrevTransferOutSnapshot = 0

			count++
		}
	}

	fmt.Printf("成功重置了 %d 个服务器的累计流量数据\n", count)
	fmt.Println("重置完成，请重启应用程序以使更改生效")
}
