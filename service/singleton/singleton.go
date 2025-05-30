package singleton

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"runtime/debug"
	"sync/atomic"
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

	// 使用合理的缓存时间，并适度调整清理频率
	Cache = cache.New(5*time.Minute, 10*time.Minute)

	// 启动适度的定期清理任务
	go func() {
		ticker := time.NewTicker(30 * time.Minute) // 改为2分钟清理一次
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

	// 添加内存使用监控任务，每1分钟执行一次，更激进的内存管理
	Cron.AddFunc("0 */1 * * * *", func() {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		// 更低的内存阈值，600MB就开始清理
		if m.Alloc > 600*1024*1024 {
			log.Printf("内存使用警告: %v MB，开始激进清理", m.Alloc/1024/1024)

			// 立即执行系统清理
			cleanupSystemBuffers()

			// 强制执行多次GC
			runtime.GC()
			runtime.GC()

			// 检查清理后的内存使用
			runtime.ReadMemStats(&m)
			log.Printf("清理后内存使用: %v MB", m.Alloc/1024/1024)
		}
	})

	// 缓冲池清理任务，改为每30分钟执行一次，降低清理频率
	Cron.AddFunc("0 */30 * * * *", func() {
		cleanupSystemBuffers()

		// 清理Go内存池
		debug.FreeOSMemory()
		runtime.GC()

		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		log.Printf("定期清理完成，当前内存使用: %v MB", m.Alloc/1024/1024)
	})

	// 数据库优化任务，改为每1小时执行一次，降低清理频率
	Cron.AddFunc("0 0 * * * *", func() {
		OptimizeDatabase()

		// 输出数据库状态
		stats := GetDatabaseStats()
		log.Printf("数据库状态: %+v", stats)
	})

	// 启动内存监控
	globalMemoryMonitor = NewMemoryMonitor()
	globalMemoryMonitor.Start()
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
		CreateBatchSize: 50,   // 减少批处理大小以降低内存使用
		PrepareStmt:     true, // 启用预处理语句以提高性能
	})
	if err != nil {
		panic(err)
	}

	// 配置SQLite连接以优化内存使用
	sqlDB, err := DB.DB()
	if err != nil {
		panic(err)
	}

	// 设置严格的连接池限制
	sqlDB.SetMaxOpenConns(3)                   // 最多3个连接
	sqlDB.SetMaxIdleConns(1)                   // 最多1个空闲连接
	sqlDB.SetConnMaxLifetime(2 * time.Minute)  // 连接最大生命周期2分钟
	sqlDB.SetConnMaxIdleTime(30 * time.Second) // 空闲连接最大时间30秒

	// 执行SQLite优化配置
	DB.Exec("PRAGMA cache_size = -2000")   // 设置2MB缓存
	DB.Exec("PRAGMA temp_store = MEMORY")  // 临时表存储在内存中
	DB.Exec("PRAGMA synchronous = NORMAL") // 平衡性能和安全
	DB.Exec("PRAGMA journal_mode = WAL")   // 使用WAL模式提高并发
	DB.Exec("PRAGMA mmap_size = 67108864") // 64MB内存映射大小

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

