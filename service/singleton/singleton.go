package singleton

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
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
	Cache = cache.New(6*time.Minute, 12*time.Minute) // 增加缓存时间（从5/10分钟增加到6/12分钟）

	// 启动适度的定期清理任务
	go func() {
		ticker := time.NewTicker(35 * time.Minute) // 改为35分钟清理一次（从30分钟增加）
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

	// 添加内存使用监控任务，每1分钟执行一次，温和的内存管理
	Cron.AddFunc("0 */1 * * * *", func() {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)

	// 温和的内存阈值检查，800MB时进行基础清理
	if m.Alloc > 800*1024*1024 {
		log.Printf("内存使用提醒: %v MB，执行温和清理", m.Alloc/1024/1024)

		// 执行温和的清理操作
		if Cache != nil {
			Cache.DeleteExpired() // 只清理过期项
		}
		
		// 单次GC即可
		runtime.GC()

			// 检查清理后的内存使用
			runtime.ReadMemStats(&m)
			log.Printf("清理后内存使用: %v MB", m.Alloc/1024/1024)
		}
	})

	// 定期温和清理任务，改为每小时执行一次
	Cron.AddFunc("0 0 * * * *", func() {
		// 执行温和的清理操作
		if Cache != nil {
			Cache.DeleteExpired() // 只清理过期项
		}

		// 清理ServiceSentinel的旧数据
		if ServiceSentinelShared != nil {
			ServiceSentinelShared.cleanupOldData()
		}

		// 清理AlertSentinel的内存数据
		cleanupAlertMemoryData()

		// 温和的GC
		runtime.GC()

		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		log.Printf("定期温和清理完成，当前内存使用: %v MB", m.Alloc/1024/1024)
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

	// 启动连接池监控
	StartConnectionPoolMonitor()
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
		CreateBatchSize: 60,   // 增加批处理大小到60（从50增加）
		PrepareStmt:     true, // 启用预处理语句以提高性能
	})
	if err != nil {
		panic(err)
	}

	// 配置SQLite连接以避免锁死问题
	sqlDB, err := DB.DB()
	if err != nil {
		panic(err)
	}

	// SQLite WAL模式优化：支持多读一写的并发模式
	sqlDB.SetMaxOpenConns(18)                  // 增加到18个并发连接（从16增加）
	sqlDB.SetMaxIdleConns(9)                   // 保持9个空闲连接（从8增加）
	sqlDB.SetConnMaxLifetime(30 * time.Minute) // 30分钟连接生命周期
	sqlDB.SetConnMaxIdleTime(10 * time.Minute) // 空闲连接保持10分钟

	// SQLite性能和锁管理优化配置
	DB.Exec("PRAGMA synchronous = NORMAL")        // 平衡性能和安全性
	DB.Exec("PRAGMA cache_size = -45000")         // 增加缓存到45MB（从40MB增加）
	DB.Exec("PRAGMA temp_store = memory")         // 临时表存储在内存
	DB.Exec("PRAGMA busy_timeout = 10000")        // 增加到10秒锁等待超时
	DB.Exec("PRAGMA optimize")                    // 启用查询优化器
	// 迁移时先使用DELETE模式，避免WAL锁竞争
	
	log.Println("开始数据库表迁移...")
	err = DB.AutoMigrate(model.Server{}, model.User{},
		model.Notification{}, model.AlertRule{}, model.Monitor{},
		model.MonitorHistory{}, model.Cron{}, model.Transfer{},
		model.ApiToken{}, model.NAT{}, model.DDNSProfile{}, model.DDNSRecordState{})
	if err != nil {
		log.Printf("数据库迁移失败: %v", err)
		panic(err)
	}
	log.Println("数据库表迁移完成")
	
	// 迁移完成后切换到WAL模式并优化并发设置
	DB.Exec("PRAGMA journal_mode = WAL")          // 使用WAL模式，支持多读一写
	DB.Exec("PRAGMA synchronous = NORMAL")        // 平衡性能和安全性
	DB.Exec("PRAGMA cache_size = -40000")         // 40MB缓存
	DB.Exec("PRAGMA temp_store = MEMORY")         // 临时表存储在内存中
	DB.Exec("PRAGMA mmap_size = 301989888")       // 增加到288MB内存映射（从256MB增加）
	DB.Exec("PRAGMA wal_autocheckpoint = 2200")   // WAL文件2200页时自动检查点（从2000增加）
	DB.Exec("PRAGMA busy_timeout = 10000")        // 10秒超时，给更多时间处理锁竞争
	DB.Exec("PRAGMA threads = 4")                 // 启用多线程支持
	DB.Exec("PRAGMA wal_checkpoint(PASSIVE)")     // 执行被动检查点
	log.Println("数据库配置完成")
	
	// 启动连接池监控
	StartConnectionPoolMonitor()
	LogConnectionPoolStats() // 立即记录一次连接池状态
	
	// 预热数据库连接池
	WarmupDatabase()

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

