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
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/xos/serverstatus/model"
)

// Migration handles data migration from SQLite to BadgerDB
type Migration struct {
	badgerDB *BadgerDB
	sqliteDB *sql.DB
}

// NewMigration creates a new Migration instance
func NewMigration(badgerDB *BadgerDB, sqlitePath string) (*Migration, error) {
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

	return &Migration{
		badgerDB: badgerDB,
		sqliteDB: sqliteDB,
	}, nil
}

// Close closes the migration instance
func (m *Migration) Close() error {
	return m.sqliteDB.Close()
}

// MigrateAll migrates all data from SQLite to BadgerDB
func (m *Migration) MigrateAll() error {
	log.Println("开始数据迁移，从SQLite到BadgerDB...")

	// 迁移表
	tables := []struct {
		name     string
		model    interface{}
		migrator func() error
	}{
		{"servers", &model.Server{}, m.migrateServers},
		{"users", &model.User{}, m.migrateUsers},
		{"monitors", &model.Monitor{}, m.migrateMonitors},
		{"monitor_histories", &model.MonitorHistory{}, m.migrateMonitorHistories},
		{"notifications", &model.Notification{}, m.migrateNotifications},
		{"alert_rules", &model.AlertRule{}, m.migrateAlertRules},
		{"crons", &model.Cron{}, m.migrateCrons},
		{"transfers", &model.Transfer{}, m.migrateTransfers},
		{"api_tokens", &model.ApiToken{}, m.migrateApiTokens},
		{"nats", &model.NAT{}, m.migrateNATs},
		{"ddns", &model.DDNSProfile{}, m.migrateDDNSProfiles},
		{"ddns_record_states", &model.DDNSRecordState{}, m.migrateDDNSRecordStates},
	}

	for _, table := range tables {
		log.Printf("MigrateAll: 准备迁移表 %s...", table.name)
		err := table.migrator()
		if err != nil {
			// 检查是否是表不存在的错误
			if strings.Contains(err.Error(), "no such table") {
				log.Printf("MigrateAll: 表 %s 不存在，跳过迁移", table.name)
				continue
			}
			log.Printf("MigrateAll: 迁移表 %s 失败: %v", table.name, err)
			return fmt.Errorf("failed to migrate %s: %w", table.name, err)
		}
		log.Printf("MigrateAll: 迁移表 %s 完成。", table.name)
	}

	log.Println("所有数据表迁移完成！")
	return nil
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
	log.Println("开始迁移服务器数据...")
	rows, err := m.sqliteDB.Query("SELECT * FROM servers WHERE deleted_at IS NULL")
	if err != nil {
		return fmt.Errorf("查询服务器数据失败: %w", err)
	}
	defer rows.Close()

	count := 0
	errorCount := 0

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

		// 确保正确处理布尔值字段
		boolFields := []string{"is_disabled", "is_online", "hide_for_guest", "show_all", "tasker"}
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
			"last_active": "LastActive",
			"last_online": "LastOnline",
		}

		for sqlField, goField := range timeFieldMapping {
			if val, ok := data[sqlField]; ok {
				var timeStr string
				switch v := val.(type) {
				case time.Time:
					// 如果是 time.Time，转换为 RFC3339 字符串
					timeStr = v.Format(time.RFC3339)
				case string:
					// 如果是字符串，尝试解析并重新格式化
					if parsedTime, err := parseTimeString(v); err == nil {
						timeStr = parsedTime.Format(time.RFC3339)
					} else {
						log.Printf("服务器ID %d: 无法解析时间字段 %s 的值 '%s': %v", id, sqlField, v, err)
						// 对于无法解析的时间，根据字段类型设置默认值
						if sqlField == "created_at" || sqlField == "updated_at" {
							timeStr = time.Now().Format(time.RFC3339)
						} else {
							timeStr = "0001-01-01T00:00:00Z"
						}
					}
				case nil:
					// 对于 nil 值，根据字段类型设置默认值
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

		// 处理其他字段名映射，使用正确的JSON标签或字段名
		fieldMapping := map[string]string{
			"id":              "ID",            // 没有JSON标签，使用字段名
			"name":            "Name",          // 没有JSON标签，使用字段名
			"tag":             "Tag",           // 没有JSON标签，使用字段名
			"display_index":   "DisplayIndex",  // 没有JSON标签，使用字段名
			"hide_for_guest":  "HideForGuest",  // 没有JSON标签，使用字段名
			"enable_ddns":     "EnableDDNS",    // 没有JSON标签，使用字段名
			"host_json":       "HostJSON",      // 有JSON标签 json:"-"，但我们保留字段名用于内部处理
			"last_state_json": "LastStateJSON", // 有JSON标签 json:"-"，但我们保留字段名用于内部处理
		}

		for sqlField, goField := range fieldMapping {
			if val, ok := data[sqlField]; ok {
				delete(data, sqlField)
				data[goField] = val
			}
		}

		// 特殊处理数值字段，确保类型正确，使用JSON标签名
		if val, ok := data["cumulative_net_in_transfer"]; ok {
			switch v := val.(type) {
			case int64:
				data["cumulative_net_in_transfer"] = uint64(v)
			case float64:
				data["cumulative_net_in_transfer"] = uint64(v)
			case string:
				if parsed, err := strconv.ParseUint(v, 10, 64); err == nil {
					data["cumulative_net_in_transfer"] = parsed
				} else {
					data["cumulative_net_in_transfer"] = uint64(0)
				}
			default:
				data["cumulative_net_in_transfer"] = uint64(0)
			}
		}

		if val, ok := data["cumulative_net_out_transfer"]; ok {
			switch v := val.(type) {
			case int64:
				data["cumulative_net_out_transfer"] = uint64(v)
			case float64:
				data["cumulative_net_out_transfer"] = uint64(v)
			case string:
				if parsed, err := strconv.ParseUint(v, 10, 64); err == nil {
					data["cumulative_net_out_transfer"] = parsed
				} else {
					data["cumulative_net_out_transfer"] = uint64(0)
				}
			default:
				data["cumulative_net_out_transfer"] = uint64(0)
			}
		}

		// 特殊处理布尔字段，使用JSON标签名
		if val, ok := data["is_online"]; ok {
			switch v := val.(type) {
			case bool:
				data["is_online"] = v
			case int64:
				data["is_online"] = v != 0
			case float64:
				data["is_online"] = v != 0
			case string:
				data["is_online"] = v == "1" || v == "true" || v == "t"
			default:
				data["is_online"] = false
			}
		}

		log.Printf("迁移服务器 ID %d: 预处理后的数据: %v", id, data)

		// 尝试构建 Server 模型对象
		var server model.Server
		serverJSON, err := json.Marshal(data)
		if err != nil {
			log.Printf("服务器ID %d: 序列化原始数据失败: %v. Data: %v", id, err, data)
			// Fallback to trying to save raw data if model processing fails
		} else {
			if err := json.Unmarshal(serverJSON, &server); err != nil {
				log.Printf("服务器ID %d: 反序列化为Server对象失败: %v. JSON Data: %s", id, err, string(serverJSON))
				// Fallback to trying to save raw data if model processing fails
			} else {
				// 确保ID正确
				server.ID = id

				// 如果服务器名称为空，给一个默认名称
				if server.Name == "" {
					server.Name = fmt.Sprintf("Server-%d", id)
					log.Printf("服务器ID %d: 名称为空，设置为默认名称 '%s'", id, server.Name)
				}

				// 添加额外的日志
				log.Printf("服务器ID %d: 准备迁移的服务器对象: %+v", server.ID, server)

				// 重新序列化为JSON以保存
				serverJSON, err = json.Marshal(server)
				if err != nil {
					log.Printf("服务器ID %d: 重新序列化处理后的Server对象失败: %v. Object: %+v", id, err, server)
					// If re-serialization fails, fall back to using the original data marshalled earlier
					// (or data before attempting to unmarshal to server object if that also failed)
					originalDataJSON, _ := json.Marshal(data) // Marshal the original map again
					serverJSON = originalDataJSON
					log.Printf("服务器ID %d: 回退到使用原始map序列化的JSON进行保存", id)
				}
			}
		}

		if len(serverJSON) == 0 {
			log.Printf("服务器ID %d: serverJSON为空，无法保存. Data: %v", id, data)
			errorCount++
			continue
		}

		// Save to BadgerDB
		key := fmt.Sprintf("server:%v", id) // Ensure correct prefix
		log.Printf("服务器ID %d: 准备保存到BadgerDB. Key: '%s', Value: %s", id, key, string(serverJSON))
		if err := m.badgerDB.Set(key, serverJSON); err != nil {
			log.Printf("服务器ID %d: 保存到BadgerDB失败: %v. Key: '%s'", id, err, key)
			errorCount++
			continue
		}

		log.Printf("服务器ID %d: 成功保存到BadgerDB. Key: '%s'", id, key)
		count++
		if count%10 == 0 {
			log.Printf("已迁移 %d 条服务器记录...", count)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("迭代服务器行时出错: %w", err)
	}

	log.Printf("服务器迁移完成: 成功 %d 条, 失败 %d 条", count, errorCount)
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

		log.Printf("用户ID %d: 成功保存到BadgerDB. Key: '%s'", id, key)
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

		log.Printf("监控器ID %d: 成功保存到BadgerDB. Key: '%s'", id, key)
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
	// For monitor histories, we might want to limit to recent data (e.g., last 30 days)
	cutoffDate := time.Now().AddDate(0, 0, -30)

	// Use parameterized query instead of string formatting to prevent SQL injection
	rows, err := m.sqliteDB.Query("SELECT * FROM monitor_histories WHERE deleted_at IS NULL AND created_at > ? LIMIT 1000", cutoffDate.Format("2006-01-02"))
	if err != nil {
		return err
	}
	defer rows.Close()

	count := 0

	for rows.Next() {
		data, err := scanToMap(rows)
		if err != nil {
			return err
		}

		// Extract ID and timestamps for key
		id, ok := data["id"]
		if !ok {
			continue
		}

		monitorID, _ := data["monitor_id"]
		createdAt, _ := data["created_at"]

		// Convert to JSON
		jsonData, err := json.Marshal(data)
		if err != nil {
			return err
		}

		// Save to BadgerDB with a compound key
		key := fmt.Sprintf("monitor_history:%v:%v:%v", monitorID, createdAt, id)
		if err := m.badgerDB.Set(key, jsonData); err != nil {
			return err
		}

		count++

		// Log progress every 100 records
		if count%100 == 0 {
			log.Printf("已处理 %d 条监控历史记录...", count)
		}
	}

	log.Printf("已迁移 %d 条监控历史记录", count)
	return rows.Err()
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

		log.Printf("通知ID %d: 成功保存到BadgerDB. Key: '%s'", id, key)
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
		key := fmt.Sprintf("alertRule:%v", id)
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
	rows, err := m.sqliteDB.Query("SELECT * FROM transfers")
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
		var transfer model.Transfer
		scanValues := getScanValues(columns, &transfer)
		if err := rows.Scan(scanValues...); err != nil {
			return err
		}

		if err := m.badgerDB.SaveModel("transfer", transfer.ID, &transfer); err != nil {
			return err
		}
		count++
	}

	log.Printf("已迁移 %d 条流量记录", count)
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
	rows, err := m.sqliteDB.Query("SELECT * FROM ddns")
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
		var profile model.DDNSProfile
		scanValues := getScanValues(columns, &profile)
		if err := rows.Scan(scanValues...); err != nil {
			return err
		}

		if err := m.badgerDB.SaveModel("ddns_profile", profile.ID, &profile); err != nil {
			return err
		}
		count++
	}

	log.Printf("已迁移 %d 条DDNS配置", count)
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
		if len(part) > 7 && part[:7] == "column:" {
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

		if id > maxID {
			maxID = id
		}
	}

	return maxID + 1, nil
}

// InitializeEmptyBadgerDB 初始化一个空的BadgerDB
func InitializeEmptyBadgerDB() error {
	// 初始化管理员用户
	adminUser := &model.User{
		Login:      "admin",
		Name:       "Administrator",
		Email:      "admin@example.com",
		Token:      "admin",
		SuperAdmin: true,
	}
	adminUser.ID = 1
	adminUser.CreatedAt = time.Now()
	adminUser.UpdatedAt = time.Now()

	if err := DB.SaveModel("user", adminUser.ID, adminUser); err != nil {
		return err
	}

	log.Println("已创建默认管理员用户（登录名: admin, Token: admin）")
	return nil
}

// 用于解析JSON字段的工具函数
func parseJSONField(data []byte, dest interface{}) error {
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, dest)
}