// cleanupSystemBuffers 清理系统缓冲区以释放内存
func cleanupSystemBuffers() {
	log.Printf("开始激进内存清理...")

	// 清理ServiceSentinel的内存数据
	if ServiceSentinelShared != nil {
		ServiceSentinelShared.cleanupOldData()
		ServiceSentinelShared.limitDataSize()
	}

	// 清理AlertSentinel的内存数据
	cleanupAlertMemoryData()

	// 清理服务器连接和状态
	CleanupServerState()

	// 适度清理缓存过期项
	if Cache != nil {
		Cache.DeleteExpired() // 只删除过期项，不完全清空
		// 如果缓存项过多，创建新的缓存实例
		if Cache.ItemCount() > 1000 {
			Cache = cache.New(5*time.Minute, 10*time.Minute) // 适度的缓存时间
		}
	}

	// 适度调整数据库连接池配置
	if DB != nil {
		sqlDB, err := DB.DB()
		if err == nil {
			sqlDB.SetMaxOpenConns(10)                  // 增加最大连接数
			sqlDB.SetMaxIdleConns(5)                   // 增加空闲连接数
			sqlDB.SetConnMaxLifetime(15 * time.Minute) // 增加连接生命周期
		}
	}

	// 强制清理Go运行时内存
	debug.FreeOSMemory()
	runtime.GC()
	runtime.GC() // 连续两次GC确保彻底清理

	log.Printf("激进内存清理完成")
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
	log.Printf("开始清理历史监控数据...")

	// 使用更小的事务批次和分批处理，避免内存峰值
	var totalCleaned int64

	// 分批清理30天前的监控记录
	batchSize := 1000
	for {
		var count int64
		err := DB.Transaction(func(tx *gorm.DB) error {
			// 批量删除30天前的监控记录
			result := tx.Unscoped().Where("created_at < ?", time.Now().AddDate(0, 0, -30)).
				Limit(batchSize).Delete(&model.MonitorHistory{})
			if result.Error != nil {
				return result.Error
			}
			count = result.RowsAffected
			return nil
		})

		if err != nil {
			log.Printf("清理监控历史记录失败: %v", err)
			break
		}

		totalCleaned += count
		if count < int64(batchSize) {
			break // 没有更多记录要删除
		}

		// 在批次之间稍作休息，减少内存压力
		time.Sleep(50 * time.Millisecond)
		runtime.GC() // 强制清理内存
	}

	// 分批清理孤立的监控记录
	for {
		var count int64
		err := DB.Transaction(func(tx *gorm.DB) error {
			result := tx.Unscoped().Where("monitor_id NOT IN (SELECT id FROM monitors)").
				Limit(batchSize).Delete(&model.MonitorHistory{})
			if result.Error != nil {
				return result.Error
			}
			count = result.RowsAffected
			return nil
		})

		if err != nil {
			log.Printf("清理孤立监控记录失败: %v", err)
			break
		}

		totalCleaned += count
		if count < int64(batchSize) {
			break
		}

		time.Sleep(50 * time.Millisecond)
		runtime.GC()
	}

	// 清理无效的流量记录
	err := DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Unscoped().Where("server_id NOT IN (SELECT id FROM servers)").
			Delete(&model.Transfer{})
		if result.Error != nil {
			return result.Error
		}
		totalCleaned += result.RowsAffected
		return nil
	})

	if err != nil {
		log.Printf("清理流量记录失败: %v", err)
	} else {
		log.Printf("历史数据清理完成，共清理 %d 条记录", totalCleaned)
	}

	// 清理后优化数据库
	DB.Exec("VACUUM")
	DB.Exec("ANALYZE")
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

// MemoryMonitor 激进内存监控器
type MemoryMonitor struct {
	ticker             *time.Ticker
	emergencyThreshold uint64 // 紧急内存阈值(MB)
	warningThreshold   uint64 // 警告内存阈值(MB)
	maxGoroutines      int64  // 最大goroutine数量
	isEmergencyMode    bool
}

var (
	memoryPressureLevel int64 // 内存压力等级：0=正常，1=警告，2=严重，3=紧急
	lastForceGCTime     time.Time
	goroutineCount      int64
)

func NewMemoryMonitor() *MemoryMonitor {
	return &MemoryMonitor{
		emergencyThreshold: 800, // 800MB紧急阈值 - 根据用户要求调整
		warningThreshold:   300, // 300MB警告阈值 - 根据用户要求调整
		maxGoroutines:      300, // 最大300个goroutine - 严格限制
		isEmergencyMode:    false,
	}
}

func (mm *MemoryMonitor) Start() {
	mm.ticker = time.NewTicker(10 * time.Second) // 每10秒检查一次

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("MemoryMonitor panic恢复: %v", r)
				time.Sleep(5 * time.Second)
				mm.Start() // 重启监控
			}
		}()

		for range mm.ticker.C {
			mm.checkMemoryPressure()
		}
	}()
}

