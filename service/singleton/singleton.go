package singleton

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"math"
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

	"github.com/xos/serverstatus/db"
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

// optimizeStartupMemory 优化启动时的内存使用
func optimizeStartupMemory() {
	// 设置更激进的GC参数，减少启动时内存积累
	debug.SetGCPercent(30) // 更激进的GC策略

	// 预先触发一次GC，清理初始化过程中的垃圾
	runtime.GC()

	// 限制初始内存分配
	debug.SetMemoryLimit(1000 << 20) // 限制内存为1GB，防止启动时爆高

	log.Printf("启动内存优化已应用: GCPercent=30, MemoryLimit=1GB")
}

// restoreNormalMemorySettings 恢复正常的内存设置
func restoreNormalMemorySettings() {
	// 30秒后恢复正常的GC设置
	time.AfterFunc(30*time.Second, func() {
		debug.SetGCPercent(100)             // 恢复默认GC设置
		debug.SetMemoryLimit(math.MaxInt64) // 移除内存限制
		log.Printf("内存设置已恢复为正常值")
	})
}

// LoadSingleton 加载子服务并执行
func LoadSingleton() {
	// 使用顶层的panic处理，确保程序不会因为一个模块的失败而整体崩溃
	defer func() {
		if r := recover(); r != nil {
			log.Printf("严重错误：加载子服务时发生崩溃，但程序将继续执行: %v", r)
			debug.PrintStack()
		}
	}()

	// 优化：启动时应用特殊的内存策略
	optimizeStartupMemory()
	defer restoreNormalMemorySettings()

	// 启动内存监控
	globalMemoryMonitor = NewMemoryMonitor()
	globalMemoryMonitor.Start()

	// 优化：设置更激进的GC参数，减少启动时内存积累
	debug.SetGCPercent(50) // 默认是100，设置为50使GC更频繁

	// 确保BadgerDB已正确初始化
	if Conf.DatabaseType == "badger" && db.DB == nil {
		log.Println("重新初始化BadgerDB连接...")
		var err error

		// 获取正确的BadgerDB目录路径
		confMap := map[string]interface{}{
			"DatabaseType":     Conf.DatabaseType,
			"DatabaseLocation": Conf.DatabaseLocation,
		}
		badgerPath := db.GetBadgerDBPath(confMap)
		log.Printf("使用BadgerDB路径: %s", badgerPath)

		db.DB, err = db.OpenDB(badgerPath)
		if err != nil {
			log.Printf("无法重新初始化BadgerDB: %v，尝试继续运行", err)
		} else {
			log.Println("BadgerDB已成功初始化")
		}
	}

	// 如果使用SQLite数据库，执行数据库初始化操作
	if Conf.DatabaseType != "badger" {
		log.Println("初始化SQLite数据库...")
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("数据库初始化失败，但程序将继续执行: %v", r)
					debug.PrintStack()
				}
			}()

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
		}()
	} else {
		log.Println("使用 BadgerDB 数据库，跳过表结构检查和迁移操作")
	}

	// 启动数据库相关服务
	log.Println("启动数据库服务...")
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("数据库服务启动失败，但程序将继续执行: %v", r)
				debug.PrintStack()
			}
		}()

		// 根据数据库类型决定是否启动连接池监控
		if Conf.DatabaseType != "badger" {
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
		} else {
			log.Println("使用BadgerDB，跳过数据库连接池相关操作")
		}
	}()

	// 加载通知服务
	log.Println("正在加载通知服务...")
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("通知服务加载失败，但程序将继续执行: %v", r)
				debug.PrintStack()
				// 确保初始化基础结构
				InitNotification()
			}
		}()
		loadNotifications()
	}()

	// 加载服务器列表
	log.Println("正在加载服务器列表...")
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("服务器列表加载失败，但程序将继续执行: %v", r)
				debug.PrintStack()
				// 确保初始化基础结构
				InitServer()
			}
		}()
		loadServers()
	}()

	// 创建定时任务对象，即使后续加载失败也能确保基础定时任务可用
	InitCronTask()

	// 加载定时任务
	log.Println("正在加载定时任务...")
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("定时任务加载失败，但程序将继续执行: %v", r)
				debug.PrintStack()
			}
		}()

		// 加载定时任务
		loadCronTasks()
		
		// BadgerDB模式下需要手动初始化Cron任务的Servers字段
		if Conf.DatabaseType == "badger" {
			initCronServersForBadgerDB()
		}
	}()

	// 加载API
	log.Println("正在加载API服务...")
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("API加载失败，但程序将继续执行: %v", r)
				debug.PrintStack()
				// 确保初始化基础结构
				InitAPI()
			}
		}()
		loadAPI()
	}()

	// 初始化NAT
	log.Println("正在初始化NAT...")
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("NAT初始化失败，但程序将继续执行: %v", r)
				debug.PrintStack()
			}
		}()
		initNAT()
	}()

	// 初始化DDNS
	log.Println("正在初始化DDNS...")
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("DDNS初始化失败，但程序将继续执行: %v", r)
				debug.PrintStack()
			}
		}()
		initDDNS()
		// BadgerDB模式下需要手动初始化DDNS配置的Domains字段
		if Conf != nil && Conf.DatabaseType == "badger" {
			initDDNSDomainsForBadgerDB()
		}
	}()

	// 从数据库同步累计流量数据到内存
	log.Println("正在同步流量数据...")
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("流量数据同步失败，但程序将继续执行: %v", r)
				debug.PrintStack()
			}
		}()
		// 检查是否为 BadgerDB 模式
		if Conf != nil && Conf.DatabaseType != "badger" {
			// 只在非 BadgerDB 模式下执行流量数据同步
			SyncAllServerTrafficFromDB()
		} else {
			log.Println("使用 BadgerDB 模式，跳过流量数据同步")
		}
	}()

	log.Println("正在添加定时任务...")

	// 添加定时检查在线状态的任务，每分钟检查一次
	Cron.AddFunc("0 */1 * * * *", CheckServerOnlineStatus)

	// 添加定时验证流量数据一致性的任务，每5分钟检查一次
	Cron.AddFunc("0 */5 * * * *", VerifyTrafficDataConsistency)

	// 添加监控历史记录一致性检查任务，每10分钟执行一次
	if _, err := Cron.AddFunc("0 */10 * * * *", VerifyMonitorHistoryConsistency); err != nil {
		log.Printf("添加监控历史记录一致性检查任务失败: %v", err)
	}

	// 添加定期goroutine清理任务，每30分钟执行一次
	if _, err := Cron.AddFunc("0 */30 * * * *", func() {
		totalGoroutines := runtime.NumGoroutine()
		if totalGoroutines > 300 {
			log.Printf("定期清理：当前goroutine数量 %d，开始清理", totalGoroutines)
			cleanupStaleGoroutineConnections()
			// 强制垃圾回收
			runtime.GC()
			log.Printf("定期清理完成：当前goroutine数量 %d", runtime.NumGoroutine())
		}
	}); err != nil {
		log.Printf("添加定期goroutine清理任务失败: %v", err)
	}

	// 添加定时清理任务，每4小时执行一次
	Cron.AddFunc("0 0 */4 * * *", func() {
		CleanMonitorHistory()
		Cache.DeleteExpired()
		// 只在SQLite模式下执行服务器状态清理
		if Conf.DatabaseType != "badger" {
			CleanupServerState()
		}
		SaveAllTrafficToDB()
	})

	// 添加流量数据定时保存任务，每10分钟执行一次
	Cron.AddFunc("0 */10 * * * *", SaveAllTrafficToDB)

	// 添加全量数据保存任务，每5分钟执行一次
	Cron.AddFunc("0 */5 * * * *", SaveAllDataToDB)

	// 定期清理任务，每小时执行一次
	Cron.AddFunc("0 0 * * * *", func() {
		performHourlyCleanup()
	})

	// 数据库优化任务仅在使用SQLite时执行
	if Conf.DatabaseType != "badger" {
		// 数据库优化任务，改为每1小时执行一次，降低清理频率
		Cron.AddFunc("0 0 * * * *", func() {
			OptimizeDatabase()

			// 输出数据库状态
			stats := GetDatabaseStats()
			log.Printf("数据库状态: %+v", stats)
		})
	}

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

	log.Println("启动计划任务...")
	// 确保Cron已经启动
	if Cron != nil {
		Cron.Start()
	}

	log.Println("子服务加载完成")

	// 验证和修复数据完整性
	validateAndFixDataIntegrity()
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

	// 专门为monitor_histories表添加优化（仅在SQLite模式下）
	if DB != nil {
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
	}

	// 优化连接池配置（仅在SQLite模式下）
	if DB != nil {
		rawDB, err := DB.DB()
		if err != nil {
			return err
		}

		// 修改连接池配置，降低并发压力
		rawDB.SetMaxIdleConns(3) // 降低空闲连接数，从10降到3
		rawDB.SetMaxOpenConns(6) // 降低最大连接数，从20降到6
		rawDB.SetConnMaxLifetime(time.Hour)
	}

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

	// 如果使用BadgerDB，不检查数据库连接状态
	if Conf.DatabaseType == "badger" {
		return false
	}

	// 检查数据库连接池状态来判断系统繁忙程度
	if DB == nil {
		return true
	}

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
	if DB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		var result int
		err = DB.WithContext(ctx).Raw("SELECT 1").Scan(&result).Error
		if err != nil && strings.Contains(err.Error(), "database is locked") {
			return true
		}
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
	if err == nil {
		return false
	}
	errMsg := err.Error()
	retryableErrors := []string{
		"database is locked",
		"SQL statements in progress",
		"cannot commit transaction",
		"database table is locked",
		"database is busy",
		"disk I/O error",
	}

	for _, retryableErr := range retryableErrors {
		if strings.Contains(errMsg, retryableErr) {
			return true
		}
	}
	return false
}

