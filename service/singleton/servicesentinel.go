package singleton

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/xos/serverstatus/db"
	"github.com/xos/serverstatus/model"
	pb "github.com/xos/serverstatus/proto"
)

const (
	_CurrentStatusSize = 30 // 统计 15 分钟内的数据为当前状态
)

var ServiceSentinelShared *ServiceSentinel

type ReportData struct {
	Data     *pb.TaskResult
	Reporter uint64
}

// _TodayStatsOfMonitor 今日监控记录
type _TodayStatsOfMonitor struct {
	Up    int     // 今日在线计数
	Down  int     // 今日离线计数
	Delay float32 // 今日平均延迟
}

// NewServiceSentinel 创建服务监控器
func NewServiceSentinel(serviceSentinelDispatchBus chan<- model.Monitor) {
	ServiceSentinelShared = &ServiceSentinel{
		serviceReportChannel:                    make(chan ReportData, 200),
		serviceStatusToday:                      make(map[uint64]*_TodayStatsOfMonitor),
		serviceCurrentStatusIndex:               make(map[uint64]*indexStore),
		serviceCurrentStatusData:                make(map[uint64][]*pb.TaskResult),
		lastStatus:                              make(map[uint64]int),
		serviceResponseDataStoreCurrentUp:       make(map[uint64]uint64),
		serviceResponseDataStoreCurrentDown:     make(map[uint64]uint64),
		serviceResponseDataStoreCurrentAvgDelay: make(map[uint64]float32),
		serviceResponsePing:                     make(map[uint64]map[uint64]*pingStore),
		monitors:                                make(map[uint64]*model.Monitor),

		// 30天数据缓存
		monthlyStatus: make(map[uint64]*model.ServiceItemResponse),
		dispatchBus:   serviceSentinelDispatchBus,
	}
	// 加载历史记录
	ServiceSentinelShared.loadMonitorHistory()

	// 启动服务监控器
	go ServiceSentinelShared.worker()

	// 每日将游标往后推一天
	_, err := Cron.AddFunc("0 0 0 * * *", ServiceSentinelShared.refreshMonthlyServiceStatus)
	if err != nil {
		panic(err)
	}
}

/*
使用缓存 channel，处理上报的 Service 请求结果，然后判断是否需要事件通知
需要记录上一次的状态信息

加锁顺序：serviceResponseDataStoreLock > monthlyStatusLock > monitorsLock
*/
type ServiceSentinel struct {
	// 服务监控任务上报通道
	serviceReportChannel chan ReportData // 服务状态汇报管道
	// 服务监控任务调度通道
	dispatchBus chan<- model.Monitor

	serviceResponseDataStoreLock sync.RWMutex
	serviceStatusToday           map[uint64]*_TodayStatsOfMonitor // [monitor_id] -> _TodayStatsOfMonitor
	serviceCurrentStatusIndex    map[uint64]*indexStore           // [monitor_id] -> 该监控ID对应的 serviceCurrentStatusData 的最新索引下标
	serviceCurrentStatusData     map[uint64][]*pb.TaskResult      // [monitor_id] -> []model.MonitorHistory

	// 优化：限制数据存储大小，防止无限增长
	serviceResponseDataStoreCurrentUp       map[uint64]uint64                // [monitor_id] -> 当前服务在线计数
	serviceResponseDataStoreCurrentDown     map[uint64]uint64                // [monitor_id] -> 当前服务离线计数
	serviceResponseDataStoreCurrentAvgDelay map[uint64]float32               // [monitor_id] -> 当前服务离线计数
	serviceResponsePing                     map[uint64]map[uint64]*pingStore // [monitor_id] -> ClientID -> delay
	lastStatus                              map[uint64]int

	monitorsLock sync.RWMutex
	monitors     map[uint64]*model.Monitor // [monitor_id] -> model.Monitor

	// 30天数据缓存 - 添加定期清理机制
	monthlyStatusLock sync.Mutex
	monthlyStatus     map[uint64]*model.ServiceItemResponse // [monitor_id] -> model.ServiceItemResponse

	// 添加缓存管理
	lastCleanupTime time.Time
}

type indexStore struct {
	index        int
	t            time.Time
	lastSaveTime time.Time // 添加最后保存时间字段
}

type pingStore struct {
	count int
	ping  float32
}

func (ss *ServiceSentinel) refreshMonthlyServiceStatus() {
	// 刷新数据防止无人访问
	ss.LoadStats()
	// 将数据往前刷一天
	ss.serviceResponseDataStoreLock.Lock()
	defer ss.serviceResponseDataStoreLock.Unlock()
	ss.monthlyStatusLock.Lock()
	defer ss.monthlyStatusLock.Unlock()
	for k, v := range ss.monthlyStatus {
		for i := 0; i < len(v.Up)-1; i++ {
			if i == 0 {
				// 30 天在线率，减去已经出30天之外的数据
				v.TotalDown -= uint64(v.Down[i])
				v.TotalUp -= uint64(v.Up[i])
			}
			v.Up[i], v.Down[i], v.Delay[i] = v.Up[i+1], v.Down[i+1], v.Delay[i+1]
		}
		v.Up[29] = 0
		v.Down[29] = 0
		v.Delay[29] = 0
		// 清理前一天数据
		ss.serviceResponseDataStoreCurrentUp[k] = 0
		ss.serviceResponseDataStoreCurrentDown[k] = 0
		ss.serviceResponseDataStoreCurrentAvgDelay[k] = 0
		ss.serviceStatusToday[k].Delay = 0
		ss.serviceStatusToday[k].Up = 0
		ss.serviceStatusToday[k].Down = 0
	}
}