func (mm *MemoryMonitor) Stop() {
	if mm.ticker != nil {
		mm.ticker.Stop()
	}
}

func (mm *MemoryMonitor) checkMemoryPressure() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	currentMemMB := m.Alloc / 1024 / 1024
	currentGoroutines := int64(runtime.NumGoroutine())
	atomic.StoreInt64(&goroutineCount, currentGoroutines)

	// 计算内存压力等级
	newPressureLevel := int64(0)
	if currentMemMB > mm.emergencyThreshold {
		newPressureLevel = 3 // 紧急
	} else if currentMemMB > mm.warningThreshold {
		newPressureLevel = 2 // 严重
	} else if currentMemMB > mm.warningThreshold/2 {
		newPressureLevel = 1 // 警告
	}

	atomic.StoreInt64(&memoryPressureLevel, newPressureLevel)

	// 硬性内存限制 - 超过900MB强制退出让systemd重启
	if currentMemMB > 900 {
		log.Printf("内存使用超过900MB (%dMB)，程序即将强制退出避免系统崩溃", currentMemMB)
		log.Printf("Goroutine数量: %d", currentGoroutines)
		log.Printf("堆内存: %dMB", m.HeapAlloc/1024/1024)
		log.Printf("系统内存: %dMB", m.Sys/1024/1024)

		// 快速清理尝试
		runtime.GC()
		runtime.GC()

		// 强制退出，让systemd重启
		os.Exit(1)
	}

	// 检查是否需要进入紧急模式
	if newPressureLevel >= 3 || currentGoroutines > mm.maxGoroutines {
		if !mm.isEmergencyMode {
			log.Printf("进入内存紧急模式: 内存=%dMB, Goroutines=%d", currentMemMB, currentGoroutines)
			mm.isEmergencyMode = true
		}
		mm.emergencyCleanup()
	} else if newPressureLevel >= 2 {
		mm.aggressiveCleanup()
	} else if newPressureLevel >= 1 {
		mm.normalCleanup()
	} else {
		mm.isEmergencyMode = false
	}

	// 限制GC频率，避免过度GC影响性能
	if time.Since(lastForceGCTime) > 30*time.Second && newPressureLevel > 0 {
		runtime.GC()
		debug.FreeOSMemory()
		lastForceGCTime = time.Now()
	}
}

