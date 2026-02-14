package model

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/xos/serverstatus/pkg/utils"
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
	Type          string          `json:"type,omitempty"`
	Min           float64         `json:"min,omitempty"`            // 最小阈值 (百分比、字节 kb ÷ 1024)
	Max           float64         `json:"max,omitempty"`            // 最大阈值 (百分比、字节 kb ÷ 1024)
	CycleStart    *time.Time      `json:"cycle_start,omitempty"`    // 流量统计的开始时间
	CycleInterval uint64          `json:"cycle_interval,omitempty"` // 流量统计周期
	CycleUnit     string          `json:"cycle_unit,omitempty"`     // 流量统计周期单位，默认hour,可选(hour, day, week, month, year)
	Duration      uint64          `json:"duration,omitempty"`       // 持续时间 (秒)
	Cover         uint64          `json:"cover,omitempty"`          // 覆盖范围 RuleCoverAll/IgnoreAll
	Ignore        map[uint64]bool `json:"ignore,omitempty"`         // 覆盖范围的排除

	// 只作为缓存使用，记录下次该检测的时间
	NextTransferAt  map[uint64]time.Time   `json:"-"`
	LastCycleStatus map[uint64]interface{} `json:"-"`
}

// MarshalJSON 自定义 JSON 序列化，确保 Ignore 字段正确序列化
func (r Rule) MarshalJSON() ([]byte, error) {
	// 创建一个临时结构体，将 Ignore 字段转换为字符串键的 map
	type Alias Rule
	aux := struct {
		Alias
		Ignore map[string]bool `json:"ignore,omitempty"`
	}{
		Alias: Alias(r),
	}

	// 转换 Ignore 字段
	if r.Ignore != nil {
		aux.Ignore = make(map[string]bool)
		for k, v := range r.Ignore {
			aux.Ignore[strconv.FormatUint(k, 10)] = v
		}
	}

	return utils.Json.Marshal(aux)
}

// UnmarshalJSON 自定义 JSON 反序列化，确保 Ignore 字段正确反序列化
func (r *Rule) UnmarshalJSON(data []byte) error {
	// 创建一个临时结构体，接收字符串键的 Ignore 字段
	type Alias Rule
	aux := struct {
		*Alias
		Ignore map[string]bool `json:"ignore,omitempty"`
	}{
		Alias: (*Alias)(r),
	}

	if err := utils.Json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// 转换 Ignore 字段
	if aux.Ignore != nil {
		r.Ignore = make(map[uint64]bool)
		for k, v := range aux.Ignore {
			if id, err := strconv.ParseUint(k, 10, 64); err == nil {
				r.Ignore[id] = v
			}
		}
	}

	return nil
}

func percentage(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) * 100 / float64(total)
}