// Dispatch 将传入的 ReportData 传给 服务状态汇报管道
func (ss *ServiceSentinel) Dispatch(r ReportData) {
	ss.serviceReportChannel <- r
}

func (ss *ServiceSentinel) Monitors() []*model.Monitor {
	ss.monitorsLock.RLock()
	defer ss.monitorsLock.RUnlock()
	var monitors []*model.Monitor
	for _, v := range ss.monitors {
		monitors = append(monitors, v)
	}
	sort.SliceStable(monitors, func(i, j int) bool {
		return monitors[i].ID < monitors[j].ID
	})
	return monitors
}

// loadMonitorHistory 加载服务监控器的历史状态信息
func (ss *ServiceSentinel) loadMonitorHistory() {
	var monitors []*model.Monitor

	// 根据数据库类型选择不同的加载方式
	if Conf.DatabaseType == "badger" {
		if db.DB != nil {
			// 使用BadgerDB加载监控器列表
			monitorOps := db.NewMonitorOps(db.DB)
			var err error
			monitors, err = monitorOps.GetAllMonitors()
			if err != nil {
				log.Printf("从BadgerDB加载监控器列表失败: %v", err)
				monitors = []*model.Monitor{} // 使用空列表避免空指针
			} else {
				// BadgerDB模式下需要手动初始化SkipServers字段
				for _, monitor := range monitors {
					if monitor != nil {
						err := monitor.InitSkipServers()
						if err != nil {
							log.Printf("初始化监控器 %s 的SkipServers失败: %v", monitor.Name, err)
							// 设置为空map，避免nil指针
							monitor.SkipServers = make(map[uint64]bool)
						}
					}
				}
			}
		} else {
			log.Println("BadgerDB未初始化，使用空的监控器列表")
			monitors = []*model.Monitor{} // 使用空列表避免空指针
		}
	} else {
		// 使用SQLite加载监控器列表
		err := DB.Find(&monitors).Error
		if err != nil {
			log.Printf("从SQLite加载监控器列表失败: %v", err)
			monitors = []*model.Monitor{} // 使用空列表避免空指针
		}
	}

	ss.serviceResponseDataStoreLock.Lock()
	defer ss.serviceResponseDataStoreLock.Unlock()
	ss.monthlyStatusLock.Lock()
	defer ss.monthlyStatusLock.Unlock()
	ss.monitorsLock.Lock()
	defer ss.monitorsLock.Unlock()

	for i := 0; i < len(monitors); i++ {
		// 旧版本可能不存在通知组 为其设置默认组
		if monitors[i].NotificationTag == "" {
			monitors[i].NotificationTag = "default"

			// 根据数据库类型选择不同的更新方式
			if Conf.DatabaseType == "badger" {
				if db.DB != nil {
					// 使用BadgerDB更新监控器
					monitors[i].NotificationTag = "default"
					monitorOps := db.NewMonitorOps(db.DB)
					if err := monitorOps.SaveMonitor(monitors[i]); err != nil {
						log.Printf("更新Monitor通知组到BadgerDB失败: %v", err)
					}
				}
			} else {
				// 使用异步队列更新SQLite，避免锁冲突
				monitorData := map[string]interface{}{
					"notification_tag": "default",
				}
				AsyncDBUpdate(monitors[i].ID, "monitors", monitorData, func(err error) {
					if err != nil {
						log.Printf("更新Monitor通知组失败: %v", err)
					}
				})
			}
		}

		// 初始化监控数据存储
		monitorID := monitors[i].ID
		ss.serviceCurrentStatusData[monitorID] = make([]*pb.TaskResult, _CurrentStatusSize)
		ss.monitors[monitorID] = monitors[i]

		task := *monitors[i]
		// 通过cron定时将服务监控任务传递给任务调度管道
		var err error
		monitors[i].CronJobID, err = Cron.AddFunc(task.CronSpec(), func() {
			ss.dispatchBus <- task
		})
		if err != nil {
			log.Printf("添加监控定时任务失败: %v", err)
			continue // 跳过此监控器，继续处理其他的
		}
		ss.serviceStatusToday[monitors[i].ID] = &_TodayStatsOfMonitor{}
	}

	year, month, day := time.Now().Date()
	today := time.Date(year, month, day, 0, 0, 0, 0, Loc)

	for i := 0; i < len(monitors); i++ {
		ServiceSentinelShared.monthlyStatus[monitors[i].ID] = &model.ServiceItemResponse{
			Monitor: monitors[i],
			Delay:   &[30]float32{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			Up:      &[30]int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			Down:    &[30]int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		}
	}

	// 加载服务监控历史记录，优化查询性能
	var mhs []model.MonitorHistory

	// 如果使用BadgerDB，则使用BadgerDB方式加载监控历史记录
	if Conf.DatabaseType == "badger" {
		if db.DB != nil {
			// 使用BadgerDB加载监控历史记录
			// 优化：启动时只加载最近4天的数据，减少内存消耗
			endTime := time.Now()
			startTime := endTime.AddDate(0, 0, -4) // 从30天减少到4天

			monitorHistoryOps := db.NewMonitorHistoryOps(db.DB)
			histories, err := monitorHistoryOps.GetAllMonitorHistoriesInRange(startTime, endTime)
			if err != nil {
				log.Printf("从BadgerDB加载监控历史记录失败: %v", err)
				mhs = []model.MonitorHistory{}
			} else {
				// 限制加载数量，最多加载1000条记录
				maxRecords := 1000
				if len(histories) > maxRecords {
					log.Printf("监控历史记录过多(%d条)，只加载最新的%d条", len(histories), maxRecords)
					histories = histories[len(histories)-maxRecords:]
				}

				// 转换指针数组为值数组
				mhs = make([]model.MonitorHistory, len(histories))
				for i, h := range histories {
					if h != nil {
						mhs[i] = *h
					}
				}
				log.Printf("BadgerDB启动时加载了%d条监控历史记录", len(mhs))
			}
		} else {
			log.Println("BadgerDB未初始化，跳过加载监控历史记录")
			return
		}
	} else {
		// 添加查询优化和超时控制
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		startTime := time.Now()

		// 优化：启动时只加载最近4天数据，降低内存压力
		fromDate := today.AddDate(0, 0, -4) // 从29天改为4天
		toDate := today

		err := DB.WithContext(ctx).
			Where("created_at > ? AND created_at < ?", fromDate, toDate).
			Order("created_at DESC").
			Limit(1000). // 限制最多加载1000条记录
			Find(&mhs).Error

		if err != nil {
			log.Printf("加载监控数据失败: %v", err)
			return
		}

		queryDuration := time.Since(startTime)
		if queryDuration > 500*time.Millisecond {
			log.Printf("慢SQL查询警告: 分批加载监控数据耗时 %v，返回 %d 条记录", queryDuration, len(mhs))
		} else {
			log.Printf("监控数据加载完成: 耗时 %v，返回 %d 条记录", queryDuration, len(mhs))
		}
	}

	var delayCount = make(map[int]int)
	for i := 0; i < len(mhs); i++ {
		dayIndex := 28 - (int(today.Sub(mhs[i].CreatedAt).Hours()) / 24)
		if dayIndex < 0 {
			continue
		}
		ServiceSentinelShared.monthlyStatus[mhs[i].MonitorID].Delay[dayIndex] = (ServiceSentinelShared.monthlyStatus[mhs[i].MonitorID].Delay[dayIndex]*float32(delayCount[dayIndex]) + mhs[i].AvgDelay) / float32(delayCount[dayIndex]+1)
		delayCount[dayIndex]++
		ServiceSentinelShared.monthlyStatus[mhs[i].MonitorID].Up[dayIndex] += int(mhs[i].Up)
		ServiceSentinelShared.monthlyStatus[mhs[i].MonitorID].TotalUp += mhs[i].Up
		ServiceSentinelShared.monthlyStatus[mhs[i].MonitorID].Down[dayIndex] += int(mhs[i].Down)
		ServiceSentinelShared.monthlyStatus[mhs[i].MonitorID].TotalDown += mhs[i].Down
	}
}

func (ss *ServiceSentinel) OnMonitorUpdate(m model.Monitor) error {
	ss.serviceResponseDataStoreLock.Lock()
	defer ss.serviceResponseDataStoreLock.Unlock()
	ss.monthlyStatusLock.Lock()
	defer ss.monthlyStatusLock.Unlock()
	ss.monitorsLock.Lock()
	defer ss.monitorsLock.Unlock()

	var err error
	// 写入新任务
	m.CronJobID, err = Cron.AddFunc(m.CronSpec(), func() {
		ss.dispatchBus <- m
	})
	if err != nil {
		return err
	}
	if ss.monitors[m.ID] != nil {
		// 停掉旧任务
		Cron.Remove(ss.monitors[m.ID].CronJobID)
	} else {
		// 新任务初始化数据
		ss.monthlyStatus[m.ID] = &model.ServiceItemResponse{
			Monitor: &m,
			Delay:   &[30]float32{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			Up:      &[30]int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			Down:    &[30]int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		}
		ss.serviceCurrentStatusData[m.ID] = make([]*pb.TaskResult, _CurrentStatusSize)
		ss.serviceStatusToday[m.ID] = &_TodayStatsOfMonitor{}
	}
	// 更新这个任务
	ss.monitors[m.ID] = &m
	return nil
}

func (ss *ServiceSentinel) OnMonitorDelete(id uint64) {
	ss.serviceResponseDataStoreLock.Lock()
	defer ss.serviceResponseDataStoreLock.Unlock()
	ss.monthlyStatusLock.Lock()
	defer ss.monthlyStatusLock.Unlock()
	ss.monitorsLock.Lock()
	defer ss.monitorsLock.Unlock()

	delete(ss.serviceCurrentStatusIndex, id)
	delete(ss.serviceCurrentStatusData, id)
	delete(ss.lastStatus, id)
	delete(ss.serviceResponseDataStoreCurrentUp, id)
	delete(ss.serviceResponseDataStoreCurrentDown, id)
	delete(ss.serviceResponseDataStoreCurrentAvgDelay, id)

	delete(ss.serviceStatusToday, id)

	// 停掉定时任务
	Cron.Remove(ss.monitors[id].CronJobID)
	delete(ss.monitors, id)

	delete(ss.monthlyStatus, id)
}

func (ss *ServiceSentinel) LoadStats() map[uint64]*model.ServiceItemResponse {
	ss.serviceResponseDataStoreLock.RLock()
	defer ss.serviceResponseDataStoreLock.RUnlock()
	ss.monthlyStatusLock.Lock()
	defer ss.monthlyStatusLock.Unlock()

	// 刷新最新一天的数据
	for k := range ss.monitors {
		ss.monthlyStatus[k].Monitor = ss.monitors[k]
		v := ss.serviceStatusToday[k]

		// 30 天在线率，
		//   |- 减去上次加的旧当天数据，防止出现重复计数
		ss.monthlyStatus[k].TotalUp -= uint64(ss.monthlyStatus[k].Up[29])
		ss.monthlyStatus[k].TotalDown -= uint64(ss.monthlyStatus[k].Down[29])
		//   |- 加上当日数据
		ss.monthlyStatus[k].TotalUp += uint64(v.Up)
		ss.monthlyStatus[k].TotalDown += uint64(v.Down)

		ss.monthlyStatus[k].Up[29] = v.Up
		ss.monthlyStatus[k].Down[29] = v.Down
		ss.monthlyStatus[k].Delay[29] = v.Delay
	}

	// 最后 5 分钟的状态 与 monitor 对象填充
	for k, v := range ss.serviceResponseDataStoreCurrentDown {
		ss.monthlyStatus[k].CurrentDown = v
	}
	for k, v := range ss.serviceResponseDataStoreCurrentUp {
		ss.monthlyStatus[k].CurrentUp = v
	}

	return ss.monthlyStatus
}

// worker 服务监控的实际工作流程
func (ss *ServiceSentinel) worker() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ServiceSentinel worker panic恢复: %v", r)
			// 不再自动重启，避免goroutine泄漏
		}
	}()

	// 定期清理旧数据，大幅降低清理频率
	cleanupTicker := time.NewTicker(30 * time.Minute) // 改为30分钟
	defer cleanupTicker.Stop()

	// 简化的内存监控，仅在必要时进行
	memoryCheckTicker := time.NewTicker(10 * time.Minute) // 每10分钟检查内存
	defer memoryCheckTicker.Stop()

	for {
		select {
		case <-cleanupTicker.C:
			// 定期清理内存数据
			ss.cleanupOldData()
			ss.limitDataSize()

		case <-memoryCheckTicker.C:
			// 每10分钟检查内存使用情况
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			currentMemMB := m.Alloc / 1024 / 1024

			// 提高内存阈值，减少不必要的清理
			if currentMemMB > 1000 { // 提高到1GB
				log.Printf("ServiceSentinel检测到高内存使用: %dMB，执行清理", currentMemMB)
				ss.limitDataSize()
				ss.cleanupOldData()

				// 如果内存仍然很高，强制GC
				if currentMemMB > 1500 { // 提高到1.5GB
					runtime.GC()
					runtime.ReadMemStats(&m)
					log.Printf("强制GC后内存: %dMB", m.Alloc/1024/1024)
				}
			}

		case r := <-ss.serviceReportChannel:
			// 处理服务监控数据
			ss.handleServiceReport(r)
		}
	}
}

