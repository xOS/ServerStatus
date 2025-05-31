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
	"gorm.io/gorm/logger"

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

	// 服务器状态缓存，减少数据库访问
	serverStatusCache *cache.Cache
)

func InitTimezoneAndCache() {
	var err error
	Loc, err = time.LoadLocation(Conf.Location)
	if err != nil {
		panic(err)
	}

	// 使用合理的缓存时间，并适度调整清理频率
	Cache = cache.New(6*time.Minute, 12*time.Minute) // 增加缓存时间（从5/10分钟增加到6/12分钟）

	// 初始化服务器状态缓存，单独设置更长的过期时间
	serverStatusCache = cache.New(15*time.Minute, 30*time.Minute)

	// 启动适度的定期清理任务
	go func() {
		ticker := time.NewTicker(35 * time.Minute) // 改为35分钟清理一次（从30分钟增加）
		defer ticker.Stop()

		for range ticker.C {
			Cache.DeleteExpired()
			serverStatusCache.DeleteExpired()
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

	// 添加监控历史记录一致性检查任务，每10分钟执行一次
	if _, err := Cron.AddFunc("0 */10 * * * *", VerifyMonitorHistoryConsistency); err != nil {
		log.Printf("添加监控历史记录一致性检查任务失败: %v", err)
	}

	// 添加定时清理任务，减少频率到每4小时执行一次，避免干扰数据保存
	Cron.AddFunc("0 0 */4 * * *", func() {
		CleanMonitorHistory()
		Cache.DeleteExpired()
		CleanupServerState() // 添加服务器状态清理
		SaveAllTrafficToDB() // 保存流量数据到数据库
	})

	// 添加内存使用监控任务，每1分钟执行一次，温和的内存管理
	Cron.AddFunc("0 */1 * * * *", func() {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		// 温和的内存阈值检查，提高到1200MB时进行基础清理，与其他组件保持一致
		if m.Alloc > 1200*1024*1024 {
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
	LogConnectionPoolStats() // 立即记录一次连接池状态

	// 预热数据库连接池
	WarmupDatabase()

	// 启动数据库写入工作器
	StartDBWriteWorker()

	// 启动数据库插入工作器
	StartDBInsertWorker()

	// 启动监控历史记录专用工作器
	StartMonitorHistoryWorker()

	// 启动数据库维护计划
	StartDatabaseMaintenanceScheduler()

	// 检查并添加新字段
	if !DB.Migrator().HasColumn(&model.Server{}, "cumulative_net_in_transfer") {
		if err := DB.Migrator().AddColumn(&model.Server{}, "cumulative_net_in_transfer"); err != nil {
			log.Println("添加cumulative_net_in_transfer字段失败:", err)
		}
	}

	if !DB.Migrator().HasColumn(&model.Server{}, "cumulative_net_out_transfer") {
		if err := DB.Migrator().AddColumn(&model.Server{}, "cumulative_net_out_transfer"); err != nil {
			log.Println("添加cumulative_net_out_transfer字段失败:", err)
		}
	}

	if !DB.Migrator().HasColumn(&model.Server{}, "last_state_json") {
		if err := DB.Migrator().AddColumn(&model.Server{}, "last_state_json"); err != nil {
			log.Println("添加last_state_json字段失败:", err)
		}
	}

	if !DB.Migrator().HasColumn(&model.Server{}, "last_online") {
		if err := DB.Migrator().AddColumn(&model.Server{}, "last_online"); err != nil {
			log.Println("添加last_online字段失败:", err)
		}
	}

	// 检查host_json字段是否存在，如果不存在则添加
	if !DB.Migrator().HasColumn(&model.Server{}, "host_json") {
		if err := DB.Migrator().AddColumn(&model.Server{}, "host_json"); err != nil {
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

// InitConfigFromPath 从给出的文件路径中加载配置
func InitConfigFromPath(path string) {
	Conf = &model.Config{}
	err := Conf.Read(path)
	if err != nil {
		panic(err)
	}
}

// InitDBFromPath 从指定路径初始化数据库
func InitDBFromPath(path string) error {
	var err error

	Loc, err = time.LoadLocation(Conf.Location)
	if err != nil {
		Loc = time.Local
	}

	dsn := fmt.Sprintf("file:%s?loc=auto&_journal_mode=WAL&_busy_timeout=%d&_synchronous=%s", path, 30000, "NORMAL")
	DB, err = gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
		NowFunc: func() time.Time {
			return time.Now().In(Loc)
		},
		CreateBatchSize: 60,   // 增加批处理大小到60（从50增加）
		PrepareStmt:     true, // 启用预处理语句以提高性能
	})

	if err != nil {
		return err
	}

	// 专门为monitor_histories表添加优化
	DB.Exec("PRAGMA temp_store = MEMORY;")
	DB.Exec("PRAGMA mmap_size = 1073741824;")        // 1GB
	DB.Exec("PRAGMA cache_size = -131072;")          // 增加到128MB
	DB.Exec("PRAGMA journal_size_limit = 67108864;") // 64MB
	DB.Exec("PRAGMA busy_timeout = 180000;")         // 180秒等待超时
	DB.Exec("PRAGMA locking_mode = EXCLUSIVE;")      // 独占锁定模式，减少锁冲突
	DB.Exec("PRAGMA wal_autocheckpoint = 50000;")    // 增加自动检查点，从20000增加到50000

	// 为monitor_histories表添加特定索引，提高查询性能
	DB.Exec("CREATE INDEX IF NOT EXISTS idx_monitor_histories_created_at ON monitor_histories(created_at);")
	DB.Exec("CREATE INDEX IF NOT EXISTS idx_monitor_histories_monitor_type ON monitor_histories(monitor_id);")
	DB.Exec("CREATE INDEX IF NOT EXISTS idx_monitor_histories_server_id ON monitor_histories(server_id);")

	// 优化连接池配置
	rawDB, err := DB.DB()
	if err != nil {
		return err
	}

	// 修改连接池配置，降低并发压力
	rawDB.SetMaxIdleConns(3) // 降低空闲连接数，从10降到3
	rawDB.SetMaxOpenConns(6) // 降低最大连接数，从20降到6
	rawDB.SetConnMaxLifetime(time.Hour)

	// 初始化数据库表
	log.Println("开始数据库表迁移...")
	err = DB.AutoMigrate(model.Server{}, model.User{},
		model.Notification{}, model.AlertRule{}, model.Monitor{},
		model.MonitorHistory{}, model.Cron{}, model.Transfer{},
		model.ApiToken{}, model.NAT{}, model.DDNSProfile{}, model.DDNSRecordState{})
	if err != nil {
		log.Printf("数据库迁移失败: %v", err)
		return err
	}
	log.Println("数据库表迁移完成")

	// 启动连接池监控
	StartConnectionPoolMonitor()
	LogConnectionPoolStats() // 立即记录一次连接池状态

	// 预热数据库连接池
	WarmupDatabase()

	// 启动写入队列和插入队列
	StartDBWriteWorker()
	StartDBInsertWorker()

	// 启动监控历史记录专用工作器
	StartMonitorHistoryWorker()

	// 启动数据库维护计划
	StartDatabaseMaintenanceScheduler()

	return nil
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

	// 检查内存使用是否超过阈值 (1200MB)
	if m.Alloc > 1200*1024*1024 {
		return true
	}

	// 检查Goroutine数量是否过多
	if runtime.NumGoroutine() > 600 {
		return true
	}

	// 检查数据库连接池状态来判断系统繁忙程度
	sqlDB, err := DB.DB()
	if err != nil {
		return true
	}

	stats := sqlDB.Stats()
	// 更严格的繁忙条件判断
	if float64(stats.InUse) >= float64(stats.OpenConnections)*0.8 ||
		(stats.InUse > 0 && stats.WaitCount > 10) ||
		stats.WaitDuration > 1*time.Second {
		return true
	}

	// 快速检查是否存在数据库锁
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var result int
	err = DB.WithContext(ctx).Raw("SELECT 1").Scan(&result).Error
	if err != nil && strings.Contains(err.Error(), "database is locked") {
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
	return executeWithAdvancedRetry(operation, 5, 50*time.Millisecond, 5*time.Second)
}

// executeWithAdvancedRetry 实现指数退避重试机制
func executeWithAdvancedRetry(operation func() error, maxRetries int, baseDelay, maxDelay time.Duration) error {
	for attempt := 0; attempt < maxRetries; attempt++ {
		err := operation()
		if err == nil {
			return nil
		}

		// 判断是否是可重试的错误
		if isRetryableError(err) {
			if attempt < maxRetries-1 {
				// 指数退避策略
				delay := baseDelay * time.Duration(1<<uint(attempt))
				if delay > maxDelay {
					delay = maxDelay
				}

				// 添加随机抖动避免同步重试
				jitterBig, jErr := rand.Int(rand.Reader, big.NewInt(int64(delay/4)))
				if jErr == nil {
					jitter := time.Duration(jitterBig.Int64())
					delay += jitter
				}

				log.Printf("数据库操作重试 %d/%d，延迟 %v: %v", attempt+1, maxRetries, delay, err)
				time.Sleep(delay)
				continue
			}
		}

		// 无法重试的错误或已到达最大重试次数
		return err
	}

	return fmt.Errorf("数据库操作在 %d 次重试后失败", maxRetries)
}

// isRetryableError 判断错误是否可以重试
func isRetryableError(err error) bool {
	errMsg := err.Error()
	return strings.Contains(errMsg, "database is locked") ||
		strings.Contains(errMsg, "SQL statements in progress") ||
		strings.Contains(errMsg, "cannot commit transaction") ||
		strings.Contains(errMsg, "database table is locked") ||
		strings.Contains(errMsg, "database is busy") ||
		strings.Contains(errMsg, "disk I/O error")
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
		batchSize := 25                                // 增加批次大小到25（从20增加）
		maxRetries := 3                                // 减少重试次数，更快失败
		cutoffDate := time.Now().AddDate(0, 0, -60)    // 延长到60天，避免误删除有效历史数据
		safetyBuffer := time.Now().Add(-6 * time.Hour) // 添加6小时安全缓冲区，避免误删新数据

		// 使用非事务方式分批清理，避免长时间事务锁定
		// 清理60天前的监控记录，并确保不删除最近6小时的数据
		for {
			var count int64
			var err error

			for retry := 0; retry < maxRetries; retry++ {
				// 等待确保没有其他操作在进行
				time.Sleep(time.Duration(retry*100) * time.Millisecond)

				// 使用子查询删除，添加安全时间检查，确保不会删除新数据
				result := DB.Exec("DELETE FROM monitor_histories WHERE rowid IN (SELECT rowid FROM monitor_histories WHERE created_at < ? AND created_at < ? LIMIT ?)",
					cutoffDate.Format("2006-01-02 15:04:05"),
					safetyBuffer.Format("2006-01-02 15:04:05"),
					batchSize)

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

		// 清理孤立的监控记录，添加安全时间检查
		for {
			var count int64
			var err error

			for retry := 0; retry < maxRetries; retry++ {
				// 等待确保没有其他操作在进行
				time.Sleep(time.Duration(retry*100) * time.Millisecond)

				// 使用子查询删除孤立记录，添加时间安全检查
				result := DB.Exec("DELETE FROM monitor_histories WHERE rowid IN (SELECT rowid FROM monitor_histories WHERE monitor_id NOT IN (SELECT id FROM monitors) AND created_at < ? LIMIT ?)",
					safetyBuffer.Format("2006-01-02 15:04:05"),
					batchSize)

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
				// 使用安全更新函数替代事务
				AsyncSafeUpdateServerStatus(server.ID, map[string]interface{}{
					"cumulative_net_in_transfer":  server.State.NetInTransfer,
					"cumulative_net_out_transfer": server.State.NetOutTransfer,
					"last_online":                 server.LastActive,
					"last_state_json":             server.LastStateJSON,
				}, func(err error) {
					if err != nil {
						log.Printf("保存服务器 %s 的累计流量数据失败: %v", server.Name, err)
					}
				})
			}
		}

		// 如果需要重置累计流量，则重置服务器的累计流量
		if shouldResetTransferStats {
			server.CumulativeNetInTransfer = 0
			server.CumulativeNetOutTransfer = 0

			// 更新数据库 - 使用安全更新函数
			AsyncSafeUpdateServerStatus(server.ID, map[string]interface{}{
				"cumulative_net_in_transfer":  0,
				"cumulative_net_out_transfer": 0,
			}, nil)
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

	// 批量构造更新语句，减少数据库操作次数
	batchSize := 5 // 每批更新5个服务器
	serverBatches := make([][]uint64, 0)

	// 将服务器ID分组
	var currentBatch []uint64
	for serverID := range serverData {
		currentBatch = append(currentBatch, serverID)

		if len(currentBatch) >= batchSize {
			serverBatches = append(serverBatches, currentBatch)
			currentBatch = make([]uint64, 0)
		}
	}

	// 处理最后一批
	if len(currentBatch) > 0 {
		serverBatches = append(serverBatches, currentBatch)
	}

	// 每批异步处理
	var wg sync.WaitGroup
	for _, batch := range serverBatches {
		wg.Add(1)
		go func(serverIDs []uint64) {
			defer wg.Done()

			// 为每个服务器构建更新语句
			for _, serverID := range serverIDs {
				server := serverData[serverID]

				// 使用无事务更新
				err := SafeUpdateServerStatus(serverID, map[string]interface{}{
					"cumulative_net_in_transfer":  server.CumulativeNetInTransfer,
					"cumulative_net_out_transfer": server.CumulativeNetOutTransfer,
				})

				if err != nil {
					log.Printf("保存服务器 %s (ID:%d) 累计流量失败: %v", server.Name, serverID, err)
				}

				// 添加延迟，避免并发更新
				time.Sleep(50 * time.Millisecond)
			}
		}(batch)
	}

	// 等待所有批次完成
	wg.Wait()

	log.Printf("成功保存 %d 个服务器的累计流量数据", len(serverData))
}

// AutoSyncTraffic 自动同步流量的空实现
func AutoSyncTraffic() {
	// 功能已移除
}

// VerifyTrafficDataConsistency 验证流量数据一致性，添加重试机制
func VerifyTrafficDataConsistency() {
	// 检查系统负载
	if isSystemBusy() {
		log.Printf("系统繁忙，跳过流量数据一致性检查")
		return
	}

	// 使用读锁安全获取服务器列表
	ServerLock.RLock()
	serverIDs := make([]uint64, 0, len(ServerList))
	for serverID, server := range ServerList {
		if server == nil {
			continue
		}
		serverIDs = append(serverIDs, serverID)
	}
	ServerLock.RUnlock()

	// 分批处理，每批最多处理5个服务器
	batchSize := 5
	for i := 0; i < len(serverIDs); i += batchSize {
		end := i + batchSize
		if end > len(serverIDs) {
			end = len(serverIDs)
		}

		currentBatch := serverIDs[i:end]

		// 处理当前批次
		for _, serverID := range currentBatch {
			// 使用读锁获取内存中的服务器数据
			ServerLock.RLock()
			server := ServerList[serverID]
			if server == nil {
				ServerLock.RUnlock()
				continue
			}

			memoryIn := server.CumulativeNetInTransfer
			memoryOut := server.CumulativeNetOutTransfer
			ServerLock.RUnlock()

			// 从数据库读取数据，使用重试机制
			var dbServer model.Server
			err := executeWithAdvancedRetry(func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				return DB.WithContext(ctx).First(&dbServer, serverID).Error
			}, 3, 100*time.Millisecond, 1*time.Second)

			if err != nil {
				log.Printf("无法从数据库读取服务器 %d: %v", serverID, err)
				continue
			}

			// 比较内存和数据库中的数据
			dbIn := dbServer.CumulativeNetInTransfer
			dbOut := dbServer.CumulativeNetOutTransfer

			if memoryIn != dbIn || memoryOut != dbOut {
				log.Printf("[不一致] 服务器 %s (ID:%d) - 内存: 入站=%d 出站=%d, 数据库: 入站=%d 出站=%d",
					server.Name, serverID,
					memoryIn, memoryOut,
					dbIn, dbOut)
			}

			// 添加短暂延迟，避免同时访问导致锁争用
			time.Sleep(50 * time.Millisecond)
		}
	}
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
		highThreshold:    1200, // 提高到1200MB，减少误触发清理
		warningThreshold: 600,  // 提高到600MB警告阈值
		maxGoroutines:    600,  // 提高到600个goroutine限制
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

// OptimizeDatabase 定期优化数据库性能
func OptimizeDatabase() {
	if isSystemBusy() {
		log.Println("系统繁忙，跳过数据库优化")
		return
	}

	log.Println("开始数据库优化...")

	// 先获取数据库状态
	sqlDB, err := DB.DB()
	if err != nil {
		log.Printf("获取数据库连接失败: %v", err)
		return
	}

	// 记录连接池状态
	LogConnectionPoolStats()

	// 只进行轻量级优化操作，避免长时间锁定
	err = executeWithAdvancedRetry(func() error {
		// 使用短超时执行optimize
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return DB.WithContext(ctx).Exec("PRAGMA optimize").Error
	}, 3, 100*time.Millisecond, 500*time.Millisecond)

	if err != nil {
		log.Printf("数据库优化失败: %v", err)
	}

	// 安排低峰期执行WAL检查点和VACUUM，避免立即执行
	hour := time.Now().Hour()
	// 只在深夜3-5点之间尝试执行完整优化
	if hour >= 3 && hour <= 5 && !isSystemBusy() {
		go func() {
			// 在后台线程中执行，等待更长时间确保无其他操作
			time.Sleep(30 * time.Second)

			// 再次检查系统负载
			if isSystemBusy() {
				log.Printf("系统繁忙，跳过深度优化")
				return
			}

			// 重新获取连接状态
			stats := sqlDB.Stats()
			if stats.InUse > 2 {
				log.Printf("数据库有%d个活跃连接，跳过深度优化", stats.InUse)
				return
			}

			// 执行WAL检查点（被动模式，不阻塞其他操作）
			err := executeWithAdvancedRetry(func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				return DB.WithContext(ctx).Exec("PRAGMA wal_checkpoint(PASSIVE)").Error
			}, 3, 100*time.Millisecond, 500*time.Millisecond)

			if err != nil {
				log.Printf("WAL检查点失败: %v", err)
				return
			}

			// 仅在确认系统极度空闲时执行VACUUM
			if runtime.NumGoroutine() < 200 && !isSystemBusy() {
				log.Println("系统空闲，开始执行VACUUM操作...")
				vacCtx, vacCancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer vacCancel()

				if err := DB.WithContext(vacCtx).Exec("VACUUM").Error; err != nil {
					log.Printf("VACUUM操作失败: %v", err)
				} else {
					log.Println("VACUUM操作完成")
				}
			}
		}()
	}

	log.Println("数据库优化任务已安排")
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

// 数据库写入队列，用于序列化数据库操作，避免并发冲突
type DBWriteRequest struct {
	ServerID  uint64
	TableName string
	Updates   map[string]interface{}
	Callback  func(error)
}

// 数据库插入队列，用于序列化数据库插入操作
type DBInsertRequest struct {
	TableName string
	Data      map[string]interface{}
	Callback  func(error)
}

var (
	dbWriteQueue         = make(chan DBWriteRequest, 1000)
	dbWriteWorkerStarted = false
	dbWriteWorkerMutex   sync.Mutex

	dbInsertQueue         = make(chan DBInsertRequest, 1000)
	dbInsertWorkerStarted = false
	dbInsertWorkerMutex   sync.Mutex

	monitorHistoryQueue         = make(chan DBInsertRequest, 2000)
	monitorHistoryWorkerStarted = false
	monitorHistoryWorkerMutex   sync.Mutex

	// 监控历史批处理
	monitorHistoryBatchQueue      = make(chan DBInsertRequest, 10000) // 更大的缓冲区，从5000增加到10000
	monitorHistoryBatchProcessor  sync.Once
	monitorHistoryBatchBuffer     = make([]DBInsertRequest, 0, 200) // 从100增加到200
	monitorHistoryBatchBufferLock sync.Mutex
	monitorHistoryBatchSize       = 100              // 默认批量大小，从50增加到100
	monitorHistoryBatchInterval   = 10 * time.Second // 从3秒增加到10秒，降低处理频率
)

// StartDBWriteWorker 启动数据库写入工作器
func StartDBWriteWorker() {
	dbWriteWorkerMutex.Lock()
	defer dbWriteWorkerMutex.Unlock()

	if dbWriteWorkerStarted {
		return
	}

	dbWriteWorkerStarted = true
	log.Println("启动数据库写入工作器...")

	go func() {
		for req := range dbWriteQueue {
			err := executeDBWriteRequest(req)
			if req.Callback != nil {
				req.Callback(err)
			}
		}
	}()
}

// StartDBInsertWorker 启动数据库插入工作器
func StartDBInsertWorker() {
	dbInsertWorkerMutex.Lock()
	defer dbInsertWorkerMutex.Unlock()

	if dbInsertWorkerStarted {
		return
	}

	dbInsertWorkerStarted = true
	log.Println("启动数据库插入工作器...")

	go func() {
		for req := range dbInsertQueue {
			err := executeDBInsertRequest(req)
			if req.Callback != nil {
				req.Callback(err)
			}
		}
	}()
}

// executeDBWriteRequest 执行单个数据库写入请求
func executeDBWriteRequest(req DBWriteRequest) error {
	return ExecuteWithRetry(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// 直接更新，避免不必要的事务开销
		return DB.WithContext(ctx).Table(req.TableName).Where("id = ?", req.ServerID).Updates(req.Updates).Error
	})
}

// executeDBInsertRequest 执行单个数据库插入请求
func executeDBInsertRequest(req DBInsertRequest) error {
	// 设置更长的超时时间
	return ExecuteWithRetry(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second) // 从5秒增加到8秒
		defer cancel()

		// 特殊处理monitor_histories表，确保不使用@id字段
		if req.TableName == "monitor_histories" {
			// 移除@id字段如果存在
			if _, exists := req.Data["@id"]; exists {
				delete(req.Data, "@id")
			}

			// 避免直接使用GORM的Create方法，改用手动SQL
			fields, values, args := prepareInsertSQL(req.Data)
			insertSQL := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
				req.TableName, fields, values)

			return DB.WithContext(ctx).Exec(insertSQL, args...).Error
		}

		// 直接插入，避免不必要的事务开销
		return DB.WithContext(ctx).Table(req.TableName).Create(req.Data).Error
	})
}

// prepareInsertSQL 准备插入SQL的字段、占位符和参数
func prepareInsertSQL(data map[string]interface{}) (string, string, []interface{}) {
	var fields []string
	var placeholders []string
	var args []interface{}

	for field, value := range data {
		fields = append(fields, field)
		placeholders = append(placeholders, "?")
		args = append(args, value)
	}

	return strings.Join(fields, ", "), strings.Join(placeholders, ", "), args
}

// AsyncDBUpdate 异步数据库更新，通过队列避免并发冲突
func AsyncDBUpdate(serverID uint64, tableName string, updates map[string]interface{}, callback func(error)) {
	select {
	case dbWriteQueue <- DBWriteRequest{
		ServerID:  serverID,
		TableName: tableName,
		Updates:   updates,
		Callback:  callback,
	}:
		// 成功入队
	default:
		// 队列满，执行回调通知错误
		if callback != nil {
			callback(fmt.Errorf("数据库写入队列已满"))
		}
	}
}

// AsyncDBInsert 异步数据库插入，通过队列避免并发冲突
func AsyncDBInsert(tableName string, data map[string]interface{}, callback func(error)) {
	// 如果是监控历史记录，使用专用队列
	if tableName == "monitor_histories" {
		AsyncMonitorHistoryInsert(data, callback)
		return
	}

	select {
	case dbInsertQueue <- DBInsertRequest{
		TableName: tableName,
		Data:      data,
		Callback:  callback,
	}:
		// 成功入队
	default:
		// 队列满，执行回调通知错误
		if callback != nil {
			callback(fmt.Errorf("数据库插入队列已满"))
		}
	}
}

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

	// 预热连接池 - 降低并发度，避免锁竞争
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ { // 从8减少到3
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			// 延迟执行，避免同时访问
			time.Sleep(time.Duration(index*500) * time.Millisecond)

			// 执行简单查询来预热连接
			var count int64
			DB.Model(&model.Server{}).Count(&count)
			log.Printf("预热连接 %d 完成，服务器数量: %d", index, count)
		}(i)
	}

	wg.Wait()
	log.Println("数据库连接池预热完成")
}

// StartMonitorHistoryWorker 启动监控历史记录插入专用工作器
func StartMonitorHistoryWorker() {
	monitorHistoryWorkerMutex.Lock()
	defer monitorHistoryWorkerMutex.Unlock()

	if monitorHistoryWorkerStarted {
		return
	}

	monitorHistoryWorkerStarted = true
	log.Println("启动监控历史记录处理工作器...")

	go func() {
		for req := range monitorHistoryQueue {
			// 确保没有使用@id字段
			if _, exists := req.Data["@id"]; exists {
				delete(req.Data, "@id")
			}

			// 增加更多字段检查和验证
			var monitorID uint64
			if mid, ok := req.Data["monitor_id"]; ok {
				if midVal, ok := mid.(uint64); ok {
					monitorID = midVal
				}
			}

			// 简单地重试几次，然后放弃
			var err error
			for retry := 0; retry < 5; retry++ { // 增加重试次数
				func() {
					// 使用函数包装以确保cancel被调用
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()

					// 不使用事务，直接执行插入
					sqlStr := "INSERT INTO monitor_histories (monitor_id, server_id, avg_delay, data, up, down, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"

					// 准备参数
					now := time.Now()
					var serverID uint64
					var avgDelay float32
					var data string
					var up, down uint64

					if v, ok := req.Data["monitor_id"]; ok {
						if val, ok := v.(uint64); ok {
							monitorID = val
						}
					}
					if v, ok := req.Data["server_id"]; ok {
						if val, ok := v.(uint64); ok {
							serverID = val
						}
					}
					if v, ok := req.Data["avg_delay"]; ok {
						if val, ok := v.(float32); ok {
							avgDelay = val
						} else if val, ok := v.(float64); ok {
							avgDelay = float32(val)
						}
					}
					if v, ok := req.Data["data"]; ok {
						if val, ok := v.(string); ok {
							data = val
						}
					}
					if v, ok := req.Data["up"]; ok {
						if val, ok := v.(uint64); ok {
							up = val
						} else if val, ok := v.(int); ok {
							up = uint64(val)
						}
					}
					if v, ok := req.Data["down"]; ok {
						if val, ok := v.(uint64); ok {
							down = val
						} else if val, ok := v.(int); ok {
							down = uint64(val)
						}
					}

					// 直接执行SQL，避免GORM的自动化处理
					err = DB.WithContext(ctx).Exec(sqlStr, monitorID, serverID, avgDelay, data, up, down, now, now).Error
				}()

				if err == nil {
					break
				}

				// 只对特定错误进行重试
				if strings.Contains(err.Error(), "database is locked") ||
					strings.Contains(err.Error(), "SQL statements in progress") ||
					strings.Contains(err.Error(), "cannot commit transaction") {
					if retry < 4 {
						// 使用简单的延迟策略，避免过度复杂的重试逻辑
						delay := time.Duration(100*(retry+1)) * time.Millisecond
						time.Sleep(delay)
						log.Printf("监控历史记录插入失败 (MonitorID: %d)，重试 %d/5: %v", monitorID, retry+1, err)
						continue
					}
				} else if strings.Contains(err.Error(), "no column named @id") {
					// 如果是@id列错误，记录日志但不重试
					log.Printf("监控历史记录插入遇到@id列错误 (MonitorID: %d): %v", monitorID, err)
					// 尝试直接使用Map创建，避免使用@id
					if retry < 4 {
						// 清理数据
						cleanData := make(map[string]interface{})
						for k, v := range req.Data {
							if k != "@id" {
								cleanData[k] = v
							}
						}

						ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
						err = DB.WithContext(ctx).Table("monitor_histories").Create(cleanData).Error
						cancel()

						if err == nil {
							break
						}

						delay := time.Duration(100*(retry+1)) * time.Millisecond
						time.Sleep(delay)
						continue
					}
				}

				// 其他错误不重试
				break
			}

			if req.Callback != nil {
				req.Callback(err)
			}
		}
	}()
}

// AsyncMonitorHistoryInsert 异步插入监控历史记录数据
func AsyncMonitorHistoryInsert(data map[string]interface{}, callback func(error)) {
	select {
	case monitorHistoryQueue <- DBInsertRequest{
		TableName: "monitor_histories",
		Data:      data,
		Callback:  callback,
	}:
		// 成功入队
	default:
		// 队列满，丢弃数据并记录日志，避免阻塞主流程
		log.Printf("监控历史记录队列已满，丢弃一条记录")
		if callback != nil {
			callback(fmt.Errorf("监控历史记录队列已满"))
		}
	}
}

// StartDatabaseMaintenanceScheduler 启动数据库维护计划
func StartDatabaseMaintenanceScheduler() {
	log.Println("启动数据库维护计划...")

	// 每6小时执行一次数据库优化
	go func() {
		// 启动时延迟30分钟再执行第一次优化，避免与启动时的其他操作冲突
		time.Sleep(30 * time.Minute)

		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()

		for range ticker.C {
			if !isSystemBusy() {
				OptimizeDatabase()
			} else {
				log.Println("系统繁忙，跳过定期数据库优化")
			}
		}
	}()

	// 安排更频繁的WAL检查点（每小时）
	go func() {
		// 启动时延迟5分钟
		time.Sleep(5 * time.Minute)

		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()

		for range ticker.C {
			if !isSystemBusy() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				if err := DB.WithContext(ctx).Exec("PRAGMA wal_checkpoint(PASSIVE)").Error; err != nil {
					log.Printf("执行WAL检查点失败: %v", err)
				}
				cancel()
			}
		}
	}()
}

// SafeUpdateServerStatus 使用无事务模式更新服务器状态，避免锁竞争
func SafeUpdateServerStatus(serverID uint64, updates map[string]interface{}) error {
	// 构建SQL语句
	var setClauses []string
	var args []interface{}

	for key, value := range updates {
		setClauses = append(setClauses, fmt.Sprintf("`%s` = ?", key))
		args = append(args, value)
	}

	// 添加服务器ID到参数列表
	args = append(args, serverID)
	sql := fmt.Sprintf("UPDATE servers SET %s WHERE id = ?", strings.Join(setClauses, ", "))

	// 执行SQL，不使用事务
	return executeWithAdvancedRetry(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		return DB.WithContext(ctx).Exec(sql, args...).Error
	}, 5, 20*time.Millisecond, 500*time.Millisecond)
}

// AsyncSafeUpdateServerStatus 异步安全更新服务器状态
func AsyncSafeUpdateServerStatus(serverID uint64, updates map[string]interface{}, callback func(error)) {
	go func() {
		err := SafeUpdateServerStatus(serverID, updates)
		if callback != nil {
			callback(err)
		} else if err != nil {
			log.Printf("异步更新服务器 %d 状态失败: %v", serverID, err)
		}
	}()
}

// GetCachedServerStatus 获取缓存的服务器状态，如果不存在则从数据库加载
func GetCachedServerStatus(serverID uint64) (*model.Server, error) {
	cacheKey := fmt.Sprintf("server:%d", serverID)

	// 尝试从缓存获取
	if cached, found := serverStatusCache.Get(cacheKey); found {
		if server, ok := cached.(*model.Server); ok {
			return server, nil
		}
	}

	// 缓存未命中，从数据库加载，使用重试机制
	var server model.Server
	err := executeWithAdvancedRetry(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		return DB.WithContext(ctx).First(&server, serverID).Error
	}, 3, 100*time.Millisecond, 1*time.Second)

	if err != nil {
		return nil, err
	}

	// 更新缓存
	serverStatusCache.Set(cacheKey, &server, cache.DefaultExpiration)
	return &server, nil
}

// UpdateServerStatusCache 更新服务器状态缓存
func UpdateServerStatusCache(server *model.Server) {
	if server == nil || server.ID == 0 {
		return
	}

	cacheKey := fmt.Sprintf("server:%d", server.ID)
	serverStatusCache.Set(cacheKey, server, cache.DefaultExpiration)
}

// InvalidateServerCache 使服务器缓存失效
func InvalidateServerCache(serverID uint64) {
	cacheKey := fmt.Sprintf("server:%d", serverID)
	serverStatusCache.Delete(cacheKey)
}

// StartMonitorHistoryBatchProcessor 启动监控历史记录批处理器
func StartMonitorHistoryBatchProcessor() {
	monitorHistoryBatchProcessor.Do(func() {
		log.Println("启动监控历史记录批处理器...")

		// 创建刷新定时器
		flushTicker := time.NewTicker(monitorHistoryBatchInterval)

		go func() {
			defer flushTicker.Stop()

			for {
				select {
				case req := <-monitorHistoryBatchQueue:
					// 添加到缓冲区
					monitorHistoryBatchBufferLock.Lock()
					monitorHistoryBatchBuffer = append(monitorHistoryBatchBuffer, req)

					// 如果达到批处理大小，执行批处理
					if len(monitorHistoryBatchBuffer) >= monitorHistoryBatchSize {
						batch := monitorHistoryBatchBuffer
						monitorHistoryBatchBuffer = make([]DBInsertRequest, 0, 200) // 从100增加到200
						monitorHistoryBatchBufferLock.Unlock()

						// 异步执行批处理
						go processBatchInserts(batch)
					} else {
						monitorHistoryBatchBufferLock.Unlock()
					}

				case <-flushTicker.C:
					// 定时刷新缓冲区
					monitorHistoryBatchBufferLock.Lock()
					if len(monitorHistoryBatchBuffer) > 0 {
						batch := monitorHistoryBatchBuffer
						monitorHistoryBatchBuffer = make([]DBInsertRequest, 0, 200) // 从100增加到200
						monitorHistoryBatchBufferLock.Unlock()

						// 异步执行批处理
						go processBatchInserts(batch)
					} else {
						monitorHistoryBatchBufferLock.Unlock()
					}
				}
			}
		}()
	})
}

// processBatchInserts 批量处理插入请求
func processBatchInserts(batch []DBInsertRequest) {
	if len(batch) == 0 {
		return
	}

	// 分组处理，按表名分组
	tableGroups := make(map[string][]DBInsertRequest)
	for _, req := range batch {
		tableGroups[req.TableName] = append(tableGroups[req.TableName], req)
	}

	// 对每个表进行批量处理
	for tableName, requests := range tableGroups {
		// 仅处理monitor_histories表
		if tableName != "monitor_histories" {
			for _, req := range requests {
				executeDBInsertRequest(req)
				if req.Callback != nil {
					req.Callback(nil)
				}
			}
			continue
		}

		// 构建批量插入SQL
		if len(requests) == 1 {
			// 单条记录直接处理
			err := executeDBInsertRequest(requests[0])
			if requests[0].Callback != nil {
				requests[0].Callback(err)
			}
			continue
		}

		// 多条记录构建批量插入
		err := batchInsertMonitorHistories(requests)

		// 处理回调
		for _, req := range requests {
			if req.Callback != nil {
				req.Callback(err)
			}
		}
	}
}

// batchInsertMonitorHistories 批量插入监控历史记录
func batchInsertMonitorHistories(requests []DBInsertRequest) error {
	if len(requests) == 0 {
		return nil
	}

	// 收集所有数据
	allData := make([]map[string]interface{}, len(requests))
	for i, req := range requests {
		// 移除@id字段
		if _, exists := req.Data["@id"]; exists {
			delete(req.Data, "@id")
		}
		allData[i] = req.Data
	}

	// 构建批量插入SQL
	var fieldNames []string

	// 获取所有字段名
	for field := range allData[0] {
		fieldNames = append(fieldNames, field)
	}

	// 构建VALUES部分
	var valueStrings []string
	var valueArgs []interface{}

	for _, data := range allData {
		valueString := "("
		var placeholders []string

		for _, field := range fieldNames {
			placeholders = append(placeholders, "?")
			valueArgs = append(valueArgs, data[field])
		}

		valueString += strings.Join(placeholders, ", ") + ")"
		valueStrings = append(valueStrings, valueString)
	}

	// 构建最终SQL
	query := fmt.Sprintf(
		"INSERT INTO monitor_histories (%s) VALUES %s",
		strings.Join(fieldNames, ", "),
		strings.Join(valueStrings, ", "),
	)

	// 使用改进的重试机制包装批量插入，提高原子性和可靠性
	return executeWithAdvancedRetry(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // 从20秒增加到30秒
		defer cancel()

		// 使用事务确保批量插入的原子性
		return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// 在事务内执行批量插入
			result := tx.Exec(query, valueArgs...)
			if result.Error != nil {
				// 专门处理锁定错误，使其可重试
				if strings.Contains(result.Error.Error(), "database is locked") ||
					strings.Contains(result.Error.Error(), "SQL statements in progress") ||
					strings.Contains(result.Error.Error(), "cannot commit") {
					log.Printf("数据库锁定，等待重试批量插入: %v", result.Error)
				} else {
					// 其他错误情况
					log.Printf("批量插入失败，错误: %v", result.Error)
				}
				return result.Error
			}

			// 事务提交
			return nil
		})
	}, 10, 200*time.Millisecond, 8*time.Second) // 增加重试次数和等待时间
}

