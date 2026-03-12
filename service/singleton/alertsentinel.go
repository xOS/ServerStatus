package singleton

import (
	"fmt"
	"log"
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/xos/serverstatus/db"
	"github.com/xos/serverstatus/model"
)

const (
	_RuleCheckNoData = iota
	_RuleCheckFail
	_RuleCheckPass
)

type NotificationHistory struct {
	Duration time.Duration
	Until    time.Time
}

// 事件规则
var (
	AlertsLock                    sync.RWMutex
	Alerts                        []*model.AlertRule
	alertsStore                   map[uint64]map[uint64][][]interface{} // [alert_id][server_id] -> 对应事件规则的检查结果
	alertsPrevState               map[uint64]map[uint64]uint            // [alert_id][server_id] -> 对应事件规则的上一次事件状态
	AlertsCycleTransferStatsStore map[uint64]*model.CycleTransferStats  // [alert_id] -> 对应事件规则的周期流量统计
	serverLastOnlineTime          map[uint64]time.Time                  // [server_id] -> 服务器离线前的最后在线时间（用于恢复通知计算离线时长）
	alertStartTime                time.Time                             // 记录 AlertSentinel 启动时间，用于启动保护期
)

// addCycleTransferStatsInfo 向AlertsCycleTransferStatsStore中添加周期流量事件统计信息
func addCycleTransferStatsInfo(alert *model.AlertRule) {
	if !alert.Enabled() {
		return
	}
	for j := 0; j < len(alert.Rules); j++ {
		if !alert.Rules[j].IsTransferDurationRule() {
			continue
		}
		if AlertsCycleTransferStatsStore[alert.ID] == nil {
			from := alert.Rules[j].GetTransferDurationStart()
			to := alert.Rules[j].GetTransferDurationEnd()
			AlertsCycleTransferStatsStore[alert.ID] = &model.CycleTransferStats{
				Name:          alert.Name,
				From:          from,
				To:            to,
				Max:           uint64(alert.Rules[j].Max),
				Min:           uint64(alert.Rules[j].Min),
				ServerName:    make(map[uint64]string),
				Transfer:      make(map[uint64]uint64),
				NextUpdate:    make(map[uint64]time.Time),
				LastResetTime: make(map[uint64]time.Time),
			}
		}
	}
}

// 添加大小限制常量，防止内存无限增长
const (
	maxAlertsStoreSize    = 1000 // 最多存储1000个alert
	maxServerDataPerAlert = 100  // 每个alert最多存储100个server的数据
	maxHistoryPerServer   = 10   // 每个server最多存储10条历史记录
)

// AlertSentinelStart 事件监控器启动
func AlertSentinelStart() {
	alertsStore = make(map[uint64]map[uint64][][]interface{})
	alertsPrevState = make(map[uint64]map[uint64]uint)
	AlertsCycleTransferStatsStore = make(map[uint64]*model.CycleTransferStats)
	serverLastOnlineTime = make(map[uint64]time.Time)
	alertStartTime = time.Now() // 记录启动时间，用于启动保护期
	AlertsLock.Lock()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("AlertSentinelStart goroutine panic恢复: %v", r)
			// 打印调用栈以便调试
			if Conf.Debug {
				log.Printf("调用栈: %s", debug.Stack())
			}
			// 不要自动重启，避免goroutine泄漏
		}
	}()

	// 根据数据库类型选择不同的加载方式
	if Conf.DatabaseType == "badger" {
		// 使用BadgerDB加载事件规则
		if db.DB != nil {
			// BadgerDB模式，使用AlertRuleOps查询AlertRule
			alertOps := db.NewAlertRuleOps(db.DB)
			alerts, err := alertOps.GetAllAlertRules()
			if err != nil {
				log.Printf("从BadgerDB加载事件规则失败: %v", err)
				AlertsLock.Unlock()
				return
			}
			Alerts = alerts
		} else {
			log.Println("BadgerDB未初始化，跳过加载事件规则")
			AlertsLock.Unlock()
			return
		}
	} else {
		// 使用SQLite加载事件规则
		err := executeWithRetry(func() error {
			return DB.Find(&Alerts).Error
		})
		if err != nil {
			log.Printf("从SQLite加载事件规则失败: %v", err)
			AlertsLock.Unlock()
			return // 不要panic，而是返回并稍后重试
		}
	}

	for _, alert := range Alerts {
		// 旧版本可能不存在通知组 为其添加默认值
		if alert.NotificationTag == "" {
			alert.NotificationTag = "default"

			if Conf.DatabaseType == "badger" {
				// 使用BadgerDB更新
				if db.DB != nil {
					// 直接保存AlertRule
					err := db.DB.SaveModel("alert_rule", alert.ID, alert)
					if err != nil {
						log.Printf("更新AlertRule通知组到BadgerDB失败: %v", err)
					}
				}
			} else {
				// 使用异步队列保存，避免锁冲突
				alertData := map[string]interface{}{
					"notification_tag": "default",
				}
				AsyncDBUpdate(alert.ID, "alert_rules", alertData, func(err error) {
					if err != nil {
						log.Printf("更新AlertRule通知组失败: %v", err)
					}
				})
			}
		}
		alertsStore[alert.ID] = make(map[uint64][][]interface{})
		alertsPrevState[alert.ID] = make(map[uint64]uint)
		addCycleTransferStatsInfo(alert)
	}
	AlertsLock.Unlock()

	time.Sleep(time.Second * 10)
	var lastPrint time.Time
	var checkCount uint64

	// 内存清理计时器 - 调整为每1小时清理一次，降低清理频率
	lastCleanupTime := time.Now()

	for {
		startedAt := time.Now()
		checkStatus()
		checkCount++

		if startedAt.Sub(lastCleanupTime) >= 1*time.Hour {
			cleanupAlertMemoryData()
			lastCleanupTime = startedAt
		}

		// 调整内存使用阈值，与全局内存策略保持一致
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		if m.Alloc > 1200*1024*1024 { // 1200MB阈值，与全局策略协调
			log.Printf("AlertSentinel内存使用过高: %dMB，立即执行清理", m.Alloc/1024/1024)
			cleanupAlertMemoryData()
			runtime.GC()                // 强制垃圾回收
			lastCleanupTime = startedAt // 更新清理时间，避免频繁清理
		}

		if lastPrint.Before(startedAt.Add(-1 * time.Hour)) {
			if Conf.Debug {
				log.Println("NG>> 事件规则检测每小时", checkCount, "次", startedAt, time.Now())
			}
			checkCount = 0
			lastPrint = startedAt
		}

		// 优化检查间隔：根据事件规则数量动态调整
		var checkInterval time.Duration
		AlertsLock.RLock()
		alertCount := len(Alerts) // 使用Alerts而不是alertsStore，更准确
		AlertsLock.RUnlock()

		if alertCount == 0 {
			checkInterval = 15 * time.Second // 没有事件规则时15秒检查一次
		} else {
			checkInterval = 5 * time.Second // 有事件规则时5秒检查一次
		}

		// 计算剩余时间并睡眠，避免忙等待
		elapsed := time.Since(startedAt)
		if elapsed < checkInterval {
			remainingTime := checkInterval - elapsed
			if remainingTime > 0 {
				time.Sleep(remainingTime)
			}
		}
	}
}

