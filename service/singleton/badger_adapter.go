package singleton

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/xos/serverstatus/db"
	"github.com/xos/serverstatus/model"
)

// InitBadgerDBFromPath 从指定路径初始化BadgerDB数据库
func InitBadgerDBFromPath(path string) error {
	// 确保目录存在
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建BadgerDB目录失败: %w", err)
	}

	// 打开BadgerDB
	_, err := db.OpenDB(path)
	if err != nil {
		return fmt.Errorf("打开BadgerDB失败: %w", err)
	}

	// 初始化或从SQLite迁移数据
	if isSQLiteFileExists() {
		log.Println("检测到SQLite数据库，开始数据迁移...")
		if err := migrateFromSQLiteToBadgerDB(path); err != nil {
			return fmt.Errorf("从SQLite迁移数据失败: %w", err)
		}
		log.Println("数据迁移完成！重命名旧SQLite数据库为备份...")

		// 重命名旧SQLite数据库文件为备份
		if err := renameSQLiteFileToBackup(); err != nil {
			log.Printf("警告：无法重命名旧SQLite数据库: %v", err)
		}
	} else {
		log.Println("未检测到SQLite数据库，初始化新的BadgerDB...")
		if err := db.InitializeEmptyBadgerDB(); err != nil {
			return fmt.Errorf("初始化BadgerDB失败: %w", err)
		}
	}

	// 初始化BadgerDB的索引和缓存
	if err := initializeBadgerDBIndexes(); err != nil {
		log.Printf("警告：初始化BadgerDB索引失败: %v", err)
	}

	// 加载服务器列表到内存
	if err := loadServersFromBadgerDB(); err != nil {
		log.Printf("警告：从BadgerDB加载服务器列表失败: %v", err)
	}

	// 启动BadgerDB的后台维护任务
	startBadgerDBMaintenanceTasks()

	return nil
}

// isSQLiteFileExists 检查SQLite数据库文件是否存在
func isSQLiteFileExists() bool {
	sqlitePath := "data/sqlite.db"
	if Conf != nil && Conf.DatabaseLocation != "" {
		sqlitePath = Conf.DatabaseLocation
	}

	_, err := os.Stat(sqlitePath)
	return err == nil
}

// renameSQLiteFileToBackup 将SQLite数据库文件重命名为备份
func renameSQLiteFileToBackup() error {
	sqlitePath := "data/sqlite.db"
	if Conf != nil && Conf.DatabaseLocation != "" {
		sqlitePath = Conf.DatabaseLocation
	}

	backupPath := sqlitePath + ".bak." + time.Now().Format("20060102150405")
	return os.Rename(sqlitePath, backupPath)
}

// migrateFromSQLiteToBadgerDB 从SQLite迁移数据到BadgerDB
func migrateFromSQLiteToBadgerDB(badgerPath string) error {
	sqlitePath := "data/sqlite.db"
	if Conf != nil && Conf.DatabaseLocation != "" {
		sqlitePath = Conf.DatabaseLocation
	}

	// 创建迁移实例
	migration, err := db.NewMigration(db.DB, sqlitePath)
	if err != nil {
		return err
	}
	defer migration.Close()

	// 执行全部迁移
	return migration.MigrateAll()
}

// initializeBadgerDBIndexes 初始化BadgerDB的索引
func initializeBadgerDBIndexes() error {
	// BadgerDB不需要像传统数据库那样创建索引
	// 这里可以进行其他必要的初始化
	return nil
}

// loadServersFromBadgerDB 从BadgerDB加载服务器列表到内存
func loadServersFromBadgerDB() error {
	ServerLock.Lock()
	defer ServerLock.Unlock()

	// 使用ServerOps获取所有服务器
	serverOps := db.NewServerOps(db.DB)
	servers, err := serverOps.GetAllServers()
	if err != nil {
		return err
	}

	// 初始化服务器列表
	for _, server := range servers {
		ServerList[server.ID] = server
		ServerList[server.ID].Host = &model.Host{}
		ServerList[server.ID].State = &model.HostState{}

		// 解析持久化的主机信息和状态
		if server.HostJSON != "" {
			err := json.Unmarshal([]byte(server.HostJSON), ServerList[server.ID].Host)
			if err != nil {
				log.Printf("解析服务器主机信息失败，服务器ID：%d：%v", server.ID, err)
			}
		}

		if server.LastStateJSON != "" {
			err := json.Unmarshal([]byte(server.LastStateJSON), ServerList[server.ID].State)
			if err != nil {
				log.Printf("解析服务器状态信息失败，服务器ID：%d：%v", server.ID, err)
			}
			ServerList[server.ID].LastStateBeforeOffline = ServerList[server.ID].State
		}
	}

	return nil
}

// startBadgerDBMaintenanceTasks 启动BadgerDB的后台维护任务
func startBadgerDBMaintenanceTasks() {
	// BadgerDB自己会管理GC等维护任务
	// 这里可以启动其他自定义的维护任务

	// 例如定期清理过期的监控历史记录
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				// 清理30天前的监控历史记录
				monitorHistoryOps := db.NewMonitorHistoryOps(db.DB)
				count, err := monitorHistoryOps.CleanupOldMonitorHistories(30 * 24 * time.Hour)
				if err != nil {
					log.Printf("清理过期监控历史记录失败: %v", err)
				} else if count > 0 {
					log.Printf("已清理 %d 条过期的监控历史记录", count)
				}
			}
		}
	}()
}

// 以下是替代原有数据库操作的函数

// SaveServerToBadgerDB 保存服务器信息到BadgerDB
func SaveServerToBadgerDB(server *model.Server) error {
	serverOps := db.NewServerOps(db.DB)
	return serverOps.SaveServer(server)
}

// GetBadgerDBStats 获取BadgerDB的统计信息
func GetBadgerDBStats() map[string]interface{} {
	// BadgerDB没有与SQLite完全等价的统计信息，返回一些基本信息
	stats := map[string]interface{}{
		"database_type": "BadgerDB",
		"version":       "v3",
		"in_memory":     false,
	}

	return stats
}

// CleanBadgerDBMonitorHistory 清理BadgerDB中的监控历史记录
func CleanBadgerDBMonitorHistory(days int) (int, error) {
	monitorHistoryOps := db.NewMonitorHistoryOps(db.DB)
	return monitorHistoryOps.CleanupOldMonitorHistories(time.Duration(days) * 24 * time.Hour)
}

// CloseBadgerDB 关闭BadgerDB
func CloseBadgerDB() {
	if db.DB != nil {
		db.DB.Close()
	}
}
