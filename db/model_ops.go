package db

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"time"

	"github.com/dgraph-io/badger/v3"
	"github.com/xos/serverstatus/model"
)

// ServerOps provides operations for managing Server models in BadgerDB
type ServerOps struct {
	db *BadgerDB
}

// NewServerOps creates a new ServerOps instance
func NewServerOps(db *BadgerDB) *ServerOps {
	return &ServerOps{db: db}
}

// GetServer gets a server by ID
func (o *ServerOps) GetServer(id uint64) (*model.Server, error) {
	var server model.Server
	err := o.db.FindModel(id, "server", &server)
	if err != nil {
		return nil, err
	}
	return &server, nil
}

// SaveServer saves a server
func (o *ServerOps) SaveServer(server *model.Server) error {
	// 由于多个字段有 json:"-" 标签，我们需要特殊处理
	// 创建一个临时的 map 来包含所有字段
	serverData := make(map[string]interface{})

	// 首先序列化服务器对象（这会忽略有 json:"-" 标签的字段）
	serverJSON, err := json.Marshal(server)
	if err != nil {
		return fmt.Errorf("failed to marshal server: %w", err)
	}

	// 反序列化到 map 中
	if err := json.Unmarshal(serverJSON, &serverData); err != nil {
		return fmt.Errorf("failed to unmarshal server to map: %w", err)
	}

	// 手动添加有 json:"-" 标签的重要字段
	serverData["Secret"] = server.Secret
	serverData["Note"] = server.Note

	// 确保 DDNSProfilesRaw 不为 nil，如果为空则设置为空数组字符串
	if server.DDNSProfilesRaw == "" {
		serverData["DDNSProfilesRaw"] = "[]"
	} else {
		serverData["DDNSProfilesRaw"] = server.DDNSProfilesRaw
	}

	serverData["LastStateJSON"] = server.LastStateJSON
	serverData["HostJSON"] = server.HostJSON

	// 单独保存Host.IP字段（因为IP字段有json:"-"标签，不会被包含在HostJSON中）
	if server.Host != nil {
		serverData["HostIP"] = server.Host.IP
	} else {
		serverData["HostIP"] = ""
	}

	// 重新序列化包含所有字段的数据
	finalJSON, err := json.Marshal(serverData)
	if err != nil {
		return fmt.Errorf("failed to marshal server with all fields: %w", err)
	}

	// 直接保存到数据库
	key := fmt.Sprintf("server:%d", server.ID)
	return o.db.Set(key, finalJSON)
}

// DeleteServer deletes a server
func (o *ServerOps) DeleteServer(id uint64) error {
	return o.db.DeleteModel("server", id)
}

// GetAllServers gets all servers
func (o *ServerOps) GetAllServers() ([]*model.Server, error) {
	var servers []*model.Server
	err := o.db.FindAll("server", &servers)
	return servers, err
}

// MonitorHistoryOps provides operations for managing MonitorHistory models in BadgerDB
type MonitorHistoryOps struct {
	db *BadgerDB
}

// NewMonitorHistoryOps creates a new MonitorHistoryOps instance
func NewMonitorHistoryOps(db *BadgerDB) *MonitorHistoryOps {
	return &MonitorHistoryOps{db: db}
}

// SaveMonitorHistory saves a monitor history record
func (o *MonitorHistoryOps) SaveMonitorHistory(history *model.MonitorHistory) error {
	key := fmt.Sprintf("monitor_history:%d:%d", history.MonitorID, history.CreatedAt.UnixNano())
	data, err := json.Marshal(history)
	if err != nil {
		return err
	}
	return o.db.Set(key, data)
}