// Snapshot 未通过规则返回 struct{}{}, 通过返回 nil
func (u *Rule) Snapshot(cycleTransferStats *CycleTransferStats, server *Server, db *gorm.DB) interface{} {
	// 安全检查：确保server不为nil
	if server == nil {
		return nil
	}

	// 监控全部但是排除了此服务器
	if u.Cover == RuleCoverAll && u.Ignore[server.ID] {
		return nil
	}
	// 忽略全部但是指定监控了此服务器
	if u.Cover == RuleCoverIgnoreAll && !u.Ignore[server.ID] {
		return nil
	}

	// 安全检查：确保server.State不为nil（除了offline类型的规则）
	if server.State == nil && u.Type != "offline" {
		return nil
	}

	// 安全检查：确保server.Host不为nil（对于需要Host信息的规则）
	if server.Host == nil && (u.Type == "memory" || u.Type == "swap" || u.Type == "disk") {
		return nil
	}

	// 循环区间流量检测 · 优化检测频率，确保超限时能及时检测
	if u.IsTransferDurationRule() && u.NextTransferAt[server.ID].After(time.Now()) {
		// 如果上次状态是超限（非nil），立即重新检测以确保及时报警
		if u.LastCycleStatus != nil && u.LastCycleStatus[server.ID] != nil {
			// 超限状态下不使用缓存，强制重新检测
		} else {
			// 正常状态下可以使用缓存
			return u.LastCycleStatus[server.ID]
		}
	}

	var src float64

	switch u.Type {
	case "cpu":
		src = float64(server.State.CPU)
	case "gpu":
		src = float64(server.State.GPU)
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
		src = float64(server.State.NetInSpeed + server.State.NetOutSpeed)
	case "transfer_in":
		src = float64(server.State.NetInTransfer)
	case "transfer_out":
		src = float64(server.State.NetOutTransfer)
	case "transfer_all":
		src = float64(server.State.NetOutTransfer + server.State.NetInTransfer)
	case "offline":
		// 修复离线检测逻辑：区分"从未上线"和"曾经在线但现在离线"
		if server.LastActive.IsZero() {
			// 从未上线过的服务器，不应该触发离线通知
			// 使用当前时间戳，确保不会触发离线检测
			src = float64(time.Now().Unix())
		} else {
			src = float64(server.LastActive.Unix())
		}
	case "transfer_in_cycle":
		src = float64(utils.Uint64SubInt64(server.State.NetInTransfer, server.PrevTransferInSnapshot))
		if u.CycleInterval != 0 && db != nil {
			var res NResult
			db.Model(&Transfer{}).Select("SUM(`in`) AS n").Where("datetime(`created_at`) >= datetime(?) AND server_id = ?", u.GetTransferDurationStart().UTC(), server.ID).Scan(&res)
			src += float64(res.N)
		}
	case "transfer_out_cycle":
		src = float64(utils.Uint64SubInt64(server.State.NetOutTransfer, server.PrevTransferOutSnapshot))
		if u.CycleInterval != 0 && db != nil {
			var res NResult
			db.Model(&Transfer{}).Select("SUM(`out`) AS n").Where("datetime(`created_at`) >= datetime(?) AND server_id = ?", u.GetTransferDurationStart().UTC(), server.ID).Scan(&res)
			src += float64(res.N)
		}
	case "transfer_all_cycle":
		src = float64(utils.Uint64SubInt64(server.State.NetOutTransfer, server.PrevTransferOutSnapshot) + utils.Uint64SubInt64(server.State.NetInTransfer, server.PrevTransferInSnapshot))
		if u.CycleInterval != 0 && db != nil {
			var res NResult
			db.Model(&Transfer{}).Select("SUM(`in`+`out`) AS n").Where("datetime(`created_at`) >= datetime(?) AND server_id = ?", u.GetTransferDurationStart().UTC(), server.ID).Scan(&res)
			src += float64(res.N)
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
	case "temperature_max":
		var temp []float64
		if server.State.Temperatures != nil {
			for _, tempStat := range server.State.Temperatures {
				if tempStat.Temperature != 0 {
					temp = append(temp, tempStat.Temperature)
				}
			}
			// 防御：空切片调用 slices.Max 会 panic
			if len(temp) > 0 {
				src = slices.Max(temp)
			} else {
				src = 0
			}
		}
	}

	// 循环区间流量检测 · 更新下次需要检测时间
	if u.IsTransferDurationRule() {
		// 优化的分层检测算法：确保超限时能及时检测和报警
		var seconds float64
		if u.Max > 0 {
			usagePercent := (src / u.Max) * 100
			switch {
			case usagePercent >= 100:
				// 已超限 → 立即检测（30秒）
				seconds = 30
			case usagePercent >= 95:
				// 95%+ 使用率 → 1分钟检测（即将超限）
				seconds = 60
			case usagePercent >= 90:
				// 90-94% 使用率 → 2分钟检测（高风险）
				seconds = 120
			case usagePercent >= 80:
				// 80-89% 使用率 → 5分钟检测（中高风险）
				seconds = 300
			case usagePercent >= 60:
				// 60-79% 使用率 → 10分钟检测
				seconds = 600
			case usagePercent >= 30:
				// 30-59% 使用率 → 15分钟检测
				seconds = 900
			case usagePercent >= 10:
				// 10-29% 使用率 → 20分钟检测
				seconds = 1200
			default:
				// 0-9% 使用率 → 30分钟检测
				seconds = 1800
			}
		} else {
			// 如果没有设置最大值，使用默认15分钟
			seconds = 900
		}
		if u.NextTransferAt == nil {
			u.NextTransferAt = make(map[uint64]time.Time)
		}
		if u.LastCycleStatus == nil {
			u.LastCycleStatus = make(map[uint64]interface{})
		}

		// 检查是否超限
		isOverLimit := (u.Max > 0 && src > u.Max) || (u.Min > 0 && src < u.Min)

		// 设置下次检测时间
		u.NextTransferAt[server.ID] = time.Now().Add(time.Second * time.Duration(seconds))

		// 更新缓存状态
		if isOverLimit {
			u.LastCycleStatus[server.ID] = struct{}{}
		} else {
			u.LastCycleStatus[server.ID] = nil
		}
		// 安全检查：确保cycleTransferStats不为nil
		if cycleTransferStats != nil {
			// 防御：确保内部map已初始化
			if cycleTransferStats.ServerName == nil {
				cycleTransferStats.ServerName = make(map[uint64]string)
			}
			if cycleTransferStats.Transfer == nil {
				cycleTransferStats.Transfer = make(map[uint64]uint64)
			}
			if cycleTransferStats.NextUpdate == nil {
				cycleTransferStats.NextUpdate = make(map[uint64]time.Time)
			}
			if cycleTransferStats.ServerName[server.ID] != server.Name {
				cycleTransferStats.ServerName[server.ID] = server.Name
			}
			cycleTransferStats.Transfer[server.ID] = uint64(src)
			cycleTransferStats.NextUpdate[server.ID] = u.NextTransferAt[server.ID]
			// 自动更新周期流量展示起止时间
			cycleTransferStats.From = u.GetTransferDurationStart()
			cycleTransferStats.To = u.GetTransferDurationEnd()
		}
	}

	if u.Type == "offline" {
		// 修复离线检测逻辑：只有曾经上线过且当前离线的服务器才能触发离线通知
		// 使用用户配置的 Duration（秒）作为离线阈值，如果未设置则默认 60 秒
		offlineThreshold := float64(u.Duration)
		if offlineThreshold <= 0 {
			offlineThreshold = 10 // 默认 10 秒，加快离线告警响应
		}
		offlineSeconds := float64(time.Now().Unix()) - src
		if !server.LastActive.IsZero() && !server.IsOnline && offlineSeconds > offlineThreshold {
			return struct{}{}
		}
		// 从未上线的服务器或当前在线的服务器不触发离线通知
		return nil
	} else if (u.Max > 0 && src > u.Max) || (u.Min > 0 && src < u.Min) {
		return struct{}{}
	}

	return nil
}

// IsTransferDurationRule 判断该规则是否属于周期流量规则 属于则返回true
func (rule Rule) IsTransferDurationRule() bool {
	return strings.HasSuffix(rule.Type, "_cycle")
}

// GetTransferDurationStart 获取周期流量的起始时间
func (rule Rule) GetTransferDurationStart() time.Time {
	// 防御：CycleStart 可能为 nil
	if rule.CycleStart == nil {
		return time.Now().Add(-1 * time.Hour)
	}
	// Accept uppercase and lowercase
	unit := strings.ToLower(rule.CycleUnit)
	startTime := *rule.CycleStart
	var nextTime time.Time
	switch unit {
	case "year":
		nextTime = startTime.AddDate(int(rule.CycleInterval), 0, 0)
		for time.Now().After(nextTime) {
			startTime = nextTime
			nextTime = nextTime.AddDate(int(rule.CycleInterval), 0, 0)
		}
	case "month":
		nextTime = startTime.AddDate(0, int(rule.CycleInterval), 0)
		for time.Now().After(nextTime) {
			startTime = nextTime
			nextTime = nextTime.AddDate(0, int(rule.CycleInterval), 0)
		}
	case "week":
		nextTime = startTime.AddDate(0, 0, 7*int(rule.CycleInterval))
		for time.Now().After(nextTime) {
			startTime = nextTime
			nextTime = nextTime.AddDate(0, 0, 7*int(rule.CycleInterval))
		}
	case "day":
		nextTime = startTime.AddDate(0, 0, int(rule.CycleInterval))
		for time.Now().After(nextTime) {
			startTime = nextTime
			nextTime = nextTime.AddDate(0, 0, int(rule.CycleInterval))
		}
	default:
		// For hour unit or not set.
		interval := 3600 * int64(rule.CycleInterval)
		startTime = time.Unix(rule.CycleStart.Unix()+(time.Now().Unix()-rule.CycleStart.Unix())/interval*interval, 0)
	}

	return startTime
}

// GetTransferDurationEnd 获取周期流量结束时间
func (rule Rule) GetTransferDurationEnd() time.Time {
	// 防御：CycleStart 可能为 nil
	if rule.CycleStart == nil {
		return time.Now()
	}
	// Accept uppercase and lowercase
	unit := strings.ToLower(rule.CycleUnit)
	startTime := *rule.CycleStart
	var nextTime time.Time
	switch unit {
	case "year":
		nextTime = startTime.AddDate(int(rule.CycleInterval), 0, 0)
		for time.Now().After(nextTime) {
			startTime = nextTime
			nextTime = nextTime.AddDate(int(rule.CycleInterval), 0, 0)
		}
	case "month":
		nextTime = startTime.AddDate(0, int(rule.CycleInterval), 0)
		for time.Now().After(nextTime) {
			startTime = nextTime
			nextTime = nextTime.AddDate(0, int(rule.CycleInterval), 0)
		}
	case "week":
		nextTime = startTime.AddDate(0, 0, 7*int(rule.CycleInterval))
		for time.Now().After(nextTime) {
			startTime = nextTime
			nextTime = nextTime.AddDate(0, 0, 7*int(rule.CycleInterval))
		}
	case "day":
		nextTime = startTime.AddDate(0, 0, int(rule.CycleInterval))
		for time.Now().After(nextTime) {
			startTime = nextTime
			nextTime = nextTime.AddDate(0, 0, int(rule.CycleInterval))
		}
	default:
		// For hour unit or not set.
		interval := 3600 * int64(rule.CycleInterval)
		startTime = time.Unix(rule.CycleStart.Unix()+(time.Now().Unix()-rule.CycleStart.Unix())/interval*interval, 0)
		nextTime = time.Unix(startTime.Unix()+interval, 0)
	}

	return nextTime
}
