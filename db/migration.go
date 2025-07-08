package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/xos/serverstatus/model"
)

// MigrationConfig holds configuration for migration process
type MigrationConfig struct {
	BatchSize            int           // Number of records to process in each batch
	MonitorHistoryDays   int           // Number of days of monitor history to migrate (-1 for all)
	MonitorHistoryLimit  int           // Maximum number of monitor history records to migrate (-1 for all)
	ProgressInterval     int           // Interval for progress reporting (every N records)
	EnableResume         bool          // Enable resumable migration
	MaxRetries           int           // Maximum retries for failed records
	RetryDelay           time.Duration // Delay between retries
	Workers              int           // Number of concurrent workers for batch processing
	SkipLargeHistoryData bool          // Skip large data fields in monitor_histories
}

// DefaultMigrationConfig returns default migration configuration
func DefaultMigrationConfig() *MigrationConfig {
	return &MigrationConfig{
		BatchSize:            100,
		MonitorHistoryDays:   30,
		MonitorHistoryLimit:  -1,
		ProgressInterval:     1000,
		EnableResume:         true,
		MaxRetries:           3,
		RetryDelay:           time.Second,
		Workers:              4,
		SkipLargeHistoryData: false,
	}
}

// MigrationProgress tracks the progress of migration
type MigrationProgress struct {
	TableName     string    `json:"table_name"`
	TotalRecords  int64     `json:"total_records"`
	Processed     int64     `json:"processed"`
	Success       int64     `json:"success"`
	Failed        int64     `json:"failed"`
	LastProcessed string    `json:"last_processed_id"`
	StartTime     time.Time `json:"start_time"`
	UpdateTime    time.Time `json:"update_time"`
	Completed     bool      `json:"completed"`
}

// Migration handles data migration from SQLite to BadgerDB
type Migration struct {
	badgerDB  *BadgerDB
	sqliteDB  *sql.DB
	config    *MigrationConfig
	progress  map[string]*MigrationProgress
	progressMu sync.RWMutex
	idMapping  map[string]map[uint64]uint64 // table -> oldID -> newID
	idMappingMu sync.RWMutex
}

// NewMigration creates a new Migration instance
func NewMigration(badgerDB *BadgerDB, sqlitePath string) (*Migration, error) {
	return NewMigrationWithConfig(badgerDB, sqlitePath, DefaultMigrationConfig())
}

// NewMigrationWithConfig creates a new Migration instance with custom configuration
func NewMigrationWithConfig(badgerDB *BadgerDB, sqlitePath string, config *MigrationConfig) (*Migration, error) {
	// 检查SQLite数据库文件是否存在
	if _, err := os.Stat(sqlitePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("SQLite数据库文件不存在: %s", sqlitePath)
	}

	// 打开SQLite数据库
	sqliteDB, err := sql.Open("sqlite3", sqlitePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite database: %w", err)
	}

	// 测试SQLite连接
	if err := sqliteDB.Ping(); err != nil {
		sqliteDB.Close()
		return nil, fmt.Errorf("failed to connect to SQLite database: %w", err)
	}

	// Set SQLite pragmas for better performance
	if _, err := sqliteDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		log.Printf("Warning: Failed to set journal_mode=WAL: %v", err)
	}
	if _, err := sqliteDB.Exec("PRAGMA synchronous=NORMAL"); err != nil {
		log.Printf("Warning: Failed to set synchronous=NORMAL: %v", err)
	}

	migration := &Migration{
		badgerDB:  badgerDB,
		sqliteDB:  sqliteDB,
		config:    config,
		progress:  make(map[string]*MigrationProgress),
		idMapping: make(map[string]map[uint64]uint64),
	}

	// Load progress if resumable migration is enabled
	if config.EnableResume {
		if err := migration.loadProgress(); err != nil {
			log.Printf("Warning: Failed to load migration progress: %v", err)
		}
	}

	return migration, nil
}

// Close closes the migration instance
func (m *Migration) Close() error {
	// Save final progress
	if m.config.EnableResume {
		if err := m.saveProgress(); err != nil {
			log.Printf("Warning: Failed to save migration progress: %v", err)
		}
	}
	return m.sqliteDB.Close()
}

// loadProgress loads migration progress from BadgerDB
func (m *Migration) loadProgress() error {
	data, err := m.badgerDB.Get("migration:progress")
	if err != nil {
		return nil // No progress saved yet
	}

	var progress map[string]*MigrationProgress
	if err := json.Unmarshal(data, &progress); err != nil {
		return fmt.Errorf("failed to unmarshal progress: %w", err)
	}

	m.progressMu.Lock()
	m.progress = progress
	m.progressMu.Unlock()

	// Load ID mapping
	idMapData, err := m.badgerDB.Get("migration:id_mapping")
	if err == nil {
		var idMapping map[string]map[uint64]uint64
		if err := json.Unmarshal(idMapData, &idMapping); err == nil {
			m.idMappingMu.Lock()
			m.idMapping = idMapping
			m.idMappingMu.Unlock()
		}
	}

	log.Println("已加载迁移进度")
	return nil
}

// saveProgress saves migration progress to BadgerDB
func (m *Migration) saveProgress() error {
	m.progressMu.RLock()
	progressData, err := json.Marshal(m.progress)
	m.progressMu.RUnlock()
	if err != nil {
		return fmt.Errorf("failed to marshal progress: %w", err)
	}

	if err := m.badgerDB.Set("migration:progress", progressData); err != nil {
		return fmt.Errorf("failed to save progress: %w", err)
	}

	// Save ID mapping
	m.idMappingMu.RLock()
	idMapData, err := json.Marshal(m.idMapping)
	m.idMappingMu.RUnlock()
	if err == nil {
		m.badgerDB.Set("migration:id_mapping", idMapData)
	}

	return nil
}

// getTableProgress gets or creates progress for a table
func (m *Migration) getTableProgress(tableName string) *MigrationProgress {
	m.progressMu.Lock()
	defer m.progressMu.Unlock()

	if progress, exists := m.progress[tableName]; exists {
		return progress
	}

	progress := &MigrationProgress{
		TableName: tableName,
		StartTime: time.Now(),
		UpdateTime: time.Now(),
	}
	m.progress[tableName] = progress
	return progress
}

// updateProgress updates migration progress
func (m *Migration) updateProgress(tableName string, processed, success, failed int64, lastID string) {
	m.progressMu.Lock()
	progress := m.progress[tableName]
	if progress != nil {
		progress.Processed = processed
		progress.Success = success
		progress.Failed = failed
		progress.LastProcessed = lastID
		progress.UpdateTime = time.Now()
	}
	m.progressMu.Unlock()

	// Save progress periodically
	if m.config.EnableResume && processed%int64(m.config.ProgressInterval) == 0 {
		if err := m.saveProgress(); err != nil {
			log.Printf("Warning: Failed to save progress: %v", err)
		}
	}
}

// mapID maps old ID to new ID and saves the mapping
func (m *Migration) mapID(tableName string, oldID, newID uint64) {
	m.idMappingMu.Lock()
	if m.idMapping[tableName] == nil {
		m.idMapping[tableName] = make(map[uint64]uint64)
	}
	m.idMapping[tableName][oldID] = newID
	m.idMappingMu.Unlock()
}

// getMappedID gets the new ID for an old ID
func (m *Migration) getMappedID(tableName string, oldID uint64) (uint64, bool) {
	m.idMappingMu.RLock()
	defer m.idMappingMu.RUnlock()

	if tableMap, exists := m.idMapping[tableName]; exists {
		if newID, exists := tableMap[oldID]; exists {
			return newID, true
		}
	}
	return 0, false
}

