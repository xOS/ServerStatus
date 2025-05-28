package singleton

import (
	"fmt"
	"log"
	"runtime"
	"time"

	"github.com/patrickmn/go-cache"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/jinzhu/copier"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/utils"
)

var Version = "debug"

var (
	Conf  *model.Config
	Cache *cache.Cache
	DB    *gorm.DB
	Loc   *time.Location
)

func InitTimezoneAndCache() {
	var err error
	Loc, err = time.LoadLocation(Conf.Location)
	if err != nil {
		panic(err)
	}

	// 使用更短的缓存时间，并添加定期清理
	Cache = cache.New(1*time.Minute, 2*time.Minute)

	// 启动定期清理任务
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			Cache.DeleteExpired()
		}
	}()
}

// LoadSingleton 加载子服务并执行
func LoadSingleton() {
	loadNotifications() // 加载通知服务
	loadServers()       // 加载服务器列表
	loadCronTasks()     // 加载定时任务
	loadAPI()
	initNAT()
	initDDNS()

	// 从数据库同步累计流量数据到内存
	SyncAllServerTrafficFromDB()

	// 添加定时检查在线状态的任务，每分钟检查一次
	Cron.AddFunc("0 */1 * * * *", CheckServerOnlineStatus)

	// 添加定时验证流量数据一致性的任务，每5分钟检查一次
	Cron.AddFunc("0 */5 * * * *", VerifyTrafficDataConsistency)

	// 添加定时清理任务，每10分钟执行一次
	Cron.AddFunc("0 */10 * * * *", func() {
		CleanMonitorHistory()
		Cache.DeleteExpired()
		CleanupServerState() // 添加服务器状态清理
		SaveAllTrafficToDB() // 保存流量数据到数据库
	})

	// 添加内存使用监控任务，每5分钟执行一次
	Cron.AddFunc("0 */5 * * * *", func() {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		if m.Alloc > 500*1024*1024 { // 如果内存使用超过500MB
			log.Printf("内存使用警告: %v MB", m.Alloc/1024/1024)
			// 触发GC
			runtime.GC()
		}
	})
}

// InitConfigFromPath 从给出的文件路径中加载配置
func InitConfigFromPath(path string) {
	Conf = &model.Config{}
	err := Conf.Read(path)
	if err != nil {
		panic(err)
	}
}

// InitDBFromPath 从给出的文件路径中加载数据库
func InitDBFromPath(path string) {
	var err error
	DB, err = gorm.Open(sqlite.Open(path), &gorm.Config{
		CreateBatchSize: 200,
	})
	if err != nil {
		panic(err)
	}
	err = DB.AutoMigrate(model.Server{}, model.User{},
		model.Notification{}, model.AlertRule{}, model.Monitor{},
		model.MonitorHistory{}, model.Cron{}, model.Transfer{},
		model.ApiToken{}, model.NAT{}, model.DDNSProfile{}, model.DDNSRecordState{})
	if err != nil {
		panic(err)
	}

	// 检查并添加新字段
	if !DB.Migrator().HasColumn(&model.Server{}, "cumulative_net_in_transfer") {
		err = DB.Migrator().AddColumn(&model.Server{}, "cumulative_net_in_transfer")
		if err != nil {
			log.Println("添加cumulative_net_in_transfer字段失败:", err)
		}
	}

	if !DB.Migrator().HasColumn(&model.Server{}, "cumulative_net_out_transfer") {
		err = DB.Migrator().AddColumn(&model.Server{}, "cumulative_net_out_transfer")
		if err != nil {
			log.Println("添加cumulative_net_out_transfer字段失败:", err)
		}
	}

	if !DB.Migrator().HasColumn(&model.Server{}, "last_state_json") {
		err = DB.Migrator().AddColumn(&model.Server{}, "last_state_json")
		if err != nil {
			log.Println("添加last_state_json字段失败:", err)
		}
	}

	if !DB.Migrator().HasColumn(&model.Server{}, "last_online") {
		err = DB.Migrator().AddColumn(&model.Server{}, "last_online")
		if err != nil {
			log.Println("添加last_online字段失败:", err)
		}
	}

	// 检查host_json字段是否存在，如果不存在则添加
	if !DB.Migrator().HasColumn(&model.Server{}, "host_json") {
		err = DB.Migrator().AddColumn(&model.Server{}, "host_json")
		if err != nil {
			log.Println("添加host_json字段失败:", err)
		}
	}

	// 检查是否需要从last_reported_host表迁移数据到servers表
	var hasLegacyTable bool
	if err := DB.Raw("SELECT 1 FROM sqlite_master WHERE type='table' AND name='last_reported_host'").Scan(&hasLegacyTable).Error; err == nil && hasLegacyTable {
		log.Println("检测到旧的last_reported_host表，开始迁移数据...")

		// 迁移数据
		if err := DB.Exec(`
			UPDATE servers 
			SET host_json = (
				SELECT host_json 
				FROM last_reported_host 
				WHERE last_reported_host.server_id = servers.id
			)
			WHERE id IN (SELECT server_id FROM last_reported_host)
		`).Error; err != nil {
			log.Println("迁移host_json数据失败:", err)
		} else {
			log.Println("迁移host_json数据成功")

			// 删除旧表
			if err := DB.Exec("DROP TABLE last_reported_host").Error; err != nil {
				log.Println("删除last_reported_host表失败:", err)
			} else {
				log.Println("删除last_reported_host表成功")
			}
		}
	}
}

