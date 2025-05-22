package singleton

import (
	"log"
	"time"

	"github.com/patrickmn/go-cache"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/jinzhu/copier"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/utils"
)

var Version = "debug"

var (
	Conf  *model.Config
	Cache *cache.Cache
	DB    *gorm.DB
	Loc   *time.Location
)

func InitTimezoneAndCache() {
	var err error
	Loc, err = time.LoadLocation(Conf.Location)
	if err != nil {
		panic(err)
	}

	Cache = cache.New(5*time.Minute, 10*time.Minute)
}

// LoadSingleton 加载子服务并执行
func LoadSingleton() {
	loadNotifications() // 加载通知服务
	loadServers()       // 加载服务器列表

	// 打印调试信息
	debugServersStatus()

	loadCronTasks() // 加载定时任务
	loadAPI()
	initNAT()
	initDDNS()

	// 添加定时检查在线状态的任务，每分钟检查一次
	Cron.AddFunc("*/1 * * * *", CheckServerOnlineStatus)
}

// debugServersStatus 输出服务器状态加载情况的调试信息
func debugServersStatus() {
	log.Println("NG>> ==================== 服务器状态加载情况 ====================")
	for id, server := range ServerList {
		log.Printf("NG>> 服务器 #%d [%s] 状态:", id, server.Name)
		log.Printf("NG>>   - IsOnline: %v", server.IsOnline)

		if server.LastStateBeforeOffline != nil {
			log.Printf("NG>>   - LastStateBeforeOffline: 有")
			log.Printf("NG>>     - CPU: %.2f%%", server.LastStateBeforeOffline.CPU)
			log.Printf("NG>>     - 内存: %d/%d", server.LastStateBeforeOffline.MemUsed, server.Host.MemTotal)
			log.Printf("NG>>     - 流量统计: 入站 %d / 出站 %d",
				server.LastStateBeforeOffline.NetInTransfer,
				server.LastStateBeforeOffline.NetOutTransfer)
		} else {
			log.Printf("NG>>   - LastStateBeforeOffline: 无")
		}

		if server.State != nil {
			log.Printf("NG>>   - State: 有")
			log.Printf("NG>>     - CPU: %.2f%%", server.State.CPU)
			log.Printf("NG>>     - 内存: %d/%d", server.State.MemUsed, server.Host.MemTotal)
			log.Printf("NG>>     - 流量统计: 入站 %d / 出站 %d",
				server.State.NetInTransfer,
				server.State.NetOutTransfer)
		} else {
			log.Printf("NG>>   - State: 无")
		}

		log.Printf("NG>>   - 累计流量: 入站 %d / 出站 %d",
			server.CumulativeNetInTransfer,
			server.CumulativeNetOutTransfer)
	}
	log.Println("NG>> ==========================================================")
}

// InitConfigFromPath 从给出的文件路径中加载配置
func InitConfigFromPath(path string) {
	Conf = &model.Config{}
	err := Conf.Read(path)
	if err != nil {
		panic(err)
	}
}

// InitDBFromPath 从给出的文件路径中加载数据库
func InitDBFromPath(path string) {
	var err error
	DB, err = gorm.Open(sqlite.Open(path), &gorm.Config{
		CreateBatchSize: 200,
	})
	if err != nil {
		panic(err)
	}
	if Conf.Debug {
		DB = DB.Debug()
	}
	err = DB.AutoMigrate(model.Server{}, model.User{},
		model.Notification{}, model.AlertRule{}, model.Monitor{},
		model.MonitorHistory{}, model.Cron{}, model.Transfer{},
		model.ApiToken{}, model.NAT{}, model.DDNSProfile{})
	if err != nil {
		panic(err)
	}

	// 检查并添加新字段
	if !DB.Migrator().HasColumn(&model.Server{}, "cumulative_net_in_transfer") {
		err = DB.Migrator().AddColumn(&model.Server{}, "cumulative_net_in_transfer")
		if err != nil {
			log.Println("NG>> 添加cumulative_net_in_transfer字段失败:", err)
		}
	}

	if !DB.Migrator().HasColumn(&model.Server{}, "cumulative_net_out_transfer") {
		err = DB.Migrator().AddColumn(&model.Server{}, "cumulative_net_out_transfer")
		if err != nil {
			log.Println("NG>> 添加cumulative_net_out_transfer字段失败:", err)
		}
	}

	if !DB.Migrator().HasColumn(&model.Server{}, "last_state_json") {
		err = DB.Migrator().AddColumn(&model.Server{}, "last_state_json")
		if err != nil {
			log.Println("NG>> 添加last_state_json字段失败:", err)
		}
	}

	if !DB.Migrator().HasColumn(&model.Server{}, "last_online") {
		err = DB.Migrator().AddColumn(&model.Server{}, "last_online")
		if err != nil {
			log.Println("NG>> 添加last_online字段失败:", err)
		}
	}

	// 创建存储Host信息的表
	err = DB.Exec(`
		CREATE TABLE IF NOT EXISTS last_reported_host (
			server_id INTEGER PRIMARY KEY,
			host_json TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`).Error

	if err != nil {
		log.Println("NG>> 创建last_reported_host表失败:", err)
	} else {
		log.Println("NG>> last_reported_host表检查/创建成功")
	}
}