// MigrateAll migrates all data from SQLite to BadgerDB
func (m *Migration) MigrateAll() error {
	log.Println("开始数据迁移，从SQLite到BadgerDB...")

	// 迁移表
	tables := []struct {
		name     string
		model    interface{}
		migrator func() error
		priority int // 优先级，数字越小优先级越高
	}{
		// 先迁移基础表
		{"users", &model.User{}, m.migrateUsers, 1},
		{"servers", &model.Server{}, m.migrateServers, 2},
		{"notifications", &model.Notification{}, m.migrateNotifications, 3},
		{"alert_rules", &model.AlertRule{}, m.migrateAlertRules, 4},
		{"monitors", &model.Monitor{}, m.migrateMonitors, 5},
		{"crons", &model.Cron{}, m.migrateCrons, 6},
		{"api_tokens", &model.ApiToken{}, m.migrateApiTokens, 7},
		{"nats", &model.NAT{}, m.migrateNATs, 8},
		{"ddns", &model.DDNSProfile{}, m.migrateDDNSProfiles, 9},
		{"ddns_record_states", &model.DDNSRecordState{}, m.migrateDDNSRecordStates, 10},
		// 最后迁移大表
		{"transfers", &model.Transfer{}, m.migrateTransfers, 11},
		{"monitor_histories", &model.MonitorHistory{}, m.migrateMonitorHistories, 12},
	}

	// 计算总记录数
	totalRecords := int64(0)
	for _, table := range tables {
		progress := m.getTableProgress(table.name)
		if progress.Completed {
			log.Printf("表 %s 已完成迁移，跳过", table.name)
			continue
		}

		// 获取表记录数
		count, err := m.getTableRecordCount(table.name)
		if err != nil {
			if strings.Contains(err.Error(), "no such table") {
				log.Printf("表 %s 不存在，跳过", table.name)
				progress.Completed = true
				continue
			}
			log.Printf("获取表 %s 记录数失败: %v", table.name, err)
		}
		progress.TotalRecords = count
		totalRecords += count
	}

	log.Printf("总共需要迁移 %d 条记录", totalRecords)

	// 按优先级迁移表
	for _, table := range tables {
		progress := m.getTableProgress(table.name)
		if progress.Completed {
			continue
		}

		log.Printf("MigrateAll: 准备迁移表 %s (总计 %d 条记录)...", table.name, progress.TotalRecords)
		startTime := time.Now()
		err := table.migrator()
		duration := time.Since(startTime)

		if err != nil {
			// 检查是否是表不存在的错误
			if strings.Contains(err.Error(), "no such table") {
				log.Printf("MigrateAll: 表 %s 不存在，跳过迁移", table.name)
				progress.Completed = true
				continue
			}
			log.Printf("MigrateAll: 迁移表 %s 失败: %v", table.name, err)
			return fmt.Errorf("failed to migrate %s: %w", table.name, err)
		}

		progress.Completed = true
		m.saveProgress()

		log.Printf("MigrateAll: 迁移表 %s 完成。成功: %d, 失败: %d, 耗时: %v", 
			table.name, progress.Success, progress.Failed, duration)
	}

	log.Println("所有数据表迁移完成！")
	
	// 清理迁移进度
	if m.config.EnableResume {
		m.badgerDB.Delete("migration:progress")
		m.badgerDB.Delete("migration:id_mapping")
	}
	
	return nil
}

// getTableRecordCount gets the total number of records in a table
func (m *Migration) getTableRecordCount(tableName string) (int64, error) {
	var count int64
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE deleted_at IS NULL", tableName)
	
	// Special handling for tables without deleted_at
	if tableName == "transfers" || tableName == "monitor_histories" || tableName == "ddns_record_states" {
		query = fmt.Sprintf("SELECT COUNT(*) FROM %s", tableName)
	}
	
	err := m.sqliteDB.QueryRow(query).Scan(&count)
	return count, err
}

// migrateWithRetry migrates a single record with retry logic
func (m *Migration) migrateWithRetry(saveFunc func() error) error {
	var err error
	for retry := 0; retry <= m.config.MaxRetries; retry++ {
		if retry > 0 {
			time.Sleep(m.config.RetryDelay * time.Duration(retry))
		}
		
		err = saveFunc()
		if err == nil {
			return nil
		}
		
		if retry < m.config.MaxRetries {
			log.Printf("保存失败，重试 %d/%d: %v", retry+1, m.config.MaxRetries, err)
		}
	}
	return err
}

// scanToMap scans a row into a map with proper type conversion
func scanToMap(rows *sql.Rows) (map[string]interface{}, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	values := make([]interface{}, len(columns))
	pointers := make([]interface{}, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}

	if err := rows.Scan(pointers...); err != nil {
		return nil, err
	}

	m := make(map[string]interface{})
	for i, column := range columns {
		val := values[i]

		// Handle different data types properly
		switch v := val.(type) {
		case []byte:
			str := string(v)
			// Try to parse as time for known time columns
			if isTimeColumn(column) {
				if parsedTime, err := parseTimeString(str); err == nil {
					m[column] = parsedTime
				} else {
					log.Printf("警告：无法解析时间字段 %s 的值 '%s': %v", column, str, err)
					m[column] = time.Time{} // Use zero time as fallback
				}
			} else {
				m[column] = str
			}
		case string:
			// Try to parse as time for known time columns
			if isTimeColumn(column) {
				if parsedTime, err := parseTimeString(v); err == nil {
					m[column] = parsedTime
				} else {
					log.Printf("警告：无法解析时间字段 %s 的值 '%s': %v", column, v, err)
					m[column] = time.Time{} // Use zero time as fallback
				}
			} else {
				m[column] = v
			}
		case nil:
			m[column] = nil
		default:
			m[column] = val
		}
	}

	return m, nil
}

// isTimeColumn checks if a column name represents a time field
func isTimeColumn(columnName string) bool {
	timeColumns := []string{
		"created_at", "updated_at", "deleted_at",
		"last_active", "last_online", "last_flow_save_time",
		"last_db_update_time", "last_seen", "last_ping",
		"CreatedAt", "UpdatedAt", "DeletedAt",
		"LastActive", "LastOnline", "LastFlowSaveTime",
		"LastDBUpdateTime", "LastSeen", "LastPing",
	}

	for _, timeCol := range timeColumns {
		if columnName == timeCol {
			return true
		}
	}
	return false
}

// parseTimeString attempts to parse various time string formats
func parseTimeString(timeStr string) (time.Time, error) {
	if timeStr == "" || timeStr == "NULL" {
		return time.Time{}, nil
	}

	// Common time formats used by SQLite and GORM
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05.999999999Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05+00:00",
		"2006-01-02 15:04:05 UTC",
	}

	for _, format := range formats {
		if t, err := time.Parse(format, timeStr); err == nil {
			return t, nil
		}
	}

	// Try parsing as Unix timestamp (seconds)
	if timestamp, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
		return time.Unix(timestamp, 0), nil
	}

	// Try parsing as Unix timestamp (milliseconds)
	if timestamp, err := strconv.ParseInt(timeStr, 10, 64); err == nil && timestamp > 1000000000000 {
		return time.Unix(timestamp/1000, (timestamp%1000)*1000000), nil
	}

	return time.Time{}, fmt.Errorf("无法解析时间字符串: %s", timeStr)
}

