package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v3"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/utils"
)

// Global variables
var (
	DB            *BadgerDB
	globalBadger  *badger.DB
	globalContext context.Context
	globalCancel  context.CancelFunc
)

// ErrorNotFound is returned when a key is not found
var ErrorNotFound = errors.New("key not found")

// BadgerDB provides an implementation of the database interface using BadgerDB
type BadgerDB struct {
	db      *badger.DB
	ctx     context.Context
	cancel  context.CancelFunc
	rwMutex sync.RWMutex
}

// OpenDB opens a BadgerDB database at the given path
func OpenDB(path string) (*BadgerDB, error) {
	// Ensure directory exists
	if err := os.MkdirAll(path, 0750); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	// Configure BadgerDB
	options := badger.DefaultOptions(path).
		WithLoggingLevel(badger.INFO).
		WithValueLogFileSize(64 << 20). // 64MB
		WithNumVersionsToKeep(1)

	// Open database
	db, err := badger.Open(options)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Create context for database operations
	ctx, cancel := context.WithCancel(context.Background())

	// Create and return BadgerDB instance
	badgerDB := &BadgerDB{
		db:     db,
		ctx:    ctx,
		cancel: cancel,
	}

	// Start background maintenance tasks
	badgerDB.startMaintenance()

	// Set global variables
	globalBadger = db
	globalContext = ctx
	globalCancel = cancel
	DB = badgerDB

	return badgerDB, nil
}

// Close closes the database
func (b *BadgerDB) Close() error {
	b.cancel()
	return b.db.Close()
}

// startMaintenance starts background maintenance tasks
func (b *BadgerDB) startMaintenance() {
	// 根本修复：确保只启动一个维护goroutine，并且能正确退出
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("BadgerDB maintenance panic恢复: %v", r)
				// 不再自动重启，避免goroutine泄漏
			}
		}()

		// 增加GC间隔，减少资源占用
		ticker := time.NewTicker(15 * time.Minute) // 从5分钟增加到15分钟
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				// 只在必要时执行GC
				err := b.db.RunValueLogGC(0.7) // 提高阈值，减少GC频率
				if err != nil && err != badger.ErrNoRewrite {
					log.Printf("Value log GC failed: %v", err)
				}
			case <-b.ctx.Done():
				log.Printf("BadgerDB maintenance goroutine正常退出")
				return
			}
		}
	}()
}

// SaveModel saves a model to the database
func (b *BadgerDB) SaveModel(modelType string, id uint64, model interface{}) error {
	key := fmt.Sprintf("%s:%d", modelType, id)

	value, err := json.Marshal(model)
	if err != nil {
		return fmt.Errorf("failed to marshal model: %w", err)
	}

	return b.Set(key, value)
}

