package model

import (
	"math"
	"time"

	"github.com/xos/serverstatus/pkg/utils"
)

const (
	defaultTrafficMaxDelta = uint64(10 * 1024 * 1024 * 1024 * 1024) // 10 TiB
	minTrafficMaxDelta     = uint64(10 * 1024 * 1024 * 1024)        // 10 GiB
	maxTrafficBytesPerSec  = uint64(50 * 1024 * 1024 * 1024)        // 400 Gbps
)

type TrafficReportOptions struct {
	BaselineOnly bool
	Elapsed      time.Duration
}

type TrafficReportResult struct {
	RawIn       uint64
	RawOut      uint64
	IncreaseIn  uint64
	IncreaseOut uint64
	MaxDelta    uint64
	In          TrafficDirectionResult
	Out         TrafficDirectionResult
}

type TrafficDirectionResult struct {
	Delta         uint64
	Increase      uint64
	CounterReset  bool
	RejectedSpike bool
	Overflow      bool
	BaselineOnly  bool
}

func TotalTransfer(in, out uint64) uint64 {
	return utils.Uint64SaturatingAdd(in, out)
}

func TrafficUsagePercent(used, max uint64) float64 {
	if max == 0 {
		return 0
	}
	return math.Round((float64(used)/float64(max))*10000) / 100
}

func (s *Server) ApplyTrafficReport(state *HostState, rawIn, rawOut uint64, opts TrafficReportOptions) TrafficReportResult {
	result := TrafficReportResult{
		RawIn:    rawIn,
		RawOut:   rawOut,
		MaxDelta: PlausibleTrafficDelta(opts.Elapsed),
	}
	if s == nil || state == nil {
		return result
	}

	if opts.BaselineOnly {
		s.PrevTransferInSnapshot = rawIn
		s.PrevTransferOutSnapshot = rawOut
		s.PrevTransferInSnapshotSet = true
		s.PrevTransferOutSnapshotSet = true
		state.NetInTransfer = s.CumulativeNetInTransfer
		state.NetOutTransfer = s.CumulativeNetOutTransfer
		result.In.BaselineOnly = true
		result.Out.BaselineOnly = true
		return result
	}

	result.In = applyTrafficDirection(rawIn, &s.PrevTransferInSnapshot, &s.PrevTransferInSnapshotSet, &s.CumulativeNetInTransfer, result.MaxDelta)
	result.Out = applyTrafficDirection(rawOut, &s.PrevTransferOutSnapshot, &s.PrevTransferOutSnapshotSet, &s.CumulativeNetOutTransfer, result.MaxDelta)
	result.IncreaseIn = result.In.Increase
	result.IncreaseOut = result.Out.Increase

	state.NetInTransfer = s.CumulativeNetInTransfer
	state.NetOutTransfer = s.CumulativeNetOutTransfer
	return result
}

func PlausibleTrafficDelta(elapsed time.Duration) uint64 {
	if elapsed <= 0 {
		return minTrafficMaxDelta
	}

	seconds := uint64(elapsed.Seconds())
	if seconds == 0 {
		seconds = 1
	}
	if seconds >= 31536000 {
		return defaultTrafficMaxDelta
	}

	dynamicMax := maxTrafficBytesPerSec * seconds
	if dynamicMax < minTrafficMaxDelta {
		return minTrafficMaxDelta
	}
	if dynamicMax > defaultTrafficMaxDelta {
		return defaultTrafficMaxDelta
	}
	return dynamicMax
}

func applyTrafficDirection(raw uint64, snapshot *uint64, snapshotSet *bool, cumulative *uint64, maxDelta uint64) TrafficDirectionResult {
	var result TrafficDirectionResult
	if snapshot == nil || cumulative == nil {
		return result
	}

	initialized := snapshotSet != nil && *snapshotSet
	if !initialized && *snapshot != 0 {
		initialized = true
		if snapshotSet != nil {
			*snapshotSet = true
		}
	}
	if !initialized {
		*snapshot = raw
		if snapshotSet != nil {
			*snapshotSet = true
		}
		result.BaselineOnly = true
		return result
	}

	prev := *snapshot
	*snapshot = raw
	if snapshotSet != nil {
		*snapshotSet = true
	}

	if raw < prev {
		result.CounterReset = true
		return result
	}

	increase := raw - prev
	result.Delta = increase
	if increase > maxDelta {
		result.RejectedSpike = true
		return result
	}

	next := utils.Uint64SaturatingAdd(*cumulative, increase)
	if next == ^uint64(0) && increase > 0 && *cumulative != ^uint64(0) {
		result.Overflow = true
	}
	*cumulative = next
	result.Increase = increase
	return result
}
