package model

import (
	"time"

	"gorm.io/gorm"
)

const (
	RuleCoverAll = iota
	RuleCoverIgnoreAll
)

type NResult struct {
	N uint64
}

type Rule struct {
	// 指标类型，cpu、memory、swap、disk、net_in_speed、net_out_speed
	// net_all_speed、transfer_in、transfer_out、transfer_all、offline
	// transfer_in_cycle、transfer_out_cycle、transfer_all_cycle
	Type     string          `json:"type,omitempty"`
	Min      float64         `json:"min,omitempty"`      // 最小阈值 (百分比、字节 kb ÷ 1024)
	Max      float64         `json:"max,omitempty"`      // 最大阈值 (百分比、字节 kb ÷ 1024)
	Duration uint64          `json:"duration,omitempty"` // 持续时间 (秒)
	Cover    uint64          `json:"cover,omitempty"`    // 覆盖范围 RuleCoverAll/IgnoreAll
	Ignore   map[uint64]bool `json:"ignore,omitempty"`   // 覆盖范围的排除

	// 只作为缓存使用，记录下次该检测的时间
	LastCycleStatus map[uint64]interface{} `json:"-"`
}

func percentage(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) * 100 / float64(total)
}

// Snapshot 未通过规则返回 struct{}{}, 通过返回 nil
func (u *Rule) Snapshot(server *Server, db *gorm.DB) interface{} {
	// 监控全部但是排除了此服务器
	if u.Cover == RuleCoverAll && u.Ignore[server.ID] {
		return nil
	}
	// 忽略全部但是指定监控了此服务器
	if u.Cover == RuleCoverIgnoreAll && !u.Ignore[server.ID] {
		return nil
	}

	var src float64

	switch u.Type {
	case "cpu":
		src = float64(server.State.CPU)
	case "memory":
		src = percentage(server.State.MemUsed, server.Host.MemTotal)
	case "swap":
		src = percentage(server.State.SwapUsed, server.Host.SwapTotal)
	case "disk":
		src = percentage(server.State.DiskUsed, server.Host.DiskTotal)
	case "net_in_speed":
		src = float64(server.State.NetInSpeed)
	case "net_out_speed":
		src = float64(server.State.NetOutSpeed)
	case "net_all_speed":
		src = float64(server.State.NetOutSpeed + server.State.NetOutSpeed)
	case "offline":
		if server.LastActive.IsZero() {
			src = 0
		} else {
			src = float64(server.LastActive.Unix())
		}
	case "load1":
		src = server.State.Load1
	case "load5":
		src = server.State.Load5
	case "load15":
		src = server.State.Load15
	case "tcp_conn_count":
		src = float64(server.State.TcpConnCount)
	case "udp_conn_count":
		src = float64(server.State.UdpConnCount)
	case "process_count":
		src = float64(server.State.ProcessCount)
	}

	if u.Type == "offline" && float64(time.Now().Unix())-src > 6 {
		return struct{}{}
	} else if (u.Max > 0 && src > u.Max) || (u.Min > 0 && src < u.Min) {
		return struct{}{}
	}

	return nil
}