// handleServiceReport 处理单个服务监控报告
func (ss *ServiceSentinel) handleServiceReport(r ReportData) {
	if ss.monitors[r.Data.GetId()] == nil || ss.monitors[r.Data.GetId()].ID == 0 {
		log.Printf("NG>> 错误的服务监控上报 %+v", r)
		return
	}

	mh := r.Data

	// 添加边界检查，防止panic
	if mh.GetId() == 0 {
		log.Printf("NG>> 无效的监控ID: %+v", r)
		return
	}
	if mh.Type == model.TaskTypeTCPPing || mh.Type == model.TaskTypeICMPPing {
		monitorTcpMap, ok := ss.serviceResponsePing[mh.GetId()]
		if !ok {
			monitorTcpMap = make(map[uint64]*pingStore)
			ss.serviceResponsePing[mh.GetId()] = monitorTcpMap
		}
		ts, ok := monitorTcpMap[r.Reporter]
		if !ok {
			ts = &pingStore{}
		}
		ts.count++
		ts.ping = (ts.ping*float32(ts.count-1) + mh.Delay) / float32(ts.count)
		if ts.count == Conf.AvgPingCount {
			if ts.ping > float32(Conf.MaxTCPPingValue) {
				ts.ping = float32(Conf.MaxTCPPingValue)
			}
			ts.count = 0

			// 根据数据库类型选择不同的保存方式
			if Conf.DatabaseType == "badger" {
				// BadgerDB模式：直接保存监控历史记录
				history := &model.MonitorHistory{
					MonitorID: mh.GetId(),
					ServerID:  r.Reporter,
					AvgDelay:  ts.ping,
					Data:      mh.Data,
					Up:        0,
					Down:      0,
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}

				// 异步保存到BadgerDB，增加重试机制
				go func(h *model.MonitorHistory) {
					if db.DB != nil {
						monitorOps := db.NewMonitorHistoryOps(db.DB)

						// 重试机制，最多重试3次
						maxRetries := 3
						for retry := 0; retry < maxRetries; retry++ {
							err := monitorOps.SaveMonitorHistory(h)
							if err == nil {
								break
							} else {
								// 只在最后一次重试失败时记录错误日志
								if retry == maxRetries-1 {
									log.Printf("NG>> BadgerDB TCP/ICMP监控数据保存失败 (MonitorID: %d): %v", h.MonitorID, err)
								}
								if retry < maxRetries-1 {
									time.Sleep(time.Duration(retry+1) * 100 * time.Millisecond)
								}
							}
						}
					}
				}(history)
			} else {
				// SQLite模式：使用原有的异步队列
				monitorData := map[string]interface{}{
					"monitor_id": mh.GetId(),
					"avg_delay":  ts.ping,
					"data":       mh.Data,
					"server_id":  r.Reporter,
				}

				// 使用异步插入避免数据库锁冲突
				AsyncDBInsert("monitor_histories", monitorData, func(err error) {
					if err != nil {
						log.Printf("NG>> TCP/ICMP监控数据持久化失败 (MonitorID: %d): %v", mh.GetId(), err)
					}
				})
			}
		}
		monitorTcpMap[r.Reporter] = ts
	}
	
	// 优化：先收集需要的数据，减少锁持有时间
	type updateData struct {
		todayStats      *_TodayStatsOfMonitor
		currentStatus   []*pb.TaskResult
		currentIndex    int
		shouldSave      bool
		avgDelay        float32
		upCount         uint64
		downCount       uint64
		lastSaveTime    time.Time
		stateCode       uint
		monitorInfo     *model.Monitor
		lastStatusCode  uint
	}
	
	var update updateData
	currentTime := time.Now()
	
	// 第一阶段：快速更新内存状态（持锁）
	ss.serviceResponseDataStoreLock.Lock()
	
	// 写入当天状态
	if ss.serviceStatusToday[mh.GetId()] == nil {
		ss.serviceStatusToday[mh.GetId()] = &_TodayStatsOfMonitor{
			Up:    0,
			Down:  0,
			Delay: 0,
		}
	}

	if mh.Successful {
		ss.serviceStatusToday[mh.GetId()].Delay = (ss.serviceStatusToday[mh.
			GetId()].Delay*float32(ss.serviceStatusToday[mh.GetId()].Up) +
			mh.Delay) / float32(ss.serviceStatusToday[mh.GetId()].Up+1)
		ss.serviceStatusToday[mh.GetId()].Up++
	} else {
		ss.serviceStatusToday[mh.GetId()].Down++
	}

	// 初始化索引存储
	if ss.serviceCurrentStatusIndex[mh.GetId()] == nil {
		ss.serviceCurrentStatusIndex[mh.GetId()] = &indexStore{
			t:            currentTime,
			index:        0,
			lastSaveTime: time.Time{}, // 初始化为零值
		}
	}
	
	// 写入当前数据
	if ss.serviceCurrentStatusIndex[mh.GetId()].t.Before(currentTime) {
		ss.serviceCurrentStatusIndex[mh.GetId()].t = currentTime.Add(30 * time.Second)

		// 确保 serviceCurrentStatusData 已初始化
		if ss.serviceCurrentStatusData[mh.GetId()] == nil {
			ss.serviceCurrentStatusData[mh.GetId()] = make([]*pb.TaskResult, _CurrentStatusSize)
		}

		// 边界检查：确保索引不会超出当前数组的实际长度
		currentArrayLength := len(ss.serviceCurrentStatusData[mh.GetId()])
		if ss.serviceCurrentStatusIndex[mh.GetId()].index >= currentArrayLength {
			ss.serviceCurrentStatusIndex[mh.GetId()].index = 0
		}

		ss.serviceCurrentStatusData[mh.GetId()][ss.serviceCurrentStatusIndex[mh.GetId()].index] = mh
		ss.serviceCurrentStatusIndex[mh.GetId()].index++

		// 立即检查并重置index以防止越界，使用实际数组长度
		if ss.serviceCurrentStatusIndex[mh.GetId()].index >= currentArrayLength {
			ss.serviceCurrentStatusIndex[mh.GetId()].index = 0
		}
	}

	// 更新当前状态
	ss.serviceResponseDataStoreCurrentUp[mh.GetId()] = 0
	ss.serviceResponseDataStoreCurrentDown[mh.GetId()] = 0
	ss.serviceResponseDataStoreCurrentAvgDelay[mh.GetId()] = 0

	// 计算最新的 30 个数据的状态
	for i := 0; i < len(ss.serviceCurrentStatusData[mh.GetId()]); i++ {
		if ss.serviceCurrentStatusData[mh.GetId()][i] != nil && ss.serviceCurrentStatusData[mh.GetId()][i].GetId() > 0 {
			if ss.serviceCurrentStatusData[mh.GetId()][i].Successful {
				ss.serviceResponseDataStoreCurrentUp[mh.GetId()]++
				if ss.serviceResponseDataStoreCurrentUp[mh.GetId()] > 1 {
					ss.serviceResponseDataStoreCurrentAvgDelay[mh.GetId()] = (ss.serviceResponseDataStoreCurrentAvgDelay[mh.GetId()]*float32(ss.serviceResponseDataStoreCurrentUp[mh.GetId()]-1) + ss.serviceCurrentStatusData[mh.GetId()][i].Delay) / float32(ss.serviceResponseDataStoreCurrentUp[mh.GetId()])
				} else {
					ss.serviceResponseDataStoreCurrentAvgDelay[mh.GetId()] = ss.serviceCurrentStatusData[mh.GetId()][i].Delay
				}
			} else {
				ss.serviceResponseDataStoreCurrentDown[mh.GetId()]++
			}
		}
	}

	// 收集需要的数据
	update.avgDelay = ss.serviceResponseDataStoreCurrentAvgDelay[mh.GetId()]
	update.upCount = ss.serviceResponseDataStoreCurrentUp[mh.GetId()]
	update.downCount = ss.serviceResponseDataStoreCurrentDown[mh.GetId()]
	update.lastSaveTime = ss.serviceCurrentStatusIndex[mh.GetId()].lastSaveTime
	
	// 检查是否需要保存数据
	now := time.Now()
	shouldSave := false
	if update.lastSaveTime.IsZero() {
		shouldSave = true
	} else if now.Sub(update.lastSaveTime) >= 15*time.Minute {
		shouldSave = true
	} else if ss.serviceCurrentStatusIndex[mh.GetId()].index%_CurrentStatusSize == 0 &&
		ss.serviceCurrentStatusIndex[mh.GetId()].index > 0 {
		shouldSave = true
	}
	update.shouldSave = shouldSave
	
	// 计算在线率
	var upPercent uint64 = 0
	if update.downCount+update.upCount > 0 {
		upPercent = update.upCount * 100 / (update.downCount + update.upCount)
	}
	update.stateCode = uint(GetStatusCode(upPercent))
	
	ss.serviceResponseDataStoreLock.Unlock()
	
	// 第二阶段：异步保存数据（不持锁）
	if update.shouldSave {
		// 确保有数据才保存
		totalChecks := update.upCount + update.downCount
		if totalChecks > 0 {
			// 使用异步数据库插入队列来保存监控数据，避免并发冲突
			monitorData := map[string]interface{}{
				"monitor_id": mh.GetId(),
				"avg_delay":  update.avgDelay,
				"data":       mh.Data,
				"up":         update.upCount,
				"down":       update.downCount,
			}

			// 根据数据库类型选择不同的保存方式
			if Conf.DatabaseType == "badger" {
				// BadgerDB模式：直接保存监控历史记录
				history := &model.MonitorHistory{
					MonitorID: mh.GetId(),
					ServerID:  0, // 服务监控不关联特定服务器
					AvgDelay:  update.avgDelay,
					Data:      mh.Data,
					Up:        update.upCount,
					Down:      update.downCount,
					CreatedAt: now,
					UpdatedAt: now,
				}

				// 异步保存到BadgerDB
				go func(h *model.MonitorHistory, monitorID uint64) {
					if db.DB != nil {
						monitorOps := db.NewMonitorHistoryOps(db.DB)
						err := monitorOps.SaveMonitorHistory(h)
						if err != nil {
							log.Printf("NG>> BadgerDB服务监控数据保存失败 (MonitorID: %d): %v", h.MonitorID, err)
						} else {
							// 更新lastSaveTime
							ss.serviceResponseDataStoreLock.Lock()
							if ss.serviceCurrentStatusIndex[monitorID] != nil {
								ss.serviceCurrentStatusIndex[monitorID].lastSaveTime = now
							}
							ss.serviceResponseDataStoreLock.Unlock()
						}
					}
				}(history, mh.GetId())
			} else {
				// SQLite模式：使用原有的异步队列
				AsyncMonitorHistoryInsert(monitorData, func(err error) {
					if err != nil {
						log.Printf("NG>> 服务监控数据持久化失败 (MonitorID: %d): %v", mh.GetId(), err)
					} else {
						// 更新lastSaveTime
						ss.serviceResponseDataStoreLock.Lock()
						if ss.serviceCurrentStatusIndex[mh.GetId()] != nil {
							ss.serviceCurrentStatusIndex[mh.GetId()].lastSaveTime = now
						}
						ss.serviceResponseDataStoreLock.Unlock()
					}
				})
			}
		}
	}

	// 延迟事件通知
	if mh.Delay > 0 {
		ss.monitorsLock.RLock()
		if ss.monitors[mh.GetId()].LatencyNotify {
			notificationTag := ss.monitors[mh.GetId()].NotificationTag
			minMuteLabel := NotificationMuteLabel.ServiceLatencyMin(mh.GetId())
			maxMuteLabel := NotificationMuteLabel.ServiceLatencyMax(mh.GetId())
			if mh.Delay > ss.monitors[mh.GetId()].MaxLatency {
				// 延迟超过最大值
				ServerLock.RLock()
				reporterServer := ServerList[r.Reporter]
				msg := fmt.Sprintf("[Latency] %s %2f > %2f, Reporter: %s", ss.monitors[mh.GetId()].Name, mh.Delay, ss.monitors[mh.GetId()].MaxLatency, reporterServer.Name)
				SafeSendNotification(notificationTag, msg, minMuteLabel)
				ServerLock.RUnlock()
			} else if mh.Delay < ss.monitors[mh.GetId()].MinLatency {
				// 延迟低于最小值
				ServerLock.RLock()
				reporterServer := ServerList[r.Reporter]
				msg := fmt.Sprintf("[Latency] %s %2f < %2f, Reporter: %s", ss.monitors[mh.GetId()].Name, mh.Delay, ss.monitors[mh.GetId()].MinLatency, reporterServer.Name)
				SafeSendNotification(notificationTag, msg, maxMuteLabel)
				ServerLock.RUnlock()
			} else {
				// 正常延迟， 清除静音缓存
				UnMuteNotification(notificationTag, minMuteLabel)
				UnMuteNotification(notificationTag, maxMuteLabel)
			}
		}
		ss.monitorsLock.RUnlock()
	}

	// 状态变更事件通知+触发任务执行
	if update.stateCode == StatusDown || update.stateCode != uint(ss.lastStatus[mh.GetId()]) {
		ss.monitorsLock.Lock()
		lastStatus := ss.lastStatus[mh.GetId()]
		// 存储新的状态值
		ss.lastStatus[mh.GetId()] = int(update.stateCode)

		// 判断是否需要发送通知
		isNeedSendNotification := ss.monitors[mh.GetId()].Notify && (lastStatus != 0 || update.stateCode == StatusDown)
		if isNeedSendNotification {
			ServerLock.RLock()

			reporterServer := ServerList[r.Reporter]
			notificationTag := ss.monitors[mh.GetId()].NotificationTag
			notificationMsg := fmt.Sprintf("[%s] %s Reporter: %s, Error: %s", StatusCodeToString(int(update.stateCode)), ss.monitors[mh.GetId()].Name, reporterServer.Name, mh.Data)
			muteLabel := NotificationMuteLabel.ServiceStateChanged(mh.GetId())

			// 状态变更时，清除静音缓存
			if int(update.stateCode) != lastStatus {
				UnMuteNotification(notificationTag, muteLabel)
			}

			// 使用Goroutine池发送通知，防止泄漏
			SafeSendNotification(notificationTag, notificationMsg, muteLabel)
			ServerLock.RUnlock()
		}

		// 判断是否需要触发任务
		isNeedTriggerTask := ss.monitors[mh.GetId()].EnableTriggerTask && lastStatus != 0
		if isNeedTriggerTask {
			ServerLock.RLock()
			reporterServer := ServerList[r.Reporter]
			ServerLock.RUnlock()

			if update.stateCode == StatusGood && lastStatus != int(update.stateCode) {
				// 当前状态正常 前序状态非正常时 触发恢复任务
				SafeSendTriggerTasks(ss.monitors[mh.GetId()].RecoverTriggerTasks, reporterServer.ID)
			} else if lastStatus == StatusGood && lastStatus != int(update.stateCode) {
				// 前序状态正常 当前状态非正常时 触发失败任务
				SafeSendTriggerTasks(ss.monitors[mh.GetId()].FailTriggerTasks, reporterServer.ID)
			}
		}

		ss.monitorsLock.Unlock()
	}


}