// 数据库并发控制 - 移除全局锁，使用SQLite WAL模式的原生并发控制

// isSystemBusy 检查系统是否处于繁忙状态
func isSystemBusy() bool {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	
	// 检查内存使用是否超过阈值 (800MB)
	if m.Alloc > 800*1024*1024 {
		return true
	}
	
	// 检查Goroutine数量是否过多
	if runtime.NumGoroutine() > 400 {
		return true
	}
	
	// 检查数据库连接池状态来判断系统繁忙程度
	sqlDB, err := DB.DB()
	if err != nil {
		return true
	}
	
	stats := sqlDB.Stats()
	// 如果所有连接都在使用且等待队列不为空，说明系统繁忙
	if stats.InUse >= stats.OpenConnections && stats.WaitCount > 0 {
		return true
	}
	
	return false
}

// executeWithoutLock 直接执行数据库操作，依赖SQLite WAL模式的并发控制
func executeWithoutLock(operation func() error) error {
	return operation()
}

// ExecuteWithRetry 带重试机制的数据库操作，用于处理临时的数据库锁定（导出版本）
func ExecuteWithRetry(operation func() error) error {
	const maxRetries = 5                      // 增加重试次数到5次
	const baseDelay = 25 * time.Millisecond   // 减少基础延迟
	const maxDelay = 500 * time.Millisecond   // 最大延迟500ms
	
	for attempt := 0; attempt < maxRetries; attempt++ {
		err := operation()
		if err == nil {
			return nil
		}
		
		// 检查是否是数据库锁定错误
		if strings.Contains(err.Error(), "database is locked") || 
		   strings.Contains(err.Error(), "SQL statements in progress") ||
		   strings.Contains(err.Error(), "cannot commit transaction") ||
		   strings.Contains(err.Error(), "database table is locked") {
			
			if attempt < maxRetries-1 {
				// 更智能的延迟策略：随机化指数退避
				delay := baseDelay * time.Duration(1<<attempt)
				if delay > maxDelay {
					delay = maxDelay
				}
				// 添加安全的随机抖动，避免雷群效应
				maxJitter := int64(delay) / 4
				if maxJitter > 0 {
					jitterBig, err := rand.Int(rand.Reader, big.NewInt(maxJitter))
					if err == nil {
						jitter := time.Duration(jitterBig.Int64())
						delay += jitter
					}
				}
				
				log.Printf("数据库操作重试 %d/%d，延迟 %v: %v", attempt+1, maxRetries, delay, err)
				time.Sleep(delay)
				continue
			}
		}
		
		// 非数据库锁定错误或已达到最大重试次数
		return err
	}
	
	return fmt.Errorf("数据库操作在 %d 次重试后失败", maxRetries)
}

// executeWithRetry 带重试机制的数据库操作，用于处理临时的数据库锁定
func executeWithRetry(operation func() error) error {
	return ExecuteWithRetry(operation)
}

