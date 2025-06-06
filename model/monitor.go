package model

import (
	"fmt"
	"log"

	"github.com/robfig/cron/v3"
	"github.com/xos/serverstatus/pkg/utils"
	pb "github.com/xos/serverstatus/proto"
	"gorm.io/gorm"
)

const (
	_ = iota
	TaskTypeICMPPing
	TaskTypeTCPPing
	TaskTypeCommand
	TaskTypeTerminal
	TaskTypeUpgrade
	TaskTypeKeepalive
	TaskTypeTerminalGRPC
	TaskTypeNAT
	TaskTypeReportHostInfo
	TaskTypeFM
)

type TerminalTask struct {
	StreamID string
}

type TaskNAT struct {
	StreamID string
	Host     string
}

type TaskFM struct {
	StreamID string
}

const (
	MonitorCoverAll = iota
	MonitorCoverIgnoreAll
)

type Monitor struct {
	Common
	Name            string
	Type            uint8
	Target          string
	SkipServersRaw  string
	Duration        uint64
	Notify          bool
	NotificationTag string // 当前服务监控所属的通知组
	Cover           uint8

	EnableTriggerTask      bool     `gorm:"default: false"`
	EnableShowInService    bool     `gorm:"default: false"`
	FailTriggerTasksRaw    string   `gorm:"default:'[]'"`
	RecoverTriggerTasksRaw string   `gorm:"default:'[]'"`
	FailTriggerTasks       []uint64 `gorm:"-" json:"-"` // 失败时执行的触发任务id
	RecoverTriggerTasks    []uint64 `gorm:"-" json:"-"` // 恢复时执行的触发任务id

	MinLatency    float32
	MaxLatency    float32
	LatencyNotify bool

	SkipServers map[uint64]bool `gorm:"-" json:"-"`
	CronJobID   cron.EntryID    `gorm:"-" json:"-"`
}

func (m *Monitor) PB() *pb.Task {
	return &pb.Task{
		Id:   m.ID,
		Type: uint64(m.Type),
		Data: m.Target,
	}
}

// CronSpec 返回服务监控请求间隔对应的 cron 表达式
func (m *Monitor) CronSpec() string {
	if m.Duration == 0 {
		// 默认间隔 30 秒
		m.Duration = 30
	}
	return fmt.Sprintf("@every %ds", m.Duration)
}

func (m *Monitor) BeforeSave(tx *gorm.DB) error {

	if data, err := utils.Json.Marshal(m.FailTriggerTasks); err != nil {
		return err
	} else {
		m.FailTriggerTasksRaw = string(data)
	}
	if data, err := utils.Json.Marshal(m.RecoverTriggerTasks); err != nil {
		return err
	} else {
		m.RecoverTriggerTasksRaw = string(data)
	}
	return nil
}

func (m *Monitor) AfterFind(tx *gorm.DB) error {
	m.SkipServers = make(map[uint64]bool)
	var skipServers []uint64
	if err := utils.Json.Unmarshal([]byte(m.SkipServersRaw), &skipServers); err != nil {
		log.Println("NG>> Monitor.AfterFind:", err)
		return nil
	}
	for i := 0; i < len(skipServers); i++ {
		m.SkipServers[skipServers[i]] = true
	}

	// 加载触发任务列表
	if err := utils.Json.Unmarshal([]byte(m.FailTriggerTasksRaw), &m.FailTriggerTasks); err != nil {
		log.Printf("解析Monitor %s 的FailTriggerTasksRaw失败（%s），重置为空数组: %v", m.Name, m.FailTriggerTasksRaw, err)
		m.FailTriggerTasks = []uint64{}
		m.FailTriggerTasksRaw = "[]"
		// 更新数据库中的无效数据
		tx.Model(m).Update("fail_trigger_tasks_raw", "[]")
	}
	if err := utils.Json.Unmarshal([]byte(m.RecoverTriggerTasksRaw), &m.RecoverTriggerTasks); err != nil {
		log.Printf("解析Monitor %s 的RecoverTriggerTasksRaw失败（%s），重置为空数组: %v", m.Name, m.RecoverTriggerTasksRaw, err)
		m.RecoverTriggerTasks = []uint64{}
		m.RecoverTriggerTasksRaw = "[]"
		// 更新数据库中的无效数据
		tx.Model(m).Update("recover_trigger_tasks_raw", "[]")
	}

	return nil
}

// IsServiceSentinelNeeded 判断该任务类型是否需要进行服务监控 需要则返回true
func IsServiceSentinelNeeded(t uint64) bool {
	return t != TaskTypeCommand && t != TaskTypeTerminalGRPC && t != TaskTypeUpgrade
}

func (m *Monitor) InitSkipServers() error {
	m.SkipServers = make(map[uint64]bool)

	// 如果SkipServersRaw为空或无效，设置为空数组
	if m.SkipServersRaw == "" || m.SkipServersRaw == "null" {
		m.SkipServersRaw = "[]"
		return nil
	}

	var skipServers []uint64
	if err := utils.Json.Unmarshal([]byte(m.SkipServersRaw), &skipServers); err != nil {
		log.Printf("监控器 %s 的SkipServersRaw格式无效（%s），重置为空数组: %v", m.Name, m.SkipServersRaw, err)
		m.SkipServersRaw = "[]"
		return nil
	}

	for i := 0; i < len(skipServers); i++ {
		m.SkipServers[skipServers[i]] = true
	}
	return nil
}
