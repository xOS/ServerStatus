package model

import (
	"github.com/robfig/cron/v3"
	pb "github.com/xos/serverstatus/proto"
)

const (
	_ = iota
	TaskTypeTerminal
	TaskTypeUpgrade
)

type TerminalTask struct {
	// websocket 主机名
	Host string `json:"host,omitempty"`
	// 是否启用 SSL
	UseSSL bool `json:"use_ssl,omitempty"`
	// 会话标识
	Session string `json:"session,omitempty"`
	// Agent在连接Server时需要的额外Cookie信息
	Cookie string `json:"cookie,omitempty"`
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