// RecordTransferHourlyUsage 记录每小时流量使用情况
func RecordTransferHourlyUsage() {
	// 废弃TrafficManager, 仅保留函数框架
}

// StartTrafficManager 初始化流量管理器
func StartTrafficManager() {
	// 废弃TrafficManager, 仅保留函数框架
}

// GetTrafficManager 模拟流量管理器，向后兼容
func GetTrafficManager() interface{} {
	return &struct {
		SaveToDatabase func() error
		Shutdown       func() error
	}{
		SaveToDatabase: func() error {
			return nil
		},
		Shutdown: func() error {
			return nil
		},
	}
}

// CleanMonitorHistory 清理无效或过时的监控记录和流量记录
func CleanMonitorHistory() {
	// 使用事务确保数据一致性
	err := DB.Transaction(func(tx *gorm.DB) error {
		// 清理30天前的监控记录
		if err := tx.Unscoped().Delete(&model.MonitorHistory{},
			"created_at < ? OR monitor_id NOT IN (SELECT `id` FROM monitors)",
			time.Now().AddDate(0, 0, -30)).Error; err != nil {
			return err
		}

		// 清理1天前的网络监控记录
		if err := tx.Unscoped().Delete(&model.MonitorHistory{},
			"(created_at < ? AND server_id != 0) OR monitor_id NOT IN (SELECT `id` FROM monitors)",
			time.Now().AddDate(0, 0, -1)).Error; err != nil {
			return err
		}

		// 清理无效的流量记录
		if err := tx.Unscoped().Delete(&model.Transfer{},
			"server_id NOT IN (SELECT `id` FROM servers)").Error; err != nil {
			return err
		}

		// 清理过期的累计流量数据
		CleanCumulativeTransferData(30)

		return nil
	})

	if err != nil {
		log.Printf("清理历史记录失败: %v", err)
	}
}

// CleanCumulativeTransferData 清理累计流量数据
func CleanCumulativeTransferData(days int) {
	// 废弃TrafficManager, 仅保留函数框架
}

// IPDesensitize 根据设置选择是否对IP进行打码处理 返回处理后的IP(关闭打码则返回原IP)
func IPDesensitize(ip string) string {
	if Conf.EnablePlainIPInNotification {
		return ip
	}
	return utils.IPDesensitize(ip)
}

// CheckServerOnlineStatus 检查服务器在线状态，将超时未上报的服务器标记为离线
func CheckServerOnlineStatus() {
	ServerLock.Lock()
	defer ServerLock.Unlock()

	now := time.Now()
	offlineTimeout := time.Minute * 2 // 2分钟无心跳视为离线

	// 检查是否需要重置累计流量数据
	shouldResetTransferStats := checkShouldResetTransferStats()

	for _, server := range ServerList {
		// 已经标记为在线且长时间未活动，标记为离线
		if server.IsOnline && now.Sub(server.LastActive) > offlineTimeout {
			server.IsOnline = false

			// 清理任务连接资源，防止内存泄漏
			server.TaskCloseLock.Lock()
			if server.TaskClose != nil {
				// 安全发送关闭信号，避免向已关闭的通道发送
				select {
				case server.TaskClose <- fmt.Errorf("server offline"):
				default:
					// 通道可能已关闭或已满，忽略
				}
				// 标记通道为nil，但不在这里关闭
				// 让RequestTask方法的goroutine负责关闭
				server.TaskClose = nil
			}
			server.TaskStream = nil
			server.TaskCloseLock.Unlock()

			// 如果还没有保存离线前状态，保存当前状态
			if server.LastStateBeforeOffline == nil && server.State != nil {
				lastState := model.HostState{}
				if err := copier.Copy(&lastState, server.State); err == nil {
					server.LastStateBeforeOffline = &lastState

					// 将最后状态序列化为JSON并保存到数据库
					lastStateJSON, err := utils.Json.Marshal(lastState)
					if err == nil {
						server.LastStateJSON = string(lastStateJSON)
						server.LastOnline = server.LastActive

						// 更新数据库
						DB.Model(server).Updates(map[string]interface{}{
							"last_state_json": server.LastStateJSON,
							"last_online":     server.LastOnline,
						})

						// 确保Host信息也已保存
						if server.Host != nil {
							// 检查Host信息是否为空
							if len(server.Host.CPU) > 0 || server.Host.MemTotal > 0 {
								// 将Host信息保存到servers表
								hostJSON, hostErr := utils.Json.Marshal(server.Host)
								if hostErr == nil && len(hostJSON) > 0 {
									DB.Exec("UPDATE servers SET host_json = ? WHERE id = ?",
										string(hostJSON), server.ID)
								}
							}
						}
					} else {
						log.Printf("序列化服务器 %s 的最后状态失败: %v", server.Name, err)
					}
				}
			}

			// 离线前保存累计流量数据到数据库
			if server.State != nil {
				// 使用事务确保数据一致性
				tx := DB.Begin()
				if err := tx.Model(server).Updates(map[string]interface{}{
					"cumulative_net_in_transfer":  server.State.NetInTransfer,
					"cumulative_net_out_transfer": server.State.NetOutTransfer,
				}).Error; err != nil {
					tx.Rollback()
					log.Printf("保存服务器 %s 的累计流量数据失败: %v", server.Name, err)
				} else {
					if err := tx.Commit().Error; err != nil {
						log.Printf("提交服务器 %s 的累计流量数据事务失败: %v", server.Name, err)
					}
				}
			}
		}

		// 如果需要重置累计流量，则重置服务器的累计流量
		if shouldResetTransferStats {
			server.CumulativeNetInTransfer = 0
			server.CumulativeNetOutTransfer = 0

			// 更新数据库
			DB.Model(server).Updates(map[string]interface{}{
				"cumulative_net_in_transfer":  0,
				"cumulative_net_out_transfer": 0,
			})
		}
	}
}

