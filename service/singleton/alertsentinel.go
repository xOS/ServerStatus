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
	for {
		startedAt := time.Now()
		checkStatus()
		checkCount++
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
					message := fmt.Sprintf("#%s"+"\n"+"[%s]"+"\n"+"%s[%s]"+"\n"+"%s%s",
						Localizer.MustLocalize(&i18n.LocalizeConfig{
							MessageID: "Notify",
						}),
						Localizer.MustLocalize(&i18n.LocalizeConfig{
							MessageID: "Incident",
						}), server.Name, IPDesensitize(server.Host.IP),
						Localizer.MustLocalize(&i18n.LocalizeConfig{
							MessageID: "Rule",
						}),
						alert.Name)
					go SendTriggerTasks(alert.FailTriggerTasks, curServer.ID)
					go SendNotification(alert.NotificationTag, message, NotificationMuteLabel.ServerIncident(server.ID, alert.ID), &curServer)
					// 清除恢复通知的静音缓存
					UnMuteNotification(alert.NotificationTag, NotificationMuteLabel.ServerIncidentResolved(server.ID, alert.ID))
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
					go SendNotification(alert.NotificationTag, message, NotificationMuteLabel.ServerIncidentResolved(server.ID, alert.ID), &curServer)
					// 清除失败通知的静音缓存
					UnMuteNotification(alert.NotificationTag, NotificationMuteLabel.ServerIncident(server.ID, alert.ID))
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
	AlertsLock.Lock()
	defer AlertsLock.Unlock()

	// 没有报警规则时，不需要更新
	if len(Alerts) == 0 || AlertsCycleTransferStatsStore == nil {
		return
	}

	// 查找服务器名称
	var serverName string
	if ServerList != nil {
		if server := ServerList[serverID]; server != nil {
			serverName = server.Name
		}
	}

	// 流量总计
	totalTransfer := inTransfer + outTransfer

	if isTrafficDebugEnabled {
		log.Printf("服务器 [%d:%s] 准备更新流量数据: IN=%d, OUT=%d, TOTAL=%d",
			serverID, serverName, inTransfer, outTransfer, totalTransfer)
	}

	// 记录已更新的规则ID，避免在日志中显示多次
	var updatedRules []uint64

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
				currentTransfer, exists := stats.Transfer[serverID]

				// 如果新值大于当前值，或者当前不存在，则更新
				if !exists || totalTransfer > currentTransfer {
					stats.Transfer[serverID] = totalTransfer

					// 更新服务器名称
					if serverName != "" && stats.ServerName[serverID] != serverName {
						stats.ServerName[serverID] = serverName
					}

					// 添加到已更新规则列表
					if !containsUint64(updatedRules, alert.ID) {
						updatedRules = append(updatedRules, alert.ID)
					}

					// 打印详细日志
					if isTrafficDebugEnabled {
						if exists {
							log.Printf("服务器 [%d:%s] 在规则 [%d:%s] 中的流量更新: %d -> %d (增加 %d)",
								serverID, serverName, alert.ID, alert.Name,
								currentTransfer, totalTransfer, totalTransfer-currentTransfer)
						} else {
							log.Printf("服务器 [%d:%s] 在规则 [%d:%s] 中的流量初始化: %d",
								serverID, serverName, alert.ID, alert.Name, totalTransfer)
						}
					}
				}

				// 找到一个满足条件的规则即可退出循环
				break
			}
		}
	}

	if len(updatedRules) > 0 && !isTrafficDebugEnabled {
		log.Printf("更新服务器 [%d:%s] 的流量数据: 总计=%d, 更新了%d个规则",
			serverID, serverName, totalTransfer, len(updatedRules))
	} else if len(updatedRules) == 0 && isTrafficDebugEnabled {
		log.Printf("服务器 [%d:%s] 不在任何流量监控规则范围内，未更新流量数据", serverID, serverName)
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
