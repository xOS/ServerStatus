package singleton

import (
	"context"
	"log"
	"runtime"
	"sync"
	"time"
	
	"github.com/xos/serverstatus/model"
)

// GoroutinePool 管理Goroutine池以防止泄漏
type GoroutinePool struct {
	workers    int64
	queue      chan func()
	wg         sync.WaitGroup
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.RWMutex
	maxWorkers int64
	minWorkers int64
}

var (
	// 全局通知Goroutine池
	NotificationPool *GoroutinePool
	// 全局任务触发Goroutine池  
	TriggerTaskPool *GoroutinePool
	poolInitOnce    sync.Once
)

// InitGoroutinePools 初始化Goroutine池
func InitGoroutinePools() {
	poolInitOnce.Do(func() {
		// 通知池：最多20个worker，队列大小1000
		NotificationPool = NewGoroutinePool(5, 20, 1000)
		NotificationPool.Start()
		
		// 任务触发池：最多10个worker，队列大小500
		TriggerTaskPool = NewGoroutinePool(2, 10, 500)
		TriggerTaskPool.Start()
		
		log.Printf("Goroutine池初始化完成 - 通知池: 5-20 workers, 任务池: 2-10 workers")
	})
}

// NewGoroutinePool 创建新的Goroutine池
func NewGoroutinePool(minWorkers, maxWorkers int64, queueSize int) *GoroutinePool {
	ctx, cancel := context.WithCancel(context.Background())
	return &GoroutinePool{
		workers:    0,
		queue:      make(chan func(), queueSize),
		ctx:        ctx,
		cancel:     cancel,
		maxWorkers: maxWorkers,
		minWorkers: minWorkers,
	}
}

// Start 启动Goroutine池
func (p *GoroutinePool) Start() {
	// 启动最小数量的worker
	for i := int64(0); i < p.minWorkers; i++ {
		p.startWorker()
	}
	
	// 启动监控goroutine，定期检查池状态
	go p.monitor()
}

// Submit 提交任务到池中
func (p *GoroutinePool) Submit(task func()) bool {
	select {
	case p.queue <- task:
		// 检查是否需要增加worker
		p.mu.RLock()
		queueLen := len(p.queue)
		workers := p.workers
		p.mu.RUnlock()
		
		// 如果队列积压且worker数量未达到最大值，增加worker
		if queueLen > int(workers*2) && workers < p.maxWorkers {
			p.startWorker()
		}
		return true
	case <-p.ctx.Done():
		return false
	default:
		// 队列满了，记录警告但不阻塞
		log.Printf("警告：Goroutine池队列已满，丢弃任务")
		return false
	}
}

// startWorker 启动一个新的worker
func (p *GoroutinePool) startWorker() {
	p.mu.Lock()
	p.workers++
	currentWorkers := p.workers
	p.mu.Unlock()
	
	p.wg.Add(1)
	go func() {
		defer func() {
			p.wg.Done()
			p.mu.Lock()
			p.workers--
			p.mu.Unlock()
			
			if r := recover(); r != nil {
				log.Printf("Goroutine池worker panic恢复: %v", r)
			}
		}()
		
		idleTimer := time.NewTimer(time.Minute * 5) // 空闲5分钟后退出
		defer idleTimer.Stop()
		
		for {
			select {
			case task := <-p.queue:
				idleTimer.Reset(time.Minute * 5)
				
				// 执行任务
				func() {
					defer func() {
						if r := recover(); r != nil {
							log.Printf("Goroutine池任务执行panic恢复: %v", r)
						}
					}()
					task()
				}()
				
			case <-idleTimer.C:
				// 空闲超时，如果当前worker数量大于最小值，则退出
				p.mu.RLock()
				canExit := p.workers > p.minWorkers
				p.mu.RUnlock()
				
				if canExit {
					log.Printf("Goroutine池worker空闲退出，当前workers: %d", currentWorkers-1)
					return
				}
				idleTimer.Reset(time.Minute * 5)
				
			case <-p.ctx.Done():
				return
			}
		}
	}()
	
	log.Printf("Goroutine池启动新worker，当前workers: %d", currentWorkers)
}

// monitor 监控池状态
func (p *GoroutinePool) monitor() {
	ticker := time.NewTicker(time.Minute * 2)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			p.mu.RLock()
			workers := p.workers
			queueLen := len(p.queue)
			p.mu.RUnlock()
			
			// 记录状态
			if queueLen > 10 || workers > p.minWorkers {
				log.Printf("Goroutine池状态 - Workers: %d, 队列长度: %d, Goroutines总数: %d", 
					workers, queueLen, runtime.NumGoroutine())
			}
			
			// 内存压力过高时，强制减少worker
			if GetMemoryPressureLevel() >= 2 && workers > p.minWorkers {
				log.Printf("内存压力过高，减少Goroutine池worker数量")
				// 通过缩小队列容量来自然减少worker
			}
			
		case <-p.ctx.Done():
			return
		}
	}
}

// Stop 停止Goroutine池
func (p *GoroutinePool) Stop() {
	p.cancel()
	close(p.queue)
	p.wg.Wait()
	log.Printf("Goroutine池已停止")
}

// GetStats 获取池统计信息
func (p *GoroutinePool) GetStats() (workers int64, queueLen int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.workers, len(p.queue)
}

// SafeSendNotification 安全发送通知（使用池）
func SafeSendNotification(notificationTag string, desc string, muteLabel *string, ext ...*model.Server) {
	if NotificationPool == nil {
		InitGoroutinePools()
	}
	
	task := func() {
		SendNotification(notificationTag, desc, muteLabel, ext...)
	}
	
	if !NotificationPool.Submit(task) {
		log.Printf("警告：无法提交通知任务到池中，直接执行")
		// 如果池满了，直接执行但不创建新goroutine
		task()
	}
}

// SafeSendTriggerTasks 安全发送触发任务（使用池）
func SafeSendTriggerTasks(taskIDs []uint64, triggerServer uint64) {
	if TriggerTaskPool == nil {
		InitGoroutinePools()
	}
	
	task := func() {
		SendTriggerTasks(taskIDs, triggerServer)
	}
	
	if !TriggerTaskPool.Submit(task) {
		log.Printf("警告：无法提交触发任务到池中，直接执行")
		// 如果池满了，直接执行但不创建新goroutine
		task()
	}
}

// CleanupGoroutinePools 清理Goroutine池
func CleanupGoroutinePools() {
	if NotificationPool != nil {
		NotificationPool.Stop()
	}
	if TriggerTaskPool != nil {
		TriggerTaskPool.Stop()
	}
	log.Printf("Goroutine池清理完成")
}