func OnRefreshOrAddAlert(alert model.AlertRule) {
	AlertsLock.Lock()
	defer AlertsLock.Unlock()
	delete(alertsStore, alert.ID)
	delete(alertsPrevState, alert.ID)
	var isEdit bool
	for i := 0; i < len(Alerts); i++ {
		if Alerts[i].ID == alert.ID {
			Alerts[i] = &alert
			isEdit = true
		}
	}
	if !isEdit {
		Alerts = append(Alerts, &alert)
	}
	alertsStore[alert.ID] = make(map[uint64][][]interface{})
	alertsPrevState[alert.ID] = make(map[uint64]uint)
	delete(AlertsCycleTransferStatsStore, alert.ID)
	addCycleTransferStatsInfo(&alert)
}

func OnDeleteAlert(id uint64) {
	AlertsLock.Lock()
	defer AlertsLock.Unlock()
	delete(alertsStore, id)
	delete(alertsPrevState, id)
	for i := 0; i < len(Alerts); i++ {
		if Alerts[i].ID == id {
			Alerts = append(Alerts[:i], Alerts[i+1:]...)
			i--
		}
	}
	delete(AlertsCycleTransferStatsStore, id)
}

// 用于控制空规则警告的输出频率
var (
	emptyRulesWarningTime  = make(map[uint64]time.Time)
	emptyRulesWarningMutex sync.Mutex
)