// RecordTransferHourlyUsage 对流量记录进行打点
func RecordTransferHourlyUsage() {
	ServerLock.Lock()
	defer ServerLock.Unlock()
	now := time.Now()
	nowTrimSeconds := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
	var txs []model.Transfer

	for id, server := range ServerList {
		if server.State == nil {
			continue
		}

		// 增量流量
		incrementalIn := utils.Uint64SubInt64(server.State.NetInTransfer-server.CumulativeNetInTransfer, server.PrevTransferInSnapshot)
		incrementalOut := utils.Uint64SubInt64(server.State.NetOutTransfer-server.CumulativeNetOutTransfer, server.PrevTransferOutSnapshot)

		// 避免重启后的异常大值
		if incrementalIn > server.State.NetInTransfer || incrementalOut > server.State.NetOutTransfer {
			log.Printf("NG>> 服务器 %s 流量增量异常，可能是重启导致: 入站 %d / 出站 %d，使用原始值代替",
				server.Name, incrementalIn, incrementalOut)

			incrementalIn = server.State.NetInTransfer - server.CumulativeNetInTransfer
			incrementalOut = server.State.NetOutTransfer - server.CumulativeNetOutTransfer

			if incrementalIn < 0 {
				incrementalIn = 0
			}
			if incrementalOut < 0 {
				incrementalOut = 0
			}
		}

		tx := model.Transfer{
			ServerID: id,
			In:       incrementalIn,
			Out:      incrementalOut,
		}

		if tx.In == 0 && tx.Out == 0 {
			continue
		}

		log.Printf("NG>> 服务器 %s 本小时流量增量: 入站 %d / 出站 %d",
			server.Name, incrementalIn, incrementalOut)

		// 记录本次计算后的数据点，用于下次增量计算
		server.PrevTransferInSnapshot = int64(server.State.NetInTransfer - server.CumulativeNetInTransfer)
		server.PrevTransferOutSnapshot = int64(server.State.NetOutTransfer - server.CumulativeNetOutTransfer)

		tx.CreatedAt = nowTrimSeconds
		txs = append(txs, tx)
	}

	if len(txs) > 0 {
		log.Printf("NG>> Cron 流量统计入库: %d 条记录", len(txs))
		err := DB.Create(txs).Error
		if err != nil {
			log.Printf("NG>> Cron 流量统计入库失败: %v", err)
		}
	}
}

