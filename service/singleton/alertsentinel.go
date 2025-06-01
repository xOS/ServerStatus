package singleton

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"time"

	"github.com/jinzhu/copier"

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

// 报警规则
var (
	AlertsLock                    sync.RWMutex
	Alerts                        []*model.AlertRule
	alertsStore                   map[uint64]map[uint64][][]interface{} // [alert_id][server_id] -> 对应报警规则的检查结果
	alertsPrevState               map[uint64]map[uint64]uint            // [alert_id][server_id] -> 对应报警规则的上一次报警状态
	AlertsCycleTransferStatsStore map[uint64]*model.CycleTransferStats  // [alert_id] -> 对应报警规则的周期流量统计
)

// addCycleTransferStatsInfo 向AlertsCycleTransferStatsStore中添加周期流量报警统计信息
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
				Name:       alert.Name,
				From:       from,
				To:         to,
				Max:        uint64(alert.Rules[j].Max),
				Min:        uint64(alert.Rules[j].Min),
				ServerName: make(map[uint64]string),
				Transfer:   make(map[uint64]uint64),
				NextUpdate: make(map[uint64]time.Time),
			}
		}
	}
}

// AlertSentinelStart 报警器启动
func AlertSentinelStart() {
	alertsStore = make(map[uint64]map[uint64][][]interface{})
	alertsPrevState = make(map[uint64]map[uint64]uint)
	AlertsCycleTransferStatsStore = make(map[uint64]*model.CycleTransferStats)
	AlertsLock.Lock()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("AlertSentinelStart goroutine panic恢复: %v", r)
			// 重新启动
			go AlertSentinelStart()
		}
	}()

	// 根据数据库类型选择不同的加载方式
	if Conf.DatabaseType == "badger" {
		// 使用BadgerDB加载报警规则
		if db.DB != nil {
			// BadgerDB模式，直接查询AlertRule
			var alerts []*model.AlertRule
			err := db.DB.FindAll("alert_rule", &alerts)
			if err != nil {
				log.Printf("从BadgerDB加载报警规则失败: %v", err)
				AlertsLock.Unlock()
				return
			}
			Alerts = alerts
		} else {
			log.Println("BadgerDB未初始化，跳过加载报警规则")
			AlertsLock.Unlock()
			return
		}
	} else {
		// 使用SQLite加载报警规则
		err := executeWithRetry(func() error {
			return DB.Find(&Alerts).Error
		})
		if err != nil {
			log.Printf("从SQLite加载报警规则失败: %v", err)
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
				log.Println("NG>> 报警规则检测每小时", checkCount, "次", startedAt, time.Now())
			}
			checkCount = 0
			lastPrint = startedAt
		}
		time.Sleep(time.Until(startedAt.Add(time.Second * 3))) // 3秒钟检查一次
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