// FindModel finds a model by ID
func (b *BadgerDB) FindModel(id uint64, modelType string, result interface{}) error {
	key := fmt.Sprintf("%s:%d", modelType, id)
	data, err := b.Get(key)
	if err != nil {
		return err
	}

	// 针对不同的数据类型进行特殊处理
	switch modelType {
	case "user":
		// 用户记录需要特殊处理Token字段（有json:"-"标签）
		var userData map[string]interface{}
		if err := json.Unmarshal(data, &userData); err != nil {
			return err
		}

		// 转换字段类型，确保布尔字段正确
		convertDbFieldTypes(&userData)

		// 重新序列化为 JSON
		userJSON, err := json.Marshal(userData)
		if err != nil {
			return err
		}

		// 反序列化到结果
		if err := json.Unmarshal(userJSON, result); err != nil {
			return err
		}

		// 手动设置Token字段（因为它有json:"-"标签）
		if user, ok := result.(*model.User); ok {
			if token, exists := userData["Token"]; exists {
				if tokenStr, isStr := token.(string); isStr {
					user.Token = tokenStr
				}
			}
		}

		return nil
	case "server":
		// 服务器记录需要特殊处理Secret、HostJSON、LastStateJSON和DDNSProfilesRaw字段（有json:"-"标签）
		var serverData map[string]interface{}
		if err := json.Unmarshal(data, &serverData); err != nil {
			return err
		}

		// 转换字段类型，确保字段正确
		convertDbFieldTypes(&serverData)

		// 重新序列化为 JSON
		serverJSON, err := json.Marshal(serverData)
		if err != nil {
			return err
		}

		// 反序列化到结果
		if err := json.Unmarshal(serverJSON, result); err != nil {
			return err
		}

		// 手动设置有json:"-"标签的字段
		if server, ok := result.(*model.Server); ok {
			if hostJSON, exists := serverData["HostJSON"]; exists {
				if hostJSONStr, isStr := hostJSON.(string); isStr && hostJSONStr != "" {
					server.HostJSON = hostJSONStr
				}
			}
			if lastStateJSON, exists := serverData["LastStateJSON"]; exists {
				if lastStateJSONStr, isStr := lastStateJSON.(string); isStr && lastStateJSONStr != "" {
					server.LastStateJSON = lastStateJSONStr
				}
			}
			if secret, exists := serverData["Secret"]; exists {
				if secretStr, isStr := secret.(string); isStr {
					server.Secret = secretStr
				}
			}
			if note, exists := serverData["Note"]; exists {
				if noteStr, isStr := note.(string); isStr {
					server.Note = noteStr
				}
			}
			// 恢复Host.IP字段（因为IP字段有json:"-"标签，不会被包含在HostJSON中）
			if hostIP, exists := serverData["HostIP"]; exists {
				if hostIPStr, isStr := hostIP.(string); isStr && hostIPStr != "" {
					if server.Host != nil {
						server.Host.IP = hostIPStr
					}
				}
			}

			// 设置 DDNSProfilesRaw 字段
			if ddnsRaw, exists := serverData["DDNSProfilesRaw"]; exists {
				if ddnsStr, isStr := ddnsRaw.(string); isStr && ddnsStr != "" {
					server.DDNSProfilesRaw = ddnsStr
					// 同时解析到 DDNSProfiles 字段
					if err := utils.Json.Unmarshal([]byte(ddnsStr), &server.DDNSProfiles); err != nil {
						log.Printf("解析服务器 %d 的DDNSProfiles失败: %v", server.ID, err)
						server.DDNSProfiles = []uint64{}
					}
				} else {
					// 处理 nil 或空字符串的情况
					server.DDNSProfilesRaw = "[]"
					server.DDNSProfiles = []uint64{}
				}
			} else {
				// 字段不存在，设置默认值
				server.DDNSProfilesRaw = "[]"
				server.DDNSProfiles = []uint64{}
			}
		}

		return nil
	case "alert_rule":
		// 报警规则需要特殊处理布尔字段
		var ruleData map[string]interface{}
		if err := json.Unmarshal(data, &ruleData); err != nil {
			return err
		}

		// 转换字段类型，确保布尔字段正确
		convertDbFieldTypes(&ruleData)

		// 重新序列化为 JSON
		ruleJSON, err := json.Marshal(ruleData)
		if err != nil {
			return err
		}

		// 反序列化到结果
		return json.Unmarshal(ruleJSON, result)
	default:
		// 其他类型的记录，也需要进行字段类型转换
		var dataMap map[string]interface{}
		if err := json.Unmarshal(data, &dataMap); err != nil {
			return err
		}

		// 转换字段类型，确保布尔字段正确
		convertDbFieldTypes(&dataMap)

		// 重新序列化为 JSON
		dataJSON, err := json.Marshal(dataMap)
		if err != nil {
			return err
		}

		// 反序列化到结果
		return json.Unmarshal(dataJSON, result)
	}
}

// DeleteModel deletes a model from the database
func (b *BadgerDB) DeleteModel(modelType string, id uint64) error {
	key := fmt.Sprintf("%s:%d", modelType, id)
	return b.Delete(key)
}

// Set sets a key-value pair in the database
func (b *BadgerDB) Set(key string, value []byte) error {
	b.rwMutex.Lock()
	defer b.rwMutex.Unlock()

	return b.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(key), value)
	})
}

// Get retrieves a value from the database
func (b *BadgerDB) Get(key string) ([]byte, error) {
	b.rwMutex.RLock()
	defer b.rwMutex.RUnlock()

	var valCopy []byte
	err := b.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}

		return item.Value(func(val []byte) error {
			valCopy = append([]byte{}, val...)
			return nil
		})
	})

	if err == badger.ErrKeyNotFound {
		return nil, ErrorNotFound
	}

	return valCopy, err
}

// Delete deletes a key from the database
func (b *BadgerDB) Delete(key string) error {
	b.rwMutex.Lock()
	defer b.rwMutex.Unlock()

	return b.db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(key))
	})
}