// CleanMonitorHistory 清理无效或过时的 监控记录 和 流量记录
func CleanMonitorHistory() {
	// 清理已被删除的服务器的监控记录与流量记录
	DB.Unscoped().Delete(&model.MonitorHistory{}, "created_at < ? OR monitor_id NOT IN (SELECT `id` FROM monitors)", time.Now().AddDate(0, 0, -30))
	// 由于网络监控记录的数据较多，并且前端仅使用了 1 天的数据
	// 考虑到 sqlite 数据量问题，仅保留一天数据，
	// server_id = 0 的数据会用于/service页面的可用性展示
	DB.Unscoped().Delete(&model.MonitorHistory{}, "(created_at < ? AND server_id != 0) OR monitor_id NOT IN (SELECT `id` FROM monitors)", time.Now().AddDate(0, 0, -1))
	DB.Unscoped().Delete(&model.Transfer{}, "server_id NOT IN (SELECT `id` FROM servers)")

	// 清理过期的累计流量数据（保留30天）
	cleanCumulativeTransferData(30)

	// 计算可清理流量记录的时长
	var allServerKeep time.Time
	specialServerKeep := make(map[uint64]time.Time)
	var specialServerIDs []uint64
	var alerts []model.AlertRule
	DB.Find(&alerts)
	for _, alert := range alerts {
		for _, rule := range alert.Rules {
			// 是不是流量记录规则
			if !rule.IsTransferDurationRule() {
				continue
			}
			dataCouldRemoveBefore := rule.GetTransferDurationStart().UTC()
			// 判断规则影响的机器范围
			if rule.Cover == model.RuleCoverAll {
				// 更新全局可以清理的数据点
				if allServerKeep.IsZero() || allServerKeep.After(dataCouldRemoveBefore) {
					allServerKeep = dataCouldRemoveBefore
				}
			} else {
				// 更新特定机器可以清理数据点
				for id := range rule.Ignore {
					if specialServerKeep[id].IsZero() || specialServerKeep[id].After(dataCouldRemoveBefore) {
						specialServerKeep[id] = dataCouldRemoveBefore
						specialServerIDs = append(specialServerIDs, id)
					}
				}
			}
		}
	}
	for id, couldRemove := range specialServerKeep {
		DB.Unscoped().Delete(&model.Transfer{}, "server_id = ? AND datetime(`created_at`) < datetime(?)", id, couldRemove)
	}
	if allServerKeep.IsZero() {
		DB.Unscoped().Delete(&model.Transfer{}, "server_id NOT IN (?)", specialServerIDs)
	} else {
		DB.Unscoped().Delete(&model.Transfer{}, "server_id NOT IN (?) AND datetime(`created_at`) < datetime(?)", specialServerIDs, allServerKeep)
	}
}

// cleanCumulativeTransferData 清理超过指定天数的累计流量数据
func cleanCumulativeTransferData(days int) {
	// 获取保留期限的开始时间点
	retentionStart := time.Now().AddDate(0, 0, -days)
	log.Println("NG>> 开始清理", days, "天前的累计流量数据")

	// 从Transfer表中查询保留期内最早的有效数据日期
	var oldestValidTransfers []model.Transfer
	if err := DB.Where("datetime(`created_at`) >= datetime(?)", retentionStart).Order("created_at ASC").Limit(10).Find(&oldestValidTransfers).Error; err != nil {
		log.Println("NG>> 查询保留期内流量记录失败:", err)
		return
	}

	if len(oldestValidTransfers) == 0 {
		log.Println("NG>> 未找到保留期内的有效流量记录")
		return
	}

	// 计算每个服务器在保留期内的总流量
	serverFlows := make(map[uint64]struct {
		In  uint64
		Out uint64
	})

	// 查询所有在保留期内的流量记录
	var transfers []model.Transfer
	if err := DB.Where("datetime(`created_at`) >= datetime(?)", retentionStart).Find(&transfers).Error; err != nil {
		log.Println("NG>> 查询流量记录失败:", err)
		return
	}

	// 计算每个服务器在保留期内的总流量
	for _, transfer := range transfers {
		flow := serverFlows[transfer.ServerID]
		flow.In += transfer.In
		flow.Out += transfer.Out
		serverFlows[transfer.ServerID] = flow
	}

	// 更新每个服务器的累计流量为保留期内的总流量
	ServerLock.Lock()
	defer ServerLock.Unlock()

	var serversToUpdate []model.Server
	for id, flow := range serverFlows {
		if server, ok := ServerList[id]; ok {
			// 重置服务器的累计流量为保留期内的总流量
			server.CumulativeNetInTransfer = flow.In
			server.CumulativeNetOutTransfer = flow.Out

			// 添加到待更新列表
			serversToUpdate = append(serversToUpdate, model.Server{
				Common:                   model.Common{ID: id},
				CumulativeNetInTransfer:  flow.In,
				CumulativeNetOutTransfer: flow.Out,
			})
		}
	}

	// 批量更新数据库中的累计流量值
	if len(serversToUpdate) > 0 {
		for i := range serversToUpdate {
			DB.Model(&serversToUpdate[i]).Updates(map[string]interface{}{
				"cumulative_net_in_transfer":  serversToUpdate[i].CumulativeNetInTransfer,
				"cumulative_net_out_transfer": serversToUpdate[i].CumulativeNetOutTransfer,
			})
		}
		log.Println("NG>> 已更新", len(serversToUpdate), "个服务器的累计流量数据")
	}
}

// IPDesensitize 根据设置选择是否对IP进行打码处理 返回处理后的IP(关闭打码则返回原IP)
func IPDesensitize(ip string) string {
	if Conf.EnablePlainIPInNotification {
		return ip
	}
	return utils.IPDesensitize(ip)
}

