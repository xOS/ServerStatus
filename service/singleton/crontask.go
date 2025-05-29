package singleton

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/jinzhu/copier"

	"github.com/robfig/cron/v3"
	"github.com/xos/serverstatus/model"
	pb "github.com/xos/serverstatus/proto"
)

var (
	Cron     *cron.Cron
	Crons    map[uint64]*model.Cron // [CrondID] -> *model.Cron
	CronLock sync.RWMutex
)

func InitCronTask() {
	Cron = cron.New(cron.WithSeconds(), cron.WithLocation(Loc))
	Crons = make(map[uint64]*model.Cron)

	// 添加基础的系统定时任务
	// 改为每5分钟保存一次流量数据，降低频率
	if _, err := Cron.AddFunc("0 */5 * * * *", RecordTransferHourlyUsage); err != nil {
		panic(err)
	}

	// 每天凌晨3点清理30天前的数据
	if _, err := Cron.AddFunc("0 0 3 * * *", func() {
		CleanCumulativeTransferData(30)
	}); err != nil {
		panic(err)
	}

	// 每天的3:30 对 监控记录 和 流量记录 进行清理
	if _, err := Cron.AddFunc("0 30 3 * * *", CleanMonitorHistory); err != nil {
		panic(err)
	}

	// 改为每2小时对流量记录进行打点，降低频率
	if _, err := Cron.AddFunc("0 0 */2 * * *", RecordTransferHourlyUsage); err != nil {
		panic(err)
	}

	// 改为每30分钟同步一次所有服务器的累计流量，降低频率
	if _, err := Cron.AddFunc("0 */30 * * * *", SyncAllServerTrafficFromDB); err != nil {
		panic(err)
	}

	// 改为每5分钟保存一次流量数据到数据库，降低频率
	if _, err := Cron.AddFunc("0 */5 * * * *", SaveAllTrafficToDB); err != nil {
		panic(err)
	}
}

// loadCronTasks 加载计划任务
func loadCronTasks() {
	InitCronTask()
	var crons []model.Cron
	DB.Find(&crons)
	var err error
	var notificationTagList []string
	notificationMsgMap := make(map[string]*bytes.Buffer)
	for i := 0; i < len(crons); i++ {
		// 触发任务类型无需注册
		if crons[i].TaskType == model.CronTypeTriggerTask {
			Crons[crons[i].ID] = &crons[i]
			continue
		}
		// 旧版本计划任务可能不存在通知组 为其添加默认通知组
		if crons[i].NotificationTag == "" {
			crons[i].NotificationTag = "default"
			DB.Save(crons[i])
		}
		// 注册计划任务
		crons[i].CronJobID, err = Cron.AddFunc(crons[i].Scheduler, CronTrigger(crons[i]))
		if err == nil {
			Crons[crons[i].ID] = &crons[i]
		} else {
			// 当前通知组首次出现 将其加入通知组列表并初始化通知组消息缓存
			if _, ok := notificationMsgMap[crons[i].NotificationTag]; !ok {
				notificationTagList = append(notificationTagList, crons[i].NotificationTag)
				notificationMsgMap[crons[i].NotificationTag] = bytes.NewBufferString("")
				notificationMsgMap[crons[i].NotificationTag].WriteString("调度失败的计划任务：[")
			}
			notificationMsgMap[crons[i].NotificationTag].WriteString(fmt.Sprintf("%d,", crons[i].ID))
		}
	}
	// 向注册错误的计划任务所在通知组发送通知
	for _, tag := range notificationTagList {
		notificationMsgMap[tag].WriteString("] 这些任务将无法正常执行,请进入后点重新修改保存。")
		SendNotification(tag, notificationMsgMap[tag].String(), nil)
	}
	Cron.Start()
}

func ManualTrigger(c model.Cron) {
	CronTrigger(c)()
}

func SendTriggerTasks(taskIDs []uint64, triggerServer uint64) {
	CronLock.RLock()
	var cronLists []*model.Cron
	for _, taskID := range taskIDs {
		if c, ok := Crons[taskID]; ok {
			cronLists = append(cronLists, c)
		}
	}
	CronLock.RUnlock()

	// 依次调用CronTrigger发送任务
	for _, c := range cronLists {
		// 使用Goroutine池执行CronTrigger，防止泄漏
		func(cron *model.Cron) {
			if TriggerTaskPool != nil {
				task := func() {
					CronTrigger(*cron, triggerServer)()
				}
				if !TriggerTaskPool.Submit(task) {
					// 如果池满了，直接执行但不创建新goroutine
					CronTrigger(*cron, triggerServer)()
				}
			} else {
				// 如果池还未初始化，直接执行
				CronTrigger(*cron, triggerServer)()
			}
		}(c)
	}
}

func CronTrigger(cr model.Cron, triggerServer ...uint64) func() {
	crIgnoreMap := make(map[uint64]bool)
	for j := 0; j < len(cr.Servers); j++ {
		crIgnoreMap[cr.Servers[j]] = true
	}
	return func() {
		if cr.Cover == model.CronCoverAlertTrigger {
			if len(triggerServer) == 0 {
				return
			}
			ServerLock.RLock()
			defer ServerLock.RUnlock()
			if s, ok := ServerList[triggerServer[0]]; ok {
				if s.TaskStream != nil {
					s.TaskStream.Send(&pb.Task{
						Id:   cr.ID,
						Data: cr.Command,
						Type: model.TaskTypeCommand,
					})
				} else {
					// 保存当前服务器状态信息
					curServer := model.Server{}
					copier.Copy(&curServer, s)
					SendNotification(cr.NotificationTag, fmt.Sprintf("[任务失败] %s，服务器 %s 离线，无法执行。", cr.Name, s.Name), nil, &curServer)
				}
			}
			return
		}

		ServerLock.RLock()
		defer ServerLock.RUnlock()
		for _, s := range ServerList {
			if cr.Cover == model.CronCoverAll && crIgnoreMap[s.ID] {
				continue
			}
			if cr.Cover == model.CronCoverIgnoreAll && !crIgnoreMap[s.ID] {
				continue
			}
			if s.TaskStream != nil {
				s.TaskStream.Send(&pb.Task{
					Id:   cr.ID,
					Data: cr.Command,
					Type: model.TaskTypeCommand,
				})
			} else {
				// 保存当前服务器状态信息
				curServer := model.Server{}
				copier.Copy(&curServer, s)
				SendNotification(cr.NotificationTag, fmt.Sprintf("[任务失败] %s，服务器 %s 离线，无法执行。", cr.Name, s.Name), nil, &curServer)
			}
		}
	}
}