// FindAll retrieves all items with a specific prefix
func (b *BadgerDB) FindAll(prefix string, result interface{}) error {
	b.rwMutex.RLock()
	defer b.rwMutex.RUnlock()

	prefixBytes := []byte(prefix + ":")
	items := [][]byte{}

	if b.db == nil {
		log.Printf("FindAll: BadgerDB实例未初始化，返回空结果")
		// 返回空数组结果而不是错误
		return json.Unmarshal([]byte("[]"), result)
	}

	// 移除频繁的查询日志输出，只在调试模式下输出
	// log.Printf("FindAll: 查询前缀 %s 的数据", prefix)

	err := b.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefixBytes); it.ValidForPrefix(prefixBytes); it.Next() {
			item := it.Item()
			err := item.Value(func(val []byte) error {
				items = append(items, append([]byte{}, val...))
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})

	if err != nil {
		log.Printf("FindAll: BadgerDB查询错误: %v", err)
		return err
	}

	// 如果没有找到任何记录，返回空数组
	if len(items) == 0 {
		return json.Unmarshal([]byte("[]"), result)
	}

	// 针对不同的数据类型进行特殊处理
	switch prefix {
	case "server":
		// 服务器记录可能需要特殊处理，并且需要去重
		var servers []*map[string]interface{}
		seenIDs := make(map[uint64]bool) // 用于去重

		for _, item := range items {
			var data map[string]interface{}
			if err := json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保 JSON 字段正确
			convertDbFieldTypes(&data)

			// 检查是否重复ID
			if idVal, exists := data["ID"]; exists {
				if id, ok := idVal.(uint64); ok {
					if seenIDs[id] {
						log.Printf("FindAll: 跳过重复的服务器ID %d", id)
						continue // 跳过重复的ID
					}
					seenIDs[id] = true
				}
			}

			servers = append(servers, &data)
		}

		log.Printf("FindAll: 去重后保留 %d 个服务器记录", len(servers))

		// 重新序列化为 JSON
		serversJSON, err := json.Marshal(servers)
		if err != nil {
			return err
		}

		// 先反序列化到结果
		if err := json.Unmarshal(serversJSON, result); err != nil {
			return err
		}

		// 由于HostJSON、LastStateJSON和Secret字段有json:"-"标签，需要手动设置这些字段
		if serverSlice, ok := result.(*[]*model.Server); ok {
			for i, server := range *serverSlice {
				if i < len(servers) {
					serverData := *servers[i]
					if hostJSON, exists := serverData["HostJSON"]; exists {
						if hostJSONStr, isStr := hostJSON.(string); isStr && hostJSONStr != "" {
							server.HostJSON = hostJSONStr
						}
					}
					if lastStateJSON, exists := serverData["LastStateJSON"]; exists {
						if lastStateJSONStr, isStr := lastStateJSON.(string); isStr && lastStateJSONStr != "" {
							server.LastStateJSON = lastStateJSONStr
						}
					}
					// 手动设置 Secret 字段
					if secret, exists := serverData["Secret"]; exists {
						if secretStr, isStr := secret.(string); isStr {
							server.Secret = secretStr
						}
					}
					// 手动设置 Note 字段
					if note, exists := serverData["Note"]; exists {
						if noteStr, isStr := note.(string); isStr {
							server.Note = noteStr
						}
					}
					// 恢复Host.IP字段（因为IP字段有json:"-"标签，不会被包含在HostJSON中）
					if hostIP, exists := serverData["HostIP"]; exists {
						if hostIPStr, isStr := hostIP.(string); isStr && hostIPStr != "" {
							if server.Host != nil {
								server.Host.IP = hostIPStr
							}
						}
					}

					// 设置 DDNSProfilesRaw 字段
					if ddnsRaw, exists := serverData["DDNSProfilesRaw"]; exists {
						if ddnsStr, isStr := ddnsRaw.(string); isStr && ddnsStr != "" {
							server.DDNSProfilesRaw = ddnsStr
							// 同时解析到 DDNSProfiles 字段
							if err := utils.Json.Unmarshal([]byte(ddnsStr), &server.DDNSProfiles); err != nil {
								log.Printf("解析服务器 %d 的DDNSProfiles失败: %v", server.ID, err)
								server.DDNSProfiles = []uint64{}
							}
						} else {
							// 处理 nil 或空字符串的情况
							server.DDNSProfilesRaw = "[]"
							server.DDNSProfiles = []uint64{}
						}
					} else {
						// 字段不存在，设置默认值
						server.DDNSProfilesRaw = "[]"
						server.DDNSProfiles = []uint64{}
					}
				}
			}
		}

		return nil
	case "monitor":
		// 监控器记录也需要特殊处理布尔字段
		var monitors []*map[string]interface{}
		for _, item := range items {
			var data map[string]interface{}
			if err := json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			monitors = append(monitors, &data)
		}

		// 重新序列化为 JSON
		monitorsJSON, err := json.Marshal(monitors)
		if err != nil {
			return err
		}

		// 反序列化到结果，然后手动解析特殊字段
		if err := json.Unmarshal(monitorsJSON, result); err != nil {
			return err
		}

		// 手动解析监控器的特殊字段（模拟 GORM 的 AfterFind 钩子）
		if monitorSlice, ok := result.(*[]*model.Monitor); ok {
			for _, monitor := range *monitorSlice {
				if monitor != nil {
					// 解析 SkipServersRaw 到 SkipServers 字段
					monitor.SkipServers = make(map[uint64]bool)
					if monitor.SkipServersRaw != "" {
						var skipServers []uint64
						if err := utils.Json.Unmarshal([]byte(monitor.SkipServersRaw), &skipServers); err != nil {
							log.Printf("解析监控器 %d 的 SkipServersRaw 失败: %v", monitor.ID, err)
							skipServers = []uint64{}
						}
						for _, serverID := range skipServers {
							monitor.SkipServers[serverID] = true
						}
					}

					// 解析 FailTriggerTasksRaw 到 FailTriggerTasks 字段
					if monitor.FailTriggerTasksRaw != "" {
						if err := utils.Json.Unmarshal([]byte(monitor.FailTriggerTasksRaw), &monitor.FailTriggerTasks); err != nil {
							log.Printf("解析监控器 %d 的 FailTriggerTasksRaw 失败: %v", monitor.ID, err)
							monitor.FailTriggerTasks = []uint64{}
						}
					} else {
						monitor.FailTriggerTasks = []uint64{}
					}

					// 解析 RecoverTriggerTasksRaw 到 RecoverTriggerTasks 字段
					if monitor.RecoverTriggerTasksRaw != "" {
						if err := utils.Json.Unmarshal([]byte(monitor.RecoverTriggerTasksRaw), &monitor.RecoverTriggerTasks); err != nil {
							log.Printf("解析监控器 %d 的 RecoverTriggerTasksRaw 失败: %v", monitor.ID, err)
							monitor.RecoverTriggerTasks = []uint64{}
						}
					} else {
						monitor.RecoverTriggerTasks = []uint64{}
					}
				}
			}
		}

		return nil
	case "user":
		// 用户记录需要特殊处理布尔字段和Token字段
		var users []*map[string]interface{}
		for _, item := range items {
			var data map[string]interface{}
			if err := json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			users = append(users, &data)
		}

		// 重新序列化为 JSON
		usersJSON, err := json.Marshal(users)
		if err != nil {
			return err
		}

		// 反序列化到结果，然后手动设置Token字段
		if err := json.Unmarshal(usersJSON, result); err != nil {
			return err
		}

		// 手动设置Token字段（因为它有json:"-"标签）
		if userSlice, ok := result.(*[]*model.User); ok {
			for i, user := range *userSlice {
				if user != nil && i < len(users) {
					userData := users[i]
					if token, exists := (*userData)["Token"]; exists {
						if tokenStr, isStr := token.(string); isStr {
							user.Token = tokenStr
						}
					}
				}
			}
		}

		return nil
	case "alert_rule":
		// 报警规则需要特殊处理布尔字段和JSON字段
		var rules []*map[string]interface{}
		for _, item := range items {
			var data map[string]interface{}
			if err := json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			rules = append(rules, &data)
		}

		// 重新序列化为 JSON
		rulesJSON, err := json.Marshal(rules)
		if err != nil {
			return err
		}

		// 反序列化到结果
		if err := json.Unmarshal(rulesJSON, result); err != nil {
			return err
		}

		// 手动解析 Rules 字段（模拟 GORM 的 AfterFind 钩子）
		if alertRules, ok := result.(*[]*model.AlertRule); ok {
			for _, rule := range *alertRules {
				if rule != nil {
					if rule.RulesRaw != "" {
						// 解析 RulesRaw 到 Rules 字段
						if err := utils.Json.Unmarshal([]byte(rule.RulesRaw), &rule.Rules); err != nil {
							log.Printf("解析报警规则 %d 的 RulesRaw 失败: %v, RulesRaw内容: %s", rule.ID, err, rule.RulesRaw)
							rule.Rules = []model.Rule{} // 设置为空数组
						}

						// 解析 FailTriggerTasksRaw 到 FailTriggerTasks 字段
						if rule.FailTriggerTasksRaw != "" {
							if err := utils.Json.Unmarshal([]byte(rule.FailTriggerTasksRaw), &rule.FailTriggerTasks); err != nil {
								log.Printf("解析报警规则 %d 的 FailTriggerTasksRaw 失败: %v", rule.ID, err)
								rule.FailTriggerTasks = []uint64{}
							}
						}

						// 解析 RecoverTriggerTasksRaw 到 RecoverTriggerTasks 字段
						if rule.RecoverTriggerTasksRaw != "" {
							if err := utils.Json.Unmarshal([]byte(rule.RecoverTriggerTasksRaw), &rule.RecoverTriggerTasks); err != nil {
								log.Printf("解析报警规则 %d 的 RecoverTriggerTasksRaw 失败: %v", rule.ID, err)
								rule.RecoverTriggerTasks = []uint64{}
							}
						}
					} else {
						log.Printf("报警规则 %d 的 RulesRaw 为空", rule.ID)
					}
				}
			}
		}

		return nil
	case "api_token":
		// API令牌记录需要特殊处理布尔字段和Token字段
		var tokens []*map[string]interface{}
		for _, item := range items {
			var data map[string]interface{}
			if err := json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)

			// 确保Token字段存在且不为空
			if tokenVal, exists := data["Token"]; !exists || tokenVal == "" {
				// 如果Token字段不存在或为空，尝试从其他字段获取
				if token, ok := data["token"]; ok && token != "" {
					data["Token"] = token
				}
			}

			tokens = append(tokens, &data)
		}

		// 重新序列化为 JSON
		tokensJSON, err := json.Marshal(tokens)
		if err != nil {
			return err
		}

		// 反序列化到结果，然后手动设置Token字段
		if err := json.Unmarshal(tokensJSON, result); err != nil {
			return err
		}

		// 手动设置Token字段（因为它可能有json:"-"标签）
		if tokenSlice, ok := result.(*[]*model.ApiToken); ok {
			for i, token := range *tokenSlice {
				if token != nil && i < len(tokens) {
					tokenData := tokens[i]
					if tokenVal, exists := (*tokenData)["Token"]; exists {
						if tokenStr, isStr := tokenVal.(string); isStr && tokenStr != "" {
							token.Token = tokenStr
						}
					}
					// 如果Token仍然为空，尝试从小写token字段获取
					if token.Token == "" {
						if tokenVal, exists := (*tokenData)["token"]; exists {
							if tokenStr, isStr := tokenVal.(string); isStr && tokenStr != "" {
								token.Token = tokenStr
							}
						}
					}
				}
			}
		}

		return nil
	case "ddns_profile":
		// DDNS配置记录需要特殊处理布尔字段
		var profiles []*map[string]interface{}
		for _, item := range items {
			var data map[string]interface{}
			if err := json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			profiles = append(profiles, &data)
		}

		// 重新序列化为 JSON
		profilesJSON, err := json.Marshal(profiles)
		if err != nil {
			return err
		}

		// 反序列化到结果，然后手动解析特殊字段
		if err := json.Unmarshal(profilesJSON, result); err != nil {
			return err
		}

		// 手动解析 DDNS 配置的特殊字段（模拟 GORM 的 AfterFind 钩子）
		if profileSlice, ok := result.(*[]*model.DDNSProfile); ok {
			for _, profile := range *profileSlice {
				if profile != nil {
					// 解析 DomainsRaw 到 Domains 字段
					if profile.DomainsRaw != "" {
						profile.Domains = strings.Split(profile.DomainsRaw, ",")
					} else {
						profile.Domains = []string{}
					}
				}
			}
		}

		return nil
	case "notification":
		// 通知配置记录需要特殊处理布尔字段
		var notifications []*map[string]interface{}
		for _, item := range items {
			var data map[string]interface{}
			if err := json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			notifications = append(notifications, &data)
		}

		// 重新序列化为 JSON
		notificationsJSON, err := json.Marshal(notifications)
		if err != nil {
			return err
		}

		return json.Unmarshal(notificationsJSON, result)
	case "nat":
		// NAT配置记录需要特殊处理布尔字段
		var nats []*map[string]interface{}
		for _, item := range items {
			var data map[string]interface{}
			if err := json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			nats = append(nats, &data)
		}

		// 重新序列化为 JSON
		natsJSON, err := json.Marshal(nats)
		if err != nil {
			return err
		}

		return json.Unmarshal(natsJSON, result)

	case "cron":
		// 定时任务记录需要特殊处理布尔字段
		var crons []*map[string]interface{}
		for _, item := range items {
			var data map[string]interface{}
			if err := json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			crons = append(crons, &data)
		}

		// 重新序列化为 JSON
		cronsJSON, err := json.Marshal(crons)
		if err != nil {
			return err
		}

		// 反序列化到结果，然后手动解析特殊字段
		if err := json.Unmarshal(cronsJSON, result); err != nil {
			return err
		}

		// 手动解析计划任务的特殊字段（模拟 GORM 的 AfterFind 钩子）
		if cronSlice, ok := result.(*[]*model.Cron); ok {
			for _, cron := range *cronSlice {
				if cron != nil {
					// 解析 ServersRaw 到 Servers 字段
					if cron.ServersRaw == "" {
						cron.ServersRaw = "[]"
						cron.Servers = []uint64{}
					} else {
						if err := utils.Json.Unmarshal([]byte(cron.ServersRaw), &cron.Servers); err != nil {
							log.Printf("解析计划任务 %d 的 ServersRaw 失败: %v", cron.ID, err)
							cron.Servers = []uint64{}
							cron.ServersRaw = "[]"
						}
					}
				}
			}
		}

		return nil
	case "ddns_record_state":
		// DDNS记录状态需要特殊处理布尔字段
		var states []*map[string]interface{}
		for _, item := range items {
			var data map[string]interface{}
			if err := json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			states = append(states, &data)
		}

		// 重新序列化为 JSON
		statesJSON, err := json.Marshal(states)
		if err != nil {
			return err
		}

		return json.Unmarshal(statesJSON, result)
	default:
		// 其他类型的记录，也需要进行字段类型转换
		var others []*map[string]interface{}
		for _, item := range items {
			var data map[string]interface{}
			if err := json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			others = append(others, &data)
		}

		// 重新序列化为 JSON
		othersJSON, err := json.Marshal(others)
		if err != nil {
			return err
		}

		return json.Unmarshal(othersJSON, result)
	}
}