// checkStatus 检查通知规则并发送通知
func checkStatus() {
	// 优化：先复制需要的数据，避免长时间持锁
	var alertsCopy []*model.AlertRule
	var serversCopy map[uint64]*model.Server
	var alertsStoreCopy map[uint64]map[uint64][][]interface{}
	var alertsPrevStateCopy map[uint64]map[uint64]uint
	var cycleTransferStatsCopy map[uint64]*model.CycleTransferStats

	// 第一步：快速复制alerts数据
	AlertsLock.RLock()
	alertsCopy = make([]*model.AlertRule, 0, len(Alerts))
	for _, alert := range Alerts {
		if alert != nil && alert.Enabled() {
			if len(alert.Rules) > 0 {
				// 浅拷贝alert，避免深拷贝带来的性能开销
				// 由于我们只读取数据，浅拷贝是安全的
				alertsCopy = append(alertsCopy, alert)
			} else {
				// 处理已启用但无规则的通知
				emptyRulesWarningMutex.Lock()
				if lastWarning, ok := emptyRulesWarningTime[alert.ID]; !ok || time.Since(lastWarning) > time.Hour {
					log.Printf("警告: 通知规则 '%s' (ID: %d) 已启用但没有任何规则。", alert.Name, alert.ID)
					emptyRulesWarningTime[alert.ID] = time.Now()
				}
				emptyRulesWarningMutex.Unlock()
			}
		}
	}

	// 复制必要的存储数据
	alertsStoreCopy = make(map[uint64]map[uint64][][]interface{})
	for alertID, serverMap := range alertsStore {
		alertsStoreCopy[alertID] = make(map[uint64][][]interface{})
		for serverID, data := range serverMap {
			alertsStoreCopy[alertID][serverID] = append([][]interface{}{}, data...)
		}
	}

	alertsPrevStateCopy = make(map[uint64]map[uint64]uint)
	for alertID, serverMap := range alertsPrevState {
		alertsPrevStateCopy[alertID] = make(map[uint64]uint)
		for serverID, state := range serverMap {
			alertsPrevStateCopy[alertID][serverID] = state
		}
	}

	cycleTransferStatsCopy = make(map[uint64]*model.CycleTransferStats)
	for alertID, stats := range AlertsCycleTransferStatsStore {
		if stats != nil {
			statsCopy := &model.CycleTransferStats{
				Name:       stats.Name,
				From:       stats.From,
				To:         stats.To,
				Max:        stats.Max,
				Min:        stats.Min,
				ServerName: make(map[uint64]string),
				Transfer:   make(map[uint64]uint64),
				NextUpdate: make(map[uint64]time.Time),
			}
			for k, v := range stats.ServerName {
				statsCopy.ServerName[k] = v
			}
			for k, v := range stats.Transfer {
				statsCopy.Transfer[k] = v
			}
			for k, v := range stats.NextUpdate {
				statsCopy.NextUpdate[k] = v
			}
			cycleTransferStatsCopy[alertID] = statsCopy
		}
	}
	AlertsLock.RUnlock()

	// 第二步：快速复制servers数据
	ServerLock.RLock()
	serversCopy = make(map[uint64]*model.Server, len(ServerList))
	for id, server := range ServerList {
		if server != nil {
			serversCopy[id] = server
		}
	}
	ServerLock.RUnlock()

	// 第三步：不持锁进行检查计算
	// 存储需要更新的数据
	type updateData struct {
		alertID  uint64
		serverID uint64
		snapshot [][]interface{}
		passed   bool
		max      int
	}
	var updates []updateData

	// 初始化alerts的字段
	for _, alert := range alertsCopy {
		// 安全检查：确保alert和Rules不为nil
		if alert == nil || alert.Rules == nil {
			if Conf.Debug {
				log.Printf("警告：发现nil通知规则或规则列表，跳过初始化")
			}
			continue
		}

		// 初始化每个Rule的字段，避免nil指针
		for i := range alert.Rules {
			if alert.Rules[i].NextTransferAt == nil {
				alert.Rules[i].NextTransferAt = make(map[uint64]time.Time)
			}
			if alert.Rules[i].LastCycleStatus == nil {
				alert.Rules[i].LastCycleStatus = make(map[uint64]interface{})
			}
			if alert.Rules[i].Ignore == nil {
				alert.Rules[i].Ignore = make(map[uint64]bool)
			}
		}

		for _, server := range serversCopy {
			// 安全检查：确保server不为nil
			if server == nil {
				if Conf.Debug {
					log.Printf("警告：发现nil服务器，跳过检查")
				}
				continue
			}

			// 安全检查：确保alert不为nil
			if alert == nil {
				if Conf.Debug {
					log.Printf("警告：发现nil通知规则，跳过检查")
				}
				continue
			}

			// 确保alertsStore对应的键存在
			if alertsStoreCopy[alert.ID] == nil {
				alertsStoreCopy[alert.ID] = make(map[uint64][][]interface{})
			}
			if alertsStoreCopy[alert.ID][server.ID] == nil {
				alertsStoreCopy[alert.ID][server.ID] = make([][]interface{}, 0)
			}

			// 监测点
			// 根据数据库类型决定是否传入DB参数
			var snapshot []interface{}
			cycleStats := cycleTransferStatsCopy[alert.ID]

			// 安全检查：确保alert不为nil再调用Snapshot
			if alert == nil {
				if Conf.Debug {
					log.Printf("警告：alert为nil，跳过Snapshot调用")
				}
				continue
			}

			if Conf.DatabaseType == "badger" {
				// BadgerDB模式下，传入nil作为DB参数
				snapshot = alert.Snapshot(cycleStats, server, nil)
			} else {
				// SQLite模式下，传入DB参数
				snapshot = alert.Snapshot(cycleStats, server, DB)
			}

			// 安全检查：确保snapshot不为nil
			if snapshot == nil {
				snapshot = make([]interface{}, 0)
			}

			alertsStoreCopy[alert.ID][server.ID] = append(alertsStoreCopy[alert.ID][server.ID], snapshot)

			// 强制执行大小限制，防止内存无限增长
			if len(alertsStoreCopy[alert.ID][server.ID]) > maxHistoryPerServer {
				alertsStoreCopy[alert.ID][server.ID] = alertsStoreCopy[alert.ID][server.ID][len(alertsStoreCopy[alert.ID][server.ID])-maxHistoryPerServer:]
			}

			// 发送通知，分为触发通知和恢复通知
			max, passed := alert.Check(alertsStoreCopy[alert.ID][server.ID])

			// 记录更新数据
			updates = append(updates, updateData{
				alertID:  alert.ID,
				serverID: server.ID,
				snapshot: alertsStoreCopy[alert.ID][server.ID],
				passed:   passed,
				max:      max,
			})

			// 保存当前服务器状态信息 - 手动拷贝避免反射内存泄漏
			curServer := model.Server{
				Common: model.Common{
					ID:        server.ID,
					CreatedAt: server.CreatedAt,
					UpdatedAt: server.UpdatedAt,
				},
				Name:                     server.Name,
				Tag:                      server.Tag,
				Secret:                   server.Secret,
				Note:                     server.Note,
				PublicNote:               server.PublicNote,
				DisplayIndex:             server.DisplayIndex,
				HideForGuest:             server.HideForGuest,
				EnableDDNS:               server.EnableDDNS,
				DDNSProfiles:             server.DDNSProfiles,
				DDNSProfilesRaw:          server.DDNSProfilesRaw,
				Host:                     server.Host,  // 结构体指针，浅拷贝即可
				State:                    server.State, // 结构体指针，浅拷贝即可
				LastActive:               server.LastActive,
				LastStateBeforeOffline:   server.LastStateBeforeOffline,
				IsOnline:                 server.IsOnline,
				LastStateJSON:            server.LastStateJSON,
				LastOnline:               server.LastOnline,
				HostJSON:                 server.HostJSON,
				TaskClose:                nil, // 不拷贝channel，避免并发问题
				TaskCloseLock:            nil, // 不拷贝mutex，避免并发问题
				TaskStream:               nil, // 不拷贝stream，避免并发问题
				PrevTransferInSnapshot:   server.PrevTransferInSnapshot,
				PrevTransferOutSnapshot:  server.PrevTransferOutSnapshot,
				CumulativeNetInTransfer:  server.CumulativeNetInTransfer,
				CumulativeNetOutTransfer: server.CumulativeNetOutTransfer,
				LastFlowSaveTime:         server.LastFlowSaveTime,
			}

			// 本次未通过检查
			if !passed {
				// 确保alertsPrevState正确初始化
				if alertsPrevStateCopy[alert.ID] == nil {
					alertsPrevStateCopy[alert.ID] = make(map[uint64]uint)
				}

				// 检查是否为离线通知且服务器从未上线过
				isOfflineAlert := false
				for _, rule := range alert.Rules {
					if rule.Type == "offline" {
						isOfflineAlert = true
						break
					}
				}

				// 启动保护期：启动后 2 分钟内不触发离线通知，等待 Agent 重新连接
				if isOfflineAlert && time.Since(alertStartTime) < 2*time.Minute {
					if Conf.Debug {
						log.Printf("启动保护期：跳过服务器 %s (ID:%d) 的离线通知", server.Name, server.ID)
					}
					alertsPrevStateCopy[alert.ID][server.ID] = _RuleCheckPass
					continue
				}

				// 如果是离线通知且服务器从未上线过，不触发通知
				if isOfflineAlert && server.LastActive.IsZero() {
					// 从未上线的服务器，设置为通过状态，避免误报
					alertsPrevStateCopy[alert.ID][server.ID] = _RuleCheckPass
				} else {
					// 始终触发模式或上次检查不为失败时触发通知（跳过单次触发+上次失败的情况）
					if alert.TriggerMode == model.ModeAlwaysTrigger || alertsPrevStateCopy[alert.ID][server.ID] != _RuleCheckFail {
						alertsPrevStateCopy[alert.ID][server.ID] = _RuleCheckFail
						log.Printf("[事件]\n%s\n规则：%s %s", server.Name, alert.Name, *NotificationMuteLabel.ServerIncident(alert.ID, server.ID))

						// 记录最后在线时间（用于恢复通知计算真实离线时长）
						if isOfflineAlert {
							if _, exists := serverLastOnlineTime[server.ID]; !exists {
								// 保存服务器离线前的最后在线时间
								if !server.LastOnline.IsZero() {
									serverLastOnlineTime[server.ID] = server.LastOnline
								} else if !server.LastActive.IsZero() {
									serverLastOnlineTime[server.ID] = server.LastActive
								} else {
									serverLastOnlineTime[server.ID] = time.Now()
								}
							}
						}

						// 生成详细的通知消息
						message := generateDetailedAlertMessage(alert, server, alertsStoreCopy[alert.ID][server.ID])

						SafeSendTriggerTasks(alert.FailTriggerTasks, curServer.ID)
						SafeSendNotification(alert.NotificationTag, message, NotificationMuteLabel.ServerIncident(alert.ID, server.ID), &curServer)
						// 清除恢复通知的静音缓存
						UnMuteNotification(alert.NotificationTag, NotificationMuteLabel.ServerIncidentResolved(alert.ID, server.ID))
					}
				}
			} else {
				// 确保alertsPrevState正确初始化
				if alertsPrevStateCopy[alert.ID] == nil {
					alertsPrevStateCopy[alert.ID] = make(map[uint64]uint)
				}

				// 本次通过检查但上一次的状态为失败，则发送恢复通知
				if alertsPrevStateCopy[alert.ID][server.ID] == _RuleCheckFail {
					// 生成详细的恢复消息
					message := generateDetailedRecoveryMessage(alert, server)

					SafeSendTriggerTasks(alert.RecoverTriggerTasks, curServer.ID)
					SafeSendNotification(alert.NotificationTag, message, NotificationMuteLabel.ServerIncidentResolved(alert.ID, server.ID), &curServer)
					// 清除失败通知的静音缓存
					UnMuteNotification(alert.NotificationTag, NotificationMuteLabel.ServerIncident(alert.ID, server.ID))
				}
				alertsPrevStateCopy[alert.ID][server.ID] = _RuleCheckPass
			}
		}
	}

	// 第四步：批量更新全局状态（持锁时间短）
	AlertsLock.Lock()
	// 更新alertsStore
	for _, update := range updates {
		if alertsStore[update.alertID] == nil {
			alertsStore[update.alertID] = make(map[uint64][][]interface{})
		}
		alertsStore[update.alertID][update.serverID] = update.snapshot

		// 清理旧数据
		if update.max > 0 && update.max < len(alertsStore[update.alertID][update.serverID]) {
			alertsStore[update.alertID][update.serverID] = alertsStore[update.alertID][update.serverID][len(alertsStore[update.alertID][update.serverID])-update.max:]
		}
	}

	// 更新alertsPrevState
	for alertID, serverMap := range alertsPrevStateCopy {
		if alertsPrevState[alertID] == nil {
			alertsPrevState[alertID] = make(map[uint64]uint)
		}
		for serverID, state := range serverMap {
			alertsPrevState[alertID][serverID] = state
		}
	}
	AlertsLock.Unlock()
}

