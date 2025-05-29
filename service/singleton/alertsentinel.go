package singleton

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jinzhu/copier"

	"github.com/nicksnyder/go-i18n/v2/i18n"
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
	if err := DB.Find(&Alerts).Error; err != nil {
		panic(err)
	}
	for _, alert := range Alerts {
		// 旧版本可能不存在通知组 为其添加默认值
		if alert.NotificationTag == "" {
			alert.NotificationTag = "default"
			DB.Save(alert)
		}
		alertsStore[alert.ID] = make(map[uint64][][]interface{})
		alertsPrevState[alert.ID] = make(map[uint64]uint)
		addCycleTransferStatsInfo(alert)
	}
	AlertsLock.Unlock()

	time.Sleep(time.Second * 10)
	var lastPrint time.Time
	var checkCount uint64

	// 内存清理计时器 - 每小时清理一次
	lastCleanupTime := time.Now()

	for {
		startedAt := time.Now()
		checkStatus()
		checkCount++

		// 定期清理内存数据
		if startedAt.Sub(lastCleanupTime) >= time.Hour {
			cleanupAlertMemoryData()
			lastCleanupTime = startedAt
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
		for _, server := range ServerList {
			// 监测点
			alertsStore[alert.ID][server.ID] = append(alertsStore[alert.
				ID][server.ID], alert.Snapshot(AlertsCycleTransferStatsStore[alert.ID], server, DB))
			// 发送通知，分为触发报警和恢复通知
			max, passed := alert.Check(alertsStore[alert.ID][server.ID])
			// 保存当前服务器状态信息
			curServer := model.Server{}
			copier.Copy(&curServer, server)

			// 本次未通过检查
			if !passed {
				// 始终触发模式或上次检查不为失败时触发报警（跳过单次触发+上次失败的情况）
				if alert.TriggerMode == model.ModeAlwaysTrigger || alertsPrevState[alert.ID][server.ID] != _RuleCheckFail {
					alertsPrevState[alert.ID][server.ID] = _RuleCheckFail
					log.Printf("[事件]\n%s\n规则：%s %s", server.Name, alert.Name, *NotificationMuteLabel.ServerIncident(alert.ID, server.ID))

					// 生成详细的报警消息
					message := generateDetailedAlertMessage(alert, server, alertsStore[alert.ID][server.ID])

					go SendTriggerTasks(alert.FailTriggerTasks, curServer.ID)
					go SendNotification(alert.NotificationTag, message, NotificationMuteLabel.ServerIncident(alert.ID, server.ID), &curServer)
					// 清除恢复通知的静音缓存
					UnMuteNotification(alert.NotificationTag, NotificationMuteLabel.ServerIncidentResolved(alert.ID, server.ID))
				}
			} else {
				// 本次通过检查但上一次的状态为失败，则发送恢复通知
				if alertsPrevState[alert.ID][server.ID] == _RuleCheckFail {
					message := fmt.Sprintf("#%s"+"\n"+"[%s]"+"\n"+"%s[%s]"+"\n"+"%s%s",
						Localizer.MustLocalize(&i18n.LocalizeConfig{
							MessageID: "Notify",
						}),
						Localizer.MustLocalize(&i18n.LocalizeConfig{
							MessageID: "Resolved",
						}), server.Name, IPDesensitize(server.Host.IP),
						Localizer.MustLocalize(&i18n.LocalizeConfig{
							MessageID: "Rule",
						}), alert.Name)
					go SendTriggerTasks(alert.RecoverTriggerTasks, curServer.ID)
					go SendNotification(alert.NotificationTag, message, NotificationMuteLabel.ServerIncidentResolved(alert.ID, server.ID), &curServer)
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
	// 使用轻量级的锁定以提高效率
	AlertsLock.RLock()
	defer AlertsLock.RUnlock()

	// 没有报警规则时，不需要更新
	if len(Alerts) == 0 || AlertsCycleTransferStatsStore == nil {
		return
	}

	// 查找服务器名称
	var serverName string
	ServerLock.RLock()
	if server := ServerList[serverID]; server != nil {
		serverName = server.Name
	}
	ServerLock.RUnlock()

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

// containsUint64 检查切片中是否包含指定的uint64值
func containsUint64(slice []uint64, val uint64) bool {
	for _, item := range slice {
		if item == val {
			return true
		}
	}
	return false
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
				lastSeen := time.Unix(int64(server.State.Uptime), 0)
				message += fmt.Sprintf("• 服务器离线: 最后在线时间 %s\n",
					lastSeen.Format("2006-01-02 15:04:05"))
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
	AlertsLock.Lock()
	defer AlertsLock.Unlock()

	const maxHistoryPerAlert = 100 // 每个报警规则最多保留100条历史记录
	const maxHistoryPerServer = 50  // 每个服务器最多保留50条历史记录

	cleanedAlerts := 0
	cleanedServers := 0

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
			if len(history) > maxHistoryPerServer {
				// 只保留最新的历史记录
				alertsStore[alertID][serverID] = history[len(history)-maxHistoryPerServer:]
				cleanedServers++
			}

			// 检查服务器是否还存在
			ServerLock.RLock()
			serverExists := ServerList[serverID] != nil
			ServerLock.RUnlock()

			if !serverExists {
				delete(alertsStore[alertID], serverID)
				delete(alertsPrevState[alertID], serverID)
				cleanedServers++
			}
		}
	}

	// 清理AlertsCycleTransferStatsStore中无效的服务器数据
	for alertID, stats := range AlertsCycleTransferStatsStore {
		if stats == nil {
			continue
		}

		// 清理不存在的服务器
		for serverID := range stats.Transfer {
			ServerLock.RLock()
			serverExists := ServerList[serverID] != nil
			ServerLock.RUnlock()

			if !serverExists {
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

	if Conf.Debug {
		log.Printf("报警系统内存清理完成: 清理了 %d 个失效报警规则, %d 个服务器历史记录", cleanedAlerts, cleanedServers)
	}
}