// convertDbFieldTypes 转换从 BadgerDB 读取的字段类型，确保兼容性
func convertDbFieldTypes(data *map[string]interface{}) {
	// 转换已知需要特殊处理的字段
	d := *data

	// 处理数值型字段，确保它们是正确的类型
	// 注意：ID字段不应该被转换为float64，因为会导致精度丢失
	numericFields := []string{"group", "sort", "latest_version"}
	for _, field := range numericFields {
		if val, ok := d[field]; ok {
			switch v := val.(type) {
			case string:
				if v == "" {
					d[field] = float64(0)
				} else if f, err := strconv.ParseFloat(v, 64); err == nil {
					d[field] = f
				}
			}
		}
	}

	// 特殊处理ID字段，保持其原始类型（uint64）
	idFields := []string{"id", "ID", "user_id", "UserID"}
	for _, field := range idFields {
		if val, ok := d[field]; ok {
			switch v := val.(type) {
			case string:
				if v == "" {
					d[field] = uint64(0)
				} else if i, err := strconv.ParseUint(v, 10, 64); err == nil {
					d[field] = i
				}
			case float64:
				// 如果已经是float64，转换回uint64
				d[field] = uint64(v)
			case int:
				d[field] = uint64(v)
			case int64:
				d[field] = uint64(v)
			}
		}
	}

	// 处理时间字段，确保它们是正确的时间格式
	timeFields := []string{
		"created_at", "updated_at", "deleted_at",
		"last_active", "last_online", "last_flow_save_time",
		"last_db_update_time", "last_seen", "last_ping",
		"CreatedAt", "UpdatedAt", "DeletedAt",
		"LastActive", "LastOnline", "LastFlowSaveTime",
		"LastDBUpdateTime", "LastSeen", "LastPing",
		"token_expired", "TokenExpired",
	}
	for _, field := range timeFields {
		if val, ok := d[field]; ok {
			switch v := val.(type) {
			case string:
				if v == "" || v == "NULL" || v == "0001-01-01T00:00:00Z" {
					// 对于空时间或无效时间，设置为当前时间或保持零值
					if field == "created_at" || field == "CreatedAt" {
						d[field] = time.Now().Format(time.RFC3339)
					} else if field == "updated_at" || field == "UpdatedAt" {
						d[field] = time.Now().Format(time.RFC3339)
					} else {
						// 其他时间字段保持零值
						d[field] = "0001-01-01T00:00:00Z"
					}
				} else {
					// 尝试解析时间字符串并重新格式化
					if parsedTime, err := parseTimeString(v); err == nil {
						d[field] = parsedTime.Format(time.RFC3339)
					} else {
						log.Printf("警告：无法解析时间字段 %s 的值 '%s': %v", field, v, err)
						// 如果解析失败，根据字段类型设置默认值
						if field == "created_at" || field == "CreatedAt" {
							d[field] = time.Now().Format(time.RFC3339)
						} else if field == "updated_at" || field == "UpdatedAt" {
							d[field] = time.Now().Format(time.RFC3339)
						} else {
							d[field] = "0001-01-01T00:00:00Z"
						}
					}
				}
			case time.Time:
				// 如果已经是 time.Time 类型，转换为字符串
				d[field] = v.Format(time.RFC3339)
			}
		}
	}

	// 处理布尔型字段 (支持多种命名格式)
	boolFields := []string{
		// Server fields
		"is_online", "is_disabled", "hide_for_guest", "show_all", "tasker",
		"HideForGuest", "EnableDDNS", "enable_ddns",
		// Monitor fields
		"notify", "Notify", "enable_trigger_task", "EnableTriggerTask",
		"enable_show_in_service", "EnableShowInService",
		"latency_notify", "LatencyNotify",
		// User fields
		"hireable", "Hireable", "super_admin", "SuperAdmin",
		// AlertRule fields
		"enable", "Enable", "enabled", "Enabled",
	}
	for _, field := range boolFields {
		if val, ok := d[field]; ok {
			switch v := val.(type) {
			case string:
				d[field] = v == "1" || v == "true" || v == "t"
			case float64:
				d[field] = v != 0
			case int:
				d[field] = v != 0
			}
		}
	}

	// 处理特殊的 JSON 字符串字段
	// 注意：HostJSON和LastStateJSON字段有json:"-"标签，需要特殊处理
	jsonFields := []string{"HostJSON", "LastStateJSON", "host_json", "last_state_json"}
	for _, field := range jsonFields {
		if val, ok := d[field]; ok {
			if strVal, isStr := val.(string); isStr && strVal != "" {
				var jsonData interface{}
				if err := json.Unmarshal([]byte(strVal), &jsonData); err == nil {
					// 如果能成功解析为 JSON，保持原样，否则视为普通字符串
					// 这里不做替换，因为 model 会自己处理这些 JSON 字段
				}
			}
		}
	}

	// 确保 HostJSON 和 LastStateJSON 字段存在（这些字段有json:"-"标签）
	// 如果存在小写版本，复制到大写版本
	if hostJSON, ok := d["host_json"]; ok {
		d["HostJSON"] = hostJSON
	}
	if lastStateJSON, ok := d["last_state_json"]; ok {
		d["LastStateJSON"] = lastStateJSON
	}

	// 确保必要的字段存在
	if _, ok := d["HostJSON"]; !ok {
		d["HostJSON"] = ""
	}
	if _, ok := d["LastStateJSON"]; !ok {
		d["LastStateJSON"] = ""
	}

	// 不要在这里设置 Secret 字段的默认值
	// Secret 字段应该保持原有值，如果不存在则不设置
	// 让服务器加载逻辑根据实际情况处理空 Secret

	// 确保 Token 字段存在（用户认证Token，有json:"-"标签）
	if _, ok := d["Token"]; !ok {
		d["Token"] = ""
	}
	if _, ok := d["token"]; !ok {
		d["token"] = ""
	}

	// 处理报警规则的字段映射（小写到大写）
	if rulesRaw, ok := d["rules_raw"]; ok {
		d["RulesRaw"] = rulesRaw
	}
	if failTriggerTasksRaw, ok := d["fail_trigger_tasks_raw"]; ok {
		d["FailTriggerTasksRaw"] = failTriggerTasksRaw
	}
	if recoverTriggerTasksRaw, ok := d["recover_trigger_tasks_raw"]; ok {
		d["RecoverTriggerTasksRaw"] = recoverTriggerTasksRaw
	}
}

