package singleton

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/xos/serverstatus/model"
)

// TrafficManager 流量管理器
type TrafficManager struct {
	sync.RWMutex
	cache *cache.Cache
	// 服务器ID -> 最后更新时间的映射
	lastUpdate map[uint64]time.Time
	// 服务器ID -> 累计流量的映射
	trafficStats map[uint64]*TrafficStats
	// 批量写入缓冲区
	batchBuffer []*model.Transfer
	// 上次批量写入时间
	lastBatchWrite time.Time
}

// TrafficStats 流量统计数据
type TrafficStats struct {
	InBytes     uint64    // 入站流量
	OutBytes    uint64    // 出站流量
	LastTime    time.Time // 最后更新时间
	UpdateCount uint64    // 更新计数器
	// 用于计算速率
	lastInBytes  uint64
	lastOutBytes uint64
	InSpeed      uint64 // 入站速率 (bytes/s)
	OutSpeed     uint64 // 出站速率 (bytes/s)
}

const (
	// 批量写入阈值
	batchSize = 100
	// 批量写入时间间隔
	batchInterval = 30 * time.Second
	// 最小更新间隔
	minUpdateInterval = 100 * time.Millisecond
	// 缓存过期时间
	cacheExpiration = 5 * time.Minute
	// 缓存清理间隔
	cachePurgeInterval = 10 * time.Minute
)

var (
	trafficManager *TrafficManager
	once           sync.Once
)

// GetTrafficManager 获取流量管理器单例
func GetTrafficManager() *TrafficManager {
	once.Do(func() {
		trafficManager = &TrafficManager{
			cache:          cache.New(cacheExpiration, cachePurgeInterval),
			lastUpdate:     make(map[uint64]time.Time),
			trafficStats:   make(map[uint64]*TrafficStats),
			batchBuffer:    make([]*model.Transfer, 0, batchSize),
			lastBatchWrite: time.Now(),
		}
		// 启动后台批量写入任务
		go trafficManager.batchWriteWorker()
	})
	return trafficManager
}

// UpdateTraffic 更新服务器流量统计
func (tm *TrafficManager) UpdateTraffic(serverID uint64, inBytes, outBytes uint64) {
	tm.Lock()
	defer tm.Unlock()

	now := time.Now()
	stats, exists := tm.trafficStats[serverID]
	if !exists {
		stats = &TrafficStats{
			InBytes:      inBytes,
			OutBytes:     outBytes,
			LastTime:     now,
			UpdateCount:  1,
			lastInBytes:  inBytes,
			lastOutBytes: outBytes,
		}
		tm.trafficStats[serverID] = stats
		log.Printf("服务器 %d 初始化流量统计: 入站=%d, 出站=%d", serverID, inBytes, outBytes)
		return
	}

	// 验证数据有效性
	if inBytes < stats.InBytes || outBytes < stats.OutBytes {
		log.Printf("警告: 服务器 %d 的流量数据异常: 新入站=%d < 旧入站=%d 或 新出站=%d < 旧出站=%d",
			serverID, inBytes, stats.InBytes, outBytes, stats.OutBytes)
		// 如果新数据小于旧数据，可能是服务器重启，使用增量
		inBytes = stats.InBytes + (inBytes - stats.lastInBytes)
		outBytes = stats.OutBytes + (outBytes - stats.lastOutBytes)
	}

	// 计算时间差
	duration := now.Sub(stats.LastTime)
	if duration < minUpdateInterval {
		return // 忽略过于频繁的更新
	}

	// 计算速率
	if duration.Seconds() > 0 {
		stats.InSpeed = uint64(float64(inBytes-stats.lastInBytes) / duration.Seconds())
		stats.OutSpeed = uint64(float64(outBytes-stats.lastOutBytes) / duration.Seconds())
	}

	// 更新统计数据
	stats.lastInBytes = inBytes
	stats.lastOutBytes = outBytes
	stats.InBytes = inBytes
	stats.OutBytes = outBytes
	stats.LastTime = now
	stats.UpdateCount++

	log.Printf("服务器 %d 更新流量统计: 入站=%d, 出站=%d, 入站速率=%d/s, 出站速率=%d/s",
		serverID, inBytes, outBytes, stats.InSpeed, stats.OutSpeed)

	// 缓存最新数据
	tm.cache.Set(
		tm.getCacheKey(serverID),
		stats,
		cache.DefaultExpiration,
	)

	// 添加到批量写入缓冲区
	tm.batchBuffer = append(tm.batchBuffer, &model.Transfer{
		ServerID: serverID,
		In:       inBytes,
		Out:      outBytes,
	})

	// 检查是否需要立即写入
	if len(tm.batchBuffer) >= batchSize || now.Sub(tm.lastBatchWrite) >= batchInterval {
		tm.writeBatchToDatabase()
	}
}