// migrateServers migrates servers from SQLite to BadgerDB
func (m *Migration) migrateServers() error {
	tableName := "servers"
	progress := m.getTableProgress(tableName)
	
	log.Printf("开始迁移服务器数据... (已处理: %d/%d)", progress.Processed, progress.TotalRecords)
	
	// 构建查询
	query := "SELECT * FROM servers WHERE deleted_at IS NULL"
	if m.config.EnableResume && progress.LastProcessed != "" {
		query += fmt.Sprintf(" AND id > %s", progress.LastProcessed)
	}
	query += " ORDER BY id"
	
	rows, err := m.sqliteDB.Query(query)
	if err != nil {
		return fmt.Errorf("查询服务器数据失败: %w", err)
	}
	defer rows.Close()

	count := progress.Processed
	errorCount := progress.Failed
	successCount := progress.Success

	// 获取列名
	columns, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("获取列名失败: %w", err)
	}

	log.Printf("服务器数据列: %v", columns)

	for rows.Next() {
		data, err := scanToMap(rows)
		if err != nil {
			log.Printf("扫描行数据失败: %v，跳过", err)
			errorCount++
			continue
		}

		// Extract ID for key
		idVal, ok := data["id"]
		if !ok {
			log.Printf("服务器数据缺少ID字段，跳过: %v", data)
			errorCount++
			continue
		}

		// 确保ID是有效的
		var id uint64
		switch v := idVal.(type) {
		case int64:
			id = uint64(v)
		case float64:
			id = uint64(v)
		case string:
			parsed, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				log.Printf("解析服务器ID '%s' 失败: %v，跳过. Data: %v", v, err, data)
				errorCount++
				continue
			}
			id = parsed
		default:
			log.Printf("服务器ID类型无效: %T，跳过. Data: %v", idVal, data)
			errorCount++
			continue
		}

		if id == 0 {
			log.Printf("服务器ID为0，跳过. Data: %v", data)
			errorCount++
			continue
		}

		// 保留原始ID，不重新分配
		newID := id

		// 确保正确处理布尔值字段
		boolFields := []string{"hide_for_guest", "enable_d_dns", "enable_ipv4", "enable_ipv6"}
		for _, field := range boolFields {
			if val, ok := data[field]; ok {
				switch v := val.(type) {
				case int64:
					data[field] = v != 0
				case float64:
					data[field] = v != 0
				case string:
					data[field] = v == "1" || v == "true" || v == "t"
				}
			}
		}

		log.Printf("迁移服务器 ID %d: 原始数据: %v", id, data)

		// 预处理时间字段，确保它们是正确的格式，映射到正确的Go字段名
		timeFieldMapping := map[string]string{
			"created_at":  "CreatedAt",
			"updated_at":  "UpdatedAt",
			"last_online": "LastOnline",
		}

		for sqlField, goField := range timeFieldMapping {
			if val, ok := data[sqlField]; ok {
				var timeStr string
				switch v := val.(type) {
				case time.Time:
					timeStr = v.Format(time.RFC3339)
				case string:
					if parsedTime, err := parseTimeString(v); err == nil {
						timeStr = parsedTime.Format(time.RFC3339)
					} else {
						log.Printf("服务器ID %d: 无法解析时间字段 %s 的值 '%s': %v", id, sqlField, v, err)
						if sqlField == "created_at" || sqlField == "updated_at" {
							timeStr = time.Now().Format(time.RFC3339)
						} else {
							timeStr = "0001-01-01T00:00:00Z"
						}
					}
				case nil:
					if sqlField == "created_at" || sqlField == "updated_at" {
						timeStr = time.Now().Format(time.RFC3339)
					} else {
						timeStr = "0001-01-01T00:00:00Z"
					}
				default:
					log.Printf("服务器ID %d: 时间字段 %s 的类型无效: %T，值: %v", id, sqlField, v, v)
					if sqlField == "created_at" || sqlField == "updated_at" {
						timeStr = time.Now().Format(time.RFC3339)
					} else {
						timeStr = "0001-01-01T00:00:00Z"
					}
				}

				// 删除原始字段名，添加Go格式的字段名
				delete(data, sqlField)
				data[goField] = timeStr
			} else {
				// 如果字段不存在，添加默认值
				if sqlField == "created_at" || sqlField == "updated_at" {
					data[goField] = time.Now().Format(time.RFC3339)
				} else {
					data[goField] = "0001-01-01T00:00:00Z"
				}
			}
		}

		// 处理其他字段名映射
		fieldMapping := map[string]string{
			"id":                          "ID",
			"name":                        "Name",
			"tag":                         "Tag",
			"secret":                      "Secret",
			"note":                        "Note",
			"display_index":               "DisplayIndex",
			"hide_for_guest":              "HideForGuest",
			"enable_d_dns":                "EnableDDNS",
			"d_dns_domain":                "DDNSDomain",
			"enable_ipv4":                 "EnableIPv4",
			"enable_ipv6":                 "EnableIPv6",
			"d_dns_profile":               "DDNSProfile",
			"public_note":                 "PublicNote",
			"ddns_profiles_raw":           "DDNSProfilesRaw",
			"cumulative_net_in_transfer":  "CumulativeNetInTransfer",
			"cumulative_net_out_transfer": "CumulativeNetOutTransfer",
			"host_json":                   "HostJSON",
			"last_state_json":             "LastStateJSON",
		}

		for sqlField, goField := range fieldMapping {
			if val, ok := data[sqlField]; ok {
				delete(data, sqlField)
				data[goField] = val
			}
		}

		// 特殊处理数值字段，确保类型正确
		if val, ok := data["CumulativeNetInTransfer"]; ok {
			switch v := val.(type) {
			case int64:
				data["CumulativeNetInTransfer"] = uint64(v)
			case float64:
				data["CumulativeNetInTransfer"] = uint64(v)
			case string:
				if parsed, err := strconv.ParseUint(v, 10, 64); err == nil {
					data["CumulativeNetInTransfer"] = parsed
				} else {
					data["CumulativeNetInTransfer"] = uint64(0)
				}
			default:
				data["CumulativeNetInTransfer"] = uint64(0)
			}
		}

		if val, ok := data["CumulativeNetOutTransfer"]; ok {
			switch v := val.(type) {
			case int64:
				data["CumulativeNetOutTransfer"] = uint64(v)
			case float64:
				data["CumulativeNetOutTransfer"] = uint64(v)
			case string:
				if parsed, err := strconv.ParseUint(v, 10, 64); err == nil {
					data["CumulativeNetOutTransfer"] = parsed
				} else {
					data["CumulativeNetOutTransfer"] = uint64(0)
				}
			default:
				data["CumulativeNetOutTransfer"] = uint64(0)
			}
		}

		log.Printf("迁移服务器 ID %d: 预处理后的数据: %v", id, data)

		// 尝试构建 Server 模型对象
		var server model.Server

		// 先保存HostJSON和LastStateJSON，因为它们有json:"-"标签
		hostJSON := ""
		lastStateJSON := ""
		if val, ok := data["HostJSON"]; ok {
			if str, ok := val.(string); ok {
				hostJSON = str
			}
		}
		if val, ok := data["LastStateJSON"]; ok {
			if str, ok := val.(string); ok {
				lastStateJSON = str
			}
		}

		serverJSON, err := json.Marshal(data)
		if err != nil {
			log.Printf("服务器ID %d: 序列化原始数据失败: %v. Data: %v", id, err, data)
			errorCount++
			continue
		}

		if err := json.Unmarshal(serverJSON, &server); err != nil {
			log.Printf("服务器ID %d: 反序列化为Server对象失败: %v. JSON Data: %s", id, err, string(serverJSON))
			errorCount++
			continue
		}

		// 保留原始ID
		server.ID = newID

		// 手动设置被json:"-"忽略的字段
		server.HostJSON = hostJSON
		server.LastStateJSON = lastStateJSON

		// 如果服务器名称为空，给一个默认名称
		if server.Name == "" {
			server.Name = fmt.Sprintf("Server-%d", server.ID)
			log.Printf("服务器ID %d: 名称为空，设置为默认名称 '%s'", id, server.Name)
		}

		// 添加额外的日志
		log.Printf("服务器ID %d: 准备迁移的服务器对象: %+v", id, server)
		log.Printf("服务器ID %d: HostJSON长度=%d, LastStateJSON长度=%d", server.ID, len(server.HostJSON), len(server.LastStateJSON))

		// 重新序列化为JSON以保存
		serverJSON, err = json.Marshal(server)
		if err != nil {
			log.Printf("服务器ID %d: 重新序列化处理后的Server对象失败: %v. Object: %+v", id, err, server)
			errorCount++
			continue
		}

		// 由于HostJSON和LastStateJSON有json:"-"标签，需要手动添加这些字段
		var serverMap map[string]interface{}
		if err := json.Unmarshal(serverJSON, &serverMap); err == nil {
			// 添加被忽略的字段
			if server.HostJSON != "" {
				serverMap["HostJSON"] = server.HostJSON
			}
			if server.LastStateJSON != "" {
				serverMap["LastStateJSON"] = server.LastStateJSON
			}

			// 重新序列化
			if modifiedJSON, err := json.Marshal(serverMap); err == nil {
				serverJSON = modifiedJSON
				log.Printf("服务器ID %d: 已添加HostJSON和LastStateJSON字段到序列化数据", id)
			}
		}

		if len(serverJSON) == 0 {
			log.Printf("服务器ID %d: serverJSON为空，无法保存. Data: %v", id, data)
			errorCount++
			continue
		}

		// Save to BadgerDB using original ID
		key := fmt.Sprintf("server:%v", server.ID)
		log.Printf("服务器ID %d: 准备保存到BadgerDB. Key: '%s', Value: %s", id, key, string(serverJSON))
		
		err = m.migrateWithRetry(func() error {
			return m.badgerDB.Set(key, serverJSON)
		})
		
		if err != nil {
			log.Printf("服务器ID %d: 保存到BadgerDB失败: %v. Key: '%s'", server.ID, err, key)
			errorCount++
			continue
		}

		// 保存ID映射
		m.mapID(tableName, id, newID)

		count++
		successCount++
		
		// 更新进度
		m.updateProgress(tableName, count, successCount, errorCount, strconv.FormatUint(id, 10))
		
		if count%int64(m.config.ProgressInterval) == 0 {
			log.Printf("已迁移 %d 条服务器记录...", count)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("迭代服务器行时出错: %w", err)
	}

	log.Printf("服务器迁移完成: 成功 %d 条, 失败 %d 条", successCount, errorCount)
	return nil
}

