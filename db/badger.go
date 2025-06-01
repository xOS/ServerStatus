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
	"sync"
	"time"

	"github.com/dgraph-io/badger/v3"
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
	// Start value log garbage collection
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				err := b.db.RunValueLogGC(0.5)
				if err != nil && err != badger.ErrNoRewrite {
					log.Printf("Value log GC failed: %v", err)
				}
			case <-b.ctx.Done():
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

	return json.Unmarshal(data, result)
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

	log.Printf("FindAll: 查询前缀 %s 的数据", prefix)

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

	log.Printf("FindAll: 找到 %d 条 %s 记录", len(items), prefix)

	// 如果没有找到任何记录，返回空数组
	if len(items) == 0 {
		return json.Unmarshal([]byte("[]"), result)
	}

	// 针对不同的数据类型进行特殊处理
	switch prefix {
	case "server":
		// 服务器记录可能需要特殊处理
		var servers []*map[string]interface{}
		for i, item := range items {
			log.Printf("FindAll (server case): Processing item %d, raw data: %s", i, string(item))
			var data map[string]interface{}
			if err := json.Unmarshal(item, &data); err != nil {
				log.Printf("FindAll (server case): Item %d, 解析服务器数据失败: %v, 数据: %s", i, err, string(item))
				continue
			}
			log.Printf("FindAll (server case): Item %d, successfully unmarshalled to map: %v", i, data)

			// 转换字段类型，确保 JSON 字段正确
			convertDbFieldTypes(&data)
			log.Printf("FindAll (server case): Item %d, after convertDbFieldTypes: %v", i, data)
			servers = append(servers, &data)
		}

		// 重新序列化为 JSON
		serversJSON, err := json.Marshal(servers)
		if err != nil {
			log.Printf("FindAll (server case): 重新序列化服务器数据失败: %v. Processed servers data: %v", err, servers)
			return err
		}

		log.Printf("FindAll (server case): 已处理 %d 条服务器记录. Final JSON to unmarshal to result: %s", len(servers), string(serversJSON))
		return json.Unmarshal(serversJSON, result)
	case "monitor":
		// 监控器记录也需要特殊处理布尔字段
		var monitors []*map[string]interface{}
		for i, item := range items {
			log.Printf("FindAll (monitor case): Processing item %d, raw data: %s", i, string(item))
			var data map[string]interface{}
			if err := json.Unmarshal(item, &data); err != nil {
				log.Printf("FindAll (monitor case): Item %d, 解析监控器数据失败: %v, 数据: %s", i, err, string(item))
				continue
			}
			log.Printf("FindAll (monitor case): Item %d, successfully unmarshalled to map: %v", i, data)

			// 转换字段类型，确保布尔字段正确
			convertDbFieldTypes(&data)
			log.Printf("FindAll (monitor case): Item %d, after convertDbFieldTypes: %v", i, data)
			monitors = append(monitors, &data)
		}

		// 重新序列化为 JSON
		monitorsJSON, err := json.Marshal(monitors)
		if err != nil {
			log.Printf("FindAll (monitor case): 重新序列化监控器数据失败: %v. Processed monitors data: %v", err, monitors)
			return err
		}

		log.Printf("FindAll (monitor case): 已处理 %d 条监控器记录. Final JSON to unmarshal to result: %s", len(monitors), string(monitorsJSON))
		return json.Unmarshal(monitorsJSON, result)
	default:
		// 其他类型的记录，使用标准处理方式
		itemsJSON := "["
		for i, item := range items {
			if i > 0 {
				itemsJSON += ","
			}
			itemsJSON += string(item)
		}
		itemsJSON += "]"

		return json.Unmarshal([]byte(itemsJSON), result)
	}
}

// convertDbFieldTypes 转换从 BadgerDB 读取的字段类型，确保兼容性
func convertDbFieldTypes(data *map[string]interface{}) {
	// 转换已知需要特殊处理的字段
	d := *data

	// 处理数值型字段，确保它们是正确的类型
	numericFields := []string{"id", "group", "sort", "latest_version"}
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

	// 处理布尔型字段 (支持多种命名格式)
	boolFields := []string{
		// Server fields
		"is_online", "is_disabled", "hide_for_guest", "show_all", "tasker",
		"HideForGuest", "EnableDDNS", "enable_ddns",
		// Monitor fields  
		"notify", "Notify", "enable_trigger_task", "EnableTriggerTask", 
		"enable_show_in_service", "EnableShowInService", 
		"latency_notify", "LatencyNotify",
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
	jsonFields := []string{"host_json", "last_state_json"}
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

	// 确保必要的字段存在，但不要覆盖已存在的id字段
	if _, ok := d["host_json"]; !ok {
		d["host_json"] = ""
	}
	if _, ok := d["last_state_json"]; !ok {
		d["last_state_json"] = ""
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