// UpdateTrafficStats 更新服务器流量统计到AlertsCycleTransferStatsStore
// 这个函数直接更新流量数据，确保前端显示正确
func UpdateTrafficStats(serverID uint64, inTransfer, outTransfer uint64) {
	// 优化：先批量收集需要的数据，避免嵌套锁
	var serverName string
	var alertsToUpdate []*model.AlertRule

	// 第一步：快速获取服务器信息
	ServerLock.RLock()
	if server := ServerList[serverID]; server != nil {
		serverName = server.Name
		// 确保服务器状态中的流量数据是最新的，不依赖通知规则
		if server.State != nil {
			server.State.NetInTransfer = inTransfer
			server.State.NetOutTransfer = outTransfer
		}
	}
	ServerLock.RUnlock()

	// 第二步：收集需要更新的通知规则和统计数据
	AlertsLock.RLock()
	if len(Alerts) == 0 || AlertsCycleTransferStatsStore == nil {
		AlertsLock.RUnlock()
		return
	}

	// 遍历所有通知规则，收集需要更新的数据
	for _, alert := range Alerts {
		if !alert.Enabled() {
			continue
		}

		// 获取此通知规则的流量统计数据
		stats := AlertsCycleTransferStatsStore[alert.ID]
		if stats == nil {
			continue
		}

		// 检查是否包含流量监控规则
		hasTrafficRule := false
		for j := 0; j < len(alert.Rules); j++ {
			if alert.Rules[j].IsTransferDurationRule() {
				// 检查此规则是否监控该服务器
				if alert.Rules[j].Cover == model.RuleCoverAll {
					// 监控全部服务器但排除了此服务器
					if alert.Rules[j].Ignore[serverID] {
						continue
					}
				} else if alert.Rules[j].Cover == model.RuleCoverIgnoreAll {
					// 忽略全部服务器但监控此服务器
					if !alert.Rules[j].Ignore[serverID] {
						continue
					}
				}
				hasTrafficRule = true
				break
			}
		}

		if hasTrafficRule {
			alertsToUpdate = append(alertsToUpdate, alert)
		}
	}
	AlertsLock.RUnlock()

	// 第三步：无锁更新流量数据并检查阈值
	for _, alert := range alertsToUpdate {
		// 找到对应的流量规则
		for j := 0; j < len(alert.Rules); j++ {
			if alert.Rules[j].IsTransferDurationRule() {
				// 服务器在规则监控范围内，根据规则类型更新相应的流量数据
				var transferValue uint64
				switch alert.Rules[j].Type {
				case "transfer_in_cycle":
					transferValue = inTransfer
				case "transfer_out_cycle":
					transferValue = outTransfer
				case "transfer_all_cycle":
					transferValue = inTransfer + outTransfer
				default:
					transferValue = inTransfer + outTransfer
				}

				// 第四步：持写锁更新统计数据
				AlertsLock.Lock()
				// 再次检查stats是否仍然有效
				if currentStats := AlertsCycleTransferStatsStore[alert.ID]; currentStats != nil {
					currentStats.Transfer[serverID] = transferValue
					if serverName != "" {
						currentStats.ServerName[serverID] = serverName
					}
					currentStats.NextUpdate[serverID] = time.Now()
				}
				AlertsLock.Unlock()

				// 第五步：检查阈值（无锁操作）
				ServerLock.RLock()
				if server := ServerList[serverID]; server != nil {
					// 创建服务器副本，避免在锁外使用
					serverCopy := &model.Server{
						Common: server.Common,
						Name:   server.Name,
					}
					if server.State != nil {
						serverCopy.State = server.State
					}
					if server.Host != nil {
						serverCopy.Host = server.Host
					}
					ServerLock.RUnlock()
					checkTrafficThresholds(alert, serverCopy, &alert.Rules[j], transferValue)
				} else {
					ServerLock.RUnlock()
				}

				// 找到一个满足条件的规则即可退出循环
				break
			}
		}
	}
}