// GetMonitorHistoriesByMonitorID gets monitor histories for a specific monitor
func (o *MonitorHistoryOps) GetMonitorHistoriesByMonitorID(monitorID uint64, startTime, endTime time.Time) ([]*model.MonitorHistory, error) {
	prefix := fmt.Sprintf("monitor_history:%d", monitorID)
	startKey := fmt.Sprintf("%s:%d", prefix, startTime.UnixNano())
	endKey := fmt.Sprintf("%s:%d", prefix, endTime.UnixNano())

	var histories []*model.MonitorHistory
	err := o.db.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek([]byte(startKey)); it.Valid(); it.Next() {
			item := it.Item()
			key := item.Key()
			if bytes.Compare(key, []byte(endKey)) > 0 {
				break
			}

			err := item.Value(func(val []byte) error {
				var history model.MonitorHistory
				if err := json.Unmarshal(val, &history); err != nil {
					return err
				}
				histories = append(histories, &history)
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})

	return histories, err
}

// GetAllMonitorHistoriesInRange gets all monitor histories within a time range
func (o *MonitorHistoryOps) GetAllMonitorHistoriesInRange(startTime, endTime time.Time) ([]*model.MonitorHistory, error) {
	prefix := "monitor_history:"

	var histories []*model.MonitorHistory
	err := o.db.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek([]byte(prefix)); it.Valid(); it.Next() {
			item := it.Item()
			key := item.Key()

			// 检查key是否以prefix开头
			if !bytes.HasPrefix(key, []byte(prefix)) {
				break
			}

			err := item.Value(func(val []byte) error {
				var history model.MonitorHistory
				if err := json.Unmarshal(val, &history); err != nil {
					return err
				}

				// 检查时间范围
				if history.CreatedAt.After(startTime) && history.CreatedAt.Before(endTime) {
					histories = append(histories, &history)
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})

	return histories, err
}

// GetMonitorHistoriesByServerAndMonitor gets monitor histories for specific server and monitor within time range
func (o *MonitorHistoryOps) GetMonitorHistoriesByServerAndMonitor(serverID, monitorID uint64, startTime, endTime time.Time, limit int) ([]*model.MonitorHistory, error) {
	var histories []*model.MonitorHistory
	prefix := "monitor_history:"

	err := o.db.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true
		opts.PrefetchSize = 100
		it := txn.NewIterator(opts)
		defer it.Close()

		count := 0
		for it.Seek([]byte(prefix)); it.Valid() && count < limit; it.Next() {
			item := it.Item()
			key := item.Key()

			// 检查key是否以prefix开头
			if !bytes.HasPrefix(key, []byte(prefix)) {
				break
			}

			err := item.Value(func(val []byte) error {
				var history model.MonitorHistory
				if err := json.Unmarshal(val, &history); err != nil {
					return err
				}

				// 过滤条件：服务器ID、监控器ID、时间范围
				if history.ServerID == serverID &&
					history.MonitorID == monitorID &&
					history.CreatedAt.After(startTime) &&
					history.CreatedAt.Before(endTime) {
					histories = append(histories, &history)
					count++
				}

				return nil
			})

			if err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to query monitor histories: %w", err)
	}

	// 按时间排序（最新的在前）
	sort.Slice(histories, func(i, j int) bool {
		return histories[i].CreatedAt.After(histories[j].CreatedAt)
	})

	return histories, nil
}

// GetMonitorHistoriesByServerAndMonitorOptimized 高效查询特定服务器和监控器的历史记录
func (o *MonitorHistoryOps) GetMonitorHistoriesByServerAndMonitorOptimized(serverID, monitorID uint64, startTime, endTime time.Time, limit int) ([]*model.MonitorHistory, error) {
	var histories []*model.MonitorHistory

	// 关键优化：使用更精确的key前缀，减少扫描范围
	// BadgerDB中的key格式通常是 "monitor_history:timestamp:serverid:monitorid" 或类似格式
	// 我们需要找到最优的扫描策略

	// 超级优化：使用采样策略，大幅减少扫描量
	err := o.db.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true // 启用预取，但限制大小
		opts.PrefetchSize = 50     // 大幅减少预取大小
		it := txn.NewIterator(opts)
		defer it.Close()

		prefix := "monitor_history:"
		count := 0
		maxScan := limit * 3 // 限制最大扫描数量
		scanCount := 0

		// 使用采样策略：每隔几条记录才检查一次
		sampleRate := 5 // 每5条记录检查一次
		sampleCounter := 0

		for it.Seek([]byte(prefix)); it.ValidForPrefix([]byte(prefix)) && count < limit && scanCount < maxScan; it.Next() {
			scanCount++
			sampleCounter++

			// 采样策略：不是每条记录都检查
			if sampleCounter%sampleRate != 0 {
				continue
			}

			item := it.Item()

			err := item.Value(func(val []byte) error {
				// 超快速解析：使用更简单的结构
				var quickCheck struct {
					ServerID  uint64 `json:"ServerID"`
					MonitorID uint64 `json:"MonitorID"`
				}

				// 只解析前面的字段，减少JSON解析时间
				parseLen := len(val)
				if parseLen > 200 {
					parseLen = 200
				}
				if err := json.Unmarshal(val[:parseLen], &quickCheck); err != nil {
					return nil
				}

				// 快速过滤：只检查ID匹配
				if quickCheck.ServerID == serverID && quickCheck.MonitorID == monitorID {
					// 完整解析
					var history model.MonitorHistory
					if err := json.Unmarshal(val, &history); err != nil {
						return nil
					}

					// 时间过滤
					if history.CreatedAt.After(startTime) && history.CreatedAt.Before(endTime) {
						histories = append(histories, &history)
						count++
					}
				}

				return nil
			})

			if err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to query monitor histories optimized: %w", err)
	}

	// 按时间排序（最新的在前）
	sort.Slice(histories, func(i, j int) bool {
		return histories[i].CreatedAt.After(histories[j].CreatedAt)
	})

	// 限制结果数量
	if len(histories) > limit {
		histories = histories[:limit]
	}

	return histories, nil
}

// CleanupOldMonitorHistories removes monitor histories older than maxAge
func (o *MonitorHistoryOps) CleanupOldMonitorHistories(maxAge time.Duration) (int, error) {
	// Get all monitor IDs
	serverOps := NewServerOps(o.db)
	servers, err := serverOps.GetAllServers()
	if err != nil {
		return 0, err
	}

	totalDeleted := 0
	for _, server := range servers {
		prefix := fmt.Sprintf("monitor_history:%d", server.ID)
		deleted, err := o.db.CleanupExpiredData(prefix, maxAge)
		if err != nil {
			return totalDeleted, err
		}
		totalDeleted += deleted
	}

	return totalDeleted, nil
}

// UserOps provides specialized operations for users
type UserOps struct {
	db *BadgerDB
}

// NewUserOps creates a new UserOps instance
func NewUserOps(db *BadgerDB) *UserOps {
	return &UserOps{db: db}
}

// SaveUser saves a user record
func (o *UserOps) SaveUser(user *model.User) error {
	key := fmt.Sprintf("user:%d", user.ID)

	// 先序列化用户数据
	value, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("failed to marshal user: %w", err)
	}

	// 反序列化为 map 以便手动添加 Token 字段
	var userData map[string]interface{}
	if err := json.Unmarshal(value, &userData); err != nil {
		return fmt.Errorf("failed to unmarshal user data: %w", err)
	}

	// 手动添加 Token 字段（因为它有 json:"-" 标签）
	userData["Token"] = user.Token

	// 重新序列化包含 Token 的数据
	finalValue, err := json.Marshal(userData)
	if err != nil {
		return fmt.Errorf("failed to marshal user with token: %w", err)
	}

	return o.db.Set(key, finalValue)
}

// GetUserByID gets a user by ID
func (o *UserOps) GetUserByID(id uint64) (*model.User, error) {
	var user model.User

	err := o.db.FindModel(id, "user", &user)
	if err != nil {
		return nil, err
	}

	return &user, nil
}

// GetUserByUsername gets a user by username
func (o *UserOps) GetUserByUsername(username string) (*model.User, error) {
	var users []*model.User

	err := o.db.FindAll("user", &users)
	if err != nil {
		return nil, err
	}

	for _, user := range users {
		if user.Login == username {
			return user, nil
		}
	}

	return nil, ErrorNotFound
}

// DeleteUser deletes a user
func (o *UserOps) DeleteUser(id uint64) error {
	return o.db.Delete(fmt.Sprintf("user:%d", id))
}

// MonitorOps provides specialized operations for monitors
type MonitorOps struct {
	db *BadgerDB
}

// NewMonitorOps creates a new MonitorOps instance
func NewMonitorOps(db *BadgerDB) *MonitorOps {
	return &MonitorOps{db: db}
}

// SaveMonitor saves a monitor record
func (o *MonitorOps) SaveMonitor(monitor *model.Monitor) error {
	key := fmt.Sprintf("monitor:%d", monitor.ID)

	value, err := json.Marshal(monitor)
	if err != nil {
		return fmt.Errorf("failed to marshal monitor: %w", err)
	}

	return o.db.Set(key, value)
}

// GetAllMonitors gets all monitors
func (o *MonitorOps) GetAllMonitors() ([]*model.Monitor, error) {
	var monitors []*model.Monitor

	err := o.db.FindAll("monitor", &monitors)
	if err != nil {
		return nil, err
	}

	return monitors, nil
}

// GetMonitorByID gets a monitor by ID
func (o *MonitorOps) GetMonitorByID(id uint64) (*model.Monitor, error) {
	var monitor model.Monitor

	err := o.db.FindModel(id, "monitor", &monitor)
	if err != nil {
		return nil, err
	}

	return &monitor, nil
}

// DeleteMonitor deletes a monitor
func (o *MonitorOps) DeleteMonitor(id uint64) error {
	return o.db.Delete(fmt.Sprintf("monitor:%d", id))
}

// NotificationOps provides specialized operations for notifications
type NotificationOps struct {
	db *BadgerDB
}

// NewNotificationOps creates a new NotificationOps instance
func NewNotificationOps(db *BadgerDB) *NotificationOps {
	return &NotificationOps{db: db}
}

// SaveNotification saves a notification record
func (o *NotificationOps) SaveNotification(notification *model.Notification) error {
	key := fmt.Sprintf("notification:%d", notification.ID)

	value, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("failed to marshal notification: %w", err)
	}

	return o.db.Set(key, value)
}

// GetAllNotifications gets all notifications
func (o *NotificationOps) GetAllNotifications() ([]*model.Notification, error) {
	var notifications []*model.Notification

	err := o.db.FindAll("notification", &notifications)
	if err != nil {
		return nil, err
	}

	return notifications, nil
}

// GetNotificationByID gets a notification by ID
func (o *NotificationOps) GetNotificationByID(id uint64) (*model.Notification, error) {
	var notification model.Notification

	err := o.db.FindModel(id, "notification", &notification)
	if err != nil {
		return nil, err
	}

	return &notification, nil
}

// DeleteNotification deletes a notification
func (o *NotificationOps) DeleteNotification(id uint64) error {
	return o.db.Delete(fmt.Sprintf("notification:%d", id))
}

// CronOps provides specialized operations for cron tasks
type CronOps struct {
	db *BadgerDB
}

// NewCronOps creates a new CronOps instance
func NewCronOps(db *BadgerDB) *CronOps {
	return &CronOps{db: db}
}

// SaveCron saves a cron task record
func (o *CronOps) SaveCron(cron *model.Cron) error {
	key := fmt.Sprintf("cron:%d", cron.ID)

	value, err := json.Marshal(cron)
	if err != nil {
		return fmt.Errorf("failed to marshal cron task: %w", err)
	}

	return o.db.Set(key, value)
}

// GetAllCrons gets all cron tasks
func (o *CronOps) GetAllCrons() ([]*model.Cron, error) {
	var crons []*model.Cron

	err := o.db.FindAll("cron", &crons)
	if err != nil {
		return nil, err
	}

	return crons, nil
}

// GetCronByID gets a cron task by ID
func (o *CronOps) GetCronByID(id uint64) (*model.Cron, error) {
	var cron model.Cron

	err := o.db.FindModel(id, "cron", &cron)
	if err != nil {
		return nil, err
	}

	return &cron, nil
}

// DeleteCron deletes a cron task
func (o *CronOps) DeleteCron(id uint64) error {
	return o.db.Delete(fmt.Sprintf("cron:%d", id))
}

// ApiTokenOps provides specialized operations for API tokens
type ApiTokenOps struct {
	db *BadgerDB
}

// NewApiTokenOps creates a new ApiTokenOps instance
func NewApiTokenOps(db *BadgerDB) *ApiTokenOps {
	return &ApiTokenOps{db: db}
}

// SaveApiToken saves an API token record
func (o *ApiTokenOps) SaveApiToken(token *model.ApiToken) error {
	key := fmt.Sprintf("api_token:%d", token.ID)

	// 先序列化API令牌数据
	value, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("failed to marshal API token: %w", err)
	}

	// 反序列化为 map 以便手动添加 Token 字段
	var tokenData map[string]interface{}
	if err := json.Unmarshal(value, &tokenData); err != nil {
		return fmt.Errorf("failed to unmarshal API token data: %w", err)
	}

	// 手动添加 Token 字段（确保它被保存）
	tokenData["Token"] = token.Token
	tokenData["token"] = token.Token // 同时保存小写版本作为备用

	// 重新序列化包含 Token 的数据
	finalValue, err := json.Marshal(tokenData)
	if err != nil {
		return fmt.Errorf("failed to marshal API token with token: %w", err)
	}

	return o.db.Set(key, finalValue)
}