const (
	_ = iota
	StatusNoData
	StatusGood
	StatusLowAvailability
	StatusDown
)

func GetStatusCode[T float32 | uint64](percent T) int {
	if percent == 0 {
		return StatusNoData
	}
	if percent > 95 {
		return StatusGood
	}
	if percent > 80 {
		return StatusLowAvailability
	}
	return StatusDown
}

func StatusCodeToString(statusCode int) string {
	switch statusCode {
	case StatusNoData:
		return Localizer.MustLocalize(&i18n.LocalizeConfig{MessageID: "StatusNoData"})
	case StatusGood:
		return Localizer.MustLocalize(&i18n.LocalizeConfig{MessageID: "StatusGood"})
	case StatusLowAvailability:
		return Localizer.MustLocalize(&i18n.LocalizeConfig{MessageID: "StatusLowAvailability"})
	case StatusDown:
		return Localizer.MustLocalize(&i18n.LocalizeConfig{MessageID: "StatusDown"})
	default:
		return ""
	}
}

// cleanupOldData 清理旧的缓存数据以防止内存泄漏
func (ss *ServiceSentinel) cleanupOldData() {
	ss.serviceResponseDataStoreLock.Lock()
	defer ss.serviceResponseDataStoreLock.Unlock()

	now := time.Now()

	// 只有超过1小时才进行清理，避免频繁清理
	if now.Sub(ss.lastCleanupTime) < time.Hour {
		return
	}
	ss.lastCleanupTime = now



	// 清理ping数据，限制每个监控项的ping存储
	for monitorID, pingMap := range ss.serviceResponsePing {
		if len(pingMap) > 120 { // 限制每个监控项最多120个ping记录
			// 重要修复：按服务器ID排序，只删除最旧的记录，保护活跃服务器
			serverIDs := make([]uint64, 0, len(pingMap))
			for serverID := range pingMap {
				serverIDs = append(serverIDs, serverID)
			}

			// 按ID排序，删除较小ID的记录（通常是较旧的）
			if len(serverIDs) > 60 {
				// 按ID升序排序
				for i := 0; i < len(serverIDs)-1; i++ {
					for j := i + 1; j < len(serverIDs); j++ {
						if serverIDs[i] > serverIDs[j] {
							serverIDs[i], serverIDs[j] = serverIDs[j], serverIDs[i]
						}
					}
				}

				// 只删除最旧的记录，保留最新的60个，但保护活跃服务器
				for i := 0; i < len(serverIDs)-60; i++ {
					// 重要：检查是否为活跃服务器，如果是则跳过删除
					if !ss.isActiveServer(serverIDs[i]) {
						delete(pingMap, serverIDs[i])
					}
				}
			}
		}
		// 清理不存在的监控项
		if _, exists := ss.monitors[monitorID]; !exists {
			delete(ss.serviceResponsePing, monitorID)
		}
	}

	log.Printf("内存清理完成，清理时间：%v", now)
}

