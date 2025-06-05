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
	sqlitePath := "data/sqlite.db"
	// 注意：当DatabaseType为badger时，DatabaseLocation指向BadgerDB目录
	// 我们总是使用默认的SQLite路径进行迁移
	if Conf != nil && Conf.DatabaseType != "badger" && Conf.DatabaseLocation != "" {
		sqlitePath = Conf.DatabaseLocation
	}

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

	log.Printf("从BadgerDB加载了 %d 台服务器", len(servers))

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

		log.Printf("加载服务器: ID=%d, 名称=%s", server.ID, server.Name)

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

	log.Printf("服务器列表加载完成: 共 %d 台服务器, 排序后 %d 台, 访客可见 %d 台",
		len(ServerList), len(SortedServerList), len(SortedServerListForGuest))

	// 显示服务器ID列表，用于调试
	if len(ServerList) > 0 {
		ids := []uint64{}
		for id := range ServerList {
			ids = append(ids, id)
		}
		log.Printf("服务器ID列表: %v", ids)
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