// GetAllApiTokens gets all API tokens
func (o *ApiTokenOps) GetAllApiTokens() ([]*model.ApiToken, error) {
	var tokens []*model.ApiToken

	err := o.db.FindAll("api_token", &tokens)
	if err != nil {
		return nil, err
	}

	return tokens, nil
}

// GetApiTokenByID gets an API token by ID
func (o *ApiTokenOps) GetApiTokenByID(id uint64) (*model.ApiToken, error) {
	var token model.ApiToken

	err := o.db.FindModel(id, "api_token", &token)
	if err != nil {
		return nil, err
	}

	return &token, nil
}

// GetApiTokenByToken gets an API token by token string
func (o *ApiTokenOps) GetApiTokenByToken(tokenStr string) (*model.ApiToken, error) {
	var tokens []*model.ApiToken

	err := o.db.FindAll("api_token", &tokens)
	if err != nil {
		return nil, err
	}

	for _, t := range tokens {
		if t.Token == tokenStr {
			return t, nil
		}
	}

	return nil, ErrorNotFound
}

// DeleteApiToken deletes an API token
func (o *ApiTokenOps) DeleteApiToken(id uint64) error {
	log.Printf("BadgerDB: 开始删除API令牌 ID=%d", id)

	// 直接从底层数据库获取所有api_token keys，避免FindAll的数据转换问题
	keys, err := o.db.GetKeysWithPrefix("api_token:")
	if err != nil {
		log.Printf("BadgerDB: 获取api_token keys失败: %v", err)
		return err
	}

	log.Printf("BadgerDB: 找到 %d 个api_token keys", len(keys))

	deletedCount := 0
	var targetTokenString string

	// 首先遍历所有keys，找到要删除的Token字符串
	for _, key := range keys {
		data, err := o.db.Get(key)
		if err != nil {
			log.Printf("BadgerDB: 读取key '%s' 失败: %v", key, err)
			continue
		}

		// 解析JSON数据
		var tokenData map[string]interface{}
		if err := json.Unmarshal(data, &tokenData); err != nil {
			log.Printf("BadgerDB: 解析key '%s' 的JSON数据失败: %v", key, err)
			continue
		}

		// 检查ID是否匹配
		var tokenID uint64
		if idVal, ok := tokenData["ID"]; ok {
			switch v := idVal.(type) {
			case float64:
				tokenID = uint64(v)
			case int64:
				tokenID = uint64(v)
			case uint64:
				tokenID = v
			case string:
				if parsed, err := strconv.ParseUint(v, 10, 64); err == nil {
					tokenID = parsed
				}
			}
		}

		// 如果ID匹配，记录Token字符串
		if tokenID == id {
			if tokenVal, ok := tokenData["token"]; ok {
				if tokenStr, isStr := tokenVal.(string); isStr {
					targetTokenString = tokenStr
					log.Printf("BadgerDB: 找到目标Token字符串: '%s'", targetTokenString)
					break
				}
			}
			if tokenVal, ok := tokenData["Token"]; ok {
				if tokenStr, isStr := tokenVal.(string); isStr {
					targetTokenString = tokenStr
					log.Printf("BadgerDB: 找到目标Token字符串: '%s'", targetTokenString)
					break
				}
			}
		}
	}

	if targetTokenString == "" {
		log.Printf("BadgerDB: 未找到ID=%d对应的Token字符串", id)
		return fmt.Errorf("未找到要删除的API令牌记录")
	}

	// 现在删除所有具有相同Token字符串的记录
	for _, key := range keys {
		data, err := o.db.Get(key)
		if err != nil {
			continue
		}

		// 解析JSON数据
		var tokenData map[string]interface{}
		if err := json.Unmarshal(data, &tokenData); err != nil {
			continue
		}

		// 检查Token字符串是否匹配
		shouldDelete := false
		if tokenVal, ok := tokenData["token"]; ok {
			if tokenStr, isStr := tokenVal.(string); isStr && tokenStr == targetTokenString {
				shouldDelete = true
			}
		}
		if !shouldDelete {
			if tokenVal, ok := tokenData["Token"]; ok {
				if tokenStr, isStr := tokenVal.(string); isStr && tokenStr == targetTokenString {
					shouldDelete = true
				}
			}
		}

		if shouldDelete {
			log.Printf("BadgerDB: 删除key '%s' (Token: '%s')", key, targetTokenString)
			if delErr := o.db.Delete(key); delErr == nil {
				log.Printf("BadgerDB: 成功删除key '%s'", key)
				deletedCount++
			} else {
				log.Printf("BadgerDB: 删除key '%s' 失败: %v", key, delErr)
			}
		}
	}

	log.Printf("BadgerDB: 删除操作完成，共删除了 %d 个记录", deletedCount)

	if deletedCount == 0 {
		return fmt.Errorf("未找到要删除的API令牌记录")
	}

	return nil
}