// limitDataSize 强制限制数据结构大小，防止内存无限增长
func (ss *ServiceSentinel) limitDataSize() {
	ss.serviceResponseDataStoreLock.Lock()
	defer ss.serviceResponseDataStoreLock.Unlock()

	const maxStatusRecords = 6 // 每个监控项最多6条状态记录（从5增加）
	const maxPingRecords = 12  // 每个监控项最多12条ping记录（从10增加）
	const maxMonitors = 1200   // 全局最多1200个监控项（从1000增加）

	// 限制状态数据大小
	totalStatusRecords := 0
	for monitorID, statusData := range ss.serviceCurrentStatusData {
		if statusData != nil {
			if len(statusData) > maxStatusRecords {
				// 只保留最新的记录
				ss.serviceCurrentStatusData[monitorID] = statusData[len(statusData)-maxStatusRecords:]

				// 重要：当数组被缩减时，必须重置索引以防止越界
				if ss.serviceCurrentStatusIndex[monitorID] != nil &&
					ss.serviceCurrentStatusIndex[monitorID].index >= maxStatusRecords {
					ss.serviceCurrentStatusIndex[monitorID].index = 0
				}
			}
			totalStatusRecords += len(ss.serviceCurrentStatusData[monitorID])
		}
	}

	// 如果总记录数过多，清理一些旧监控项
	if totalStatusRecords > maxMonitors*maxStatusRecords {
		monitorsToClean := (totalStatusRecords - maxMonitors*maxStatusRecords) / maxStatusRecords
		count := 0
		for monitorID := range ss.serviceCurrentStatusData {
			if count >= monitorsToClean {
				break
			}
			// 检查监控项是否仍然存在
			if _, exists := ss.monitors[monitorID]; !exists {
				delete(ss.serviceCurrentStatusData, monitorID)
				delete(ss.serviceCurrentStatusIndex, monitorID) // 清理索引
				delete(ss.serviceStatusToday, monitorID)
				delete(ss.serviceResponseDataStoreCurrentUp, monitorID)
				delete(ss.serviceResponseDataStoreCurrentDown, monitorID)
				delete(ss.serviceResponseDataStoreCurrentAvgDelay, monitorID)
				delete(ss.serviceResponsePing, monitorID)
				delete(ss.lastStatus, monitorID)
				count++
			}
		}
	}

	// 限制ping数据大小
	for monitorID, pingMap := range ss.serviceResponsePing {
		if len(pingMap) > maxPingRecords {
			// 重要修复：按服务器ID排序删除，避免删除活跃服务器
			keys := make([]uint64, 0, len(pingMap))
			for k := range pingMap {
				keys = append(keys, k)
			}

			// 按ID排序，保护高ID的活跃服务器
			for i := 0; i < len(keys)-1; i++ {
				for j := i + 1; j < len(keys); j++ {
					if keys[i] > keys[j] {
						keys[i], keys[j] = keys[j], keys[i]
					}
				}
			}

			// 只删除最旧的记录，保留最新的 maxPingRecords 个，但保护活跃服务器
			if len(keys) > maxPingRecords {
				for i := 0; i < len(keys)-maxPingRecords; i++ {
					// 重要：检查是否为活跃服务器，如果是则跳过删除
					if !ss.isActiveServer(keys[i]) {
						delete(pingMap, keys[i])
					}
				}
			}
		}

		// 如果监控项不存在，删除整个ping映射
		if _, exists := ss.monitors[monitorID]; !exists {
			delete(ss.serviceResponsePing, monitorID)
		}
	}

	// 清理月度状态数据中的无效监控项
	ss.monthlyStatusLock.Lock()
	for monitorID := range ss.monthlyStatus {
		if _, exists := ss.monitors[monitorID]; !exists {
			delete(ss.monthlyStatus, monitorID)
		}
	}
	ss.monthlyStatusLock.Unlock()

	log.Printf("数据大小限制完成: 状态记录=%d, 监控项=%d", totalStatusRecords, len(ss.serviceCurrentStatusData))
}

