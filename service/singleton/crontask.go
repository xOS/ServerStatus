package singleton

import (
	"bytes"
	"fmt"
	"log"
	"sync"

	"github.com/robfig/cron/v3"
	"github.com/xos/serverstatus/db"
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

	// 添加基础的系统定时任务 - 修复重复任务注册问题
	// 每天凌晨3点清理累计流量数据（已废弃，保留为空函数）
	if _, err := Cron.AddFunc("0 0 3 * * *", func() {
		CleanCumulativeTransferData(3) // 改为3天，与监控历史保持一致
	}); err != nil {
		panic(err)
	}

	// 每天的3:30 对 监控记录 和 流量记录 进行清理（3天前数据）
	if _, err := Cron.AddFunc("0 30 3 * * *", func() {
		count, err := CleanMonitorHistory() // 处理返回值
		if err != nil {
			log.Printf("清理监控历史记录失败: %v", err)
		} else if count > 0 {
			log.Printf("清理监控历史记录成功，共清理 %d 条记录", count)
		}
	}); err != nil {
		panic(err)
	}

	// 每6小时清理一次监控历史记录，避免数据积累过多
	if _, err := Cron.AddFunc("0 0 */6 * * *", func() {
		count, err := CleanMonitorHistory()
		if err != nil {
			log.Printf("定时清理监控历史记录失败: %v", err)
		} else if count > 0 {
			log.Printf("定时清理监控历史记录完成，共清理 %d 条记录", count)
		}
	}); err != nil {
		panic(err)
	}

	// 每1小时对流量记录进行打点 - 只注册一次
	if _, err := Cron.AddFunc("0 0 */1 * * *", RecordTransferHourlyUsage); err != nil {
		panic(err)
	}

	// 以下任务仅在非BadgerDB模式下注册，因为它们依赖GORM
	if Conf == nil || Conf.DatabaseType != "badger" {
		// 改为每30分钟同步一次所有服务器的累计流量，降低频率
		if _, err := Cron.AddFunc("0 */30 * * * *", SyncAllServerTrafficFromDB); err != nil {
			panic(err)
		}

		// 改为每15分钟保存一次流量数据到数据库，进一步降低频率
		if _, err := Cron.AddFunc("0 */15 * * * *", SaveAllTrafficToDB); err != nil {
			panic(err)
		}
	} else {
		log.Println("BadgerDB模式：跳过注册GORM相关的定时任务")
	}
}