// migrateUsers migrates users from SQLite to BadgerDB
func (m *Migration) migrateUsers() error {
	log.Println("开始迁移用户数据...")
	rows, err := m.sqliteDB.Query("SELECT * FROM users WHERE deleted_at IS NULL")
	if err != nil {
		return fmt.Errorf("查询用户数据失败: %w", err)
	}
	defer rows.Close()

	count := 0
	errorCount := 0

	for rows.Next() {
		data, err := scanToMap(rows)
		if err != nil {
			log.Printf("扫描用户行数据失败: %v，跳过", err)
			errorCount++
			continue
		}

		// Extract ID for key
		idVal, ok := data["id"]
		if !ok {
			log.Printf("用户数据缺少ID字段，跳过: %v", data)
			errorCount++
			continue
		}

		// 确保ID是有效的
		var id uint64
		switch v := idVal.(type) {
		case int64:
			id = uint64(v)
		case float64:
			id = uint64(v)
		case string:
			parsed, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				log.Printf("解析用户ID '%s' 失败: %v，跳过. Data: %v", v, err, data)
				errorCount++
				continue
			}
			id = parsed
		default:
			log.Printf("用户ID类型无效: %T，跳过. Data: %v", idVal, data)
			errorCount++
			continue
		}

		if id == 0 {
			log.Printf("用户ID为0，跳过. Data: %v", data)
			errorCount++
			continue
		}

		log.Printf("迁移用户 ID %d: 原始数据: %v", id, data)

		// Convert to JSON
		jsonData, err := json.Marshal(data)
		if err != nil {
			log.Printf("用户ID %d: 序列化数据失败: %v. Data: %v", id, err, data)
			errorCount++
			continue
		}

		// Save to BadgerDB
		key := fmt.Sprintf("user:%v", id)
		log.Printf("用户ID %d: 准备保存到BadgerDB. Key: '%s', Value: %s", id, key, string(jsonData))
		if err := m.badgerDB.Set(key, jsonData); err != nil {
			log.Printf("用户ID %d: 保存到BadgerDB失败: %v. Key: '%s'", id, err, key)
			errorCount++
			continue
		}

		count++
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("迭代用户行时出错: %w", err)
	}

	log.Printf("用户迁移完成: 成功 %d 条, 失败 %d 条", count, errorCount)
	return nil
}

// migrateMonitors migrates monitors from SQLite to BadgerDB
func (m *Migration) migrateMonitors() error {
	log.Println("开始迁移监控器数据...")
	rows, err := m.sqliteDB.Query("SELECT * FROM monitors WHERE deleted_at IS NULL")
	if err != nil {
		return fmt.Errorf("查询监控器数据失败: %w", err)
	}
	defer rows.Close()

	count := 0
	errorCount := 0

	for rows.Next() {
		data, err := scanToMap(rows)
		if err != nil {
			log.Printf("扫描监控器行数据失败: %v，跳过", err)
			errorCount++
			continue
		}

		// Extract ID for key
		idVal, ok := data["id"]
		if !ok {
			log.Printf("监控器数据缺少ID字段，跳过: %v", data)
			errorCount++
			continue
		}

		// 确保ID是有效的
		var id uint64
		switch v := idVal.(type) {
		case int64:
			id = uint64(v)
		case float64:
			id = uint64(v)
		case string:
			parsed, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				log.Printf("解析监控器ID '%s' 失败: %v，跳过. Data: %v", v, err, data)
				errorCount++
				continue
			}
			id = parsed
		default:
			log.Printf("监控器ID类型无效: %T，跳过. Data: %v", idVal, data)
			errorCount++
			continue
		}

		if id == 0 {
			log.Printf("监控器ID为0，跳过. Data: %v", data)
			errorCount++
			continue
		}

		log.Printf("迁移监控器 ID %d: 原始数据: %v", id, data)

		// Convert to JSON
		jsonData, err := json.Marshal(data)
		if err != nil {
			log.Printf("监控器ID %d: 序列化数据失败: %v. Data: %v", id, err, data)
			errorCount++
			continue
		}

		// Save to BadgerDB
		key := fmt.Sprintf("monitor:%v", id)
		log.Printf("监控器ID %d: 准备保存到BadgerDB. Key: '%s', Value: %s", id, key, string(jsonData))
		if err := m.badgerDB.Set(key, jsonData); err != nil {
			log.Printf("监控器ID %d: 保存到BadgerDB失败: %v. Key: '%s'", id, err, key)
			errorCount++
			continue
		}

		count++
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("迭代监控器行时出错: %w", err)
	}

	log.Printf("监控器迁移完成: 成功 %d 条, 失败 %d 条", count, errorCount)
	return nil
}