// emergencyCleanup 紧急清理ServiceSentinel数据
func (ss *ServiceSentinel) emergencyCleanup() {
	ss.serviceResponseDataStoreLock.Lock()
	defer ss.serviceResponseDataStoreLock.Unlock()

	log.Printf("ServiceSentinel开始紧急清理...")

	// 清空ping数据
	ss.serviceResponsePing = make(map[uint64]map[uint64]*pingStore)

	// 清空状态数据，只保留最新的
	for id := range ss.serviceCurrentStatusData {
		// 保留当前状态，清理历史
		if len(ss.serviceCurrentStatusData[id]) > 1 {
			latest := ss.serviceCurrentStatusData[id][len(ss.serviceCurrentStatusData[id])-1]
			ss.serviceCurrentStatusData[id] = []*pb.TaskResult{latest}
		}
	}

	// 清空月度统计
	ss.monthlyStatusLock.Lock()
	ss.monthlyStatus = make(map[uint64]*model.ServiceItemResponse)
	ss.monthlyStatusLock.Unlock()

	// 重置计数器
	ss.serviceResponseDataStoreCurrentUp = make(map[uint64]uint64)
	ss.serviceResponseDataStoreCurrentDown = make(map[uint64]uint64)
	ss.serviceResponseDataStoreCurrentAvgDelay = make(map[uint64]float32)

	// 重置今日统计
	for id := range ss.serviceStatusToday {
		ss.serviceStatusToday[id] = &_TodayStatsOfMonitor{}
	}

	log.Printf("ServiceSentinel紧急清理完成")
}

// getMemoryUsageEstimate 估算当前内存使用量（仅用于调试）
func (ss *ServiceSentinel) getMemoryUsageEstimate() map[string]int {
	ss.serviceResponseDataStoreLock.RLock()
	defer ss.serviceResponseDataStoreLock.RUnlock()

	usage := map[string]int{
		"status_records":  len(ss.serviceCurrentStatusData),
		"ping_records":    0,
		"monthly_records": len(ss.monthlyStatus),
		"monitors":        len(ss.monitors),
	}

	for _, pingMap := range ss.serviceResponsePing {
		usage["ping_records"] += len(pingMap)
	}

	return usage
}

// isActiveServer 检查服务器是否处于活跃状态
func (ss *ServiceSentinel) isActiveServer(serverID uint64) bool {
	// 检查全局服务器列表中是否存在此服务器
	SortedServerLock.RLock()
	defer SortedServerLock.RUnlock()

	// 遍历SortedServerList查找活跃服务器
	for _, server := range SortedServerList {
		if server != nil && server.ID == serverID {
			// 检查服务器是否有活跃连接
			return server.TaskStream != nil
		}
	}
	return false
}