// loadCronTasks 加载计划任务
func loadCronTasks() {
	log.Println("加载计划任务...")
	InitCronTask()

	// 如果使用BadgerDB，从BadgerDB加载定时任务
	if Conf.DatabaseType == "badger" {
		log.Println("BadgerDB模式：从BadgerDB加载定时任务")
		loadCronTasksFromBadgerDB()
		// 启动定时器服务
		Cron.Start()
		return
	}

	// 以下是SQLite的处理逻辑
	var crons []model.Cron

	// 使用GORM (SQLite) 加载计划任务
	err := DB.Find(&crons).Error
	if err != nil {
		log.Printf("加载计划任务失败: %v", err)
		// 即使失败也要启动Cron服务
		Cron.Start()
		return
	}

	var taskErr error
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
		crons[i].CronJobID, taskErr = Cron.AddFunc(crons[i].Scheduler, CronTrigger(crons[i]))
		if taskErr == nil {
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
	// 向注册错误的计划任务所在通知组发送通知 - 使用安全通知池
	for _, tag := range notificationTagList {
		notificationMsgMap[tag].WriteString("] 这些任务将无法正常执行,请进入后点重新修改保存。")
		SafeSendNotification(tag, notificationMsgMap[tag].String(), nil)
	}

	// 启动定时器服务
	Cron.Start()
}

// loadCronTasksFromBadgerDB 从BadgerDB加载定时任务
func loadCronTasksFromBadgerDB() {
	if db.DB == nil {
		log.Println("BadgerDB未初始化，跳过加载定时任务")
		return
	}

	// 从BadgerDB获取所有定时任务
	cronOps := db.NewCronOps(db.DB)
	crons, err := cronOps.GetAllCrons()
	if err != nil {
		log.Printf("从BadgerDB加载定时任务失败: %v", err)
		return
	}

	log.Printf("从BadgerDB加载了 %d 个定时任务", len(crons))

	var taskErr error
	var notificationTagList []string
	notificationMsgMap := make(map[string]*bytes.Buffer)

	for i := 0; i < len(crons); i++ {
		// 触发任务类型无需注册到cron调度器
		if crons[i].TaskType == model.CronTypeTriggerTask {
			Crons[crons[i].ID] = crons[i]
			log.Printf("加载触发任务: %s (ID: %d)", crons[i].Name, crons[i].ID)
			continue
		}

		// 旧版本计划任务可能不存在通知组 为其添加默认通知组
		if crons[i].NotificationTag == "" {
			crons[i].NotificationTag = "default"
			// 更新到BadgerDB
			if err := cronOps.SaveCron(crons[i]); err != nil {
				log.Printf("更新定时任务通知组失败: %v", err)
			}
		}

		// 注册计划任务到cron调度器
		crons[i].CronJobID, taskErr = Cron.AddFunc(crons[i].Scheduler, CronTrigger(*crons[i]))
		if taskErr == nil {
			Crons[crons[i].ID] = crons[i] // 注意：crons[i] 已经是指针类型
			log.Printf("成功注册定时任务: %s (ID: %d, 调度: %s, 推送成功通知: %t)",
				crons[i].Name, crons[i].ID, crons[i].Scheduler, crons[i].PushSuccessful)
		} else {
			log.Printf("注册定时任务失败: %s (ID: %d), 错误: %v", crons[i].Name, crons[i].ID, taskErr)
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
		SafeSendNotification(tag, notificationMsgMap[tag].String(), nil)
	}

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
	taskID := cr.ID // 只保存任务ID，不保存整个任务对象
	return func() {
		// 动态获取最新的任务对象，而不是使用闭包捕获的旧对象
		CronLock.RLock()
		currentCr := Crons[taskID]
		CronLock.RUnlock()

		if currentCr == nil {
			log.Printf("警告：找不到任务ID=%d的配置，跳过执行", taskID)
			return
		}

		// 使用最新的任务对象
		cr = *currentCr

		crIgnoreMap := make(map[uint64]bool)
		for j := 0; j < len(cr.Servers); j++ {
			crIgnoreMap[cr.Servers[j]] = true
		}
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
					// 保存当前服务器状态信息 - 手动复制避免并发安全问题
					curServer := model.Server{
						Common: model.Common{
							ID: s.ID,
						},
						Name:       s.Name,
						Tag:        s.Tag,
						Note:       s.Note,
						PublicNote: s.PublicNote,
						IsOnline:   s.IsOnline,
						LastActive: s.LastActive,
						LastOnline: s.LastOnline,
					}
					SafeSendNotification(cr.NotificationTag, fmt.Sprintf("[任务失败] %s，服务器 %s 离线，无法执行。", cr.Name, s.Name), nil, &curServer)
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
				// 保存当前服务器状态信息 - 手动复制避免并发安全问题
				curServer := model.Server{
					Common: model.Common{
						ID: s.ID,
					},
					Name:       s.Name,
					Tag:        s.Tag,
					Note:       s.Note,
					PublicNote: s.PublicNote,
					IsOnline:   s.IsOnline,
					LastActive: s.LastActive,
					LastOnline: s.LastOnline,
				}
				SafeSendNotification(cr.NotificationTag, fmt.Sprintf("[任务失败] %s，服务器 %s 离线，无法执行。", cr.Name, s.Name), nil, &curServer)
			}
		}
	}
}
