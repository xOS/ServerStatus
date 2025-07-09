package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/xos/serverstatus/db"
)

func main() {
	var (
		dataDir       = flag.String("data", "./data", "Data directory path")
		sqliteFile    = flag.String("sqlite", "sqlite.db", "SQLite database filename")
		badgerDir     = flag.String("badger", "badger", "BadgerDB directory name")
		mode          = flag.String("mode", "default", "Migration mode: quick, full, or default")
		historyDays   = flag.Int("history-days", 30, "Number of days of monitor history to migrate (-1 for all)")
		historyLimit  = flag.Int("history-limit", -1, "Maximum number of monitor history records to migrate (-1 for no limit)")
		batchSize     = flag.Int("batch-size", 100, "Batch size for processing records")
		skipLargeData = flag.Bool("skip-large-data", false, "Skip large data fields in monitor histories")
		resume        = flag.Bool("resume", true, "Enable resumable migration")
		workers       = flag.Int("workers", 4, "Number of concurrent workers")
	)

	flag.Parse()

	// 构建完整路径
	sqlitePath := filepath.Join(*dataDir, *sqliteFile)
	badgerPath := filepath.Join(*dataDir, *badgerDir)

	// 检查SQLite文件是否存在
	if _, err := os.Stat(sqlitePath); os.IsNotExist(err) {
		log.Fatalf("SQLite数据库文件不存在: %s", sqlitePath)
	}

	// 打开BadgerDB
	log.Printf("正在打开BadgerDB: %s", badgerPath)
	badgerDB, err := db.OpenDB(badgerPath)
	if err != nil {
		log.Fatalf("无法打开BadgerDB: %v", err)
	}
	defer badgerDB.Close()

	// 根据模式选择配置
	var config *db.MigrationConfig
	switch *mode {
	case "quick":
		config = db.QuickMigrationConfig()
		log.Println("使用快速迁移模式（仅迁移最近7天的监控历史）")
	case "full":
		config = db.FullMigrationConfig()
		log.Println("使用完整迁移模式（迁移所有数据）")
	default:
		config = db.DefaultMigrationConfig()
		log.Println("使用默认迁移模式")
	}

	// 应用命令行参数覆盖
	if *historyDays != 30 {
		config.MonitorHistoryDays = *historyDays
	}
	if *historyLimit != -1 {
		config.MonitorHistoryLimit = *historyLimit
	}
	if *batchSize != 100 {
		config.BatchSize = *batchSize
	}
	config.SkipLargeHistoryData = *skipLargeData
	config.EnableResume = *resume
	config.Workers = *workers

	// 显示配置信息
	log.Printf("迁移配置:")
	log.Printf("  - 批量大小: %d", config.BatchSize)
	log.Printf("  - 监控历史天数: %d", config.MonitorHistoryDays)
	log.Printf("  - 监控历史限制: %d", config.MonitorHistoryLimit)
	log.Printf("  - 跳过大数据字段: %v", config.SkipLargeHistoryData)
	log.Printf("  - 可恢复迁移: %v", config.EnableResume)
	log.Printf("  - 并发Workers: %d", config.Workers)

	// 执行迁移
	log.Printf("开始从 %s 迁移到 %s", sqlitePath, badgerPath)
	if err := db.RunMigrationWithConfig(badgerDB, sqlitePath, config); err != nil {
		log.Fatalf("迁移失败: %v", err)
	}

	log.Println("数据迁移成功完成！")

	// 显示迁移统计
	fmt.Println("\n迁移统计:")
	tables := []string{"server", "user", "monitor", "notification", "alert_rule", "cron", "transfer", "api_token", "nat", "ddns_profile", "ddns_record_state", "monitor_history"}

	for _, table := range tables {
		keys, err := badgerDB.GetKeysWithPrefix(table + ":")
		if err == nil {
			fmt.Printf("  %s: %d 条记录\n", table, len(keys))
		}
	}
}