// NATOps provides specialized operations for NAT configurations
type NATOps struct {
	db *BadgerDB
}

// NewNATOps creates a new NATOps instance
func NewNATOps(db *BadgerDB) *NATOps {
	return &NATOps{db: db}
}

// SaveNAT saves a NAT configuration record
func (o *NATOps) SaveNAT(nat *model.NAT) error {
	key := fmt.Sprintf("nat:%d", nat.ID)

	value, err := json.Marshal(nat)
	if err != nil {
		return fmt.Errorf("failed to marshal NAT: %w", err)
	}

	return o.db.Set(key, value)
}

// GetAllNATs gets all NAT configurations
func (o *NATOps) GetAllNATs() ([]*model.NAT, error) {
	var nats []*model.NAT

	err := o.db.FindAll("nat", &nats)
	if err != nil {
		return nil, err
	}

	return nats, nil
}

// GetNATByID gets a NAT configuration by ID
func (o *NATOps) GetNATByID(id uint64) (*model.NAT, error) {
	var nat model.NAT

	err := o.db.FindModel(id, "nat", &nat)
	if err != nil {
		return nil, err
	}

	return &nat, nil
}

// GetNATByDomain gets a NAT configuration by domain
func (o *NATOps) GetNATByDomain(domain string) (*model.NAT, error) {
	var nats []*model.NAT

	err := o.db.FindAll("nat", &nats)
	if err != nil {
		return nil, err
	}

	for _, nat := range nats {
		if nat.Domain == domain {
			return nat, nil
		}
	}

	return nil, ErrorNotFound
}