// checkStatus 检查报警规则并发送报警
func checkStatus() {
	AlertsLock.RLock()
	defer AlertsLock.RUnlock()
	ServerLock.RLock()
	defer ServerLock.RUnlock()

	for _, alert := range Alerts {
		// 跳过未启用
		if !alert.Enabled() {
			continue
		}

		// 确保Rules属性已初始化
		if alert.Rules == nil || len(alert.Rules) == 0 {
			if Conf.Debug {
				log.Printf("警告：报警规则 %s (ID=%d) 的Rules为空，跳过检查", alert.Name, alert.ID)
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

		for _, server := range ServerList {
			// 确保alertsStore对应的键存在
			if alertsStore[alert.ID] == nil {
				alertsStore[alert.ID] = make(map[uint64][][]interface{})
			}
			if alertsStore[alert.ID][server.ID] == nil {
				alertsStore[alert.ID][server.ID] = make([][]interface{}, 0)
			}

			// 监测点
			// 根据数据库类型决定是否传入DB参数
			var snapshot []interface{}
			if Conf.DatabaseType == "badger" {
				// BadgerDB模式下，传入nil作为DB参数
				snapshot = alert.Snapshot(AlertsCycleTransferStatsStore[alert.ID], server, nil)
			} else {
				// SQLite模式下，传入DB参数
				snapshot = alert.Snapshot(AlertsCycleTransferStatsStore[alert.ID], server, DB)
			}

			alertsStore[alert.ID][server.ID] = append(alertsStore[alert.ID][server.ID], snapshot)

			// 发送通知，分为触发报警和恢复通知
			max, passed := alert.Check(alertsStore[alert.ID][server.ID])
			// 保存当前服务器状态信息
			curServer := model.Server{}
			copier.Copy(&curServer, server)

			// 本次未通过检查
			if !passed {
				// 确保alertsPrevState正确初始化
				if alertsPrevState[alert.ID] == nil {
					alertsPrevState[alert.ID] = make(map[uint64]uint)
				}

				// 始终触发模式或上次检查不为失败时触发报警（跳过单次触发+上次失败的情况）
				if alert.TriggerMode == model.ModeAlwaysTrigger || alertsPrevState[alert.ID][server.ID] != _RuleCheckFail {
					alertsPrevState[alert.ID][server.ID] = _RuleCheckFail
					log.Printf("[事件]\n%s\n规则：%s %s", server.Name, alert.Name, *NotificationMuteLabel.ServerIncident(alert.ID, server.ID))

					// 生成详细的报警消息
					message := generateDetailedAlertMessage(alert, server, alertsStore[alert.ID][server.ID])

					SafeSendTriggerTasks(alert.FailTriggerTasks, curServer.ID)
					SafeSendNotification(alert.NotificationTag, message, NotificationMuteLabel.ServerIncident(alert.ID, server.ID), &curServer)
					// 清除恢复通知的静音缓存
					UnMuteNotification(alert.NotificationTag, NotificationMuteLabel.ServerIncidentResolved(alert.ID, server.ID))
				}
			} else {
				// 确保alertsPrevState正确初始化
				if alertsPrevState[alert.ID] == nil {
					alertsPrevState[alert.ID] = make(map[uint64]uint)
				}

				// 本次通过检查但上一次的状态为失败，则发送恢复通知
				if alertsPrevState[alert.ID][server.ID] == _RuleCheckFail {
					// 生成详细的恢复消息
					message := generateDetailedRecoveryMessage(alert, server)

					SafeSendTriggerTasks(alert.RecoverTriggerTasks, curServer.ID)
					SafeSendNotification(alert.NotificationTag, message, NotificationMuteLabel.ServerIncidentResolved(alert.ID, server.ID), &curServer)
					// 清除失败通知的静音缓存
					UnMuteNotification(alert.NotificationTag, NotificationMuteLabel.ServerIncident(alert.ID, server.ID))
				}
				alertsPrevState[alert.ID][server.ID] = _RuleCheckPass
			}
			// 清理旧数据
			if max > 0 && max < len(alertsStore[alert.ID][server.ID]) {
				alertsStore[alert.ID][server.ID] = alertsStore[alert.ID][server.ID][len(alertsStore[alert.ID][server.ID])-max:]
			}
		}
	}
}

// UpdateTrafficStats 更新服务器流量统计到AlertsCycleTransferStatsStore
// 这个函数直接更新流量数据，确保前端显示正确
func UpdateTrafficStats(serverID uint64, inTransfer, outTransfer uint64) {
	// 修复死锁问题：先获取ServerLock，再获取AlertsLock，确保锁顺序一致
	ServerLock.RLock()
	var serverName string
	if server := ServerList[serverID]; server != nil {
		serverName = server.Name
	}
	ServerLock.RUnlock()

	// 使用轻量级的锁定以提高效率
	AlertsLock.RLock()
	defer AlertsLock.RUnlock()

	// 没有报警规则时，不需要更新
	if len(Alerts) == 0 || AlertsCycleTransferStatsStore == nil {
		return
	}

	// 流量总计
	totalTransfer := inTransfer + outTransfer

	// 遍历所有报警规则，只更新包含此服务器的规则
	for _, alert := range Alerts {
		if !alert.Enabled() {
			continue
		}

		// 获取此报警规则的流量统计数据
		stats := AlertsCycleTransferStatsStore[alert.ID]
		if stats == nil {
			continue
		}

		// 检查是否包含流量监控规则
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

				// 服务器在规则监控范围内，更新流量数据
				// 不论大小如何，总是更新最新值
				stats.Transfer[serverID] = totalTransfer

				// 更新服务器名称
				if serverName != "" {
					stats.ServerName[serverID] = serverName
				}

				// 更新最后更新时间
				stats.NextUpdate[serverID] = time.Now()

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

// generateDetailedAlertMessage 生成详细的报警消息
func generateDetailedAlertMessage(alert *model.AlertRule, server *model.Server, checkResultsHistory [][]interface{}) string {
	now := time.Now()

	// 基础报警信息
	message := fmt.Sprintf("#%s"+"\n"+"[%s]"+"\n"+"%s[%s]"+"\n"+"服务器ID: %d"+"\n"+"报警时间: %s"+"\n",
		Localizer.MustLocalize(&i18n.LocalizeConfig{
			MessageID: "Notify",
		}),
		Localizer.MustLocalize(&i18n.LocalizeConfig{
			MessageID: "Incident",
		}),
		server.Name, IPDesensitize(server.Host.IP),
		server.ID,
		now.Format("2006-01-02 15:04:05"))

	// 添加规则基本信息
	message += fmt.Sprintf("报警规则: %s\n", alert.Name)

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
				// 使用正确的最后在线时间字段
				var lastSeenTime time.Time
				if !server.LastOnline.IsZero() {
					lastSeenTime = server.LastOnline
				} else if !server.LastActive.IsZero() {
					lastSeenTime = server.LastActive
				} else {
					lastSeenTime = time.Now().Add(-24 * time.Hour) // 默认24小时前
				}

				// 计算离线时长
				offlineDuration := time.Since(lastSeenTime)

				message += fmt.Sprintf("• 服务器离线: 最后在线时间 %s (离线时长: %s)\n",
					lastSeenTime.Format("2006-01-02 15:04:05"),
					formatDuration(offlineDuration))
			default:
				message += fmt.Sprintf("• %s 超限 (阈值: %.2f)\n", ruleType, rule.Max)
			}
		}
	}

	return message
}

// generateTrafficAlertDetails 生成流量报警的详细信息
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

	// 计算超额部分
	var overageAmount uint64
	if currentUsage > maxLimit {
		overageAmount = currentUsage - maxLimit
	}

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

	// 生成详细信息
	details += fmt.Sprintf("• %s超限:\n", trafficType)
	details += fmt.Sprintf("  - 当前使用: %s (%.2f%%)\n", formatBytes(currentUsage), usagePercent)
	details += fmt.Sprintf("  - 额定流量: %s\n", formatBytes(maxLimit))

	if overageAmount > 0 {
		details += fmt.Sprintf("  - 超额: %s\n", formatBytes(overageAmount))
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

	AlertsLock.Lock()
	defer AlertsLock.Unlock()

	// 温和的清理策略，适度减少历史记录保留数量
	const maxHistoryPerServer = 25 // 从20增加到25

	cleanedAlerts := 0
	cleanedServers := 0

	// 获取当前内存使用情况
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	// 清理alertsStore中的历史数据
	for alertID, serverMap := range alertsStore {
		// 检查报警规则是否还存在
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

	// 清理AlertsCycleTransferStatsStore中无效的服务器数据
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

	// 强制垃圾回收
	runtime.GC()

	// 获取清理后的内存使用情况
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	memFreed := int64(memBefore.Alloc) - int64(memAfter.Alloc)

	log.Printf("报警系统内存清理完成: 清理了 %d 个失效报警规则, %d 个服务器历史记录, 释放内存 %dMB",
		cleanedAlerts, cleanedServers, memFreed/1024/1024)
}

// generateDetailedRecoveryMessage 生成详细的恢复通知消息
func generateDetailedRecoveryMessage(alert *model.AlertRule, server *model.Server) string {
	now := time.Now()

	// 基础恢复信息
	message := fmt.Sprintf("#%s"+"\n"+"[%s]"+"\n"+"%s[%s]"+"\n"+"服务器ID: %d"+"\n"+"恢复时间: %s"+"\n",
		Localizer.MustLocalize(&i18n.LocalizeConfig{
			MessageID: "Notify",
		}),
		Localizer.MustLocalize(&i18n.LocalizeConfig{
			MessageID: "Resolved",
		}),
		server.Name, IPDesensitize(server.Host.IP),
		server.ID,
		now.Format("2006-01-02 15:04:05"))

	// 添加规则基本信息
	message += fmt.Sprintf("报警规则: %s\n", alert.Name)

	// 检查是否包含离线规则，如果是则计算离线时长
	hasOfflineRule := false
	for _, rule := range alert.Rules {
		if rule.Type == "offline" {
			hasOfflineRule = true
			break
		}
	}

	if hasOfflineRule {
		// 使用正确的最后在线时间字段
		var lastSeenTime time.Time
		if !server.LastOnline.IsZero() {
			lastSeenTime = server.LastOnline
		} else if !server.LastActive.IsZero() {
			lastSeenTime = server.LastActive
		} else {
			lastSeenTime = now.Add(-time.Hour) // 默认1小时前
		}

		// 计算离线时长（从离线到恢复）
		offlineDuration := now.Sub(lastSeenTime)

		message += fmt.Sprintf("• 服务器已恢复上线: 上次离线时间 %s (离线时长: %s)\n",
			lastSeenTime.Format("2006-01-02 15:04:05"),
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