// formatBytes 格式化字节大小为易读形式
func formatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// generateDetailedAlertMessage 生成详细的通知消息
func generateDetailedAlertMessage(alert *model.AlertRule, server *model.Server, checkResultsHistory [][]interface{}) string {
	now := time.Now()

	// 基础通知信息（移除了#探针通知前缀）
	message := fmt.Sprintf("[%s]"+"\n"+"%s[%s]"+"\n"+"服务器ID: %d"+"\n"+"通知时间: %s"+"\n",
		Localizer.MustLocalize(&i18n.LocalizeConfig{
			MessageID: "Incident",
		}),
		server.Name, IPDesensitize(server.Host.IP),
		server.ID,
		now.Format("2006-01-02 15:04:05"))

	// 添加规则基本信息
	message += fmt.Sprintf("通知规则: %s\n", alert.Name)

	// 获取最新的检查结果（最后一组）
	var latestResults []interface{}
	if len(checkResultsHistory) > 0 {
		latestResults = checkResultsHistory[len(checkResultsHistory)-1]
	}

	// 逐个检查失败的规则，生成详细信息
	for i, rule := range alert.Rules {
		if i < len(latestResults) && latestResults[i] != nil {
			// 这个规则检查失败了
			ruleType := rule.Type
			// 创建局部副本以避免内存别名问题
			ruleCopy := rule

			// 根据规则类型生成详细信息
			switch {
			case rule.IsTransferDurationRule():
				// 流量规则的详细信息
				message += generateTrafficAlertDetails(&ruleCopy, server, alert.ID)
			case ruleType == "cpu":
				message += fmt.Sprintf("• CPU使用率超限: %.2f%% (阈值: %.2f%%)\n",
					server.State.CPU, rule.Max)
			case ruleType == "memory":
				memPercent := float64(server.State.MemUsed) * 100 / float64(server.Host.MemTotal)
				message += fmt.Sprintf("• 内存使用率超限: %.2f%% (阈值: %.2f%%)\n",
					memPercent, rule.Max)
			case ruleType == "swap":
				swapPercent := float64(server.State.SwapUsed) * 100 / float64(server.Host.SwapTotal)
				message += fmt.Sprintf("• Swap使用率超限: %.2f%% (阈值: %.2f%%)\n",
					swapPercent, rule.Max)
			case ruleType == "disk":
				diskPercent := float64(server.State.DiskUsed) * 100 / float64(server.Host.DiskTotal)
				message += fmt.Sprintf("• 磁盘使用率超限: %.2f%% (阈值: %.2f%%)\n",
					diskPercent, rule.Max)
			case ruleType == "net_in_speed":
				message += fmt.Sprintf("• 入站网速过高: %s/s (阈值: %s/s)\n",
					formatBytes(server.State.NetInSpeed), formatBytes(uint64(rule.Max)))
			case ruleType == "net_out_speed":
				message += fmt.Sprintf("• 出站网速过高: %s/s (阈值: %s/s)\n",
					formatBytes(server.State.NetOutSpeed), formatBytes(uint64(rule.Max)))
			case ruleType == "net_all_speed":
				allSpeed := server.State.NetInSpeed + server.State.NetOutSpeed
				message += fmt.Sprintf("• 总网速过高: %s/s (阈值: %s/s)\n",
					formatBytes(allSpeed), formatBytes(uint64(rule.Max)))
			case ruleType == "load1":
				message += fmt.Sprintf("• 1分钟负载过高: %.2f (阈值: %.2f)\n",
					server.State.Load1, rule.Max)
			case ruleType == "load5":
				message += fmt.Sprintf("• 5分钟负载过高: %.2f (阈值: %.2f)\n",
					server.State.Load5, rule.Max)
			case ruleType == "load15":
				message += fmt.Sprintf("• 15分钟负载过高: %.2f (阈值: %.2f)\n",
					server.State.Load15, rule.Max)
			case ruleType == "tcp_conn_count":
				message += fmt.Sprintf("• TCP连接数过多: %d (阈值: %.0f)\n",
					server.State.TcpConnCount, rule.Max)
			case ruleType == "udp_conn_count":
				message += fmt.Sprintf("• UDP连接数过多: %d (阈值: %.0f)\n",
					server.State.UdpConnCount, rule.Max)
			case ruleType == "process_count":
				message += fmt.Sprintf("• 进程数过多: %d (阈值: %.0f)\n",
					server.State.ProcessCount, rule.Max)
			case ruleType == "offline":
				// 修复离线时长计算：使用服务器实际离线的时间点
				var lastSeenTime time.Time
				var offlineDuration time.Duration

				// 优先使用LastOnline字段（这是服务器最后一次在线的准确时间）
				if !server.LastOnline.IsZero() {
					lastSeenTime = server.LastOnline
					offlineDuration = time.Since(lastSeenTime)
				} else if !server.LastActive.IsZero() {
					// 如果没有LastOnline，使用LastActive，但需要考虑离线超时时间
					lastSeenTime = server.LastActive
					// 减去离线检测的超时时间（3分钟），得到更准确的离线时长
					offlineDuration = time.Since(lastSeenTime) - (3 * time.Minute)
					if offlineDuration < 0 {
						offlineDuration = time.Since(lastSeenTime)
					}
				} else {
					// 如果都没有，说明服务器从未上线过
					lastSeenTime = time.Now().Add(-time.Hour) // 默认1小时前
					offlineDuration = time.Hour
				}

				message += fmt.Sprintf("• 服务器离线: 最后在线时间 %s\n• 已离线时长: %s\n",
					lastSeenTime.Format("2006-01-02 15:04:05"),
					formatDuration(offlineDuration))
			default:
				message += fmt.Sprintf("• %s 超限 (阈值: %.2f)\n", ruleType, rule.Max)
			}
		}
	}

	return message
}