// AsyncBatchMonitorHistoryInsert 将监控历史记录添加到批处理队列
func AsyncBatchMonitorHistoryInsert(data map[string]interface{}, callback func(error)) {
	// 确保批处理器已启动
	StartMonitorHistoryBatchProcessor()

	// 为ICMP和TCP监控尝试直接保存一份到数据库，确保数据不丢失
	monitorID, hasMonitorID := data["monitor_id"]
	if hasMonitorID {
		// 安全类型转换
		var midVal uint64
		switch mid := monitorID.(type) {
		case uint64:
			midVal = mid
		case uint:
			midVal = uint64(mid)
		case int64:
			midVal = uint64(mid)
		case int:
			midVal = uint64(mid)
		case float64:
			midVal = uint64(mid)
		default:
			// 无法转换，使用0值
			midVal = 0
		}

		// 对ICMP和TCP监控使用直接插入，绕过批处理系统
		if midVal == model.TaskTypeICMPPing || midVal == model.TaskTypeTCPPing {
			// 克隆数据，避免竞态条件
			directData := make(map[string]interface{})
			for k, v := range data {
				directData[k] = v
			}

			go func(insertData map[string]interface{}) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()

				// 使用直接SQL插入，避免GORM的@id字段问题
				sqlStr := "INSERT INTO monitor_histories (monitor_id, server_id, avg_delay, data, up, down, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"

				now := time.Now()
				monitorID, _ := insertData["monitor_id"].(uint64)

				var serverID uint64
				if sid, ok := insertData["server_id"]; ok {
					switch s := sid.(type) {
					case uint64:
						serverID = s
					case uint:
						serverID = uint64(s)
					case int64:
						serverID = uint64(s)
					case int:
						serverID = uint64(s)
					case float64:
						serverID = uint64(s)
					}
				}

				var avgDelay float32
				if ad, ok := insertData["avg_delay"]; ok {
					switch a := ad.(type) {
					case float32:
						avgDelay = a
					case float64:
						avgDelay = float32(a)
					}
				}

				dataStr, _ := insertData["data"].(string)

				var up, down uint64
				if u, ok := insertData["up"]; ok {
					switch uv := u.(type) {
					case uint64:
						up = uv
					case uint:
						up = uint64(uv)
					case int64:
						up = uint64(uv)
					case int:
						up = uint64(uv)
					case float64:
						up = uint64(uv)
					}
				}

				if d, ok := insertData["down"]; ok {
					switch dv := d.(type) {
					case uint64:
						down = dv
					case uint:
						down = uint64(dv)
					case int64:
						down = uint64(dv)
					case int:
						down = uint64(dv)
					case float64:
						down = uint64(dv)
					}
				}

				// 使用高级重试逻辑执行插入
				err := executeWithAdvancedRetry(func() error {
					return DB.WithContext(ctx).Exec(sqlStr, monitorID, serverID, avgDelay, dataStr, up, down, now, now).Error
				}, 5, 100*time.Millisecond, 3*time.Second)

				if err != nil {
					log.Printf("ICMP/TCP监控数据直接插入失败 (MonitorID: %d): %v", monitorID, err)
				} else {
					log.Printf("ICMP/TCP监控数据直接插入成功 (MonitorID: %d)", monitorID)
				}
			}(directData)
		}
	}

	// 同时仍然放入批处理队列作为备份
	select {
	case monitorHistoryBatchQueue <- DBInsertRequest{
		TableName: "monitor_histories",
		Data:      data,
		Callback:  callback,
	}:
		// 成功入队
	default:
		// 队列满，丢弃数据并记录日志
		log.Printf("监控历史记录批处理队列已满，丢弃一条记录")
		if callback != nil {
			go callback(fmt.Errorf("监控历史记录批处理队列已满"))
		}
	}
}

