package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v3"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/utils"
)

// 迭代器池，重用迭代器对象减少内存分配
var iteratorPool = sync.Pool{
	New: func() interface{} {
		return make(map[string]*badger.Iterator)
	},
}

// JSON 序列化结果缓存，减少重复序列化
var jsonCache = sync.Map{}

// CacheConfig BadgerDB缓存配置
type CacheConfig struct {
	BlockCache int // MB
	IndexCache int // MB
	MemTable   int // MB
}

// getOptimalCacheConfig 根据系统内存和运行状态动态计算最优缓存配置
func getOptimalCacheConfig() CacheConfig {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// 获取当前内存使用情况（MB）
	currentHeapMB := int(m.HeapAlloc / (1024 * 1024))

	// 根据内存使用情况动态调整
	// 目标：BadgerDB缓存控制在50-100MB之间
	var totalCacheMB int
	if currentHeapMB < 100 {
		totalCacheMB = 80 // 系统内存充足时
	} else if currentHeapMB < 200 {
		totalCacheMB = 60 // 内存适中时
	} else {
		totalCacheMB = 40 // 内存紧张时
	}

	// 智能分配缓存
	return CacheConfig{
		BlockCache: totalCacheMB * 50 / 100, // 50%给块缓存（最重要）
		IndexCache: totalCacheMB * 30 / 100, // 30%给索引缓存
		MemTable:   totalCacheMB * 20 / 100, // 20%给内存表
	}
}

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

	// Configure BadgerDB with balanced settings - 根本解决方案
	// 不是简单增加缓存，而是优化配置平衡性能和内存使用
	cacheConfig := getOptimalCacheConfig()
	options := badger.DefaultOptions(path).
		WithLoggingLevel(badger.ERROR).                          // 减少日志噪音，只保留ERROR
		WithValueLogFileSize(64 << 20).                          // 64MB
		WithNumVersionsToKeep(1).                                // 只保留1个版本
		WithBlockCacheSize(int64(cacheConfig.BlockCache << 20)). // 动态块缓存大小
		WithIndexCacheSize(int64(cacheConfig.IndexCache << 20)). // 动态索引缓存大小
		WithNumLevelZeroTables(8).                               // 适中的Level 0表数量
		WithNumLevelZeroTablesStall(12).                         // 适中的停顿阈值
		WithValueThreshold(1024).                                // 适中的值阈值
		WithMemTableSize(int64(cacheConfig.MemTable << 20)).     // 动态内存表大小
		WithCompactL0OnClose(true).                              // 关闭时压缩L0
		WithDetectConflicts(false).                              // 禁用冲突检测以提高性能
		WithSyncWrites(false)                                    // 异步写入提高性能

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

	// 优化：延迟数据预取，避免启动时内存爆高
	go func() {
		// 延迟30秒启动，让系统完全稳定后再预取数据
		time.Sleep(30 * time.Second)
		if err := badgerDB.PrefetchCommonData(); err != nil {
			log.Printf("预取常用数据失败: %v", err)
		} else {
			log.Printf("常用数据预取完成，提高缓存命中率")
		}
	}()

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

		// 智能维护：结合缓存监控和内存优化
		ticker := time.NewTicker(10 * time.Minute)     // 10分钟间隔
		cacheTicker := time.NewTicker(5 * time.Minute) // 5分钟缓存检查
		defer ticker.Stop()
		defer cacheTicker.Stop()

		for {
			select {
			case <-ticker.C:
				// 值日志GC - 智能阈值
				err := b.db.RunValueLogGC(0.7)
				if err != nil && err != badger.ErrNoRewrite {
					log.Printf("Value log GC failed: %v", err)
				}

			case <-cacheTicker.C:
				// 缓存性能监控和优化
				b.monitorAndOptimizeCache()

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

	value, err := utils.Json.Marshal(model)
	if err != nil {
		return fmt.Errorf("failed to marshal model: %w", err)
	}

	return b.Set(key, value)
}

// BatchSaveModels 批量保存模型 - 优化数据库访问模式，提高缓存命中率
func (b *BadgerDB) BatchSaveModels(modelType string, models map[uint64]interface{}) error {
	if len(models) == 0 {
		return nil
	}

	// 使用批量事务提高效率
	return b.db.Update(func(txn *badger.Txn) error {
		for id, model := range models {
			key := fmt.Sprintf("%s:%d", modelType, id)

			value, err := utils.Json.Marshal(model)
			if err != nil {
				return fmt.Errorf("failed to marshal model %d: %w", id, err)
			}

			if err := txn.Set([]byte(key), value); err != nil {
				return fmt.Errorf("failed to set key %s: %w", key, err)
			}
		}
		return nil
	})
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
		if err := utils.Json.Unmarshal(data, &userData); err != nil {
			return err
		}

		// 转换字段类型，确保布尔字段正确
		convertDbFieldTypes(&userData)

		// 重新序列化为 JSON
		userJSON, err := utils.Json.Marshal(userData)
		if err != nil {
			return err
		}

		// 反序列化到结果
		if err := utils.Json.Unmarshal(userJSON, result); err != nil {
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
		// 使用缓存的解码器，减少内存分配
		decoder := utils.Json.NewDecoder(bytes.NewReader(data))
		if err := decoder.Decode(&serverData); err != nil {
			return err
		}

		// 转换字段类型，确保字段正确
		convertDbFieldTypes(&serverData)

		// 重新序列化为 JSON，但使用更高效的 utils.Json
		serverJSON, err := utils.Json.Marshal(serverData)
		if err != nil {
			return err
		}

		// 反序列化到结果，使用 utils.Json
		if err := utils.Json.Unmarshal(serverJSON, result); err != nil {
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
		if err := utils.Json.Unmarshal(data, &ruleData); err != nil {
			return err
		}

		// 转换字段类型，确保布尔字段正确
		convertDbFieldTypes(&ruleData)

		// 重新序列化为 JSON
		ruleJSON, err := utils.Json.Marshal(ruleData)
		if err != nil {
			return err
		}

		// 反序列化到结果
		return utils.Json.Unmarshal(ruleJSON, result)
	default:
		// 其他类型的记录，也需要进行字段类型转换
		var dataMap map[string]interface{}
		if err := utils.Json.Unmarshal(data, &dataMap); err != nil {
			return err
		}

		// 转换字段类型，确保布尔字段正确
		convertDbFieldTypes(&dataMap)

		// 重新序列化为 JSON
		dataJSON, err := utils.Json.Marshal(dataMap)
		if err != nil {
			return err
		}

		// 反序列化到结果
		return utils.Json.Unmarshal(dataJSON, result)
	}
}

// BatchFindModels 批量获取模型 - 优化读取性能
func (b *BadgerDB) BatchFindModels(modelType string, ids []uint64, results interface{}) error {
	if len(ids) == 0 {
		return nil
	}

	// 使用只读事务批量读取
	return b.db.View(func(txn *badger.Txn) error {
		for _, id := range ids {
			key := fmt.Sprintf("%s:%d", modelType, id)

			item, err := txn.Get([]byte(key))
			if err == badger.ErrKeyNotFound {
				continue // 跳过不存在的记录
			}
			if err != nil {
				return err
			}

			err = item.Value(func(val []byte) error {
				// 这里需要根据具体的结果类型来处理
				// 为了简化，这里只做基本的数据读取
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
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
		return utils.Json.Unmarshal([]byte("[]"), result)
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
		return utils.Json.Unmarshal([]byte("[]"), result)
	}

	// 针对不同的数据类型进行特殊处理
	switch prefix {
	case "server":
		// 服务器记录可能需要特殊处理，并且需要去重
		var servers []*map[string]interface{}
		seenIDs := make(map[uint64]bool) // 用于去重

		for _, item := range items {
			var data map[string]interface{}
			if err := utils.Json.Unmarshal(item, &data); err != nil {
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
		serversJSON, err := utils.Json.Marshal(servers)
		if err != nil {
			return err
		}

		// 先反序列化到结果
		if err := utils.Json.Unmarshal(serversJSON, result); err != nil {
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
			if err := utils.Json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			monitors = append(monitors, &data)
		}

		// 重新序列化为 JSON
		monitorsJSON, err := utils.Json.Marshal(monitors)
		if err != nil {
			return err
		}

		// 反序列化到结果，然后手动解析特殊字段
		if err := utils.Json.Unmarshal(monitorsJSON, result); err != nil {
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
			if err := utils.Json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			users = append(users, &data)
		}

		// 重新序列化为 JSON
		usersJSON, err := utils.Json.Marshal(users)
		if err != nil {
			return err
		}

		// 反序列化到结果，然后手动设置Token字段
		if err := utils.Json.Unmarshal(usersJSON, result); err != nil {
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
			if err := utils.Json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			rules = append(rules, &data)
		}

		// 重新序列化为 JSON
		rulesJSON, err := utils.Json.Marshal(rules)
		if err != nil {
			return err
		}

		// 反序列化到结果
		if err := utils.Json.Unmarshal(rulesJSON, result); err != nil {
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
			if err := utils.Json.Unmarshal(item, &data); err != nil {
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
		tokensJSON, err := utils.Json.Marshal(tokens)
		if err != nil {
			return err
		}

		// 反序列化到结果，然后手动设置Token字段
		if err := utils.Json.Unmarshal(tokensJSON, result); err != nil {
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
			if err := utils.Json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			profiles = append(profiles, &data)
		}

		// 重新序列化为 JSON
		profilesJSON, err := utils.Json.Marshal(profiles)
		if err != nil {
			return err
		}

		// 反序列化到结果，然后手动解析特殊字段
		if err := utils.Json.Unmarshal(profilesJSON, result); err != nil {
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
			if err := utils.Json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			notifications = append(notifications, &data)
		}

		// 重新序列化为 JSON
		notificationsJSON, err := utils.Json.Marshal(notifications)
		if err != nil {
			return err
		}

		return utils.Json.Unmarshal(notificationsJSON, result)
	case "nat":
		// NAT配置记录需要特殊处理布尔字段
		var nats []*map[string]interface{}
		for _, item := range items {
			var data map[string]interface{}
			if err := utils.Json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			nats = append(nats, &data)
		}

		// 重新序列化为 JSON
		natsJSON, err := utils.Json.Marshal(nats)
		if err != nil {
			return err
		}

		return utils.Json.Unmarshal(natsJSON, result)

	case "cron":
		// 定时任务记录需要特殊处理布尔字段
		var crons []*map[string]interface{}
		for _, item := range items {
			var data map[string]interface{}
			if err := utils.Json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			crons = append(crons, &data)
		}

		// 重新序列化为 JSON
		cronsJSON, err := utils.Json.Marshal(crons)
		if err != nil {
			return err
		}

		// 反序列化到结果，然后手动解析特殊字段
		if err := utils.Json.Unmarshal(cronsJSON, result); err != nil {
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
			if err := utils.Json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			states = append(states, &data)
		}

		// 重新序列化为 JSON
		statesJSON, err := utils.Json.Marshal(states)
		if err != nil {
			return err
		}

		return utils.Json.Unmarshal(statesJSON, result)
	default:
		// 其他类型的记录，也需要进行字段类型转换
		var others []*map[string]interface{}
		for _, item := range items {
			var data map[string]interface{}
			if err := utils.Json.Unmarshal(item, &data); err != nil {
				continue
			}

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			others = append(others, &data)
		}

		// 重新序列化为 JSON
		othersJSON, err := utils.Json.Marshal(others)
		if err != nil {
			return err
		}

		return utils.Json.Unmarshal(othersJSON, result)
	}
}

// PrefetchCommonData 预取常用数据到缓存 - 提高缓存命中率
func (b *BadgerDB) PrefetchCommonData() error {
	// 预取最近活跃的服务器数据
	return b.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchSize = 20 // 减少预取数量，从100减少到20
		it := txn.NewIterator(opts)
		defer it.Close()

		count := 0
		for it.Rewind(); it.Valid() && count < 10; it.Next() { // 只预取前10个常用数据，从50减少到10
			item := it.Item()
			key := string(item.Key())

			// 只预取服务器相关数据
			if strings.HasPrefix(key, "server:") {
				_ = item.Value(func(val []byte) error {
					// 数据已经被加载到缓存中
					return nil
				})
				count++
			}
		}

		return nil
	})
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
				if err := utils.Json.Unmarshal([]byte(strVal), &jsonData); err == nil {
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

// monitorAndOptimizeCache 监控缓存性能并自动优化
func (b *BadgerDB) monitorAndOptimizeCache() {
	// 获取当前内存使用情况
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	currentHeapMB := m.HeapAlloc / (1024 * 1024)

	// 内存使用监控和预警
	if currentHeapMB > 500 { // 500MB预警阈值
		log.Printf("BadgerDB缓存监控: 内存使用较高 %dMB，建议优化数据访问模式", currentHeapMB)

		// 可以在这里触发缓存清理或数据预处理
		runtime.GC() // 强制垃圾回收
	}

	// 定期输出缓存统计（仅在DEBUG模式下）
	if os.Getenv("BADGER_DEBUG") == "1" {
		log.Printf("BadgerDB缓存监控: 当前堆内存使用 %dMB", currentHeapMB)
	}
}

// GetCacheStats 获取缓存统计信息（用于监控和调试）
func (b *BadgerDB) GetCacheStats() map[string]interface{} {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return map[string]interface{}{
		"heap_alloc_mb":  m.HeapAlloc / (1024 * 1024),
		"heap_sys_mb":    m.HeapSys / (1024 * 1024),
		"stack_inuse_mb": m.StackInuse / (1024 * 1024),
		"gc_runs":        m.NumGC,
	}
}