// CleanMonitorHistory 清理无效或过时的监控记录和流量记录
func CleanMonitorHistory() {
	// 检查是否有其他重要操作正在进行
	if isSystemBusy() {
		log.Printf("系统繁忙，延迟历史数据清理")
		time.AfterFunc(30*time.Minute, func() {
			CleanMonitorHistory()
		})
		return
	}

	log.Printf("开始清理历史监控数据...")

	// 使用无锁方式执行清理操作，依赖SQLite WAL模式的并发控制
	err := executeWithoutLock(func() error {
		var totalCleaned int64
		batchSize := 25     // 增加批次大小到25（从20增加）
		maxRetries := 3     // 减少重试次数，更快失败
		cutoffDate := time.Now().AddDate(0, 0, -30)

		// 使用非事务方式分批清理，避免长时间事务锁定
		// 清理30天前的监控记录
		for {
			var count int64
			var err error
			
			for retry := 0; retry < maxRetries; retry++ {
				// 等待确保没有其他操作在进行
				time.Sleep(time.Duration(retry*100) * time.Millisecond)
				
				// 使用子查询删除，SQLite兼容方式
				result := DB.Exec("DELETE FROM monitor_histories WHERE rowid IN (SELECT rowid FROM monitor_histories WHERE created_at < ? LIMIT ?)", 
					cutoffDate.Format("2006-01-02 15:04:05"), batchSize)
				
				if result.Error != nil {
					err = result.Error
					
					// 检查是否为数据库锁错误
					if strings.Contains(err.Error(), "database is locked") || 
					   strings.Contains(err.Error(), "SQL statements in progress") ||
					   strings.Contains(err.Error(), "cannot commit") {
						if retry < maxRetries-1 {
							backoffDelay := time.Duration((retry+1)*1000) * time.Millisecond
							log.Printf("数据库忙碌，%v 后重试历史数据清理 (%d/%d)", backoffDelay, retry+1, maxRetries)
							time.Sleep(backoffDelay)
							continue
						}
					}
					
					log.Printf("清理监控历史记录失败: %v", err)
					return err
				}
				
				count = result.RowsAffected
				break
			}

			if err != nil {
				return err
			}

			totalCleaned += count
			if count < int64(batchSize) {
				break // 没有更多记录要删除
			}

			// 短暂暂停，让其他操作有机会执行
			time.Sleep(100 * time.Millisecond)
		}

		// 清理孤立的监控记录
		for {
			var count int64
			var err error

			for retry := 0; retry < maxRetries; retry++ {
				// 等待确保没有其他操作在进行
				time.Sleep(time.Duration(retry*100) * time.Millisecond)
				
				// 使用子查询删除孤立记录，SQLite兼容方式
				result := DB.Exec("DELETE FROM monitor_histories WHERE rowid IN (SELECT rowid FROM monitor_histories WHERE monitor_id NOT IN (SELECT id FROM monitors) LIMIT ?)", batchSize)
				
				if result.Error != nil {
					err = result.Error
					
					if strings.Contains(err.Error(), "database is locked") || 
					   strings.Contains(err.Error(), "SQL statements in progress") ||
					   strings.Contains(err.Error(), "cannot commit") {
						if retry < maxRetries-1 {
							backoffDelay := time.Duration((retry+1)*1000) * time.Millisecond
							log.Printf("数据库忙碌，%v 后重试孤立记录清理 (%d/%d)", backoffDelay, retry+1, maxRetries)
							time.Sleep(backoffDelay)
							continue
						}
					}
					
					log.Printf("清理孤立监控记录失败: %v", err)
					return err
				}
				
				count = result.RowsAffected
				break
			}

			if err != nil {
				return err
			}

			totalCleaned += count
			if count < int64(batchSize) {
				break
			}

			time.Sleep(100 * time.Millisecond)
		}

		// 清理无效的流量记录
		for retry := 0; retry < maxRetries; retry++ {
			// 等待确保没有其他操作在进行
			time.Sleep(time.Duration(retry*100) * time.Millisecond)
			
			result := DB.Exec("DELETE FROM transfers WHERE server_id NOT IN (SELECT id FROM servers)")
			
			if result.Error != nil {
				err := result.Error
				
				if strings.Contains(err.Error(), "database is locked") || 
				   strings.Contains(err.Error(), "SQL statements in progress") ||
				   strings.Contains(err.Error(), "cannot commit") {
					if retry < maxRetries-1 {
						backoffDelay := time.Duration((retry+1)*1000) * time.Millisecond
						log.Printf("数据库忙碌，%v 后重试流量记录清理 (%d/%d)", backoffDelay, retry+1, maxRetries)
						time.Sleep(backoffDelay)
						continue
					}
				}
				
				log.Printf("清理流量记录失败: %v", err)
				return err
			}
			
			totalCleaned += result.RowsAffected
			break
		}

		if totalCleaned > 0 {
			log.Printf("历史数据清理完成，共清理 %d 条记录", totalCleaned)
		}
		
		return nil
	})

	if err != nil {
		log.Printf("历史数据清理过程中发生错误: %v", err)
	}

	// 延迟执行数据库优化，避免与其他操作冲突
	go func() {
		time.Sleep(60 * time.Second) // 增加等待时间确保没有其他操作
		if !isSystemBusy() {
			optimizeDatabase()
		} else {
			log.Printf("系统繁忙，跳过数据库优化")
		}
	}()
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
	offlineTimeout := time.Minute * 3 // 增加到3分钟无心跳视为离线，减少误报

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
	// 使用读锁获取服务器列表
	ServerLock.RLock()
	serverData := make(map[uint64]*model.Server)
	for id, server := range ServerList {
		if server != nil {
			serverData[id] = server
		}
	}
	ServerLock.RUnlock()

	if len(serverData) == 0 {
		return
	}

	// 使用无锁方式进行批量更新，依赖SQLite WAL模式处理并发
	err := executeWithoutLock(func() error {
		savedCount := 0
		maxRetries := 3

		for serverID, server := range serverData {
			var err error
			
			// 重试机制处理数据库忙碌
			for retry := 0; retry < maxRetries; retry++ {
				updateSQL := `UPDATE servers SET 
								cumulative_net_in_transfer = ?, 
								cumulative_net_out_transfer = ? 
								WHERE id = ?`

				err = DB.Exec(updateSQL,
					server.CumulativeNetInTransfer,
					server.CumulativeNetOutTransfer,
					serverID).Error
				
				if err == nil {
					savedCount++
					break
				}
				
				// 检查是否为数据库锁错误
				if strings.Contains(err.Error(), "database is locked") || 
				   strings.Contains(err.Error(), "SQL statements in progress") {
					if retry < maxRetries-1 {
						backoffDelay := time.Duration((retry+1)*200) * time.Millisecond
						log.Printf("数据库忙碌，%v 后重试流量保存 (%d/%d)", backoffDelay, retry+1, maxRetries)
						time.Sleep(backoffDelay)
						continue
					}
				}
				
				log.Printf("保存服务器 %s (ID:%d) 累计流量失败: %v", server.Name, serverID, err)
				break
			}
		}

		if savedCount > 0 {
			log.Printf("成功保存 %d 个服务器的累计流量数据", savedCount)
		}
		
		return nil
	})

	if err != nil {
		log.Printf("保存流量数据过程中发生错误: %v", err)
	}
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

// MemoryMonitor 温和内存监控器
type MemoryMonitor struct {
	ticker           *time.Ticker
	highThreshold    uint64 // 高内存阈值(MB)
	warningThreshold uint64 // 警告内存阈值(MB)
	maxGoroutines    int64  // 最大goroutine数量
	isHighPressure   bool
}

var (
	memoryPressureLevel int64 // 内存压力等级：0=正常，1=警告，2=严重，3=紧急
	lastForceGCTime     time.Time
	goroutineCount      int64
)

func NewMemoryMonitor() *MemoryMonitor {
	return &MemoryMonitor{
		highThreshold:    800, // 800MB高内存阈值 - 根据用户要求调整
		warningThreshold: 400, // 400MB警告阈值 - 根据用户要求调整  
		maxGoroutines:    400, // 最大400个goroutine - 稍微提高限制
		isHighPressure:   false,
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
	if currentMemMB > mm.highThreshold {
		newPressureLevel = 3 // 高压
	} else if currentMemMB > mm.warningThreshold {
		newPressureLevel = 2 // 严重
	} else if currentMemMB > mm.warningThreshold/2 {
		newPressureLevel = 1 // 警告
	}

	atomic.StoreInt64(&memoryPressureLevel, newPressureLevel)

	// 硬性内存限制 - 超过1000MB强制退出让systemd重启
	if currentMemMB > 1000 {
		log.Printf("内存使用超过1000MB (%dMB)，程序即将强制退出避免系统崩溃", currentMemMB)
		log.Printf("Goroutine数量: %d", currentGoroutines)
		log.Printf("堆内存: %dMB", m.HeapAlloc/1024/1024)
		log.Printf("系统内存: %dMB", m.Sys/1024/1024)

		// 快速清理尝试
		runtime.GC()
		runtime.GC()

		// 强制退出，让systemd重启
		os.Exit(1)
	}

	// 检查是否需要进入高压模式
	if newPressureLevel >= 3 || currentGoroutines > mm.maxGoroutines {
		if !mm.isHighPressure {
			log.Printf("进入内存高压模式: 内存=%dMB, Goroutines=%d", currentMemMB, currentGoroutines)
			mm.isHighPressure = true
		}
		mm.moderateCleanup()
	} else if newPressureLevel >= 2 {
		mm.lightCleanup()
	} else if newPressureLevel >= 1 {
		mm.normalCleanup()
	} else {
		mm.isHighPressure = false
	}

	// 限制GC频率，避免过度GC影响性能
	if time.Since(lastForceGCTime) > 30*time.Second && newPressureLevel > 0 {
		runtime.GC()
		debug.FreeOSMemory()
		lastForceGCTime = time.Now()
	}
}

func (mm *MemoryMonitor) moderateCleanup() {
	log.Printf("执行适度内存清理...")

	// 清理过期缓存
	if Cache != nil {
		Cache.DeleteExpired()
	}

	// 清理ServiceSentinel的旧数据
	if ServiceSentinelShared != nil {
		ServiceSentinelShared.cleanupOldData()
	}

	// 清理报警数据
	cleanupAlertMemoryData()

	// 适度的GC
	runtime.GC()

	log.Printf("适度内存清理完成")
}

func (mm *MemoryMonitor) lightCleanup() {
	// 轻度清理，只清理过期项
	if Cache != nil {
		Cache.DeleteExpired()
	}
	
	// 清理ServiceSentinel
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

// OptimizeDatabase 优化数据库性能的公共函数
func OptimizeDatabase() {
	// 使用无锁方式优化数据库，依赖SQLite WAL模式处理并发
	err := executeWithoutLock(func() error {
		optimizeDatabase()
		return nil
	})
	
	if err != nil {
		log.Printf("数据库优化过程中发生错误: %v", err)
	}
}

// executeDatabaseOperation 执行数据库操作的安全包装器
func executeDatabaseOperation(operation func() error, operationName string, maxRetries int) error {
	var lastErr error
	
	for retry := 0; retry < maxRetries; retry++ {
		err := operation()
		if err == nil {
			return nil
		}
		
		lastErr = err
		
		// 检查是否为数据库锁定错误
		if strings.Contains(err.Error(), "database is locked") || 
		   strings.Contains(err.Error(), "SQL statements in progress") ||
		   strings.Contains(err.Error(), "cannot commit") {
			
			if retry < maxRetries-1 {
				// 指数退避策略
				backoffDelay := time.Duration(200*(1<<retry)) * time.Millisecond
				log.Printf("数据库操作 '%s' 忙碌，%v 后重试 (%d/%d)", operationName, backoffDelay, retry+1, maxRetries)
				time.Sleep(backoffDelay)
				continue
			}
		}
		
		// 非锁定错误，直接返回
		log.Printf("数据库操作 '%s' 失败: %v", operationName, err)
		return err
	}
	
	return fmt.Errorf("数据库操作 '%s' 在 %d 次重试后仍然失败: %v", operationName, maxRetries, lastErr)
}

// SafeDatabaseUpdate 安全的数据库更新操作
func SafeDatabaseUpdate(tableName string, updateData map[string]interface{}, whereClause string, args ...interface{}) error {
	return executeDatabaseOperation(func() error {
		return executeWithoutLock(func() error {
			// 使用事务确保操作的原子性
			return DB.Transaction(func(tx *gorm.DB) error {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				
				result := tx.WithContext(ctx).Table(tableName).Where(whereClause, args...).Updates(updateData)
				return result.Error
			})
		})
	}, fmt.Sprintf("update_%s", tableName), 3)
}

// SafeDatabaseDelete 安全的数据库删除操作
func SafeDatabaseDelete(model interface{}, whereClause string, args ...interface{}) error {
	return executeDatabaseOperation(func() error {
		return executeWithoutLock(func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			
			result := DB.WithContext(ctx).Where(whereClause, args...).Delete(model)
			return result.Error
		})
	}, "delete_operation", 3)
}

// 添加在文件末尾的全局内存监控器
var globalMemoryMonitor *MemoryMonitor

// optimizeDatabase 安全地优化数据库，避免锁竞争
func optimizeDatabase() {
	log.Printf("开始数据库优化...")
	
	// 检查数据库连接状态
	sqlDB, err := DB.DB()
	if err != nil {
		log.Printf("获取数据库连接失败: %v", err)
		return
	}
	
	// 检查连接池状态
	stats := sqlDB.Stats()
	if stats.InUse > 0 {
		log.Printf("数据库有活跃连接，跳过VACUUM操作")
		return
	}
	
	// 只在没有活跃连接时执行VACUUM
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	
	if err := DB.WithContext(ctx).Exec("VACUUM").Error; err != nil {
		if !strings.Contains(err.Error(), "SQL statements in progress") &&
		   !strings.Contains(err.Error(), "database is locked") {
			log.Printf("数据库VACUUM失败: %v", err)
		} else {
			log.Printf("数据库忙碌，跳过VACUUM操作")
		}
		return
	}
	
	// 执行ANALYZE更新统计信息
	if err := DB.WithContext(ctx).Exec("ANALYZE").Error; err != nil {
		log.Printf("数据库ANALYZE失败: %v", err)
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

// LogConnectionPoolStats 记录数据库连接池统计信息
func LogConnectionPoolStats() {
	if DB != nil {
		sqlDB, err := DB.DB()
		if err == nil {
			stats := sqlDB.Stats()
			log.Printf("数据库连接池状态: 最大连接=%d, 当前打开=%d, 使用中=%d, 空闲=%d, 等待连接=%d",
				stats.MaxOpenConnections,
				stats.OpenConnections,
				stats.InUse,
				stats.Idle,
				stats.WaitCount)
		}
	}
}

// StartConnectionPoolMonitor 启动连接池监控
func StartConnectionPoolMonitor() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		
		for range ticker.C {
			LogConnectionPoolStats()
		}
	}()
}

// WarmupDatabase 预热数据库连接池，减少首次访问时的锁竞争
func WarmupDatabase() {
	log.Println("开始数据库连接池预热...")
	
	// 预热连接池
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			
			// 执行简单查询来预热连接
			var count int64
			DB.Model(&model.Server{}).Count(&count)
			log.Printf("预热连接 %d 完成，服务器数量: %d", index, count)
		}(i)
	}
	
	wg.Wait()
	log.Println("数据库连接池预热完成")
}