// generateTrafficAlertDetails 生成流量报警的详细信息，支持多级阈值通知
func generateTrafficAlertDetails(rule *model.Rule, server *model.Server, alertID uint64) string {
	var details string

	// 获取流量统计信息
	stats := AlertsCycleTransferStatsStore[alertID]
	if stats == nil {
		details = "• 流量超限: 无法获取详细统计信息\n"
		return details
	}

	currentUsage := stats.Transfer[server.ID]
	maxLimit := uint64(rule.Max)

	// 计算使用率
	usagePercent := float64(0)
	if maxLimit > 0 {
		usagePercent = float64(currentUsage) / float64(maxLimit) * 100
	}

	// 确定流量类型
	trafficType := "流量"
	switch rule.Type {
	case "transfer_in_cycle":
		trafficType = "入站流量"
	case "transfer_out_cycle":
		trafficType = "出站流量"
	case "transfer_all_cycle":
		trafficType = "总流量"
	}

	// 生成周期信息
	periodInfo := fmt.Sprintf("周期: %s - %s",
		stats.From.Format("2006-01-02 15:04:05"),
		stats.To.Format("2006-01-02 15:04:05"))

	// 根据使用率生成不同级别的通知信息
	var alertLevel string
	var alertIcon string

	switch {
	case usagePercent >= 100:
		alertLevel = "🚨 流量超限通知"
		alertIcon = "🚨"
	case usagePercent >= 90:
		alertLevel = "⚠️ 流量高使用率通知 (90%)"
		alertIcon = "⚠️"
	case usagePercent >= 50:
		alertLevel = "📊 流量使用率提醒 (50%)"
		alertIcon = "📊"
	default:
		alertLevel = "📈 流量监控提醒"
		alertIcon = "📈"
	}

	// 生成详细信息
	details += fmt.Sprintf("• %s %s:\n", alertIcon, alertLevel)
	details += fmt.Sprintf("  - 服务器: %s\n", server.Name)
	details += fmt.Sprintf("  - %s使用: %s (%.2f%%)\n", trafficType, formatBytes(currentUsage), usagePercent)
	details += fmt.Sprintf("  - 额定流量: %s\n", formatBytes(maxLimit))

	// 计算剩余流量
	if currentUsage < maxLimit {
		remainingBytes := maxLimit - currentUsage
		details += fmt.Sprintf("  - 剩余流量: %s\n", formatBytes(remainingBytes))
	} else {
		overageAmount := currentUsage - maxLimit
		details += fmt.Sprintf("  - 超额流量: %s\n", formatBytes(overageAmount))
	}

	details += fmt.Sprintf("  - %s\n", periodInfo)

	// 添加下次重置时间信息
	nextReset := stats.To
	if nextReset.After(time.Now()) {
		duration := time.Until(nextReset)
		details += fmt.Sprintf("  - 下次重置: %s (还有 %s)\n",
			nextReset.Format("2006-01-02 15:04:05"),
			formatDuration(duration))
	}

	return details
}

// formatDuration 格式化时间长度为易读形式
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0f秒", d.Seconds())
	} else if d < time.Hour {
		return fmt.Sprintf("%.0f分钟", d.Minutes())
	} else if d < 24*time.Hour {
		return fmt.Sprintf("%.1f小时", d.Hours())
	} else {
		return fmt.Sprintf("%.1f天", d.Hours()/24)
	}
}

