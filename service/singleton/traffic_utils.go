package singleton

import (
	"log"
	"time"
)

// 是否启用流量调试日志
var isTrafficDebugEnabled = false

// EnableTrafficDebug 启用流量调试日志
func EnableTrafficDebug() {
	isTrafficDebugEnabled = true
	log.Println("流量调试模式已启用")
}

// IsTrafficDebugEnabled 检查是否启用流量调试
func IsTrafficDebugEnabled() bool {
	return isTrafficDebugEnabled
}

// TriggerTrafficRecalculation 强制重新计算所有服务器的累计流量并同步到前端显示
func TriggerTrafficRecalculation() int {
	// 首先从数据库同步数据
	SyncAllServerTrafficFromDB()

	// 然后遍历所有服务器，将其流量数据同步到前端
	ServerLock.RLock()
	defer ServerLock.RUnlock()

	count := 0
	for _, server := range ServerList {
		if server == nil || server.State == nil {
			continue
		}

		// 计算正确的流量显示数据
		originalNetInTransfer := uint64(0)
		originalNetOutTransfer := uint64(0)

		// 如果State中的总流量大于累计流量，说明有原始流量部分
		if server.State.NetInTransfer > server.CumulativeNetInTransfer {
			originalNetInTransfer = server.State.NetInTransfer - server.CumulativeNetInTransfer
		}

		if server.State.NetOutTransfer > server.CumulativeNetOutTransfer {
			originalNetOutTransfer = server.State.NetOutTransfer - server.CumulativeNetOutTransfer
		}

		// 重新设置状态显示值
		server.State.NetInTransfer = originalNetInTransfer + server.CumulativeNetInTransfer
		server.State.NetOutTransfer = originalNetOutTransfer + server.CumulativeNetOutTransfer

		// 更新前端显示
		UpdateTrafficStats(server.ID, server.CumulativeNetInTransfer, server.CumulativeNetOutTransfer)

		log.Printf("重新计算服务器 %s 的流量数据: 原始入站=%d, 原始出站=%d, 累计入站=%d, 累计出站=%d, 总入站=%d, 总出站=%d",
			server.Name, originalNetInTransfer, originalNetOutTransfer,
			server.CumulativeNetInTransfer, server.CumulativeNetOutTransfer,
			server.State.NetInTransfer, server.State.NetOutTransfer)

		count++
	}

	log.Printf("完成流量重新计算，共处理了 %d 个服务器", count)
	return count
}

// SaveAllTrafficToDB 将所有服务器的累计流量保存到数据库
func SaveAllTrafficToDB() {
	if ServerList == nil {
		log.Println("ServerList未初始化，跳过保存流量数据")
		return
	}

	ServerLock.RLock()
	defer ServerLock.RUnlock()

	count := 0
	for _, server := range ServerList {
		if server == nil {
			continue
		}

		// 检查是否有有效的流量数据
		if server.CumulativeNetInTransfer > 0 || server.CumulativeNetOutTransfer > 0 {
			// 直接使用SQL更新，更可靠
			updateSQL := `UPDATE servers 
						SET cumulative_net_in_transfer = ?, 
							cumulative_net_out_transfer = ?, 
							updated_at = ? 
						WHERE id = ?`

			result := DB.Exec(updateSQL,
				server.CumulativeNetInTransfer,
				server.CumulativeNetOutTransfer,
				time.Now(),
				server.ID)

			if result.Error != nil {
				log.Printf("保存服务器[%s]累计流量失败: %v", server.Name, result.Error)
			} else if result.RowsAffected > 0 {
				log.Printf("保存服务器[%s]累计流量成功: 入站=%d, 出站=%d",
					server.Name, server.CumulativeNetInTransfer, server.CumulativeNetOutTransfer)
				count++

				// 验证数据是否成功保存
				var verifiedData struct {
					CumulativeNetInTransfer  uint64
					CumulativeNetOutTransfer uint64
				}

				if err := DB.Raw(`SELECT cumulative_net_in_transfer, cumulative_net_out_transfer 
								FROM servers WHERE id = ?`, server.ID).Scan(&verifiedData).Error; err == nil {
					if verifiedData.CumulativeNetInTransfer != server.CumulativeNetInTransfer ||
						verifiedData.CumulativeNetOutTransfer != server.CumulativeNetOutTransfer {
						log.Printf("警告: 服务器[%s]流量数据保存验证失败: 期望(%d,%d)但获得(%d,%d)",
							server.Name,
							server.CumulativeNetInTransfer, server.CumulativeNetOutTransfer,
							verifiedData.CumulativeNetInTransfer, verifiedData.CumulativeNetOutTransfer)

						// 再次尝试保存
						if retryResult := DB.Exec(updateSQL,
							server.CumulativeNetInTransfer,
							server.CumulativeNetOutTransfer,
							time.Now(),
							server.ID); retryResult.Error != nil {
							log.Printf("重试保存服务器[%s]累计流量失败: %v", server.Name, retryResult.Error)
						} else {
							log.Printf("重试保存服务器[%s]累计流量成功", server.Name)
						}
					} else if isTrafficDebugEnabled {
						log.Printf("服务器[%s]流量数据保存验证成功: (%d,%d)",
							server.Name, verifiedData.CumulativeNetInTransfer, verifiedData.CumulativeNetOutTransfer)
					}

					// 同步数据到前端显示
					UpdateTrafficStats(server.ID, server.CumulativeNetInTransfer, server.CumulativeNetOutTransfer)
				}
			}
		}
	}

	if count > 0 {
		log.Printf("共保存了%d个服务器的累计流量数据", count)
	}
}

// AutoSyncTraffic 启动定期同步流量数据的协程
func AutoSyncTraffic() {
	// 先执行一次初始同步
	SyncAllServerTrafficFromDB()

	// 启动定时同步协程
	go func() {
		syncTicker := time.NewTicker(1 * time.Minute)
		saveTicker := time.NewTicker(30 * time.Second)
		defer syncTicker.Stop()
		defer saveTicker.Stop()

		for {
			select {
			case <-syncTicker.C:
				// 从数据库同步到内存
				SyncAllServerTrafficFromDB()
			case <-saveTicker.C:
				// 从内存保存到数据库
				SaveAllTrafficToDB()
			}
		}
	}()

	log.Println("自动流量同步任务已启动，同步间隔=1分钟，保存间隔=30秒")
}