// Begin starts a new transaction
func (b *BadgerDB) Begin() (*BadgerTxn, error) {
	b.rwMutex.Lock()
	txn := b.db.NewTransaction(true)
	return &BadgerTxn{
		txn:    txn,
		db:     b,
		active: true,
	}, nil
}

// BatchWrite executes a function within a transaction
func (b *BadgerDB) BatchWrite(fn func(txn *BadgerTxn) error) error {
	txn, err := b.Begin()
	if err != nil {
		return err
	}
	defer txn.Discard()

	if err := fn(txn); err != nil {
		return err
	}

	return txn.Commit()
}

// BatchInsert inserts multiple key-value pairs with the same prefix
func (b *BadgerDB) BatchInsert(prefix string, keyValues map[string][]byte) error {
	return b.BatchWrite(func(txn *BadgerTxn) error {
		for k, v := range keyValues {
			if err := txn.Set(prefix+":"+k, v); err != nil {
				return err
			}
		}
		return nil
	})
}

// BadgerTxn represents a BadgerDB transaction
type BadgerTxn struct {
	txn    *badger.Txn
	db     *BadgerDB
	active bool
}

// Commit commits the transaction
func (t *BadgerTxn) Commit() error {
	if !t.active {
		return errors.New("transaction already committed or discarded")
	}
	t.active = false
	err := t.txn.Commit()
	t.db.rwMutex.Unlock()
	return err
}