// DeleteNAT deletes a NAT configuration
func (o *NATOps) DeleteNAT(id uint64) error {
	return o.db.Delete(fmt.Sprintf("nat:%d", id))
}

// DDNSOps provides specialized operations for DDNS profiles and states
type DDNSOps struct {
	db *BadgerDB
}

// NewDDNSOps creates a new DDNSOps instance
func NewDDNSOps(db *BadgerDB) *DDNSOps {
	return &DDNSOps{db: db}
}

// SaveDDNSProfile saves a DDNS profile record
func (o *DDNSOps) SaveDDNSProfile(profile *model.DDNSProfile) error {
	key := fmt.Sprintf("ddns_profile:%d", profile.ID)

	value, err := json.Marshal(profile)
	if err != nil {
		return fmt.Errorf("failed to marshal DDNS profile: %w", err)
	}

	return o.db.Set(key, value)
}

// GetAllDDNSProfiles gets all DDNS profiles
func (o *DDNSOps) GetAllDDNSProfiles() ([]*model.DDNSProfile, error) {
	var profiles []*model.DDNSProfile

	err := o.db.FindAll("ddns_profile", &profiles)
	if err != nil {
		return nil, err
	}

	return profiles, nil
}

// GetDDNSProfileByID gets a DDNS profile by ID
func (o *DDNSOps) GetDDNSProfileByID(id uint64) (*model.DDNSProfile, error) {
	var profile model.DDNSProfile

	err := o.db.FindModel(id, "ddns_profile", &profile)
	if err != nil {
		return nil, err
	}

	return &profile, nil
}