// migrateMonitorHistories migrates monitor histories from SQLite to BadgerDB
func (m *Migration) migrateMonitorHistories() error {
	tableName := "monitor_histories"
	progress := m.getTableProgress(tableName)
	
	log.Printf("开始迁移监控历史数据... (已处理: %d/%d)", progress.Processed, progress.TotalRecords)
	
	// 构建查询
	query := "SELECT * FROM monitor_histories WHERE deleted_at IS NULL"
	
	// 根据配置决定是否限制数据范围
	if m.config.MonitorHistoryDays > 0 {
		cutoffDate := time.Now().AddDate(0, 0, -m.config.MonitorHistoryDays)
		query += fmt.Sprintf(" AND created_at > '%s'", cutoffDate.Format("2006-01-02"))
	}
	
	if m.config.EnableResume && progress.LastProcessed != "" {
		query += fmt.Sprintf(" AND id > %s", progress.LastProcessed)
	}
	
	query += " ORDER BY id"
	
	// 添加批量限制
	if m.config.MonitorHistoryLimit > 0 && progress.Processed < int64(m.config.MonitorHistoryLimit) {
		remaining := int64(m.config.MonitorHistoryLimit) - progress.Processed
		batchSize := int64(m.config.BatchSize)
		if remaining < batchSize {
			batchSize = remaining
		}
		query += fmt.Sprintf(" LIMIT %d", batchSize)
	} else if m.config.BatchSize > 0 {
		query += fmt.Sprintf(" LIMIT %d", m.config.BatchSize)
	}
	
	// 分批处理
	for {
		rows, err := m.sqliteDB.Query(query)
		if err != nil {
			return fmt.Errorf("查询监控历史数据失败: %w", err)
		}
		
		hasRows := false
		batchCount := 0
		lastID := ""
		
		for rows.Next() {
			hasRows = true
			data, err := scanToMap(rows)
			if err != nil {
				log.Printf("扫描监控历史行数据失败: %v，跳过", err)
				progress.Failed++
				continue
			}
			
			// Extract ID for key
			idVal, ok := data["id"]
			if !ok {
				log.Printf("监控历史数据缺少ID字段，跳过: %v", data)
				progress.Failed++
				continue
			}
			
			// 确保ID是有效的
			var id uint64
			switch v := idVal.(type) {
			case int64:
				id = uint64(v)
			case float64:
				id = uint64(v)
			case string:
				parsed, err := strconv.ParseUint(v, 10, 64)
				if err != nil {
					log.Printf("解析监控历史ID '%s' 失败: %v，跳过", v, err)
					progress.Failed++
					continue
				}
				id = parsed
			default:
				log.Printf("监控历史ID类型无效: %T，跳过", idVal)
				progress.Failed++
				continue
			}
			
			if id == 0 {
				log.Printf("监控历史ID为0，跳过")
				progress.Failed++
				continue
			}
			
			lastID = strconv.FormatUint(id, 10)
			
			// 获取monitor_id和created_at用于构建key
			monitorID, _ := data["monitor_id"]
			createdAt, _ := data["created_at"]
			
			// 如果配置了跳过大数据字段，移除data字段
			if m.config.SkipLargeHistoryData {
				delete(data, "data")
			}
			
			// Convert to JSON
			jsonData, err := json.Marshal(data)
			if err != nil {
				log.Printf("监控历史ID %d: 序列化数据失败: %v", id, err)
				progress.Failed++
				continue
			}
			
			// Save to BadgerDB with a compound key
			key := fmt.Sprintf("monitor_history:%v:%v:%v", monitorID, createdAt, id)
			
			err = m.migrateWithRetry(func() error {
				return m.badgerDB.Set(key, jsonData)
			})
			
			if err != nil {
				log.Printf("监控历史ID %d: 保存到BadgerDB失败: %v. Key: '%s'", id, err, key)
				progress.Failed++
				continue
			}
			
			progress.Processed++
			progress.Success++
			batchCount++
			
			// 定期更新进度
			if progress.Processed%int64(m.config.ProgressInterval) == 0 {
				m.updateProgress(tableName, progress.Processed, progress.Success, progress.Failed, lastID)
				log.Printf("已处理 %d 条监控历史记录...", progress.Processed)
			}
			
			// 检查是否达到限制
			if m.config.MonitorHistoryLimit > 0 && progress.Processed >= int64(m.config.MonitorHistoryLimit) {
				log.Printf("已达到监控历史记录迁移限制: %d", m.config.MonitorHistoryLimit)
				rows.Close()
				goto done
			}
		}
		
		rows.Close()
		
		if err := rows.Err(); err != nil {
			return fmt.Errorf("迭代监控历史行时出错: %w", err)
		}
		
		// 如果没有更多数据，退出循环
		if !hasRows || batchCount == 0 {
			break
		}
		
		// 更新查询以获取下一批数据
		if m.config.EnableResume && lastID != "" {
			// 更新进度
			m.updateProgress(tableName, progress.Processed, progress.Success, progress.Failed, lastID)
			
			// 构建下一批查询
			query = "SELECT * FROM monitor_histories WHERE deleted_at IS NULL"
			if m.config.MonitorHistoryDays > 0 {
				cutoffDate := time.Now().AddDate(0, 0, -m.config.MonitorHistoryDays)
				query += fmt.Sprintf(" AND created_at > '%s'", cutoffDate.Format("2006-01-02"))
			}
			query += fmt.Sprintf(" AND id > %s", lastID)
			query += " ORDER BY id"
			
			if m.config.MonitorHistoryLimit > 0 && progress.Processed < int64(m.config.MonitorHistoryLimit) {
				remaining := int64(m.config.MonitorHistoryLimit) - progress.Processed
				batchSize := int64(m.config.BatchSize)
				if remaining < batchSize {
					batchSize = remaining
				}
				query += fmt.Sprintf(" LIMIT %d", batchSize)
			} else if m.config.BatchSize > 0 {
				query += fmt.Sprintf(" LIMIT %d", m.config.BatchSize)
			}
		} else {
			// 如果不支持恢复，直接退出
			break
		}
		
		// 短暂休息，避免占用过多资源
		time.Sleep(10 * time.Millisecond)
	}
	
done:
	// 最终更新进度
	m.updateProgress(tableName, progress.Processed, progress.Success, progress.Failed, progress.LastProcessed)
	
	log.Printf("监控历史迁移完成: 成功 %d 条, 失败 %d 条", progress.Success, progress.Failed)
	return nil
}