// checkShouldResetTransferStats 检查是否应该重置流量统计
func checkShouldResetTransferStats() bool {
	// 获取当前时间
	now := time.Now()

	// 如果是本月第一天的凌晨，返回true
	return now.Day() == 1 && now.Hour() == 0 && now.Minute() < 5
}

// SyncAllServerTrafficFromDB 从数据库同步所有服务器的累计流量数据到内存
func SyncAllServerTrafficFromDB() {
	ServerLock.Lock()
	defer ServerLock.Unlock()

	for serverID, server := range ServerList {
		if server == nil {
			continue
		}

		var dbServer model.Server
		if err := DB.First(&dbServer, serverID).Error; err != nil {
			log.Printf("从数据库获取服务器 %d 数据失败: %v", serverID, err)
			continue
		}

		// 同步累计流量数据
		server.CumulativeNetInTransfer = dbServer.CumulativeNetInTransfer
		server.CumulativeNetOutTransfer = dbServer.CumulativeNetOutTransfer
	}

	// 累计流量数据同步完成
}

// TriggerTrafficRecalculation 流量重新计算的空实现
func TriggerTrafficRecalculation() int {
	return 0
}

// SaveAllTrafficToDB 保存所有服务器的累计流量到数据库
func SaveAllTrafficToDB() {
	ServerLock.RLock()
	defer ServerLock.RUnlock()

	savedCount := 0
	for serverID, server := range ServerList {
		if server == nil {
			continue
		}

		updateSQL := `UPDATE servers SET 
						cumulative_net_in_transfer = ?, 
						cumulative_net_out_transfer = ? 
						WHERE id = ?`

		if err := DB.Exec(updateSQL,
			server.CumulativeNetInTransfer,
			server.CumulativeNetOutTransfer,
			serverID).Error; err != nil {
			log.Printf("保存服务器 %s (ID:%d) 累计流量失败: %v", server.Name, serverID, err)
		} else {
			savedCount++
		}
	}

	// 累计流量数据保存完成
}

// AutoSyncTraffic 自动同步流量的空实现
func AutoSyncTraffic() {
	// 功能已移除
}

// VerifyTrafficDataConsistency 验证流量数据一致性（调试用）
func VerifyTrafficDataConsistency() {
	ServerLock.RLock()
	defer ServerLock.RUnlock()

	for serverID, server := range ServerList {
		if server == nil {
			continue
		}

		// 从数据库读取数据
		var dbServer model.Server
		if err := DB.First(&dbServer, serverID).Error; err != nil {
			log.Printf("无法从数据库读取服务器 %d: %v", serverID, err)
			continue
		}

		// 比较内存和数据库中的数据
		memoryIn := server.CumulativeNetInTransfer
		memoryOut := server.CumulativeNetOutTransfer
		dbIn := dbServer.CumulativeNetInTransfer
		dbOut := dbServer.CumulativeNetOutTransfer

		if memoryIn != dbIn || memoryOut != dbOut {
			log.Printf("[不一致] 服务器 %s (ID:%d) - 内存: 入站=%d 出站=%d, 数据库: 入站=%d 出站=%d",
				server.Name, serverID,
				memoryIn, memoryOut,
				dbIn, dbOut)
		}
	}

	// 流量数据一致性验证完成
}