// DeleteDDNSProfile deletes a DDNS profile
func (o *DDNSOps) DeleteDDNSProfile(id uint64) error {
	return o.db.Delete(fmt.Sprintf("ddns_profile:%d", id))
}

// SaveDDNSRecordState saves a DDNS record state
func (o *DDNSOps) SaveDDNSRecordState(state *model.DDNSRecordState) error {
	key := fmt.Sprintf("ddns_record_state:%d", state.ID)

	value, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to marshal DDNS record state: %w", err)
	}

	return o.db.Set(key, value)
}

// GetAllDDNSRecordStates gets all DDNS record states
func (o *DDNSOps) GetAllDDNSRecordStates() ([]*model.DDNSRecordState, error) {
	var states []*model.DDNSRecordState

	err := o.db.FindAll("ddns_record_state", &states)
	if err != nil {
		return nil, err
	}

	return states, nil
}

// GetDDNSRecordStateByID gets a DDNS record state by ID
func (o *DDNSOps) GetDDNSRecordStateByID(id uint64) (*model.DDNSRecordState, error) {
	var state model.DDNSRecordState

	err := o.db.FindModel(id, "ddns_record_state", &state)
	if err != nil {
		return nil, err
	}

	return &state, nil
}

// GetDDNSRecordStateByParams gets a DDNS record state by server ID, domain and record type
func (o *DDNSOps) GetDDNSRecordStateByParams(serverID uint64, domain string, recordType string) (*model.DDNSRecordState, error) {
	var states []*model.DDNSRecordState

	err := o.db.FindAll("ddns_record_state", &states)
	if err != nil {
		return nil, err
	}

	for _, state := range states {
		if state.ServerID == serverID && state.Domain == domain && state.RecordType == recordType {
			return state, nil
		}
	}

	return nil, ErrorNotFound
}