// CheckServerOnlineStatus 检查服务器在线状态，将超时未上报的服务器标记为离线
func CheckServerOnlineStatus() {
	ServerLock.Lock()
	defer ServerLock.Unlock()

	now := time.Now()
	offlineTimeout := time.Minute * 2 // 2分钟无心跳视为离线

	// 检查是否需要重置累计流量数据
	shouldResetTransferStats := checkShouldResetTransferStats()

	for _, server := range ServerList {
		// 已经标记为在线且长时间未活动，标记为离线
		if server.IsOnline && now.Sub(server.LastActive) > offlineTimeout {
			server.IsOnline = false

			// 如果还没有保存离线前状态，保存当前状态
			if server.LastStateBeforeOffline == nil && server.State != nil {
				lastState := model.HostState{}
				if err := copier.Copy(&lastState, server.State); err == nil {
					server.LastStateBeforeOffline = &lastState

					// 将最后状态序列化为JSON并保存到数据库
					lastStateJSON, err := utils.Json.Marshal(lastState)
					if err == nil {
						server.LastStateJSON = string(lastStateJSON)
						server.LastOnline = server.LastActive

						// 更新数据库
						DB.Model(server).Updates(map[string]interface{}{
							"last_state_json": server.LastStateJSON,
							"last_online":     server.LastOnline,
						})

						log.Printf("NG>> 服务器 %s 离线，已保存最后状态", server.Name)
					} else {
						log.Printf("NG>> 序列化服务器 %s 的最后状态失败: %v", server.Name, err)
					}
				}
			}

			// 离线前保存累计流量数据到数据库
			if server.State != nil {
				DB.Model(server).Updates(map[string]interface{}{
					"cumulative_net_in_transfer":  server.State.NetInTransfer,
					"cumulative_net_out_transfer": server.State.NetOutTransfer,
				})
			}
		}

		// 如果需要重置累计流量，则重置服务器的累计流量
		if shouldResetTransferStats {
			log.Printf("NG>> 服务器 %s 流量统计周期结束，重置累计流量 (入站: %d, 出站: %d)",
				server.Name, server.CumulativeNetInTransfer, server.CumulativeNetOutTransfer)

			server.CumulativeNetInTransfer = 0
			server.CumulativeNetOutTransfer = 0

			// 更新数据库
			DB.Model(server).Updates(map[string]interface{}{
				"cumulative_net_in_transfer":  0,
				"cumulative_net_out_transfer": 0,
			})
		}
	}
}

// checkShouldResetTransferStats 检查是否应该重置流量统计数据，基于CycleTransferStats的周期
func checkShouldResetTransferStats() bool {
	// 获取当前时间
	now := time.Now()

	// 由于有多个alert规则，可能有多个不同的周期，我们检查所有的周期规则
	for _, cycleStats := range AlertsCycleTransferStatsStore {
		if cycleStats == nil {
			continue
		}

		// 如果当前时间已经超过了周期结束时间，说明需要重置流量
		if !cycleStats.To.IsZero() && now.After(cycleStats.To) {
			log.Printf("NG>> 流量统计周期已结束 (%s 到 %s)，重置所有服务器的累计流量",
				cycleStats.From.Format("2006-01-02 15:04:05"),
				cycleStats.To.Format("2006-01-02 15:04:05"))
			return true
		}
	}

	// 检查是否存在即将到来的新周期
	var alerts []model.AlertRule
	DB.Find(&alerts)

	for _, alert := range alerts {
		for _, rule := range alert.Rules {
			// 只检查与流量有关的规则
			if rule.IsTransferDurationRule() {
				// 获取下一个周期的开始时间
				nextCycleStart := rule.GetTransferDurationStart()

				// 如果距离上次重置已经超过了12小时，并且当前时间已经进入了新的周期
				// 这里使用12小时作为缓冲，避免重复重置
				lastResetKey := "last_transfer_reset"
				lastResetTimeStr, _ := Cache.Get(lastResetKey)
				var lastResetTime time.Time

				if lastResetTimeStr != nil {
					lastResetTime = lastResetTimeStr.(time.Time)
				}

				if now.After(nextCycleStart) && now.Sub(lastResetTime) > 12*time.Hour {
					log.Printf("NG>> 检测到新的流量统计周期开始，重置所有服务器的累计流量")
					Cache.Set(lastResetKey, now, cache.DefaultExpiration)
					return true
				}
			}
		}
	}

	return false
}