// GetTrafficStats 获取服务器流量统计
func (tm *TrafficManager) GetTrafficStats(serverID uint64) *TrafficStats {
	tm.RLock()
	defer tm.RUnlock()

	// 先尝试从缓存获取
	if cached, found := tm.cache.Get(tm.getCacheKey(serverID)); found {
		if stats, ok := cached.(*TrafficStats); ok {
			return stats
		}
	}

	// 缓存未命中，返回实时数据
	return tm.trafficStats[serverID]
}

// getCacheKey 生成缓存键
func (tm *TrafficManager) getCacheKey(serverID uint64) string {
	return fmt.Sprintf("traffic_stats_%d", serverID)
}

// batchWriteWorker 后台批量写入工作器
func (tm *TrafficManager) batchWriteWorker() {
	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()

	for range ticker.C {
		tm.Lock()
		if len(tm.batchBuffer) > 0 {
			tm.writeBatchToDatabase()
		}
		tm.Unlock()
	}
}

// writeBatchToDatabase 批量写入数据库
func (tm *TrafficManager) writeBatchToDatabase() {
	if len(tm.batchBuffer) == 0 {
		return
	}

	// 创建批量写入的事务
	tx := DB.Begin()
	if tx.Error != nil {
		return
	}

	// 批量插入数据
	if err := tx.CreateInBatches(tm.batchBuffer, 100).Error; err != nil {
		tx.Rollback()
		return
	}

	// 提交事务
	if err := tx.Commit().Error; err != nil {
		tx.Rollback()
		return
	}

	// 清空缓冲区并更新最后写入时间
	tm.batchBuffer = tm.batchBuffer[:0]
	tm.lastBatchWrite = time.Now()
}

// CleanupOldData 清理旧数据
func (tm *TrafficManager) CleanupOldData(days int) error {
	deadline := time.Now().AddDate(0, 0, -days)

	// 分批删除数据以减少数据库压力
	batchSize := 1000
	for {
		result := DB.Where("created_at < ?", deadline).Limit(batchSize).Delete(&model.Transfer{})
		if result.Error != nil {
			return fmt.Errorf("failed to cleanup old traffic data: %v", result.Error)
		}

		if result.RowsAffected < int64(batchSize) {
			break
		}

		// 短暂休眠以减少数据库压力
		time.Sleep(100 * time.Millisecond)
	}

	return nil
}

// GetTrafficSpeed 获取服务器当前流量速率
func (tm *TrafficManager) GetTrafficSpeed(serverID uint64) (inSpeed, outSpeed uint64) {
	if stats := tm.GetTrafficStats(serverID); stats != nil {
		return stats.InSpeed, stats.OutSpeed
	}
	return 0, 0
}

// GetTrafficTotal 获取服务器总流量
func (tm *TrafficManager) GetTrafficTotal(serverID uint64) (inTotal, outTotal uint64) {
	if stats := tm.GetTrafficStats(serverID); stats != nil {
		return stats.InBytes, stats.OutBytes
	}
	return 0, 0
}

// SaveToDatabase 将当前的流量数据保存到数据库
func (tm *TrafficManager) SaveToDatabase() error {
	tm.Lock()
	defer tm.Unlock()

	// 强制执行一次批量写入
	tm.writeBatchToDatabase()
	return nil
}