// VerifyMonitorHistoryConsistency 检查监控历史记录的一致性
func VerifyMonitorHistoryConsistency() {
	// 检查是否有其他重要操作正在进行
	if isSystemBusy() {
		log.Printf("系统繁忙，跳过监控历史记录一致性检查")
		return
	}

	log.Printf("执行监控历史记录一致性检查...")

	// 使用重试机制执行数据库查询
	var count int64
	err := executeWithAdvancedRetry(func() error {
		// 检查最近30分钟内添加的ICMP/TCP监控记录
		checkTime := time.Now().Add(-30 * time.Minute)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		return DB.WithContext(ctx).Model(&model.MonitorHistory{}).
			Where("created_at > ? AND monitor_id IN (?, ?)",
				checkTime.Format("2006-01-02 15:04:05"),
				model.TaskTypeICMPPing, model.TaskTypeTCPPing).
			Count(&count).Error
	}, 5, 200*time.Millisecond, 2*time.Second)

	if err != nil {
		log.Printf("检查监控历史记录失败: %v", err)
		return
	}

	log.Printf("最近30分钟内ICMP/TCP监控记录数: %d", count)

	// 如果数量异常少，触发报警
	if count < 10 { // 根据实际情况调整阈值
		log.Printf("警告: ICMP/TCP监控记录数量异常少，可能存在数据丢失")

		// 记录警告日志，不通过通知系统发送，避免依赖错误
		log.Printf("监控历史记录一致性警告: 最近30分钟内仅有 %d 条ICMP/TCP监控记录，可能存在数据丢失", count)

		// 强制执行数据库优化，延迟执行避免冲突
		go func() {
			time.Sleep(30 * time.Second)
			if !isSystemBusy() {
				optimizeDatabase()
			}
		}()
	}

	// 检查空数据记录
	var emptyCount int64
	err = executeWithAdvancedRetry(func() error {
		checkTime := time.Now().Add(-30 * time.Minute)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		return DB.WithContext(ctx).Model(&model.MonitorHistory{}).
			Where("created_at > ? AND (data = '' OR data IS NULL)",
				checkTime.Format("2006-01-02 15:04:05")).
			Count(&emptyCount).Error
	}, 5, 200*time.Millisecond, 2*time.Second)

	if err != nil {
		log.Printf("检查空数据记录失败: %v", err)
		return
	}

	if emptyCount > 0 {
		log.Printf("警告: 发现 %d 条空数据记录", emptyCount)
	}
}
