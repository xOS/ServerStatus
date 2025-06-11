package singleton

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xos/serverstatus/db"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/utils"
)

// InitBadgerDBFromPath 从指定路径初始化BadgerDB数据库
func InitBadgerDBFromPath(path string) error {
	// 确保目录存在
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("创建BadgerDB目录失败: %w", err)
	}

	// 打开BadgerDB
	_, err := db.OpenDB(path)
	if err != nil {
		return fmt.Errorf("打开BadgerDB失败: %w", err)
	}

	// 注意：SQLite迁移检查已移除，因为迁移应该在部署时完成
	// 如果需要迁移，请手动运行: bash scripts/migrate_database.sh

	// 初始化BadgerDB（不进行自动迁移）
	log.Println("初始化BadgerDB...")
	if err := db.InitializeEmptyBadgerDB(); err != nil {
		return fmt.Errorf("初始化BadgerDB失败: %w", err)
	}

	// 初始化BadgerDB的索引和缓存
	if err := initializeBadgerDBIndexes(); err != nil {
		log.Printf("警告：初始化BadgerDB索引失败: %v", err)
	}

	// 注意：服务器列表将在 LoadSingleton 中统一加载，这里不需要重复加载

	// 启动BadgerDB的后台维护任务
	startBadgerDBMaintenanceTasks()

	return nil
}

// isSQLiteFileExists 检查SQLite数据库文件是否存在
func isSQLiteFileExists() bool {
	// 获取SQLite数据库路径
	sqlitePath := getSQLitePath()

	_, err := os.Stat(sqlitePath)
	return err == nil
}

// getSQLitePath 获取SQLite数据库文件路径
func getSQLitePath() string {
	// 默认路径
	defaultPath := "data/sqlite.db"

	if Conf == nil {
		return defaultPath
	}

	// 如果当前数据库类型不是badger，直接使用配置的路径
	if Conf.DatabaseType != "badger" && Conf.DatabaseLocation != "" {
		return Conf.DatabaseLocation
	}

	// 如果当前是badger类型，需要推断SQLite的路径
	// 尝试几种可能的路径
	possiblePaths := []string{
		defaultPath,      // 默认路径
		"data/sqlite.db", // 标准路径
		"/opt/server-status/dashboard/data/sqlite.db", // 生产环境路径
	}

	// 如果配置了DatabaseLocation，也尝试将其作为SQLite路径
	// （可能用户之前配置的是SQLite路径，后来改为badger）
	if Conf.DatabaseLocation != "" {
		// 如果配置的路径看起来像SQLite文件，也加入候选
		if strings.HasSuffix(Conf.DatabaseLocation, ".db") {
			possiblePaths = append([]string{Conf.DatabaseLocation}, possiblePaths...)
		} else {
			// 如果是目录，尝试在该目录下查找sqlite.db
			sqliteInConfigDir := filepath.Join(Conf.DatabaseLocation, "sqlite.db")
			possiblePaths = append([]string{sqliteInConfigDir}, possiblePaths...)
		}
	}

	// 检查哪个路径存在
	for _, path := range possiblePaths {
		if _, err := os.Stat(path); err == nil {
			log.Printf("找到SQLite数据库文件: %s", path)
			return path
		}
	}

	log.Printf("未找到SQLite数据库文件，使用默认路径: %s", defaultPath)
	return defaultPath
}

// GetBadgerDBPath 获取BadgerDB数据库目录路径
func GetBadgerDBPath() string {
	// 默认BadgerDB路径
	defaultPath := "data/badger"

	if Conf == nil {
		return defaultPath
	}

	// 如果配置了DatabaseLocation
	if Conf.DatabaseLocation != "" {
		// 检查配置的路径是否看起来像SQLite文件路径
		if strings.HasSuffix(Conf.DatabaseLocation, ".db") {
			// 如果是SQLite文件路径，转换为BadgerDB目录路径
			// 例如：data/sqlite.db -> data/badger
			dir := filepath.Dir(Conf.DatabaseLocation)
			return filepath.Join(dir, "badger")
		} else {
			// 如果不是.db文件，假设它是目录路径，直接使用
			return Conf.DatabaseLocation
		}
	}

	return defaultPath
}

// renameSQLiteFileToBackup 将SQLite数据库文件重命名为备份
func renameSQLiteFileToBackup() error {
	// 获取SQLite数据库路径
	sqlitePath := getSQLitePath()

	// 检查文件是否存在
	if _, err := os.Stat(sqlitePath); os.IsNotExist(err) {
		log.Printf("SQLite数据库文件不存在，跳过备份: %s", sqlitePath)
		return nil
	}

	backupPath := sqlitePath + ".bak." + time.Now().Format("20060102150405")
	log.Printf("将SQLite数据库文件重命名为备份: %s -> %s", sqlitePath, backupPath)
	return os.Rename(sqlitePath, backupPath)
}