// Discard discards the transaction
func (t *BadgerTxn) Discard() {
	if t.active {
		t.active = false
		t.txn.Discard()
		t.db.rwMutex.Unlock()
	}
}

// Set sets a key-value pair in the transaction
func (t *BadgerTxn) Set(key string, value []byte) error {
	if !t.active {
		return errors.New("transaction not active")
	}
	return t.txn.Set([]byte(key), value)
}

// Delete deletes a key from the transaction
func (t *BadgerTxn) Delete(key string) error {
	if !t.active {
		return errors.New("transaction not active")
	}
	return t.txn.Delete([]byte(key))
}

// ClearPrefixedKeys removes all keys with a specific prefix
func (b *BadgerDB) ClearPrefixedKeys(prefix string) error {
	b.rwMutex.Lock()
	defer b.rwMutex.Unlock()

	return b.db.Update(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()

		prefixBytes := []byte(prefix)
		for it.Seek(prefixBytes); it.ValidForPrefix(prefixBytes); it.Next() {
			if err := txn.Delete(it.Item().Key()); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetKeysWithPrefix returns all keys with a specific prefix
func (b *BadgerDB) GetKeysWithPrefix(prefix string) ([]string, error) {
	b.rwMutex.RLock()
	defer b.rwMutex.RUnlock()

	var keys []string
	err := b.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()

		prefixBytes := []byte(prefix)
		for it.Seek(prefixBytes); it.ValidForPrefix(prefixBytes); it.Next() {
			key := append([]byte{}, it.Item().Key()...)
			keys = append(keys, string(key))
		}
		return nil
	})
	return keys, err
}

// GetKeysWithPrefixAndTimestampInRange returns all keys with a specific prefix and timestamp in range
func (b *BadgerDB) GetKeysWithPrefixAndTimestampInRange(prefix string, start, end time.Time) ([]string, error) {
	b.rwMutex.RLock()
	defer b.rwMutex.RUnlock()

	startStr := fmt.Sprintf("%d", start.UnixNano())
	endStr := fmt.Sprintf("%d", end.UnixNano())
	startKey := prefix + ":" + startStr
	endKey := prefix + ":" + endStr

	var keys []string
	err := b.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek([]byte(startKey)); it.Valid(); it.Next() {
			key := it.Item().Key()
			if bytes.Compare(key, []byte(endKey)) > 0 {
				break
			}
			keys = append(keys, string(append([]byte{}, key...)))
		}
		return nil
	})
	return keys, err
}

// CleanupExpiredData removes data older than maxAge
func (b *BadgerDB) CleanupExpiredData(prefix string, maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge)
	keys, err := b.GetKeysWithPrefixAndTimestampInRange(prefix, time.Time{}, cutoff)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, key := range keys {
		if err := b.Delete(key); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
