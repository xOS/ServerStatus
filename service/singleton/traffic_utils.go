package singleton

import (
	"log"
	"time"
)

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
		syncTicker := time.NewTicker(2 * time.Minute)
		saveTicker := time.NewTicker(1 * time.Minute)
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

	log.Println("自动流量同步任务已启动，同步间隔=2分钟，保存间隔=1分钟")
}
