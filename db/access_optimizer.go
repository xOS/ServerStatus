package db

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DataAccessOptimizer 数据访问优化器 - 减少数据库访问，提高缓存效率
type DataAccessOptimizer struct {
	// 批量操作缓冲区
	pendingWrites map[string]interface{}
	writeMutex    sync.RWMutex

	// 读缓存（应用层缓存，提高命中率）
	readCache  map[string]CacheItem
	cacheMutex sync.RWMutex

	// 批量写入定时器
	flushTicker *time.Ticker
	stopCh      chan struct{}
}

// CacheItem 缓存项
type CacheItem struct {
	Data     interface{}
	ExpireAt time.Time
	AccessAt time.Time
}

var (
	optimizer     *DataAccessOptimizer
	optimizerOnce sync.Once
)

// GetDataAccessOptimizer 获取数据访问优化器单例
func GetDataAccessOptimizer() *DataAccessOptimizer {
	optimizerOnce.Do(func() {
		optimizer = &DataAccessOptimizer{
			pendingWrites: make(map[string]interface{}),
			readCache:     make(map[string]CacheItem),
			flushTicker:   time.NewTicker(30 * time.Second), // 30秒批量写入一次
			stopCh:        make(chan struct{}),
		}
		optimizer.start()
	})
	return optimizer
}

// start 启动批量写入和缓存清理
func (dao *DataAccessOptimizer) start() {
	go func() {
		defer dao.flushTicker.Stop()

		for {
			select {
			case <-dao.flushTicker.C:
				dao.flushPendingWrites()
				dao.cleanExpiredCache()

			case <-dao.stopCh:
				// 停止前最后一次写入
				dao.flushPendingWrites()
				return
			}
		}
	}()
}

// OptimizedSave 优化的保存方法 - 缓冲写入请求
func (dao *DataAccessOptimizer) OptimizedSave(modelType string, id uint64, data interface{}) {
	key := getModelKey(modelType, id)

	dao.writeMutex.Lock()
	dao.pendingWrites[key] = data
	dao.writeMutex.Unlock()

	// 同时更新读缓存
	dao.updateReadCache(key, data)
}

// OptimizedGet 优化的获取方法 - 优先从应用层缓存读取
func (dao *DataAccessOptimizer) OptimizedGet(modelType string, id uint64, result interface{}) error {
	key := getModelKey(modelType, id)

	// 首先检查应用层缓存
	dao.cacheMutex.RLock()
	if item, exists := dao.readCache[key]; exists && item.ExpireAt.After(time.Now()) {
		dao.cacheMutex.RUnlock()

		// 更新访问时间
		dao.cacheMutex.Lock()
		item.AccessAt = time.Now()
		dao.readCache[key] = item
		dao.cacheMutex.Unlock()

		// 复制数据到结果
		return copyData(item.Data, result)
	}
	dao.cacheMutex.RUnlock()

	// 缓存未命中，从数据库读取
	if err := DB.FindModel(id, modelType, result); err != nil {
		return err
	}

	// 更新缓存
	dao.updateReadCache(key, result)
	return nil
}

// flushPendingWrites 批量写入待处理的数据
func (dao *DataAccessOptimizer) flushPendingWrites() {
	dao.writeMutex.Lock()
	if len(dao.pendingWrites) == 0 {
		dao.writeMutex.Unlock()
		return
	}

	// 复制待写入数据
	writes := make(map[string]interface{})
	for k, v := range dao.pendingWrites {
		writes[k] = v
	}
	// 清空缓冲区
	dao.pendingWrites = make(map[string]interface{})
	dao.writeMutex.Unlock()

	// 按模型类型分组批量写入
	grouped := groupByModelType(writes)
	for modelType, models := range grouped {
		if err := DB.BatchSaveModels(modelType, models); err != nil {
			log.Printf("批量写入失败 %s: %v", modelType, err)
		}
	}

	log.Printf("批量写入完成: %d条记录", len(writes))
}

// updateReadCache 更新读缓存
func (dao *DataAccessOptimizer) updateReadCache(key string, data interface{}) {
	dao.cacheMutex.Lock()
	dao.readCache[key] = CacheItem{
		Data:     copyDataValue(data),
		ExpireAt: time.Now().Add(5 * time.Minute), // 5分钟过期
		AccessAt: time.Now(),
	}
	dao.cacheMutex.Unlock()
}

// cleanExpiredCache 清理过期缓存
func (dao *DataAccessOptimizer) cleanExpiredCache() {
	dao.cacheMutex.Lock()
	defer dao.cacheMutex.Unlock()

	now := time.Now()
	cleaned := 0

	for key, item := range dao.readCache {
		// 清理过期的或长时间未访问的缓存
		if item.ExpireAt.Before(now) || item.AccessAt.Before(now.Add(-10*time.Minute)) {
			delete(dao.readCache, key)
			cleaned++
		}
	}

	if cleaned > 0 {
		log.Printf("清理过期缓存: %d项", cleaned)
	}
}

// Stop 停止优化器
func (dao *DataAccessOptimizer) Stop() {
	close(dao.stopCh)
}

// 辅助函数
func getModelKey(modelType string, id uint64) string {
	return fmt.Sprintf("%s:%d", modelType, id)
}

func groupByModelType(writes map[string]interface{}) map[string]map[uint64]interface{} {
	grouped := make(map[string]map[uint64]interface{})

	for key, data := range writes {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}

		modelType := parts[0]
		id, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			continue
		}

		if grouped[modelType] == nil {
			grouped[modelType] = make(map[uint64]interface{})
		}
		grouped[modelType][id] = data
	}

	return grouped
}

func copyData(src, dst interface{}) error {
	// 简化实现，实际应该根据类型进行深拷贝
	jsonData, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(jsonData, dst)
}

func copyDataValue(data interface{}) interface{} {
	// 简化实现，返回数据的深拷贝
	jsonData, err := json.Marshal(data)
	if err != nil {
		return data // 失败时返回原数据
	}
	var result interface{}
	json.Unmarshal(jsonData, &result)
	return result
}