// cleanupAlertMemoryData 清理报警系统的内存数据
func cleanupAlertMemoryData() {
	// 修复死锁问题：先获取ServerLock，再获取AlertsLock，确保锁顺序一致
	ServerLock.RLock()
	// 快速复制ServerList避免长时间持有锁
	activeServerIDs := make(map[uint64]bool)
	for serverID := range ServerList {
		activeServerIDs[serverID] = true
	}
	ServerLock.RUnlock()

	// 优化：使用更大的批处理大小，减少锁获取次数
	const batchSize = 50 // 增加批处理大小从10到50

	// 获取当前内存使用情况
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	cleanedAlerts := 0
	cleanedServers := 0

	// 第一步：快速获取需要处理的alert列表
	var alertIDs []uint64
	AlertsLock.RLock()
	alertIDs = make([]uint64, 0, len(alertsStore))
	for alertID := range alertsStore {
		alertIDs = append(alertIDs, alertID)
	}
	AlertsLock.RUnlock()

	// 第二步：分批处理
	for i := 0; i < len(alertIDs); i += batchSize {
		end := i + batchSize
		if end > len(alertIDs) {
			end = len(alertIDs)
		}

		batchIDs := alertIDs[i:end]
		batchCleanedAlerts, batchCleanedServers := cleanupAlertBatch(batchIDs, activeServerIDs)
		cleanedAlerts += batchCleanedAlerts
		cleanedServers += batchCleanedServers

		// 每处理一批后短暂让出CPU，避免阻塞其他请求
		runtime.Gosched()
	}

	// 第三步：清理AlertsCycleTransferStatsStore（单独处理，避免与其他清理混合）
	AlertsLock.Lock()
	for alertID, stats := range AlertsCycleTransferStatsStore {
		if stats == nil {
			delete(AlertsCycleTransferStatsStore, alertID)
			continue
		}

		// 清理不存在的服务器，使用复制的列表避免嵌套锁
		for serverID := range stats.Transfer {
			if !activeServerIDs[serverID] {
				delete(stats.Transfer, serverID)
				delete(stats.ServerName, serverID)
				delete(stats.NextUpdate, serverID)
			}
		}

		// 如果所有服务器都被清理了，删除整个统计记录
		if len(stats.Transfer) == 0 {
			delete(AlertsCycleTransferStatsStore, alertID)
		}
	}
	AlertsLock.Unlock()

	// 强制垃圾回收
	runtime.GC()

	// 获取清理后的内存使用情况
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	memFreed := int64(memBefore.Alloc) - int64(memAfter.Alloc)

	log.Printf("通知系统内存清理完成: 清理了 %d 个失效通知规则, %d 个服务器历史记录, 释放内存 %dMB",
		cleanedAlerts, cleanedServers, memFreed/1024/1024)
}

// cleanupAlertBatch 分批清理alert数据
func cleanupAlertBatch(alertIDs []uint64, activeServerIDs map[uint64]bool) (int, int) {
	AlertsLock.Lock()
	defer AlertsLock.Unlock()

	cleanedAlerts := 0
	cleanedServers := 0

	// 温和的清理策略，适度减少历史记录保留数量
	const maxHistoryPerServer = 25 // 从20增加到25

	// 清理alertsStore中的历史数据
	for _, alertID := range alertIDs {
		serverMap, exists := alertsStore[alertID]
		if !exists {
			continue
		}

		// 检查通知规则是否还存在
		alertExists := false
		for _, alert := range Alerts {
			if alert.ID == alertID {
				alertExists = true
				break
			}
		}

		if !alertExists {
			delete(alertsStore, alertID)
			delete(alertsPrevState, alertID)
			delete(AlertsCycleTransferStatsStore, alertID)
			cleanedAlerts++
			continue
		}

		// 清理每个服务器的历史数据
		for serverID, history := range serverMap {
			// 温和地清理历史记录
			if len(history) > maxHistoryPerServer {
				// 只保留最新的历史记录
				alertsStore[alertID][serverID] = history[len(history)-maxHistoryPerServer:]
				cleanedServers++
			}

			// 使用复制的ServerList检查服务器是否还存在，避免嵌套锁
			if !activeServerIDs[serverID] {
				delete(alertsStore[alertID], serverID)
				delete(alertsPrevState[alertID], serverID)
				cleanedServers++
			}
		}

		// 如果服务器映射为空，清理整个报警项
		if len(serverMap) == 0 {
			delete(alertsStore, alertID)
			delete(alertsPrevState, alertID)
		}
	}

	return cleanedAlerts, cleanedServers
}

// cleanupAlertMemoryDataAsync 异步清理报警系统的内存数据，减少锁阻塞时间
func cleanupAlertMemoryDataAsync() {
	go func() {
		cleanupAlertMemoryData()
	}()
}

