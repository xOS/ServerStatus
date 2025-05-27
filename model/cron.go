package model

import (
	"log"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/xos/serverstatus/pkg/utils"
	"gorm.io/gorm"
)

const (
	CronCoverIgnoreAll = iota
	CronCoverAll
	CronCoverAlertTrigger
	CronTypeCronTask    = 0
	CronTypeTriggerTask = 1
)

type Cron struct {
	Common
	Name            string
	TaskType        uint8  `gorm:"default:0"` // 0:计划任务 1:触发任务
	Scheduler       string //分钟 小时 天 月 星期
	Command         string
	Servers         []uint64  `gorm:"-"`
	PushSuccessful  bool      // 推送成功的通知
	NotificationTag string    // 指定通知方式的分组
	LastExecutedAt  time.Time // 最后一次执行时间
	LastResult      bool      // 最后一次执行结果
	Cover           uint8     // 计划任务覆盖范围 (0:仅覆盖特定服务器 1:仅忽略特定服务器 2:由触发该计划任务的服务器执行)

	CronJobID  cron.EntryID `gorm:"-"`
	ServersRaw string
}

func (c *Cron) AfterFind(tx *gorm.DB) error {
	if c.ServersRaw == "" {
		c.ServersRaw = "[]"
		c.Servers = []uint64{}
		return nil
	}
	
	// 尝试解析JSON，如果失败则修复格式
	err := utils.Json.Unmarshal([]byte(c.ServersRaw), &c.Servers)
	if err != nil {
		// 检查是否是 "[]," 这种无效格式
		if c.ServersRaw == "[]," || c.ServersRaw == "," {
			c.ServersRaw = "[]"
			c.Servers = []uint64{}
			// 更新数据库中的无效数据
			tx.Model(c).Update("servers_raw", "[]")
			return nil
		}
		
		// 其他格式错误，尝试修复
		log.Printf("解析Cron任务 %s 的ServersRaw失败（%s），重置为空数组: %v", c.Name, c.ServersRaw, err)
		c.ServersRaw = "[]"
		c.Servers = []uint64{}
		// 更新数据库中的无效数据
		tx.Model(c).Update("servers_raw", "[]")
		return nil
	}
	
	return nil
}
