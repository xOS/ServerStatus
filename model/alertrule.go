package model

import (
	"time"

	"github.com/xos/serverstatus/pkg/utils"
	"gorm.io/gorm"
)

const (
	ModeAlwaysTrigger  = 0
	ModeOnetimeTrigger = 1
)

type CycleTransferStats struct {
	Name          string
	From          time.Time
	To            time.Time
	Max           uint64
	Min           uint64
	ServerName    map[uint64]string
	Transfer      map[uint64]uint64
	NextUpdate    map[uint64]time.Time // 下次检查时间
	LastResetTime map[uint64]time.Time // 上次重置时间（用于判断是否需要重置）
}

type AlertRule struct {
	Common
	Name                   string
	RulesRaw               string
	Enable                 *bool
	TriggerMode            int      `gorm:"default:0"` // 触发模式: 0-始终触发(默认) 1-单次触发
	NotificationTag        string   // 该通知规则所在的通知组
	FailTriggerTasksRaw    string   `gorm:"default:'[]'"`
	RecoverTriggerTasksRaw string   `gorm:"default:'[]'"`
	Rules                  []Rule   `gorm:"-" json:"-"`
	FailTriggerTasks       []uint64 `gorm:"-" json:"-"` // 失败时执行的触发任务id
	RecoverTriggerTasks    []uint64 `gorm:"-" json:"-"` // 恢复时执行的触发任务id
}

func (r *AlertRule) BeforeSave(tx *gorm.DB) error {
	if data, err := utils.Json.Marshal(r.Rules); err != nil {
		return err
	} else {
		r.RulesRaw = string(data)
	}
	if data, err := utils.Json.Marshal(r.FailTriggerTasks); err != nil {
		return err
	} else {
		r.FailTriggerTasksRaw = string(data)
	}
	if data, err := utils.Json.Marshal(r.RecoverTriggerTasks); err != nil {
		return err
	} else {
		r.RecoverTriggerTasksRaw = string(data)
	}
	return nil
}

func (r *AlertRule) AfterFind(tx *gorm.DB) error {
	var err error
	if err = utils.Json.Unmarshal([]byte(r.RulesRaw), &r.Rules); err != nil {
		// 更新数据库中的无效数据
		tx.Model(r).Update("rules_raw", "[]")
	}
	if err = utils.Json.Unmarshal([]byte(r.FailTriggerTasksRaw), &r.FailTriggerTasks); err != nil {
		// 更新数据库中的无效数据
		tx.Model(r).Update("fail_trigger_tasks_raw", "[]")
	}
	if err = utils.Json.Unmarshal([]byte(r.RecoverTriggerTasksRaw), &r.RecoverTriggerTasks); err != nil {
		// 更新数据库中的无效数据
		tx.Model(r).Update("recover_trigger_tasks_raw", "[]")
	}
	return nil
}

func (r *AlertRule) Enabled() bool {
	return r.Enable != nil && *r.Enable
}

// Snapshot 对传入的Server进行该通知规则下所有type的检查 返回包含每项检查结果的空接口
func (r *AlertRule) Snapshot(cycleTransferStats *CycleTransferStats, server *Server, db *gorm.DB) []interface{} {
	// 安全检查：确保AlertRule和Rules不为nil
	if r == nil || r.Rules == nil {
		return nil
	}

	var point []interface{}
	for i := 0; i < len(r.Rules); i++ {
		// Rule 是值类型（非指针），无需判空
		point = append(point, r.Rules[i].Snapshot(cycleTransferStats, server, db))
	}
	return point
}

// Check 传入包含当前通知规则下所有type检查结果的空接口 返回通知持续时间与是否通过通知检查(通过则返回true)
func (r *AlertRule) Check(points [][]interface{}) (int, bool) {
	var maxNum int           // 报警持续时间
	var count int            // 检查未通过的个数
	var hasTransferRule bool // 是否包含流量规则

	for i := 0; i < len(r.Rules); i++ {
		if r.Rules[i].IsTransferDurationRule() {
			// 循环区间流量报警
			hasTransferRule = true
			if maxNum < 1 {
				maxNum = 1
			}
			// 检查最新的流量状态
			if len(points) > 0 && len(points[i]) > 0 {
				// 检查最近的几个采样点，确保流量超限检测的及时性
				recentFailCount := 0
				checkPoints := len(points[i])
				if checkPoints > 3 {
					checkPoints = 3 // 只检查最近3个点
				}

				for j := len(points[i]) - checkPoints; j < len(points[i]); j++ {
					if points[i][j] != nil {
						recentFailCount++
					}
				}

				// 如果最近的采样点中有超限，则认为检查未通过
				if recentFailCount > 0 {
					count++
				}
			}
		} else if r.Rules[i].Type == "offline" {
			// 离线规则特殊处理：Duration 表示离线秒数阈值，不是采样点数
			// 只需要检查最新的采样点即可
			if maxNum < 1 {
				maxNum = 1
			}
			if len(points) > 0 && i < len(points[len(points)-1]) {
				// 检查最新采样点，如果不为 nil 说明已触发离线检测
				if points[len(points)-1][i] != nil {
					count++
				}
			}
		} else {
			// 常规报警
			total := 0.0
			fail := 0.0
			num := int(r.Rules[i].Duration)
			if num > maxNum {
				maxNum = num
			}
			if len(points) < num {
				continue
			}
			for j := len(points) - 1; j >= 0 && len(points)-num <= j; j-- {
				total++
				if points[j][i] != nil {
					fail++
				}
			}
			// 当70%以上的采样点未通过规则判断时 才认为当前检查未通过
			if fail/total > 0.7 {
				count++
				break
			}
		}
	}

	// 修改逻辑：
	// 1. 如果包含流量规则且流量超限，直接触发报警
	// 2. 对于其他规则，仍然要求所有规则都未通过才触发报警
	if hasTransferRule && count > 0 {
		// 有流量规则且有规则未通过，触发报警
		return maxNum, false
	}

	// 仅当所有检查均未通过时 返回false
	return maxNum, count != len(r.Rules)
}
