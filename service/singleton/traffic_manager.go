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
	// 批量写入阈值 - 减少以提高写入频率
	batchSize = 50
	// 批量写入时间间隔 - 减少以减少数据丢失风险
	batchInterval = 10 * time.Second
	// 最小更新间隔
	minUpdateInterval = 100 * time.Millisecond
	// 缓存过期时间
	cacheExpiration = 5 * time.Minute
	// 缓存清理间隔
	cachePurgeInterval = 10 * time.Minute
	// 强制持久化间隔 - 确保重要数据及时保存
	forcePersistInterval = 60 * time.Second
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

		// 验证Transfer表结构
		if DB != nil {
			if !DB.Migrator().HasTable(&model.Transfer{}) {
				log.Printf("流量管理器: Transfer表不存在，尝试创建...")
				if err := DB.Migrator().CreateTable(&model.Transfer{}); err != nil {
					log.Printf("流量管理器: 创建Transfer表失败: %v", err)
				} else {
					log.Printf("流量管理器: Transfer表创建成功")
				}
			} else {
				log.Printf("流量管理器: Transfer表已存在")

				// 验证表结构
				requiredColumns := []string{"server_id", "in", "out"}
				for _, col := range requiredColumns {
					if !DB.Migrator().HasColumn(&model.Transfer{}, col) {
						log.Printf("流量管理器: 警告 - Transfer表缺少列: %s", col)
					}
				}

				// 显示当前记录数
				var count int64
				if err := DB.Model(&model.Transfer{}).Count(&count); err == nil {
					log.Printf("流量管理器: Transfer表中现有 %d 条记录", count)
				}
			}
		} else {
			log.Printf("流量管理器: 警告 - 数据库连接为空")
		}

		// 启动后台批量写入任务
		go trafficManager.batchWriteWorker()
		log.Printf("流量管理器: 初始化完成，批量写入工作器已启动")
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
		log.Printf("流量管理器: 服务器 %d 初始化流量统计: 入站=%d, 出站=%d", serverID, inBytes, outBytes)

		// 直接更新服务器的累计流量 - 新添加的关键代码
		if server, ok := ServerList[serverID]; ok {
			// 计算流量增量
			var inDelta, outDelta uint64
			if inBytes > 0 {
				inDelta = inBytes
			}
			if outBytes > 0 {
				outDelta = outBytes
			}

			// 只有在有实际增量时才更新累计流量
			if inDelta > 0 || outDelta > 0 {
				// 直接在内存中更新累计值
				newInTotal := server.CumulativeNetInTransfer + inDelta
				newOutTotal := server.CumulativeNetOutTransfer + outDelta

				// 更新服务器对象
				server.CumulativeNetInTransfer = newInTotal
				server.CumulativeNetOutTransfer = newOutTotal

				// 立刻保存到数据库
				updateSQL := `UPDATE servers SET 
								cumulative_net_in_transfer = ?, 
								cumulative_net_out_transfer = ? 
								WHERE id = ?`
				if err := DB.Exec(updateSQL, newInTotal, newOutTotal, serverID).Error; err != nil {
					log.Printf("流量管理器: 更新服务器 %d 累计流量数据库失败: %v", serverID, err)
				} else {
					log.Printf("流量管理器: 更新服务器 %d 累计流量成功: 入站=%d, 出站=%d",
						serverID, newInTotal, newOutTotal)
				}
			}
		}

		return
	}

	// 计算时间差
	duration := now.Sub(stats.LastTime)
	if duration < minUpdateInterval {
		return // 忽略过于频繁的更新
	}

	// 检测服务器是否重启（流量回退）
	restarted := tm.detectServerRestart(serverID, inBytes, outBytes, stats)

	// 计算速率（使用累计流量的增量）
	if duration.Seconds() > 0 && !restarted {
		stats.InSpeed = uint64(float64(inBytes-stats.lastInBytes) / duration.Seconds())
		stats.OutSpeed = uint64(float64(outBytes-stats.lastOutBytes) / duration.Seconds())

		// 直接更新服务器的累计流量 - 新添加的关键代码
		if server, ok := ServerList[serverID]; ok {
			// 计算流量增量
			var inDelta, outDelta uint64
			if inBytes > stats.lastInBytes {
				inDelta = inBytes - stats.lastInBytes
			}
			if outBytes > stats.lastOutBytes {
				outDelta = outBytes - stats.lastOutBytes
			}

			// 只有在有实际增量时才更新累计流量
			if inDelta > 0 || outDelta > 0 {
				// 直接在内存中更新累计值
				newInTotal := server.CumulativeNetInTransfer + inDelta
				newOutTotal := server.CumulativeNetOutTransfer + outDelta

				// 更新服务器对象
				server.CumulativeNetInTransfer = newInTotal
				server.CumulativeNetOutTransfer = newOutTotal

				// 同步更新状态
				if server.State != nil {
					server.State.NetInTransfer = server.State.NetInTransfer + inDelta
					server.State.NetOutTransfer = server.State.NetOutTransfer + outDelta
				}

				log.Printf("流量管理器: 服务器 %d 流量增加: 入站+%d, 出站+%d",
					serverID, inDelta, outDelta)

				// 定期保存到数据库 (改为每5分钟保存一次，避免频繁写入)
				if time.Since(server.LastFlowSaveTime).Minutes() > 5 {
					updateSQL := `UPDATE servers SET 
									cumulative_net_in_transfer = ?, 
									cumulative_net_out_transfer = ? 
									WHERE id = ?`
					if err := DB.Exec(updateSQL, newInTotal, newOutTotal, serverID).Error; err != nil {
						log.Printf("流量管理器: 定期更新服务器 %d 累计流量到数据库失败: %v", serverID, err)
					} else {
						log.Printf("流量管理器: 定期更新服务器 %d 累计流量成功: 入站=%d, 出站=%d",
							serverID, newInTotal, newOutTotal)
						server.LastFlowSaveTime = time.Now()
					}
				}
			}
		}
	} else {
		// 如果数据异常（比如重启），重置速率
		stats.InSpeed = 0
		stats.OutSpeed = 0
		log.Printf("流量管理器: 服务器 %d 数据异常，重置速率", serverID)
	}

	// 更新统计数据
	stats.lastInBytes = inBytes
	stats.lastOutBytes = outBytes
	stats.InBytes = inBytes
	stats.OutBytes = outBytes
	stats.LastTime = now
	stats.UpdateCount++

	log.Printf("流量管理器: 服务器 %d 更新速率: 入站速率=%d/s, 出站速率=%d/s",
		serverID, stats.InSpeed, stats.OutSpeed)

	// 缓存最新数据
	tm.cache.Set(
		tm.getCacheKey(serverID),
		stats,
		cache.DefaultExpiration,
	)

	// 添加到批量写入缓冲区 (对于详细的流量记录)
	transferRecord := &model.Transfer{
		ServerID: serverID,
		In:       inBytes,
		Out:      outBytes,
	}
	tm.batchBuffer = append(tm.batchBuffer, transferRecord)

	log.Printf("流量管理器: 服务器 %d 添加到批量缓冲区: 入站=%d, 出站=%d (缓冲区大小: %d)",
		serverID, inBytes, outBytes, len(tm.batchBuffer))

	// 检查是否需要立即写入
	if len(tm.batchBuffer) >= batchSize || now.Sub(tm.lastBatchWrite) >= batchInterval {
		log.Printf("流量管理器: 触发批量写入详细记录 - 缓冲区大小: %d, 时间间隔: %v",
			len(tm.batchBuffer), now.Sub(tm.lastBatchWrite))
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

	log.Printf("流量管理器: 开始批量写入 %d 条流量记录到数据库", len(tm.batchBuffer))

	// 打印前几条记录用于调试
	for i, record := range tm.batchBuffer {
		if i < 3 { // 只打印前3条
			log.Printf("流量管理器: 记录%d - ServerID=%d, In=%d, Out=%d",
				i+1, record.ServerID, record.In, record.Out)
		}
	}

	// 验证数据库连接
	if DB == nil {
		log.Printf("流量管理器: 数据库连接为空")
		return
	}

	// 检查表是否存在
	if !DB.Migrator().HasTable(&model.Transfer{}) {
		log.Printf("流量管理器: Transfer表不存在，尝试创建...")
		if err := DB.Migrator().CreateTable(&model.Transfer{}); err != nil {
			log.Printf("流量管理器: 创建Transfer表失败: %v", err)
			return
		}
		log.Printf("流量管理器: Transfer表创建成功")
	}

	// 创建批量写入的事务
	tx := DB.Begin()
	if tx.Error != nil {
		log.Printf("流量管理器: 开始事务失败: %v", tx.Error)
		return
	}

	// 批量插入数据
	if err := tx.CreateInBatches(tm.batchBuffer, 100).Error; err != nil {
		log.Printf("流量管理器: 批量插入失败: %v", err)

		// 尝试逐一插入来查找问题
		log.Printf("流量管理器: 尝试逐一插入以识别问题...")
		tx.Rollback()

		// 开始新的事务进行逐一插入
		tx2 := DB.Begin()
		if tx2.Error != nil {
			log.Printf("流量管理器: 开始第二个事务失败: %v", tx2.Error)
			return
		}

		successCount := 0
		for i, record := range tm.batchBuffer {
			if err := tx2.Create(record).Error; err != nil {
				log.Printf("流量管理器: 插入记录%d失败: %v, 记录: ServerID=%d, In=%d, Out=%d",
					i+1, err, record.ServerID, record.In, record.Out)
			} else {
				successCount++
			}
		}

		if err := tx2.Commit().Error; err != nil {
			log.Printf("流量管理器: 提交第二个事务失败: %v", err)
			tx2.Rollback()
			return
		}

		log.Printf("流量管理器: 逐一插入完成，成功插入 %d 条记录", successCount)
	} else {
		// 提交事务
		if err := tx.Commit().Error; err != nil {
			log.Printf("流量管理器: 提交事务失败: %v", err)
			tx.Rollback()
			return
		}

		log.Printf("流量管理器: 成功批量写入 %d 条流量记录到数据库", len(tm.batchBuffer))
	}

	// 验证写入结果
	var count int64
	if err := DB.Model(&model.Transfer{}).Count(&count); err != nil {
		log.Printf("流量管理器: 验证记录数失败: %v", err)
	} else {
		log.Printf("流量管理器: 数据库中现有 %d 条Transfer记录", count)
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

// Shutdown 优雅关闭，确保数据不丢失
func (tm *TrafficManager) Shutdown() error {
	tm.Lock()
	defer tm.Unlock()

	log.Println("流量管理器正在优雅关闭...")

	// 强制写入所有缓冲区数据
	if len(tm.batchBuffer) > 0 {
		tm.writeBatchToDatabase()
	}

	log.Println("流量管理器关闭完成")
	return nil
}

// detectServerRestart 检测服务器是否重启
func (tm *TrafficManager) detectServerRestart(serverID uint64, newIn, newOut uint64, stats *TrafficStats) bool {
	// 方法1：流量值减少检测
	if newIn < stats.InBytes || newOut < stats.OutBytes {
		log.Printf("服务器 %d 流量减少，判定为重启: 入站 %d->%d, 出站 %d->%d",
			serverID, stats.InBytes, newIn, stats.OutBytes, newOut)

		// 更新服务器的累计流量
		if server, ok := ServerList[serverID]; ok && server != nil {
			// 获取当前累计流量
			cumulativeIn := server.CumulativeNetInTransfer
			cumulativeOut := server.CumulativeNetOutTransfer

			// 将减少的流量加入累计值中
			if stats.InBytes > newIn {
				cumulativeIn += stats.InBytes - newIn
			}
			if stats.OutBytes > newOut {
				cumulativeOut += stats.OutBytes - newOut
			}

			// 更新内存中的累计量
			server.CumulativeNetInTransfer = cumulativeIn
			server.CumulativeNetOutTransfer = cumulativeOut

			// 同步到状态
			if server.State != nil {
				server.State.NetInTransfer = newIn + cumulativeIn
				server.State.NetOutTransfer = newOut + cumulativeOut
			}

			// 更新数据库
			updateSQL := `UPDATE servers SET 
							cumulative_net_in_transfer = ?, 
							cumulative_net_out_transfer = ? 
							WHERE id = ?`
			if err := DB.Exec(updateSQL, cumulativeIn, cumulativeOut, serverID).Error; err != nil {
				log.Printf("流量管理器: 重启时更新服务器 %d 累计流量到数据库失败: %v", serverID, err)
			} else {
				log.Printf("流量管理器: 重启时更新服务器 %d 累计流量成功: 入站=%d, 出站=%d",
					serverID, cumulativeIn, cumulativeOut)
				server.LastFlowSaveTime = time.Now()
			}

			// 验证更新结果
			var updatedServer struct {
				CumulativeNetInTransfer  uint64
				CumulativeNetOutTransfer uint64
			}
			if err := DB.Raw(`SELECT cumulative_net_in_transfer, cumulative_net_out_transfer 
								FROM servers WHERE id = ?`, serverID).Scan(&updatedServer).Error; err != nil {
				log.Printf("流量管理器: 重启时验证更新失败: %v", err)
			} else if updatedServer.CumulativeNetInTransfer != cumulativeIn || updatedServer.CumulativeNetOutTransfer != cumulativeOut {
				log.Printf("流量管理器: 重启时更新不成功，数据库值 (入站=%d,出站=%d) 与期望值 (入站=%d,出站=%d) 不一致!",
					updatedServer.CumulativeNetInTransfer, updatedServer.CumulativeNetOutTransfer, cumulativeIn, cumulativeOut)

				// 再次尝试更新
				if err := DB.Exec(updateSQL, cumulativeIn, cumulativeOut, serverID).Error; err != nil {
					log.Printf("流量管理器: 重启时二次更新失败: %v", err)
				} else {
					log.Printf("流量管理器: 重启时二次更新完成")
				}
			} else {
				log.Printf("流量管理器: 重启时更新验证成功")
			}
		}

		return true
	}

	return false
}