// migrateNotifications migrates notifications from SQLite to BadgerDB
func (m *Migration) migrateNotifications() error {
	log.Println("开始迁移通知数据...")
	rows, err := m.sqliteDB.Query("SELECT * FROM notifications WHERE deleted_at IS NULL")
	if err != nil {
		return fmt.Errorf("查询通知数据失败: %w", err)
	}
	defer rows.Close()

	count := 0
	errorCount := 0

	for rows.Next() {
		data, err := scanToMap(rows)
		if err != nil {
			log.Printf("扫描通知行数据失败: %v，跳过", err)
			errorCount++
			continue
		}

		// Extract ID for key
		idVal, ok := data["id"]
		if !ok {
			log.Printf("通知数据缺少ID字段，跳过: %v", data)
			errorCount++
			continue
		}

		// 确保ID是有效的
		var id uint64
		switch v := idVal.(type) {
		case int64:
			id = uint64(v)
		case float64:
			id = uint64(v)
		case string:
			parsed, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				log.Printf("解析通知ID '%s' 失败: %v，跳过. Data: %v", v, err, data)
				errorCount++
				continue
			}
			id = parsed
		default:
			log.Printf("通知ID类型无效: %T，跳过. Data: %v", idVal, data)
			errorCount++
			continue
		}

		if id == 0 {
			log.Printf("通知ID为0，跳过. Data: %v", data)
			errorCount++
			continue
		}

		log.Printf("迁移通知 ID %d: 原始数据: %v", id, data)

		// Convert to JSON
		jsonData, err := json.Marshal(data)
		if err != nil {
			log.Printf("通知ID %d: 序列化数据失败: %v. Data: %v", id, err, data)
			errorCount++
			continue
		}

		// Save to BadgerDB
		key := fmt.Sprintf("notification:%v", id)
		if err := m.badgerDB.Set(key, jsonData); err != nil {
			log.Printf("通知ID %d: 保存到BadgerDB失败: %v. Key: '%s'", id, err, key)
			errorCount++
			continue
		}

		count++
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("迭代通知行时出错: %w", err)
	}

	log.Printf("通知迁移完成: 成功 %d 条, 失败 %d 条", count, errorCount)
	return nil
}

// migrateAlertRules migrates alert rules from SQLite to BadgerDB
func (m *Migration) migrateAlertRules() error {
	log.Println("开始迁移报警规则数据...")
	rows, err := m.sqliteDB.Query("SELECT * FROM alert_rules WHERE deleted_at IS NULL")
	if err != nil {
		return fmt.Errorf("查询报警规则数据失败: %w", err)
	}
	defer rows.Close()

	count := 0
	errorCount := 0

	for rows.Next() {
		data, err := scanToMap(rows)
		if err != nil {
			log.Printf("扫描报警规则行数据失败: %v，跳过", err)
			errorCount++
			continue
		}

		// Extract ID for key
		idVal, ok := data["id"]
		if !ok {
			log.Printf("报警规则数据缺少ID字段，跳过: %v", data)
			errorCount++
			continue
		}

		// 确保ID是有效的
		var id uint64
		switch v := idVal.(type) {
		case int64:
			id = uint64(v)
		case float64:
			id = uint64(v)
		case string:
			parsed, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				log.Printf("解析报警规则ID '%s' 失败: %v，跳过. Data: %v", v, err, data)
				errorCount++
				continue
			}
			id = parsed
		default:
			log.Printf("报警规则ID类型无效: %T，跳过. Data: %v", idVal, data)
			errorCount++
			continue
		}

		if id == 0 {
			log.Printf("报警规则ID为0，跳过. Data: %v", data)
			errorCount++
			continue
		}

		// Convert to JSON
		jsonData, err := json.Marshal(data)
		if err != nil {
			log.Printf("报警规则ID %d: 序列化数据失败: %v. Data: %v", id, err, data)
			errorCount++
			continue
		}

		// Save to BadgerDB
		key := fmt.Sprintf("alert_rule:%v", id)
		if err := m.badgerDB.Set(key, jsonData); err != nil {
			log.Printf("报警规则ID %d: 保存到BadgerDB失败: %v. Key: '%s'", id, err, key)
			errorCount++
			continue
		}

		count++
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("迭代报警规则行时出错: %w", err)
	}

	log.Printf("报警规则迁移完成: 成功 %d 条, 失败 %d 条", count, errorCount)
	return nil
}

// migrateCrons migrates crons from SQLite to BadgerDB
func (m *Migration) migrateCrons() error {
	log.Println("开始迁移定时任务数据...")
	rows, err := m.sqliteDB.Query("SELECT * FROM crons WHERE deleted_at IS NULL")
	if err != nil {
		return fmt.Errorf("查询定时任务数据失败: %w", err)
	}
	defer rows.Close()

	count := 0
	errorCount := 0

	for rows.Next() {
		data, err := scanToMap(rows)
		if err != nil {
			log.Printf("扫描定时任务行数据失败: %v，跳过", err)
			errorCount++
			continue
		}

		// Extract ID for key
		idVal, ok := data["id"]
		if !ok {
			log.Printf("定时任务数据缺少ID字段，跳过: %v", data)
			errorCount++
			continue
		}

		// 确保ID是有效的
		var id uint64
		switch v := idVal.(type) {
		case int64:
			id = uint64(v)
		case float64:
			id = uint64(v)
		case string:
			parsed, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				log.Printf("解析定时任务ID '%s' 失败: %v，跳过. Data: %v", v, err, data)
				errorCount++
				continue
			}
			id = parsed
		default:
			log.Printf("定时任务ID类型无效: %T，跳过. Data: %v", idVal, data)
			errorCount++
			continue
		}

		if id == 0 {
			log.Printf("定时任务ID为0，跳过. Data: %v", data)
			errorCount++
			continue
		}

		// Convert to JSON
		jsonData, err := json.Marshal(data)
		if err != nil {
			log.Printf("定时任务ID %d: 序列化数据失败: %v. Data: %v", id, err, data)
			errorCount++
			continue
		}

		// Save to BadgerDB
		key := fmt.Sprintf("cron:%v", id)
		if err := m.badgerDB.Set(key, jsonData); err != nil {
			log.Printf("定时任务ID %d: 保存到BadgerDB失败: %v. Key: '%s'", id, err, key)
			errorCount++
			continue
		}

		count++
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("迭代定时任务行时出错: %w", err)
	}

	log.Printf("定时任务迁移完成: 成功 %d 条, 失败 %d 条", count, errorCount)
	return nil
}