// migrateFromSQLiteToBadgerDB 从SQLite迁移数据到BadgerDB
func migrateFromSQLiteToBadgerDB(badgerPath string) error {
	// 获取SQLite数据库文件路径
	sqlitePath := getSQLitePath()

	log.Printf("从SQLite路径迁移: %s 到 BadgerDB路径: %s", sqlitePath, badgerPath)

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
	log.Println("从BadgerDB加载服务器列表...")
	ServerLock.Lock()
	defer ServerLock.Unlock()

	// 清空现有服务器列表
	ServerList = make(map[uint64]*model.Server)
	SortedServerList = make([]*model.Server, 0)
	SortedServerListForGuest = make([]*model.Server, 0)

	// 使用ServerOps获取所有服务器
	serverOps := db.NewServerOps(db.DB)
	if serverOps == nil || db.DB == nil {
		log.Println("警告: BadgerDB或ServerOps未初始化")
		return errors.New("BadgerDB未初始化")
	}

	servers, err := serverOps.GetAllServers()
	if err != nil {
		log.Printf("从BadgerDB获取服务器列表失败: %v", err)
		return err
	}

	// 初始化服务器列表
	for _, server := range servers {
		if server == nil {
			log.Println("警告: 跳过空的服务器记录")
			continue
		}

		if server.ID == 0 {
			log.Printf("警告: 服务器ID为0，跳过: %s", server.Name)
			continue
		}

		// 初始化必要的对象
		server.Host = &model.Host{}
		server.State = &model.HostState{}

		// 设置默认值
		server.IsOnline = false // 初始状态为离线，等待agent报告

		// 从数据库恢复LastActive时间，使用LastOnline字段
		if !server.LastOnline.IsZero() {
			server.LastActive = server.LastOnline
		} else {
			// 如果没有LastOnline记录，设置为当前时间减去一个较大的值，表示很久没有活动
			server.LastActive = time.Now().Add(-24 * time.Hour)
		}

		// 解析持久化的主机信息
		if server.HostJSON != "" {
			err := json.Unmarshal([]byte(server.HostJSON), server.Host)
			if err != nil {
				log.Printf("解析服务器主机信息失败，服务器ID：%d：%v", server.ID, err)
				// 创建新的Host对象作为回退
				server.Host = &model.Host{}
				server.Host.Initialize()
			} else if server.Host == nil {
				// 确保Host不为空
				server.Host = &model.Host{}
				server.Host.Initialize()
			}
		} else {
			// 如果没有JSON数据，初始化一个空的Host对象
			server.Host = &model.Host{}
			server.Host.Initialize()
		}

		// 解析持久化的状态信息
		if server.LastStateJSON != "" {
			err := json.Unmarshal([]byte(server.LastStateJSON), server.State)
			if err != nil {
				log.Printf("解析服务器状态信息失败，服务器ID：%d：%v", server.ID, err)
				server.State = &model.HostState{}
			} else if server.State != nil {
				server.LastStateBeforeOffline = server.State
			}
		}

		// 手动初始化DDNSProfiles字段（模拟AfterFind方法）
		if server.DDNSProfilesRaw != "" && server.DDNSProfilesRaw != "[]" {
			if err := utils.Json.Unmarshal([]byte(server.DDNSProfilesRaw), &server.DDNSProfiles); err != nil {
				log.Printf("解析服务器 %d 的DDNSProfiles失败: %v", server.ID, err)
				server.DDNSProfiles = []uint64{}
			}
		} else {
			server.DDNSProfiles = []uint64{}
		}

		// 检查并生成Secret
		if server.Secret == "" {
			log.Printf("服务器 %s (ID: %d) 没有Secret，正在生成新的Secret...", server.Name, server.ID)
			newSecret, err := utils.GenerateRandomString(18)
			if err != nil {
				log.Printf("为服务器 %s (ID: %d) 生成Secret失败: %v", server.Name, server.ID, err)
			} else {
				server.Secret = newSecret
				log.Printf("为服务器 %s (ID: %d) 生成新Secret: %s", server.Name, server.ID, newSecret)

				// 保存到数据库
				serverOps := db.NewServerOps(db.DB)
				if err := serverOps.SaveServer(server); err != nil {
					log.Printf("保存服务器 %s (ID: %d) 的新Secret到BadgerDB失败: %v", server.Name, server.ID, err)
				} else {
					log.Printf("已为服务器 %s (ID: %d) 生成并保存新Secret: %s", server.Name, server.ID, newSecret)
				}
			}
		}

		// 添加到服务器映射
		ServerList[server.ID] = server
	}

	// 构建排序后的服务器列表
	log.Println("构建排序的服务器列表...")
	// 刷新内存中的有序服务器列表
	SortedServerLock.Lock()
	defer SortedServerLock.Unlock()
	SortedServerList = []*model.Server{}
	SortedServerListForGuest = []*model.Server{}

	for _, s := range ServerList {
		SortedServerList = append(SortedServerList, s)
		if !s.HideForGuest {
			SortedServerListForGuest = append(SortedServerListForGuest, s)
		}
	}

	// 按照服务器排序值排序
	sort.SliceStable(SortedServerList, func(i, j int) bool {
		if SortedServerList[i].DisplayIndex == SortedServerList[j].DisplayIndex {
			return SortedServerList[i].ID < SortedServerList[j].ID
		}
		return SortedServerList[i].DisplayIndex < SortedServerList[j].DisplayIndex
	})

	sort.SliceStable(SortedServerListForGuest, func(i, j int) bool {
		if SortedServerListForGuest[i].DisplayIndex == SortedServerListForGuest[j].DisplayIndex {
			return SortedServerListForGuest[i].ID < SortedServerListForGuest[j].ID
		}
		return SortedServerListForGuest[i].DisplayIndex < SortedServerListForGuest[j].DisplayIndex
	})

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