// generateDetailedRecoveryMessage 生成详细的恢复通知消息
func generateDetailedRecoveryMessage(alert *model.AlertRule, server *model.Server) string {
	now := time.Now()

	// 基础恢复信息（移除了#探针通知前缀）
	message := fmt.Sprintf("[%s]"+"\n"+"%s[%s]"+"\n"+"服务器ID: %d"+"\n"+"恢复时间: %s"+"\n",
		Localizer.MustLocalize(&i18n.LocalizeConfig{
			MessageID: "Resolved",
		}),
		server.Name, IPDesensitize(server.Host.IP),
		server.ID,
		now.Format("2006-01-02 15:04:05"))

	// 添加规则基本信息
	message += fmt.Sprintf("通知规则: %s\n", alert.Name)

	// 检查是否包含离线规则，如果是则计算离线时长
	hasOfflineRule := false
	for _, rule := range alert.Rules {
		if rule.Type == "offline" {
			hasOfflineRule = true
			break
		}
	}

	if hasOfflineRule {
		// 使用记录的最后在线时间来计算真实的离线时长
		var lastOnlineTime time.Time
		var offlineDuration time.Duration

		// 优先使用记录的最后在线时间（与事件通知中显示的一致）
		if recordedTime, exists := serverLastOnlineTime[server.ID]; exists {
			lastOnlineTime = recordedTime
			offlineDuration = now.Sub(lastOnlineTime)
			// 清除记录的最后在线时间
			delete(serverLastOnlineTime, server.ID)
		} else {
			// 如果没有记录，使用当前服务器的状态作为备选
			if !server.LastOnline.IsZero() {
				lastOnlineTime = server.LastOnline
			} else if !server.LastActive.IsZero() {
				lastOnlineTime = server.LastActive
			} else {
				lastOnlineTime = now.Add(-time.Hour)
			}
			offlineDuration = now.Sub(lastOnlineTime)
		}

		message += fmt.Sprintf("• 服务器已恢复上线: 上次离线时间 %s\n• 离线时长: %s\n",
			lastOnlineTime.Format("2006-01-02 15:04:05"),
			formatDuration(offlineDuration))
	} else {
		message += "• 服务器监控指标已恢复正常\n"
	}

	// 添加当前服务器状态信息
	if server.State != nil && server.Host != nil {
		// 计算百分比
		memPercent := float64(server.State.MemUsed) * 100 / float64(server.Host.MemTotal)
		diskPercent := float64(server.State.DiskUsed) * 100 / float64(server.Host.DiskTotal)

		message += fmt.Sprintf("• 当前状态: CPU %.2f%%, 内存 %.2f%%, 磁盘 %.2f%%\n",
			server.State.CPU,
			memPercent,
			diskPercent)
	}

	return message
}

// checkTrafficThresholds 检查流量阈值并发送相应通知
func checkTrafficThresholds(alert *model.AlertRule, server *model.Server, rule *model.Rule, currentUsage uint64) {
	if rule.Max <= 0 {
		return
	}

	usagePercent := float64(currentUsage) / rule.Max * 100

	// 定义阈值
	thresholds := []struct {
		percent float64
		name    string
		icon    string
	}{
		{50.0, "50%流量使用提醒", "📊"},
		{90.0, "90%流量高使用率通知", "⚠️"},
		{100.0, "流量超限通知", "🚨"},
	}

	// 检查每个阈值（从高到低）
	for i := len(thresholds) - 1; i >= 0; i-- {
		threshold := thresholds[i]
		if usagePercent >= threshold.percent {
			// 生成阈值通知的静音标签，避免重复发送
			muteLabel := fmt.Sprintf("traffic-threshold-%d-%d-%.0f", alert.ID, server.ID, threshold.percent)

			// 检查是否已经发送过此阈值的通知
			if _, exists := Cache.Get(muteLabel); exists {
				return // 已经发送过，跳过
			}

			// 生成通知消息
			message := generateThresholdAlertMessage(alert, server, rule, currentUsage, threshold.percent, threshold.name, threshold.icon)

			// 发送通知
			SafeSendNotification(alert.NotificationTag, message, &muteLabel, server)

			// 根据阈值设置不同的静音策略
			if threshold.percent >= 90.0 {
				// 90%及以上：使用3小时重复发送机制
				Cache.Set(muteLabel, true, time.Hour*3)
			} else {
				// 90%以下：永久静音，只发送一次
				Cache.Set(muteLabel, true, time.Hour*24*365) // 设置1年，相当于永久静音
			}

			// 只发送最高达到的阈值通知
			return
		}
	}
}

// generateThresholdAlertMessage 生成阈值通知消息
func generateThresholdAlertMessage(alert *model.AlertRule, server *model.Server, rule *model.Rule, currentUsage uint64, thresholdPercent float64, thresholdName, icon string) string {
	now := time.Now()

	// 获取流量统计信息
	stats := AlertsCycleTransferStatsStore[alert.ID]

	// 确定流量类型
	trafficType := "流量"
	switch rule.Type {
	case "transfer_in_cycle":
		trafficType = "入站流量"
	case "transfer_out_cycle":
		trafficType = "出站流量"
	case "transfer_all_cycle":
		trafficType = "总流量"
	}

	usagePercent := float64(currentUsage) / rule.Max * 100

	// 根据实际使用百分比生成动态标题
	var dynamicTitle string
	if usagePercent >= 100 {
		dynamicTitle = fmt.Sprintf("%.1f%%流量超限通知", usagePercent)
	} else if usagePercent >= 90 {
		dynamicTitle = fmt.Sprintf("%.1f%%流量高使用率通知", usagePercent)
	} else {
		dynamicTitle = fmt.Sprintf("%.1f%%流量使用提醒", usagePercent)
	}

	message := fmt.Sprintf("%s %s\n", icon, dynamicTitle)
	message += fmt.Sprintf("时间: %s\n", now.Format("2006-01-02 15:04:05"))
	message += fmt.Sprintf("服务器: %s\n", server.Name)
	message += fmt.Sprintf("通知规则: %s\n\n", alert.Name)
	message += fmt.Sprintf("• %s使用情况:\n", trafficType)
	message += fmt.Sprintf("  - 当前使用: %s (%.2f%%)\n", formatBytes(currentUsage), usagePercent)
	message += fmt.Sprintf("  - 额定流量: %s\n", formatBytes(uint64(rule.Max)))

	if currentUsage < uint64(rule.Max) {
		remainingBytes := uint64(rule.Max) - currentUsage
		message += fmt.Sprintf("  - 剩余流量: %s\n", formatBytes(remainingBytes))
	} else {
		overageAmount := currentUsage - uint64(rule.Max)
		message += fmt.Sprintf("  - 超额流量: %s\n", formatBytes(overageAmount))
	}

	if stats != nil {
		message += fmt.Sprintf("  - 统计周期: %s - %s\n",
			stats.From.Format("2006-01-02 15:04:05"),
			stats.To.Format("2006-01-02 15:04:05"))
	}

	return message
}