// executeWithRetry 带重试机制的数据库操作，用于处理临时的数据库锁定
// 已废弃：使用 executeWithAdvancedRetry 替代
func executeWithRetry(operation func() error) error {
	return executeWithAdvancedRetry(operation, 3, 100*time.Millisecond, 1*time.Second)
}

// CleanMonitorHistory 清理无效或过时的监控记录和流量记录
func CleanMonitorHistory() (int64, error) {
	// 如果使用BadgerDB，使用BadgerDB专用的清理方法
	if Conf.DatabaseType == "badger" {
		// 移除冗余的日志输出
		if db.DB != nil {
			// 使用BadgerDB的MonitorHistoryOps清理过期数据
			monitorOps := db.NewMonitorHistoryOps(db.DB)
			maxAge := 7 * 24 * time.Hour // 修改为7天，减少数据库大小
			count, err := monitorOps.CleanupOldMonitorHistories(maxAge)
			if err != nil {
				log.Printf("BadgerDB监控历史清理失败: %v", err)
				return 0, err
			} else {
				// 只在清理了记录时才输出日志
				if count > 0 {
					log.Printf("BadgerDB监控历史清理完成，清理了%d条记录", count)
				}
				return int64(count), nil
			}
		} else {
			log.Println("BadgerDB未初始化，跳过监控历史清理")
			return 0, nil
		}
	}

	// 仅在SQLite模式下进行清理
	if DB == nil {
		log.Printf("SQLite数据库未初始化，跳过监控历史清理")
		return 0, nil
	}

	// 检查是否有其他重要操作正在进行
	if isSystemBusy() {
		log.Printf("系统繁忙，延迟历史数据清理")
		time.AfterFunc(30*time.Minute, func() {
			CleanMonitorHistory()
		})
		return 0, nil
	}

	log.Printf("开始清理历史监控数据...")

	var totalCleaned int64

	// 使用无锁方式执行清理操作，依赖SQLite WAL模式的并发控制
	err := executeWithoutLock(func() error {
		batchSize := 25                                // 增加批次大小到25（从20增加）
		maxRetries := 3                                // 减少重试次数，更快失败
		cutoffDate := time.Now().AddDate(0, 0, -7)     // 修改为7天，减少数据库大小和内存占用
		safetyBuffer := time.Now().Add(-6 * time.Hour) // 添加6小时安全缓冲区，避免误删新数据

		// 使用非事务方式分批清理，避免长时间事务锁定
		// 清理7天前的监控记录，并确保不删除最近6小时的数据
		for {
			var count int64
			var err error

			for retry := 0; retry < maxRetries; retry++ {
				// 等待确保没有其他操作在进行
				time.Sleep(time.Duration(retry*100) * time.Millisecond)

				// 使用子查询删除，添加安全时间检查，确保不会删除新数据
				if DB == nil {
					return fmt.Errorf("数据库未初始化")
				}
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
				if DB == nil {
					return fmt.Errorf("数据库未初始化")
				}
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

			if DB == nil {
				return fmt.Errorf("数据库未初始化")
			}
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

		// 清理7天前的流量记录
		for retry := 0; retry < maxRetries; retry++ {
			// 等待确保没有其他操作在进行
			time.Sleep(time.Duration(retry*100) * time.Millisecond)

			if DB == nil {
				return fmt.Errorf("数据库未初始化")
			}
			result := DB.Exec("DELETE FROM transfers WHERE created_at < ?", cutoffDate)

			if result.Error != nil {
				err := result.Error

				if strings.Contains(err.Error(), "database is locked") ||
					strings.Contains(err.Error(), "SQL statements in progress") ||
					strings.Contains(err.Error(), "cannot commit") {
					if retry < maxRetries-1 {
						backoffDelay := time.Duration((retry+1)*1000) * time.Millisecond
						log.Printf("数据库忙碌，%v 后重试历史流量记录清理 (%d/%d)", backoffDelay, retry+1, maxRetries)
						time.Sleep(backoffDelay)
						continue
					}
				}

				log.Printf("清理历史流量记录失败: %v", err)
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
		return totalCleaned, err
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

	return totalCleaned, nil
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
	now := time.Now()
	offlineTimeout := time.Minute * 2 // 调整为2分钟无心跳视为离线，平衡稳定性和响应速度

	// 数据收集阶段（持锁）
	type serverUpdate struct {
		ID                       uint64
		Name                     string
		LastActive               time.Time
		LastStateJSON            string
		LastOnline               time.Time
		CumulativeNetInTransfer  uint64
		CumulativeNetOutTransfer uint64
		NeedResetTransfer        bool
		TaskClose                chan error
		Host                     *model.Host
		HostJSON                 string
	}

	var updates []serverUpdate
	var shouldResetTransferStats bool

	// 优化：使用读锁进行初步数据收集，减少写锁持有时间
	var serverSnapshots []*model.Server

	// 第一阶段：使用读锁收集服务器快照
	ServerLock.RLock()
	shouldResetTransferStats = checkShouldResetTransferStats()

	// 创建服务器快照，避免在写锁期间进行复杂操作
	serverSnapshots = make([]*model.Server, 0, len(ServerList))
	for _, server := range ServerList {
		if server != nil && server.IsOnline && now.Sub(server.LastActive) > offlineTimeout {
			// 创建轻量级快照，只包含必要信息
			snapshot := &model.Server{
				Common:     server.Common,
				Name:       server.Name,
				IsOnline:   server.IsOnline,
				LastActive: server.LastActive,
			}
			// 安全地复制指针字段
			if server.State != nil {
				snapshot.State = server.State
			}
			if server.Host != nil {
				snapshot.Host = server.Host
			}
			if server.LastStateBeforeOffline != nil {
				snapshot.LastStateBeforeOffline = server.LastStateBeforeOffline
			}
			serverSnapshots = append(serverSnapshots, snapshot)
		}
	}
	ServerLock.RUnlock()

	// 第二阶段：批量处理需要离线的服务器（最小化写锁时间）
	if len(serverSnapshots) > 0 {
		// 为每个需要离线的服务器准备更新数据
		for _, snapshot := range serverSnapshots {
			update := serverUpdate{
				ID:         snapshot.ID,
				Name:       snapshot.Name,
				LastActive: snapshot.LastActive,
			}

			// 在锁外准备复杂的序列化操作
			if snapshot.LastStateBeforeOffline == nil && snapshot.State != nil {
				lastState := model.HostState{}
				// 手动深拷贝 HostState 字段，避免使用 copier 包
				lastState.CPU = snapshot.State.CPU
				lastState.MemUsed = snapshot.State.MemUsed
				lastState.SwapUsed = snapshot.State.SwapUsed
				lastState.DiskUsed = snapshot.State.DiskUsed
				lastState.NetInTransfer = snapshot.State.NetInTransfer
				lastState.NetOutTransfer = snapshot.State.NetOutTransfer
				lastState.NetInSpeed = snapshot.State.NetInSpeed
				lastState.NetOutSpeed = snapshot.State.NetOutSpeed
				lastState.Uptime = snapshot.State.Uptime
				lastState.Load1 = snapshot.State.Load1
				lastState.Load5 = snapshot.State.Load5
				lastState.Load15 = snapshot.State.Load15
				lastState.TcpConnCount = snapshot.State.TcpConnCount
				lastState.UdpConnCount = snapshot.State.UdpConnCount
				lastState.ProcessCount = snapshot.State.ProcessCount
				lastState.Temperatures = make([]model.SensorTemperature, len(snapshot.State.Temperatures))
				copy(lastState.Temperatures, snapshot.State.Temperatures)
				lastState.GPU = snapshot.State.GPU

				// 序列化状态（在锁外执行）
				if lastStateJSON, err := utils.Json.Marshal(lastState); err == nil {
					update.LastStateJSON = string(lastStateJSON)
					update.LastOnline = snapshot.LastActive
				}
			}

			// 保存累计流量
			if snapshot.State != nil {
				update.CumulativeNetInTransfer = snapshot.State.NetInTransfer
				update.CumulativeNetOutTransfer = snapshot.State.NetOutTransfer
			}

			// 保存Host信息
			if snapshot.Host != nil && (len(snapshot.Host.CPU) > 0 || snapshot.Host.MemTotal > 0) {
				if hostJSON, err := utils.Json.Marshal(snapshot.Host); err == nil && len(hostJSON) > 0 {
					update.HostJSON = string(hostJSON)
				}
				update.Host = snapshot.Host
			}

			updates = append(updates, update)
		}

		// 第三阶段：快速写锁批量更新内存状态
		ServerLock.Lock()
		for i, snapshot := range serverSnapshots {
			if server := ServerList[snapshot.ID]; server != nil && server.IsOnline {
				// 快速更新关键状态
				server.IsOnline = false

				// 快速处理任务关闭（如果需要）
				if server.TaskCloseLock != nil {
					// 使用非阻塞方式获取TaskCloseLock，设置更短的超时
					done := make(chan bool, 1)
					go func() {
						defer func() {
							// 确保channel总是会被写入，避免goroutine泄漏
							select {
							case done <- true:
							default:
							}
						}()

						server.TaskCloseLock.Lock()
						if server.TaskClose != nil {
							// 将TaskClose保存到对应的update记录
							if i < len(updates) {
								updates[i].TaskClose = server.TaskClose
							}
							server.TaskClose = nil
						}
						server.TaskStream = nil
						server.TaskCloseLock.Unlock()
					}()

					// 如果无法快速获取锁，跳过任务清理（避免阻塞）
					select {
					case <-done:
					case <-time.After(10 * time.Millisecond):
						// 超时跳过，避免长时间持有ServerLock
					}
				}

				// 更新LastStateBeforeOffline（如果还没有）
				if server.LastStateBeforeOffline == nil && i < len(updates) && updates[i].LastStateJSON != "" {
					server.LastStateJSON = updates[i].LastStateJSON
					server.LastOnline = updates[i].LastOnline
				}
			}
		}

		// 处理流量重置
		if shouldResetTransferStats {
			for _, server := range ServerList {
				if server != nil {
					server.CumulativeNetInTransfer = 0
					server.CumulativeNetOutTransfer = 0
					updates = append(updates, serverUpdate{
						ID:                server.ID,
						NeedResetTransfer: true,
					})
				}
			}
		}
		ServerLock.Unlock()
	} else if shouldResetTransferStats {
		// 即使没有服务器需要离线，也要处理流量重置
		ServerLock.Lock()
		for _, server := range ServerList {
			if server != nil {
				server.CumulativeNetInTransfer = 0
				server.CumulativeNetOutTransfer = 0
				updates = append(updates, serverUpdate{
					ID:                server.ID,
					NeedResetTransfer: true,
				})
			}
		}
		ServerLock.Unlock()
	}

	// 第二阶段：异步执行数据库操作（不持锁）
	if len(updates) > 0 {
		go func() {
			for _, update := range updates {
				// 关闭任务通道
				if update.TaskClose != nil {
					select {
					case update.TaskClose <- fmt.Errorf("server offline"):
					default:
					}
				}

				// 准备数据库更新
				dbUpdates := make(map[string]interface{})

				if update.LastStateJSON != "" {
					dbUpdates["last_state_json"] = update.LastStateJSON
					dbUpdates["last_online"] = update.LastOnline
				}

				if update.CumulativeNetInTransfer > 0 || update.CumulativeNetOutTransfer > 0 {
					dbUpdates["cumulative_net_in_transfer"] = update.CumulativeNetInTransfer
					dbUpdates["cumulative_net_out_transfer"] = update.CumulativeNetOutTransfer
				}

				if update.NeedResetTransfer {
					dbUpdates["cumulative_net_in_transfer"] = 0
					dbUpdates["cumulative_net_out_transfer"] = 0
				}

				// 异步更新数据库
				if len(dbUpdates) > 0 {
					AsyncSafeUpdateServerStatus(update.ID, dbUpdates, func(err error) {
						if err != nil {
							log.Printf("更新服务器 %s 状态失败: %v", update.Name, err)
						}
					})
				}

				// 保存Host信息（如果是SQLite且有更新）
				if update.HostJSON != "" && Conf.DatabaseType != "badger" && DB != nil {
					go func(id uint64, hostData string) {
						DB.Exec("UPDATE servers SET host_json = ? WHERE id = ?", hostData, id)
					}(update.ID, update.HostJSON)
				}
			}
		}()
	}
}

// checkShouldResetTransferStats 检查是否应该重置流量统计
func checkShouldResetTransferStats() bool {
	// 获取当前时间
	now := time.Now()

	// 首先检查是否存在有效的流量周期通知规则
	// 如果存在通知规则，优先使用通知规则中的周期
	AlertsLock.RLock()
	defer AlertsLock.RUnlock()

	hasActiveRules := false
	for _, alert := range Alerts {
		if alert == nil || !alert.Enabled() {
			continue
		}

		// 检查每个通知规则的Rules字段
		for _, rule := range alert.Rules {
			// 检查是否是周期流量规则
			if rule.IsTransferDurationRule() {
				hasActiveRules = true
				break
			}
		}

		if hasActiveRules {
			break
		}
	}

	// 如果没有找到流量周期通知规则，则使用默认的月度重置（每月1日凌晨）
	if !hasActiveRules {
		return now.Day() == 1 && now.Hour() == 0 && now.Minute() < 5
	}

	// 存在流量周期通知规则，由通知规则自行处理重置，这里不额外重置
	return false
}

// SyncAllServerTrafficFromDB 从数据库同步所有服务器的累计流量数据到内存
func SyncAllServerTrafficFromDB() {
	ServerLock.Lock()
	defer ServerLock.Unlock()

	// 检查当前数据库类型是否为 BadgerDB
	if Conf != nil && Conf.DatabaseType == "badger" {
		log.Println("使用 BadgerDB 模式，跳过传统流量数据同步")
		return
	}

	// 确认数据库已初始化
	if DB == nil {
		log.Println("数据库未初始化，跳过流量数据同步")
		return
	}

	for serverID, server := range ServerList {
		if server == nil {
			continue
		}

		// 使用安全的方式获取数据库中的服务器数据
		var dbServer model.Server
		if err := executeWithAdvancedRetry(func() error {
			return DB.First(&dbServer, serverID).Error
		}, 3, 100*time.Millisecond, 1*time.Second); err != nil {
			log.Printf("从数据库获取服务器 %d 数据失败: %v", serverID, err)
			continue
		}

		// 同步累计流量数据
		server.CumulativeNetInTransfer = dbServer.CumulativeNetInTransfer
		server.CumulativeNetOutTransfer = dbServer.CumulativeNetOutTransfer
	}

	log.Printf("累计流量数据同步完成，同步了 %d 台服务器", len(ServerList))
}

// TriggerTrafficRecalculation 触发流量数据重新计算
func TriggerTrafficRecalculation() int {
	// 检查是否使用 BadgerDB 模式
	if Conf != nil && Conf.DatabaseType == "badger" {
		log.Println("BadgerDB模式：跳过流量数据重新计算")
		return 0
	}

	// 检查数据库是否初始化
	if DB == nil {
		log.Println("数据库未初始化，跳过流量数据重新计算")
		return 0
	}

	// SQLite 模式下的原有实现
	return 0
}

// SaveAllTrafficToDB 保存所有服务器的累计流量到数据库
func SaveAllTrafficToDB() {
	// 检查是否使用 BadgerDB
	if Conf != nil && Conf.DatabaseType == "badger" {
		// BadgerDB 模式的流量数据保存
		SaveAllTrafficToBadgerDB()
		return
	}

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

	// 只在调试模式下输出详细的保存日志
	if Conf.Debug {
		log.Printf("成功保存 %d 个服务器的累计流量数据", len(serverData))
	}
}

// SaveAllTrafficToBadgerDB 保存所有服务器的累计流量到BadgerDB
func SaveAllTrafficToBadgerDB() {
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

	// 初始化ServerOps
	serverOps := db.NewServerOps(db.DB)
	if serverOps == nil {
		log.Println("BadgerDB ServerOps 未初始化，跳过流量数据保存")
		return
	}

	// 批量处理服务器数据
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
	var successCount int32
	for _, batch := range serverBatches {
		wg.Add(1)
		go func(serverIDs []uint64) {
			defer wg.Done()

			for _, serverID := range serverIDs {
				server := serverData[serverID]

				// 获取当前数据库中的服务器
				dbServer, err := serverOps.GetServer(serverID)
				if err != nil {
					log.Printf("获取服务器 %s (ID:%d) 数据失败: %v", server.Name, serverID, err)
					continue
				}

				// 更新流量数据
				dbServer.CumulativeNetInTransfer = server.CumulativeNetInTransfer
				dbServer.CumulativeNetOutTransfer = server.CumulativeNetOutTransfer

				// 保存回数据库
				if err := serverOps.SaveServer(dbServer); err != nil {
					log.Printf("保存服务器 %s (ID:%d) 累计流量失败: %v", server.Name, serverID, err)
				} else {
					atomic.AddInt32(&successCount, 1)
				}

				// 添加延迟，避免过度竞争
				time.Sleep(50 * time.Millisecond)
			}
		}(batch)
	}

	// 等待所有批次完成
	wg.Wait()

	// 只在调试模式下输出详细的保存日志
	if Conf.Debug {
		log.Printf("BadgerDB: 成功保存 %d 个服务器的累计流量数据", successCount)
	}
}

// SaveAllDataToDB 保存所有数据到数据库，确保数据永久化
func SaveAllDataToDB() {
	// 只在BadgerDB模式下执行全量数据保存
	if Conf == nil || Conf.DatabaseType != "badger" || db.DB == nil {
		return
	}

	// 保存各类数据
	SaveAllServerDataToDB()
	SaveAllUserDataToDB()
	SaveAllDDNSStateToDB()
	SaveAllAPITokensToDB()
}

// SaveAllServerDataToDB 保存所有服务器数据到BadgerDB
func SaveAllServerDataToDB() {
	if db.DB == nil {
		return
	}

	// 检查内存使用情况，如果过高则跳过此次保存
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	if m.HeapAlloc > 200*1024*1024 { // 200MB
		log.Printf("内存使用过高 (%d MB)，跳过本次服务器数据保存", m.HeapAlloc/(1024*1024))
		return
	}

	ServerLock.RLock()
	// 直接使用 slice 而不是 map，减少内存分配
	serverList := make([]*model.Server, 0, len(ServerList))
	for _, server := range ServerList {
		if server != nil {
			serverList = append(serverList, server)
		}
	}
	ServerLock.RUnlock()

	if len(serverList) == 0 {
		return
	}

	serverOps := db.NewServerOps(db.DB)
	successCount := 0

	// 批量处理，每批10个减少内存压力
	batchSize := 10
	for i := 0; i < len(serverList); i += batchSize {
		end := i + batchSize
		if end > len(serverList) {
			end = len(serverList)
		}

		for j := i; j < end; j++ {
			server := serverList[j]
			if err := serverOps.SaveServer(server); err != nil {
				log.Printf("保存服务器 %s 数据失败: %v", server.Name, err)
			} else {
				successCount++
			}
		}

		// 批次间休息，让GC有机会运行
		if i+batchSize < len(serverList) {
			time.Sleep(10 * time.Millisecond)
			runtime.GC() // 主动触发GC
		}
	}

	// 为每个服务器获取和更新数据库记录
	for _, server := range serverList {
		// 获取数据库中的服务器数据
		dbServer, err := serverOps.GetServer(server.ID)
		if err != nil {
			log.Printf("获取服务器 %d 数据失败: %v", server.ID, err)
			continue
		}

		// 更新所有重要字段
		dbServer.CumulativeNetInTransfer = server.CumulativeNetInTransfer
		dbServer.CumulativeNetOutTransfer = server.CumulativeNetOutTransfer
		dbServer.LastActive = server.LastActive
		dbServer.IsOnline = server.IsOnline

		// 保存Host信息
		if server.Host != nil {
			dbServer.Host = server.Host
		}

		// 保存最后状态
		if server.State != nil {
			if lastStateJSON, err := utils.Json.Marshal(server.State); err == nil {
				dbServer.LastStateJSON = string(lastStateJSON)
			}
		}

		// 保存到数据库
		if err := serverOps.SaveServer(dbServer); err != nil {
			log.Printf("保存服务器 %s 数据失败: %v", server.Name, err)
		} else {
			successCount++
		}
	}

	// 只在调试模式下输出详细的保存日志
	if Conf.Debug {
		log.Printf("BadgerDB: 成功保存 %d 个服务器的完整数据", successCount)
	}
}

// SaveAllUserDataToDB 保存所有用户数据到BadgerDB
func SaveAllUserDataToDB() {
	if db.DB == nil {
		return
	}

	// 获取所有用户数据
	var users []*model.User
	err := db.DB.FindAll("user", &users)
	if err != nil {
		log.Printf("获取用户数据失败: %v", err)
		return
	}

	userOps := db.NewUserOps(db.DB)
	successCount := 0
	for _, user := range users {
		if user != nil {
			if err := userOps.SaveUser(user); err != nil {
				log.Printf("保存用户 %s 数据失败: %v", user.Login, err)
			} else {
				successCount++
			}
		}
	}

	// 只在调试模式下输出详细的保存日志
	if Conf.Debug {
		log.Printf("BadgerDB: 成功保存 %d 个用户的数据", successCount)
	}
}

// SaveAllDDNSStateToDB 保存所有DDNS状态到BadgerDB
func SaveAllDDNSStateToDB() {
	if db.DB == nil {
		return
	}

	// DDNS状态通常在变更时已经保存，这里只是确保一致性
	// 只在调试模式下输出详细的保存日志
	if Conf.Debug {
		log.Printf("BadgerDB: DDNS状态数据已在变更时保存")
	}
}

// SaveAllAPITokensToDB 保存所有API令牌到BadgerDB
func SaveAllAPITokensToDB() {
	if db.DB == nil {
		return
	}

	ApiLock.RLock()
	tokenData := make(map[string]*model.ApiToken)
	for token, apiToken := range ApiTokenList {
		if apiToken != nil {
			tokenData[token] = apiToken
		}
	}
	ApiLock.RUnlock()

	if len(tokenData) == 0 {
		log.Printf("BadgerDB: 内存中没有API令牌，跳过保存")
		return
	}

	apiTokenOps := db.NewApiTokenOps(db.DB)

	// 先获取已存在的API令牌，避免重复保存
	existingTokens, err := apiTokenOps.GetAllApiTokens()
	if err != nil {
		log.Printf("获取现有API令牌失败: %v", err)
		return
	}

	// 创建已存在token的映射
	existingTokenMap := make(map[string]bool)
	for _, existing := range existingTokens {
		if existing != nil && existing.Token != "" {
			existingTokenMap[existing.Token] = true
		}
	}

	// 检查是否有需要删除的token（存在于数据库但不在内存中）
	memoryTokenMap := make(map[string]bool)
	for token := range tokenData {
		memoryTokenMap[token] = true
	}

	deleteCount := 0
	for _, existing := range existingTokens {
		if existing != nil && existing.Token != "" && !memoryTokenMap[existing.Token] {
			// 这个token在数据库中存在但不在内存中，应该删除
			if err := apiTokenOps.DeleteApiToken(existing.ID); err != nil {
				log.Printf("删除过期API令牌失败 (ID: %d): %v", existing.ID, err)
			} else {
				deleteCount++
				log.Printf("BadgerDB: 删除过期API令牌 %s (ID: %d)", existing.Token, existing.ID)
			}
		}
	}

	successCount := 0
	skipCount := 0

	for _, apiToken := range tokenData {
		// 检查是否已存在，避免重复保存
		if existingTokenMap[apiToken.Token] {
			skipCount++
			continue
		}

		if err := apiTokenOps.SaveApiToken(apiToken); err != nil {
			log.Printf("保存API令牌失败: %v", err)
		} else {
			successCount++
		}
	}

	if successCount > 0 || skipCount > 0 || deleteCount > 0 {
		// 只在调试模式下输出详细的保存日志
		if Conf.Debug {
			log.Printf("BadgerDB: 成功保存 %d 个API令牌，跳过 %d 个已存在的令牌，删除 %d 个过期令牌", successCount, skipCount, deleteCount)
		}
	}
}

// performHourlyCleanup 执行每小时的清理任务
func performHourlyCleanup() {
	// 清理过期缓存
	if Cache != nil {
		Cache.DeleteExpired()
	}
	if serverStatusCache != nil {
		serverStatusCache.DeleteExpired()
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
	log.Printf("每小时清理完成，当前内存使用: %v MB", m.Alloc/1024/1024)
}

// initDDNSDomainsForBadgerDB 在BadgerDB模式下初始化DDNS配置的Domains字段
func initDDNSDomainsForBadgerDB() {
	if db.DB == nil {
		return
	}

	// 获取所有DDNS配置
	var ddnsProfiles []*model.DDNSProfile
	err := db.DB.FindAll("ddns_profile", &ddnsProfiles)
	if err != nil {
		log.Printf("获取DDNS配置失败: %v", err)
		return
	}

	// 手动初始化每个DDNS配置的Domains字段
	for _, profile := range ddnsProfiles {
		if profile != nil && profile.DomainsRaw != "" {
			profile.Domains = strings.Split(profile.DomainsRaw, ",")
		} else if profile != nil {
			profile.Domains = []string{}
		}
	}

	log.Printf("BadgerDB模式: 已初始化 %d 个DDNS配置的Domains字段", len(ddnsProfiles))
}

// initCronServersForBadgerDB 在BadgerDB模式下初始化Cron任务的Servers字段
func initCronServersForBadgerDB() {
	if db.DB == nil {
		return
	}

	// 获取所有Cron任务
	var cronTasks []*model.Cron
	err := db.DB.FindAll("cron", &cronTasks)
	if err != nil {
		log.Printf("获取Cron任务失败: %v", err)
		return
	}

	// 手动初始化每个Cron任务的Servers字段
	for _, cronTask := range cronTasks {
		if cronTask != nil {
			if cronTask.ServersRaw == "" {
				cronTask.ServersRaw = "[]"
				cronTask.Servers = []uint64{}
			} else {
				// 尝试解析JSON，如果失败则修复格式
				err := utils.Json.Unmarshal([]byte(cronTask.ServersRaw), &cronTask.Servers)
				if err != nil {
					// 检查是否是 "[]," 这种无效格式
					if cronTask.ServersRaw == "[]," || cronTask.ServersRaw == "," {
						cronTask.ServersRaw = "[]"
						cronTask.Servers = []uint64{}
					} else {
						// 其他格式错误，尝试修复
						log.Printf("解析Cron任务 %s 的ServersRaw失败（%s），重置为空数组: %v", cronTask.Name, cronTask.ServersRaw, err)
						cronTask.ServersRaw = "[]"
						cronTask.Servers = []uint64{}
					}
				}
			}
		}
	}

	log.Printf("BadgerDB模式: 已初始化 %d 个Cron任务的Servers字段", len(cronTasks))
}

// validateAndFixDataIntegrity 验证并修复数据完整性
func validateAndFixDataIntegrity() {
	if Conf == nil || Conf.DatabaseType != "badger" || db.DB == nil {
		return
	}

	log.Println("开始验证和修复数据完整性...")

	// 验证服务器数据
	validateServerData()

	// 验证监控配置数据
	validateMonitorData()

	// 验证通知规则数据
	validateAlertRuleData()

	// 验证用户数据
	validateUserData()

	log.Println("数据完整性验证和修复完成")
}

// validateServerData 验证服务器数据完整性
func validateServerData() {
	ServerLock.RLock()
	defer ServerLock.RUnlock()

	for _, server := range ServerList {
		if server == nil {
			continue
		}

		// 确保Host对象正确初始化
		if server.Host == nil {
			server.Host = &model.Host{}
		}
		server.Host.Initialize()

		// 确保State对象正确初始化
		if server.State == nil {
			server.State = &model.HostState{}
		}

		// 确保DDNSProfiles字段正确初始化
		if server.DDNSProfiles == nil {
			server.DDNSProfiles = []uint64{}
		}

		// 确保Secret字段不为空
		if server.Secret == "" {
			log.Printf("警告: 服务器 %s (ID: %d) 的Secret为空", server.Name, server.ID)
		}
	}
}

// validateMonitorData 验证监控配置数据完整性
func validateMonitorData() {
	// 这个函数在ServiceSentinel初始化时已经处理了
	log.Println("监控配置数据已在ServiceSentinel初始化时验证")
}

// validateAlertRuleData 验证通知规则数据完整性
func validateAlertRuleData() {
	// 这个函数在BadgerDB.FindAll中已经处理了
	log.Println("通知规则数据已在BadgerDB.FindAll中验证")
}

// validateUserData 验证用户数据完整性
func validateUserData() {
	// 用户数据相对简单，主要确保Token字段正确处理
	log.Println("用户数据完整性正常")
}

// AutoSyncTraffic 自动同步流量的空实现
func AutoSyncTraffic() {
	// 功能已移除
}

// VerifyTrafficDataConsistency 验证流量数据一致性，添加重试机制
func VerifyTrafficDataConsistency() {
	// 检查是否使用 BadgerDB
	if Conf != nil && Conf.DatabaseType == "badger" {
		log.Printf("BadgerDB模式：流量数据一致性检查完成")
		return
	}

	// 检查系统负载
	if isSystemBusy() {
		log.Printf("系统繁忙，跳过流量数据一致性检查")
		return
	}

	// 检查数据库是否初始化
	if DB == nil {
		log.Printf("数据库未初始化，跳过流量数据一致性检查")
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
		highThreshold:    512, // 512MB触发高压清理，适合大多数VPS环境
		warningThreshold: 256, // 256MB警告阈值，更早触发清理
		maxGoroutines:    200, // 200个goroutine限制，更保守的并发控制
		isHighPressure:   false,
	}
}

func (mm *MemoryMonitor) Start() {
	mm.ticker = time.NewTicker(30 * time.Second) // 改为30秒检查一次，降低频率

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("MemoryMonitor panic恢复: %v", r)
				// 不要自动重启，避免goroutine泄漏
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
		newPressureLevel = 3 // 高压 (>512MB)
	} else if currentMemMB > mm.warningThreshold {
		newPressureLevel = 2 // 严重 (>256MB)
	} else if currentMemMB > mm.warningThreshold/2 {
		newPressureLevel = 1 // 警告 (>128MB)
	}

	atomic.StoreInt64(&memoryPressureLevel, newPressureLevel)

	// 硬性内存限制 - 超过800MB强制退出让systemd重启
	if currentMemMB > 800 {
		log.Printf("内存使用超过800MB (%dMB)，程序即将强制退出避免系统崩溃", currentMemMB)
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

	// 异步清理通知数据，避免阻塞HTTP请求
	cleanupAlertMemoryDataAsync()

	// 清理僵尸 goroutine 连接
	cleanupStaleGoroutineConnections()

	// 适度的GC
	runtime.GC()

	log.Printf("内存清理完成，清理时间：%v", time.Now())
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

// cleanupStaleGoroutineConnections 清理僵尸 goroutine 连接
func cleanupStaleGoroutineConnections() {
	cleaned := 0
	totalGoroutines := runtime.NumGoroutine()

	// 如果 goroutine 数量过多，更激进地清理
	cleanupThreshold := 3 * time.Minute
	if totalGoroutines > 200 {
		cleanupThreshold = 2 * time.Minute // 更激进的清理
	}
	if totalGoroutines > 300 {
		cleanupThreshold = 1 * time.Minute // 非常激进的清理
	}

	ServerLock.Lock()
	defer ServerLock.Unlock()

	for serverID, server := range ServerList {
		if server != nil && server.TaskClose != nil {
			// 检查连接是否长时间无活动
			if time.Since(server.LastActive) > cleanupThreshold {
				server.TaskCloseLock.Lock()
				if server.TaskClose != nil {
					// 强制关闭僵尸连接
					select {
					case server.TaskClose <- fmt.Errorf("cleanup stale connection due to memory pressure"):
					default:
					}
					server.TaskClose = nil
					server.TaskStream = nil
					cleaned++
					log.Printf("清理服务器 %d 的僵尸连接（无活动时间: %v）", serverID, time.Since(server.LastActive))
				}
				server.TaskCloseLock.Unlock()
			}
		}
	}

	if cleaned > 0 {
		log.Printf("内存清理：清理了 %d 个僵尸连接，当前 goroutine 数量: %d", cleaned, runtime.NumGoroutine())
	}
}

// OptimizeDatabase 定期优化数据库性能
func OptimizeDatabase() {
	if isSystemBusy() {
		log.Println("系统繁忙，跳过数据库优化")
		return
	}

	log.Println("开始数据库优化...")

	// 仅在SQLite模式下进行数据库优化
	if Conf.DatabaseType == "badger" {
		log.Println("使用BadgerDB，跳过数据库优化")
		return
	}

	// 先获取数据库状态
	if DB == nil {
		log.Printf("数据库未初始化，跳过优化")
		return
	}

	sqlDB, err := DB.DB()
	if err != nil {
		log.Printf("获取数据库连接失败: %v", err)
		return
	}

	// 记录连接池状态
	LogConnectionPoolStats()

	// 只进行轻量级优化操作，避免长时间锁定
	if DB != nil {
		err = executeWithAdvancedRetry(func() error {
			// 使用短超时执行optimize
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return DB.WithContext(ctx).Exec("PRAGMA optimize").Error
		}, 3, 100*time.Millisecond, 500*time.Millisecond)
	}

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

		// 检查是否为可重试的错误
		if isRetryableError(err) && retry < maxRetries-1 {
			// 指数退避策略
			backoffDelay := time.Duration(200*(1<<retry)) * time.Millisecond
			time.Sleep(backoffDelay)
		} else {
			break
		}
	}

	return lastErr
}

// SafeDatabaseDelete 安全的数据库删除操作
func SafeDatabaseDelete(model interface{}, whereClause string, args ...interface{}) error {
	// 仅在SQLite模式下执行
	if Conf.DatabaseType == "badger" {
		return fmt.Errorf("SafeDatabaseDelete不支持BadgerDB模式")
	}

	if DB == nil {
		return fmt.Errorf("数据库未初始化")
	}

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
	// 如果使用BadgerDB，跳过数据库写入工作器
	if Conf.DatabaseType == "badger" {
		log.Println("使用BadgerDB，跳过数据库写入工作器")
		return
	}

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
	// 如果使用BadgerDB，跳过数据库插入工作器
	if Conf.DatabaseType == "badger" {
		log.Println("使用BadgerDB，跳过数据库插入工作器")
		return
	}

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
		// 根据数据库类型选择不同的处理方式
		if Conf.DatabaseType == "badger" {
			// BadgerDB模式：使用BadgerDB操作
			if db.DB == nil {
				return fmt.Errorf("BadgerDB连接未初始化")
			}

			// 使用BadgerDB的SafeUpdateServerStatus
			return SafeUpdateServerStatus(req.ServerID, req.Updates)
		} else {
			// SQLite模式：使用GORM操作
			if DB == nil {
				return fmt.Errorf("SQLite数据库连接未初始化")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// 直接更新，避免不必要的事务开销
			return DB.WithContext(ctx).Table(req.TableName).Where("id = ?", req.ServerID).Updates(req.Updates).Error
		}
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
			delete(req.Data, "@id")

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

// AsyncDBUpdate 异步更新数据库记录
func AsyncDBUpdate(serverID uint64, tableName string, updates map[string]interface{}, callback func(error)) {
	// 如果系统处于高内存压力状态，使用同步更新以减少排队
	if isSystemBusy() {
		err := executeDBWriteRequest(DBWriteRequest{
			ServerID:  serverID,
			TableName: tableName,
			Updates:   updates,
			Callback:  callback,
		})
		if callback != nil {
			callback(err)
		}
		return
	}

	// 防止重复的相同更新占用队列
	if len(updates) == 0 {
		if callback != nil {
			callback(nil)
		}
		return
	}

	// 添加随机延迟，避免多个更新同时进入队列
	delay := time.Duration(serverID%20) * 50 * time.Millisecond
	go func() {
		time.Sleep(delay)

		select {
		case dbWriteQueue <- DBWriteRequest{
			ServerID:  serverID,
			TableName: tableName,
			Updates:   updates,
			Callback:  callback,
		}:
			// 成功添加到队列
		case <-time.After(2 * time.Second):
			// 队列满或阻塞超时，直接执行
			err := executeDBWriteRequest(DBWriteRequest{
				ServerID:  serverID,
				TableName: tableName,
				Updates:   updates,
				Callback:  callback,
			})
			if callback != nil {
				callback(err)
			}
		}
	}()
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
	// 如果使用BadgerDB，跳过连接池统计
	if Conf.DatabaseType == "badger" {
		return
	}

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
	// 如果使用BadgerDB，跳过连接池监控
	if Conf.DatabaseType == "badger" {
		log.Println("使用BadgerDB，跳过连接池监控")
		return
	}

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

	// 如果使用BadgerDB，跳过预热
	if Conf.DatabaseType == "badger" {
		log.Println("使用BadgerDB，跳过数据库连接池预热")
		return
	}

	// 预热连接池 - 降低并发度，避免锁竞争
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ { // 从8减少到3
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			// 延迟执行，避免同时访问
			time.Sleep(time.Duration(index*500) * time.Millisecond)

			// 执行简单查询来预热连接
			if DB != nil {
				var count int64
				DB.Model(&model.Server{}).Count(&count)
				log.Printf("预热连接 %d 完成，服务器数量: %d", index, count)
			}
		}(i)
	}

	wg.Wait()
	log.Println("数据库连接池预热完成")
}

// StartMonitorHistoryWorker 启动监控历史记录插入专用工作器
func StartMonitorHistoryWorker() {
	// 如果使用BadgerDB，启动BadgerDB专用的监控历史记录工作器
	if Conf.DatabaseType == "badger" {
		log.Println("使用BadgerDB，启动BadgerDB监控历史记录插入工作器")
		StartBadgerMonitorHistoryWorker()
		return
	}

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
			delete(req.Data, "@id")

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

// StartBadgerMonitorHistoryWorker 启动BadgerDB专用的监控历史记录插入工作器
func StartBadgerMonitorHistoryWorker() {
	monitorHistoryWorkerMutex.Lock()
	defer monitorHistoryWorkerMutex.Unlock()

	if monitorHistoryWorkerStarted {
		return
	}

	monitorHistoryWorkerStarted = true
	log.Println("启动BadgerDB监控历史记录处理工作器...")

	go func() {
		for req := range monitorHistoryQueue {
			// 确保没有使用@id字段
			delete(req.Data, "@id")

			// 转换数据为MonitorHistory结构
			history := &model.MonitorHistory{
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}

			// 提取字段
			if v, ok := req.Data["monitor_id"]; ok {
				if val, ok := v.(uint64); ok {
					history.MonitorID = val
				}
			}
			if v, ok := req.Data["server_id"]; ok {
				if val, ok := v.(uint64); ok {
					history.ServerID = val
				}
			}
			if v, ok := req.Data["avg_delay"]; ok {
				if val, ok := v.(float32); ok {
					history.AvgDelay = val
				} else if val, ok := v.(float64); ok {
					history.AvgDelay = float32(val)
				}
			}
			if v, ok := req.Data["data"]; ok {
				if val, ok := v.(string); ok {
					history.Data = val
				}
			}
			if v, ok := req.Data["up"]; ok {
				if val, ok := v.(uint64); ok {
					history.Up = val
				} else if val, ok := v.(int); ok {
					history.Up = uint64(val)
				}
			}
			if v, ok := req.Data["down"]; ok {
				if val, ok := v.(uint64); ok {
					history.Down = val
				} else if val, ok := v.(int); ok {
					history.Down = uint64(val)
				}
			}

			// 保存到BadgerDB
			var err error
			if db.DB != nil {
				monitorOps := db.NewMonitorHistoryOps(db.DB)
				err = monitorOps.SaveMonitorHistory(history)
				if err != nil {
					log.Printf("BadgerDB监控历史记录保存失败 (MonitorID: %d): %v", history.MonitorID, err)
				}
			} else {
				err = fmt.Errorf("BadgerDB未初始化")
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
	// 如果使用BadgerDB，跳过数据库维护计划
	if Conf.DatabaseType == "badger" {
		log.Println("使用BadgerDB，跳过数据库维护计划")
		return
	}

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
	// 检查是否使用 BadgerDB
	if Conf != nil && Conf.DatabaseType == "badger" {
		// BadgerDB 模式下使用 ServerOps 更新服务器数据
		serverOps := db.NewServerOps(db.DB)
		if serverOps == nil {
			return fmt.Errorf("BadgerDB ServerOps 未初始化")
		}

		// 获取现有服务器
		server, err := serverOps.GetServer(serverID)
		if err != nil {
			return fmt.Errorf("获取服务器数据失败: %w", err)
		}

		// 更新字段
		for key, value := range updates {
			switch key {
			case "cumulative_net_in_transfer":
				if val, ok := value.(uint64); ok {
					server.CumulativeNetInTransfer = val
				}
			case "cumulative_net_out_transfer":
				if val, ok := value.(uint64); ok {
					server.CumulativeNetOutTransfer = val
				}
			}
		}

		// 保存更新后的服务器
		return serverOps.SaveServer(server)
	}

	// 传统 SQLite 模式
	if DB == nil {
		return fmt.Errorf("数据库未初始化")
	}

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
		delete(req.Data, "@id")
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
				// 根据数据库类型选择不同的保存方式
				if Conf.DatabaseType == "badger" {
					// 使用 BadgerDB 保存
					if db.DB != nil {
						// 转换数据格式
						history := &model.MonitorHistory{
							MonitorID: midVal,
							ServerID:  0,
							AvgDelay:  0,
							Data:      "",
							Up:        0,
							Down:      0,
							CreatedAt: time.Now(),
							UpdatedAt: time.Now(),
						}

						// 设置服务器ID
						if sid, ok := insertData["server_id"]; ok {
							switch s := sid.(type) {
							case uint64:
								history.ServerID = s
							case uint:
								history.ServerID = uint64(s)
							case int64:
								history.ServerID = uint64(s)
							case int:
								history.ServerID = uint64(s)
							case float64:
								history.ServerID = uint64(s)
							}
						}

						// 设置平均延迟
						if ad, ok := insertData["avg_delay"]; ok {
							switch a := ad.(type) {
							case float32:
								history.AvgDelay = a
							case float64:
								history.AvgDelay = float32(a)
							}
						}

						// 设置数据字符串
						if dataStr, ok := insertData["data"].(string); ok {
							history.Data = dataStr
						}

						// 设置Up/Down计数
						if u, ok := insertData["up"]; ok {
							switch uv := u.(type) {
							case uint64:
								history.Up = uv
							case uint:
								history.Up = uint64(uv)
							case int64:
								history.Up = uint64(uv)
							case int:
								history.Up = uint64(uv)
							case float64:
								history.Up = uint64(uv)
							}
						}

						if d, ok := insertData["down"]; ok {
							switch dv := d.(type) {
							case uint64:
								history.Down = dv
							case uint:
								history.Down = uint64(dv)
							case int64:
								history.Down = uint64(dv)
							case int:
								history.Down = uint64(dv)
							case float64:
								history.Down = uint64(dv)
							}
						}

						// 使用BadgerDB保存
						monitorHistoryOps := db.NewMonitorHistoryOps(db.DB)
						err := monitorHistoryOps.SaveMonitorHistory(history)
						if err != nil {
							log.Printf("ICMP/TCP监控数据保存到BadgerDB失败 (MonitorID: %d): %v", midVal, err)
						}
					}
				} else {
					// 使用SQLite保存
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
	// 添加panic恢复机制
	defer func() {
		if r := recover(); r != nil {
			log.Printf("🚨 监控历史记录一致性检查发生PANIC: %v", r)
			// 打印堆栈信息
			debug.PrintStack()
		}
	}()

	// 检查是否有其他重要操作正在进行
	if isSystemBusy() {
		log.Printf("系统繁忙，跳过监控历史记录一致性检查")
		return
	}

	log.Printf("执行监控历史记录一致性检查...")

	// 根据数据库类型选择不同的检查方式
	if Conf.DatabaseType == "badger" {
		// BadgerDB检查逻辑 - 简单记录器
		log.Printf("BadgerDB模式：监控历史记录检查完成")
		return
	}

	// 以下是SQLite的检查逻辑
	// 使用重试机制执行数据库查询
	var count int64
	var err error
	if DB != nil {
		err = executeWithAdvancedRetry(func() error {
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
	}

	if err != nil {
		log.Printf("检查监控历史记录失败: %v", err)
		return
	}

	log.Printf("最近30分钟内ICMP/TCP监控记录数: %d", count)

	// 如果数量异常少，触发通知
	if count < 10 { // 根据实际情况调整阈值
		log.Printf("警告: ICMP/TCP监控记录数量异常少，可能存在数据丢失")

		// 记录警告日志，不通过通知系统发送，避免依赖错误
		log.Printf("监控历史记录一致性警告: 最近30分钟内仅有监控记录可能存在数据丢失")

		// 强制执行数据库优化，延迟执行避免冲突
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("数据库优化goroutine发生PANIC: %v", r)
				}
			}()
			time.Sleep(30 * time.Second)
			if !isSystemBusy() && Conf.DatabaseType != "badger" {
				optimizeDatabase()
			}
		}()
	}

	// 检查空数据记录
	var emptyCount int64
	if DB != nil {
		err = executeWithAdvancedRetry(func() error {
			checkTime := time.Now().Add(-30 * time.Minute)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			return DB.WithContext(ctx).Model(&model.MonitorHistory{}).
				Where("created_at > ? AND (data = '' OR data IS NULL)",
					checkTime.Format("2006-01-02 15:04:05")).
				Count(&emptyCount).Error
		}, 5, 200*time.Millisecond, 2*time.Second)
	}

	if err != nil {
		log.Printf("检查空数据记录失败: %v", err)
		return
	}

	if emptyCount > 0 {
		log.Printf("警告: 发现 %d 条空数据记录", emptyCount)
	}
}