// migrateTransfers migrates transfers from SQLite to BadgerDB
func (m *Migration) migrateTransfers() error {
	tableName := "transfers"
	progress := m.getTableProgress(tableName)
	
	log.Printf("开始迁移流量数据... (已处理: %d/%d)", progress.Processed, progress.TotalRecords)
	
	// 构建查询
	query := "SELECT * FROM transfers"
	if m.config.EnableResume && progress.LastProcessed != "" {
		query += fmt.Sprintf(" WHERE id > %s", progress.LastProcessed)
	}
	query += " ORDER BY id"
	
	// 添加批量限制
	if m.config.BatchSize > 0 {
		query += fmt.Sprintf(" LIMIT %d", m.config.BatchSize)
	}
	
	// 分批处理
	for {
		rows, err := m.sqliteDB.Query(query)
		if err != nil {
			return fmt.Errorf("查询流量数据失败: %w", err)
		}
		
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return fmt.Errorf("获取列名失败: %w", err)
		}
		
		hasRows := false
		batchCount := 0
		lastID := ""
		
		for rows.Next() {
			hasRows = true
			var transfer model.Transfer
			scanValues := getScanValues(columns, &transfer)
			if err := rows.Scan(scanValues...); err != nil {
				log.Printf("扫描流量数据失败: %v，跳过", err)
				progress.Failed++
				continue
			}
			
			if transfer.ID == 0 {
				log.Printf("流量ID为0，跳过")
				progress.Failed++
				continue
			}
			
			lastID = strconv.FormatUint(transfer.ID, 10)
			
			// 如果有ID映射，更新server_id
			if newServerID, ok := m.getMappedID("servers", uint64(transfer.ServerID)); ok {
				transfer.ServerID = uint64(newServerID)
			}
			
			err = m.migrateWithRetry(func() error {
				return m.badgerDB.SaveModel("transfer", transfer.ID, &transfer)
			})
			
			if err != nil {
				log.Printf("流量ID %d: 保存到BadgerDB失败: %v", transfer.ID, err)
				progress.Failed++
				continue
			}
			
			progress.Processed++
			progress.Success++
			batchCount++
			
			// 定期更新进度
			if progress.Processed%int64(m.config.ProgressInterval) == 0 {
				m.updateProgress(tableName, progress.Processed, progress.Success, progress.Failed, lastID)
				log.Printf("已处理 %d 条流量记录...", progress.Processed)
			}
		}
		
		rows.Close()
		
		if err := rows.Err(); err != nil {
			return fmt.Errorf("迭代流量行时出错: %w", err)
		}
		
		// 如果没有更多数据，退出循环
		if !hasRows || batchCount == 0 {
			break
		}
		
		// 更新查询以获取下一批数据
		if m.config.EnableResume && lastID != "" {
			// 更新进度
			m.updateProgress(tableName, progress.Processed, progress.Success, progress.Failed, lastID)
			
			// 构建下一批查询
			query = fmt.Sprintf("SELECT * FROM transfers WHERE id > %s ORDER BY id", lastID)
			if m.config.BatchSize > 0 {
				query += fmt.Sprintf(" LIMIT %d", m.config.BatchSize)
			}
		} else {
			// 如果不支持恢复，直接退出
			break
		}
		
		// 短暂休息，避免占用过多资源
		time.Sleep(10 * time.Millisecond)
	}
	
	// 最终更新进度
	m.updateProgress(tableName, progress.Processed, progress.Success, progress.Failed, progress.LastProcessed)
	
	log.Printf("流量记录迁移完成: 成功 %d 条, 失败 %d 条", progress.Success, progress.Failed)
	return nil
}

// migrateApiTokens migrates API tokens from SQLite to BadgerDB
func (m *Migration) migrateApiTokens() error {
	rows, err := m.sqliteDB.Query("SELECT * FROM api_tokens")
	if err != nil {
		return err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return err
	}

	count := 0
	for rows.Next() {
		var apiToken model.ApiToken
		scanValues := getScanValues(columns, &apiToken)
		if err := rows.Scan(scanValues...); err != nil {
			return err
		}

		if err := m.badgerDB.SaveModel("api_token", apiToken.ID, &apiToken); err != nil {
			return err
		}
		count++
	}

	log.Printf("已迁移 %d 条API令牌", count)
	return nil
}

// migrateNATs migrates NATs from SQLite to BadgerDB
func (m *Migration) migrateNATs() error {
	rows, err := m.sqliteDB.Query("SELECT * FROM nats")
	if err != nil {
		return err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return err
	}

	count := 0
	for rows.Next() {
		var nat model.NAT
		scanValues := getScanValues(columns, &nat)
		if err := rows.Scan(scanValues...); err != nil {
			return err
		}

		if err := m.badgerDB.SaveModel("nat", nat.ID, &nat); err != nil {
			return err
		}
		count++
	}

	log.Printf("已迁移 %d 条NAT记录", count)
	return nil
}

// migrateDDNSProfiles migrates DDNS profiles from SQLite to BadgerDB
func (m *Migration) migrateDDNSProfiles() error {
	log.Println("开始迁移DDNS配置数据...")
	rows, err := m.sqliteDB.Query("SELECT * FROM ddns WHERE deleted_at IS NULL")
	if err != nil {
		return fmt.Errorf("查询DDNS配置数据失败: %w", err)
	}
	defer rows.Close()

	count := 0
	errorCount := 0

	for rows.Next() {
		data, err := scanToMap(rows)
		if err != nil {
			log.Printf("扫描DDNS配置行数据失败: %v，跳过", err)
			errorCount++
			continue
		}

		// Extract ID for key
		idVal, ok := data["id"]
		if !ok {
			log.Printf("DDNS配置数据缺少ID字段，跳过: %v", data)
			errorCount++
			continue
		}

		// 确保ID是有效的
		var id uint64
		switch v := idVal.(type) {
		case int64:
			id = uint64(v)
		case float64:
			id = uint64(v)
		case string:
			parsed, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				log.Printf("解析DDNS配置ID '%s' 失败: %v，跳过. Data: %v", v, err, data)
				errorCount++
				continue
			}
			id = parsed
		default:
			log.Printf("DDNS配置ID类型无效: %T，跳过. Data: %v", idVal, data)
			errorCount++
			continue
		}

		if id == 0 {
			log.Printf("DDNS配置ID为0，跳过. Data: %v", data)
			errorCount++
			continue
		}

		log.Printf("迁移DDNS配置 ID %d: 原始数据: %v", id, data)

		// Convert to JSON
		jsonData, err := json.Marshal(data)
		if err != nil {
			log.Printf("DDNS配置ID %d: 序列化数据失败: %v. Data: %v", id, err, data)
			errorCount++
			continue
		}

		// Save to BadgerDB
		key := fmt.Sprintf("ddns_profile:%v", id)
		if err := m.badgerDB.Set(key, jsonData); err != nil {
			log.Printf("DDNS配置ID %d: 保存到BadgerDB失败: %v. Key: '%s'", id, err, key)
			errorCount++
			continue
		}

		count++
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("迭代DDNS配置行时出错: %w", err)
	}

	log.Printf("DDNS配置迁移完成: 成功 %d 条, 失败 %d 条", count, errorCount)
	return nil
}

// migrateDDNSRecordStates migrates DDNS record states from SQLite to BadgerDB
func (m *Migration) migrateDDNSRecordStates() error {
	rows, err := m.sqliteDB.Query("SELECT * FROM ddns_record_states")
	if err != nil {
		return err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return err
	}

	count := 0
	for rows.Next() {
		var state model.DDNSRecordState
		scanValues := getScanValues(columns, &state)
		if err := rows.Scan(scanValues...); err != nil {
			return err
		}

		if err := m.badgerDB.SaveModel("ddns_record_state", state.ID, &state); err != nil {
			return err
		}
		count++
	}

	log.Printf("已迁移 %d 条DDNS记录状态", count)
	return nil
}

// getScanValues 创建一个接收扫描结果的值数组
func getScanValues(columns []string, dest interface{}) []interface{} {
	values := make([]interface{}, len(columns))

	// 获取目标结构体的反射值
	destValue := reflect.ValueOf(dest).Elem()
	destType := destValue.Type()

	// 创建字段映射（字段名 -> 索引）
	fieldMap := make(map[string]int)
	for i := 0; i < destType.NumField(); i++ {
		field := destType.Field(i)
		fieldName := field.Name

		// 检查数据库标记
		dbTag := field.Tag.Get("gorm")
		if dbTag != "" {
			// 提取列名（如果有的话）
			if col := extractColumnName(dbTag); col != "" {
				fieldMap[col] = i
			}
		}

		// 使用字段名作为后备
		fieldMap[camelToSnake(fieldName)] = i
	}

	// 为每一列创建一个接收者
	for i, colName := range columns {
		// 默认为空接口
		var v interface{}
		values[i] = &v

		// 查找匹配的字段
		if fieldIdx, ok := fieldMap[colName]; ok {
			field := destValue.Field(fieldIdx)

			// 创建正确类型的接收者
			switch field.Kind() {
			case reflect.String:
				var s string
				values[i] = &s
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				var n int64
				values[i] = &n
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				var n uint64
				values[i] = &n
			case reflect.Float32, reflect.Float64:
				var f float64
				values[i] = &f
			case reflect.Bool:
				var b bool
				values[i] = &b
			case reflect.Struct:
				// 处理时间类型
				if field.Type() == reflect.TypeOf(time.Time{}) {
					var t time.Time
					values[i] = &t
				}
			}
		}
	}

	return values
}