func (mm *MemoryMonitor) emergencyCleanup() {
	log.Printf("执行紧急内存清理...")

	// 清空所有缓存
	if Cache != nil {
		Cache.Flush()
		Cache = cache.New(10*time.Second, 20*time.Second) // 极短缓存时间
	}

	// 激进清理ServiceSentinel
	if ServiceSentinelShared != nil {
		ServiceSentinelShared.limitDataSize() // 使用limitDataSize代替aggressiveCleanup

		// 强制限制监控数据大小
		ServiceSentinelShared.serviceResponseDataStoreLock.Lock()
		for monitorID, statusData := range ServiceSentinelShared.serviceCurrentStatusData {
			if statusData != nil && len(statusData) > 5 {
				ServiceSentinelShared.serviceCurrentStatusData[monitorID] = statusData[len(statusData)-5:]
			}
		}
		ServiceSentinelShared.serviceResponseDataStoreLock.Unlock()
	}

	// 强制清理报警数据
	cleanupAlertMemoryData()

	// 清理数据库连接
	if DB != nil {
		sqlDB, err := DB.DB()
		if err == nil {
			sqlDB.SetMaxOpenConns(1) // 紧急状态下只保留1个连接
			sqlDB.SetMaxIdleConns(0) // 不保留空闲连接
			sqlDB.SetConnMaxLifetime(10 * time.Minute)
		}
	}

	// 强制清理Goroutine池
	if NotificationPool != nil {
		NotificationPool.Clear()
		NotificationPool.ForceReduceWorkers(2) // 强制减少到2个worker
	}
	if TriggerTaskPool != nil {
		TriggerTaskPool.Clear()
		TriggerTaskPool.ForceReduceWorkers(1) // 强制减少到1个worker
	}

	// 强制多次GC
	for i := 0; i < 5; i++ { // 增加到5次GC
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	debug.FreeOSMemory()

	// 设置更严格的GC目标
	debug.SetGCPercent(20) // 进一步降低GC阈值到20%

	log.Printf("紧急内存清理完成")
}

func (mm *MemoryMonitor) aggressiveCleanup() {
	cleanupSystemBuffers()
	if ServiceSentinelShared != nil {
		ServiceSentinelShared.limitDataSize()
	}
	runtime.GC()
}

func (mm *MemoryMonitor) normalCleanup() {
	if Cache != nil {
		Cache.DeleteExpired()
	}
}

// GetMemoryPressureLevel 获取当前内存压力等级
func GetMemoryPressureLevel() int64 {
	return atomic.LoadInt64(&memoryPressureLevel)
}

// GetGoroutineCount 获取当前goroutine数量
func GetGoroutineCount() int64 {
	return atomic.LoadInt64(&goroutineCount)
}

var globalMemoryMonitor *MemoryMonitor

// OptimizeDatabase 优化数据库性能并减少内存占用
func OptimizeDatabase() {
	log.Printf("开始数据库优化...")

	// 检查数据库连接状态
	sqlDB, err := DB.DB()
	if err != nil {
		log.Printf("获取数据库连接失败: %v", err)
		return
	}

	// 获取连接池状态
	stats := sqlDB.Stats()
	log.Printf("数据库连接池状态: 打开连接=%d, 使用中=%d, 空闲=%d",
		stats.OpenConnections, stats.InUse, stats.Idle)

	// 执行数据库维护命令
	maintenanceCommands := []string{
		"PRAGMA optimize",                 // 自动优化查询计划
		"PRAGMA incremental_vacuum",       // 增量清理空闲页面
		"PRAGMA wal_checkpoint(TRUNCATE)", // 清理WAL文件
	}

	for _, cmd := range maintenanceCommands {
		if err := DB.Exec(cmd).Error; err != nil {
			log.Printf("执行数据库维护命令 '%s' 失败: %v", cmd, err)
		}
	}

	// 重新分析表统计信息
	tables := []string{"servers", "monitors", "monitor_histories", "transfers"}
	for _, table := range tables {
		DB.Exec("ANALYZE " + table)
	}

	// 检查并调整连接池设置（基于内存压力）
	pressureLevel := GetMemoryPressureLevel()
	if pressureLevel >= 2 {
		// 高内存压力下减少连接数
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(0)
		sqlDB.SetConnMaxLifetime(1 * time.Minute)
		log.Printf("数据库连接池已调整为内存节约模式")
	} else if pressureLevel >= 1 {
		// 中等内存压力
		sqlDB.SetMaxOpenConns(2)
		sqlDB.SetMaxIdleConns(1)
		sqlDB.SetConnMaxLifetime(90 * time.Second)
	} else {
		// 正常模式
		sqlDB.SetMaxOpenConns(3)
		sqlDB.SetMaxIdleConns(1)
		sqlDB.SetConnMaxLifetime(2 * time.Minute)
	}

	log.Printf("数据库优化完成")
}

// GetDatabaseStats 获取数据库统计信息用于监控
func GetDatabaseStats() map[string]interface{} {
	stats := make(map[string]interface{})

	sqlDB, err := DB.DB()
	if err != nil {
		stats["error"] = err.Error()
		return stats
	}

	dbStats := sqlDB.Stats()
	stats["open_connections"] = dbStats.OpenConnections
	stats["in_use"] = dbStats.InUse
	stats["idle"] = dbStats.Idle
	stats["wait_count"] = dbStats.WaitCount
	stats["wait_duration"] = dbStats.WaitDuration.String()
	stats["max_idle_closed"] = dbStats.MaxIdleClosed
	stats["max_lifetime_closed"] = dbStats.MaxLifetimeClosed

	// 获取数据库文件大小（如果可能）
	var dbSize int64
	if err := DB.Raw("SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()").
		Scan(&dbSize).Error; err == nil {
		stats["db_size_mb"] = dbSize / 1024 / 1024
	}

	return stats
}