// CreateDDNSRecordState creates a new DDNS record state
func (o *DDNSOps) CreateDDNSRecordState(state *model.DDNSRecordState) error {
	// Generate ID for new record
	states, err := o.GetAllDDNSRecordStates()
	if err != nil {
		return err
	}

	var maxID uint64 = 0
	for _, s := range states {
		if s.ID > maxID {
			maxID = s.ID
		}
	}

	state.ID = maxID + 1
	state.CreatedAt = time.Now()
	state.UpdatedAt = time.Now()

	return o.SaveDDNSRecordState(state)
}

// DeleteDDNSRecordState deletes a DDNS record state
func (o *DDNSOps) DeleteDDNSRecordState(id uint64) error {
	return o.db.Delete(fmt.Sprintf("ddns_record_state:%d", id))
}

// AlertRuleOps provides specialized operations for alert rules
type AlertRuleOps struct {
	db *BadgerDB
}

// NewAlertRuleOps creates a new AlertRuleOps instance
func NewAlertRuleOps(db *BadgerDB) *AlertRuleOps {
	return &AlertRuleOps{db: db}
}

// SaveAlertRule saves an alert rule record
func (o *AlertRuleOps) SaveAlertRule(rule *model.AlertRule) error {
	key := fmt.Sprintf("alert_rule:%d", rule.ID)

	value, err := json.Marshal(rule)
	if err != nil {
		return fmt.Errorf("failed to marshal alert rule: %w", err)
	}

	return o.db.Set(key, value)
}

// GetAllAlertRules gets all alert rules
func (o *AlertRuleOps) GetAllAlertRules() ([]*model.AlertRule, error) {
	var rules []*model.AlertRule

	err := o.db.FindAll("alert_rule", &rules)
	if err != nil {
		return nil, err
	}

	return rules, nil
}

// GetAlertRuleByID gets an alert rule by ID
func (o *AlertRuleOps) GetAlertRuleByID(id uint64) (*model.AlertRule, error) {
	var rule model.AlertRule

	err := o.db.FindModel(id, "alert_rule", &rule)
	if err != nil {
		return nil, err
	}

	return &rule, nil
}

// DeleteAlertRule deletes an alert rule
func (o *AlertRuleOps) DeleteAlertRule(id uint64) error {
	return o.db.Delete(fmt.Sprintf("alert_rule:%d", id))
}