// extractColumnName 从GORM标签中提取列名
func extractColumnName(tag string) string {
	// 简单实现，实际上可能需要更复杂的解析
	if tag == "-" {
		return ""
	}

	// 检查是否有column:名称
	for _, part := range splitTag(tag) {
		if len(part) >= 8 && part[:7] == "column:" {
			return part[7:]
		}
	}

	return ""
}

// splitTag 分割GORM标签
func splitTag(tag string) []string {
	var parts []string
	var current string
	inQuote := false

	for _, c := range tag {
		if c == ';' && !inQuote {
			parts = append(parts, current)
			current = ""
			continue
		}
		if c == '"' {
			inQuote = !inQuote
		}
		current += string(c)
	}

	if current != "" {
		parts = append(parts, current)
	}

	return parts
}

// camelToSnake 将驼峰命名转换为蛇形命名
func camelToSnake(s string) string {
	var result string
	for i, c := range s {
		if i > 0 && c >= 'A' && c <= 'Z' {
			result += "_"
		}
		result += string(c)
	}
	return result
}

// UpdateModelsAfterScan 根据扫描的值更新模型
func UpdateModelsAfterScan(dest interface{}, columns []string, values []interface{}) error {
	destValue := reflect.ValueOf(dest).Elem()
	destType := destValue.Type()

	// 创建字段映射（列名 -> 字段索引）
	fieldMap := make(map[string]int)
	for i := 0; i < destType.NumField(); i++ {
		field := destType.Field(i)
		fieldName := field.Name

		// 检查数据库标记
		dbTag := field.Tag.Get("gorm")
		if dbTag != "" {
			// 提取列名（如果有的话）
			if col := extractColumnName(dbTag); col != "" {
				fieldMap[col] = i
			}
		}

		// 使用字段名作为后备
		fieldMap[camelToSnake(fieldName)] = i
	}

	// 使用扫描的值更新目标
	for i, colName := range columns {
		if fieldIdx, ok := fieldMap[colName]; ok {
			field := destValue.Field(fieldIdx)
			scanVal := reflect.ValueOf(values[i]).Elem().Interface()

			// 尝试设置字段值
			switch field.Kind() {
			case reflect.String:
				if sv, ok := scanVal.(string); ok {
					field.SetString(sv)
				}
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				if iv, ok := scanVal.(int64); ok {
					field.SetInt(iv)
				}
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				if uv, ok := scanVal.(uint64); ok {
					field.SetUint(uv)
				}
			case reflect.Float32, reflect.Float64:
				if fv, ok := scanVal.(float64); ok {
					field.SetFloat(fv)
				}
			case reflect.Bool:
				if bv, ok := scanVal.(bool); ok {
					field.SetBool(bv)
				}
			case reflect.Struct:
				// 处理时间类型
				if field.Type() == reflect.TypeOf(time.Time{}) {
					if tv, ok := scanVal.(time.Time); ok {
						field.Set(reflect.ValueOf(tv))
					}
				}
			}
		}
	}

	return nil
}

// GenerateNextID 生成下一个ID（简单实现）
func GenerateNextID(modelType string) (uint64, error) {
	// 获取所有该类型的键
	keys, err := DB.GetKeysWithPrefix(modelType + ":")
	if err != nil {
		return 0, err
	}

	var maxID uint64 = 0
	for _, key := range keys {
		// 解析ID
		var id uint64
		_, err := fmt.Sscanf(key, modelType+":%d", &id)
		if err != nil {
			continue
		}

		// 忽略长ID（时间戳ID），只考虑简短ID（小于10000的ID）
		// 这样可以避免基于迁移过来的长ID生成新的长ID
		if id < 10000 && id > maxID {
			maxID = id
		}
	}

	// 如果没有找到简短ID，从1开始
	if maxID == 0 {
		log.Printf("GenerateNextID: 没有找到简短ID，从1开始生成 %s ID", modelType)
		return 1, nil
	}

	log.Printf("GenerateNextID: 找到最大简短ID %d，生成新ID %d (modelType: %s)", maxID, maxID+1, modelType)
	return maxID + 1, nil
}

// InitializeEmptyBadgerDB 初始化一个空的BadgerDB
func InitializeEmptyBadgerDB() error {
	// 检查是否已有用户数据
	var users []*model.User
	err := DB.FindAll("user", &users)
	if err != nil {
		log.Printf("检查现有用户失败: %v，继续创建默认管理员", err)
	} else if len(users) > 0 {
		log.Printf("BadgerDB已有 %d 个用户，跳过创建默认管理员", len(users))
		return nil
	}

	// 只有在没有用户的情况下才创建默认管理员
	log.Println("BadgerDB为空，创建默认管理员用户...")

	adminUser := &model.User{
		Login:        "admin",
		Name:         "Administrator",
		Email:        "admin@example.com",
		Token:        "admin",
		SuperAdmin:   true,
		TokenExpired: time.Now().AddDate(1, 0, 0), // 1年有效期
	}
	adminUser.ID = 1
	adminUser.CreatedAt = time.Now()
	adminUser.UpdatedAt = time.Now()

	if err := DB.SaveModel("user", adminUser.ID, adminUser); err != nil {
		return fmt.Errorf("创建默认管理员用户失败: %w", err)
	}

	log.Println("已创建默认管理员用户（登录名: admin, Token: admin）")
	return nil
}

// RunMigration runs the database migration with default configuration
func RunMigration(badgerDB *BadgerDB, sqlitePath string) error {
	migration, err := NewMigration(badgerDB, sqlitePath)
	if err != nil {
		return err
	}
	defer migration.Close()
	
	return migration.MigrateAll()
}

// RunMigrationWithConfig runs the database migration with custom configuration
func RunMigrationWithConfig(badgerDB *BadgerDB, sqlitePath string, config *MigrationConfig) error {
	migration, err := NewMigrationWithConfig(badgerDB, sqlitePath, config)
	if err != nil {
		return err
	}
	defer migration.Close()
	
	return migration.MigrateAll()
}

// QuickMigrationConfig returns a configuration for quick migration (smaller datasets)
func QuickMigrationConfig() *MigrationConfig {
	return &MigrationConfig{
		BatchSize:            50,
		MonitorHistoryDays:   7,  // Only last 7 days
		MonitorHistoryLimit:  10000,
		ProgressInterval:     100,
		EnableResume:         true,
		MaxRetries:           3,
		RetryDelay:           time.Second,
		Workers:              4,
		SkipLargeHistoryData: true, // Skip large data fields
	}
}

// FullMigrationConfig returns a configuration for full migration (all data)
func FullMigrationConfig() *MigrationConfig {
	return &MigrationConfig{
		BatchSize:            200,
		MonitorHistoryDays:   -1, // All history
		MonitorHistoryLimit:  -1, // No limit
		ProgressInterval:     1000,
		EnableResume:         true,
		MaxRetries:           5,
		RetryDelay:           2 * time.Second,
		Workers:              8,
		SkipLargeHistoryData: false,
	}
}

// 用于解析JSON字段的工具函数
func parseJSONField(data []byte, dest interface{}) error {
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, dest)
}
