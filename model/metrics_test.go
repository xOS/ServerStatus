package model

import (
	"math"
	"testing"
	"time"

	pb "github.com/xos/serverstatus/proto"
)

func TestPB2StateCleansInvalidFloatMetrics(t *testing.T) {
	state := PB2State(&pb.State{
		Cpu:      math.Inf(1),
		Gpu:      -1,
		Load1:    math.NaN(),
		Load5:    -2,
		Load15:   1.5,
		MemUsed:  1024,
		DiskUsed: 2048,
		Temperatures: []*pb.State_SensorTemperature{
			{Name: "cpu", Temperature: math.Inf(-1)},
			{Name: "disk", Temperature: 42.5},
		},
	})

	if state.CPU != 0 {
		t.Fatalf("CPU = %v, want 0", state.CPU)
	}
	if state.GPU != 0 {
		t.Fatalf("GPU = %v, want 0", state.GPU)
	}
	if state.Load1 != 0 || state.Load5 != 0 || state.Load15 != 1.5 {
		t.Fatalf("loads = %v/%v/%v, want 0/0/1.5", state.Load1, state.Load5, state.Load15)
	}
	if len(state.Temperatures) != 2 || state.Temperatures[0].Temperature != 0 || state.Temperatures[1].Temperature != 42.5 {
		t.Fatalf("temperatures not cleaned correctly: %#v", state.Temperatures)
	}
}

func TestCycleTransferSnapshotUsesCumulativeState(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	rule := Rule{
		Type:          "transfer_all_cycle",
		Max:           1000,
		CycleStart:    &start,
		CycleInterval: 1,
		CycleUnit:     "day",
	}
	stats := &CycleTransferStats{}
	server := &Server{
		Common:                  Common{ID: 7},
		Name:                    "node-1",
		PrevTransferInSnapshot:  100000,
		PrevTransferOutSnapshot: 100000,
		State: &HostState{
			NetInTransfer:  300,
			NetOutTransfer: 200,
		},
	}

	if got := rule.Snapshot(stats, server, nil); got != nil {
		t.Fatalf("Snapshot() = %#v, want nil", got)
	}
	if stats.Transfer[server.ID] != 500 {
		t.Fatalf("cycle transfer = %d, want 500", stats.Transfer[server.ID])
	}
}

func TestApplyTrafficReportBaselineOnly(t *testing.T) {
	server := &Server{
		CumulativeNetInTransfer:  111,
		CumulativeNetOutTransfer: 222,
	}
	state := &HostState{
		NetInTransfer:  1000,
		NetOutTransfer: 2000,
	}

	result := server.ApplyTrafficReport(state, 1000, 2000, TrafficReportOptions{BaselineOnly: true})

	if !result.In.BaselineOnly || !result.Out.BaselineOnly {
		t.Fatalf("baseline flags = %#v/%#v, want true/true", result.In, result.Out)
	}
	if server.PrevTransferInSnapshot != 1000 || server.PrevTransferOutSnapshot != 2000 {
		t.Fatalf("snapshots = %d/%d, want 1000/2000", server.PrevTransferInSnapshot, server.PrevTransferOutSnapshot)
	}
	if server.CumulativeNetInTransfer != 111 || server.CumulativeNetOutTransfer != 222 {
		t.Fatalf("cumulative changed to %d/%d, want 111/222", server.CumulativeNetInTransfer, server.CumulativeNetOutTransfer)
	}
	if state.NetInTransfer != 111 || state.NetOutTransfer != 222 {
		t.Fatalf("state transfer = %d/%d, want 111/222", state.NetInTransfer, state.NetOutTransfer)
	}
}

func TestApplyTrafficReportAccumulatesValidDelta(t *testing.T) {
	server := &Server{
		PrevTransferInSnapshot:   100,
		PrevTransferOutSnapshot:  200,
		CumulativeNetInTransfer:  1000,
		CumulativeNetOutTransfer: 2000,
	}
	state := &HostState{}

	result := server.ApplyTrafficReport(state, 160, 275, TrafficReportOptions{Elapsed: time.Second})

	if result.IncreaseIn != 60 || result.IncreaseOut != 75 {
		t.Fatalf("increase = %d/%d, want 60/75", result.IncreaseIn, result.IncreaseOut)
	}
	if server.CumulativeNetInTransfer != 1060 || server.CumulativeNetOutTransfer != 2075 {
		t.Fatalf("cumulative = %d/%d, want 1060/2075", server.CumulativeNetInTransfer, server.CumulativeNetOutTransfer)
	}
	if state.NetInTransfer != 1060 || state.NetOutTransfer != 2075 {
		t.Fatalf("state transfer = %d/%d, want 1060/2075", state.NetInTransfer, state.NetOutTransfer)
	}
}

func TestApplyTrafficReportAccumulatesAfterZeroBaseline(t *testing.T) {
	server := &Server{}
	state := &HostState{}

	server.ApplyTrafficReport(state, 0, 0, TrafficReportOptions{BaselineOnly: true})
	result := server.ApplyTrafficReport(state, 100, 50, TrafficReportOptions{Elapsed: time.Second})

	if result.IncreaseIn != 100 || result.IncreaseOut != 50 {
		t.Fatalf("increase = %d/%d, want 100/50", result.IncreaseIn, result.IncreaseOut)
	}
	if server.CumulativeNetInTransfer != 100 || server.CumulativeNetOutTransfer != 50 {
		t.Fatalf("cumulative = %d/%d, want 100/50", server.CumulativeNetInTransfer, server.CumulativeNetOutTransfer)
	}
	if state.NetInTransfer != 100 || state.NetOutTransfer != 50 {
		t.Fatalf("state transfer = %d/%d, want 100/50", state.NetInTransfer, state.NetOutTransfer)
	}
}

func TestApplyTrafficReportHandlesCounterReset(t *testing.T) {
	server := &Server{
		PrevTransferInSnapshot:  1000,
		CumulativeNetInTransfer: 5000,
	}
	state := &HostState{}

	result := server.ApplyTrafficReport(state, 10, 0, TrafficReportOptions{Elapsed: time.Second})

	if !result.In.CounterReset {
		t.Fatalf("CounterReset = false, want true: %#v", result.In)
	}
	if server.PrevTransferInSnapshot != 10 {
		t.Fatalf("snapshot = %d, want 10", server.PrevTransferInSnapshot)
	}
	if server.CumulativeNetInTransfer != 5000 || state.NetInTransfer != 5000 {
		t.Fatalf("cumulative/state = %d/%d, want 5000/5000", server.CumulativeNetInTransfer, state.NetInTransfer)
	}
}

func TestApplyTrafficReportRejectsImplausibleSpike(t *testing.T) {
	maxDelta := PlausibleTrafficDelta(time.Second)
	server := &Server{
		PrevTransferInSnapshot:  100,
		CumulativeNetInTransfer: 5000,
	}
	state := &HostState{}
	raw := uint64(101) + maxDelta

	result := server.ApplyTrafficReport(state, raw, 0, TrafficReportOptions{Elapsed: time.Second})

	if !result.In.RejectedSpike {
		t.Fatalf("RejectedSpike = false, want true: %#v", result.In)
	}
	if result.In.Delta != maxDelta+1 {
		t.Fatalf("delta = %d, want %d", result.In.Delta, maxDelta+1)
	}
	if server.PrevTransferInSnapshot != raw {
		t.Fatalf("snapshot = %d, want %d", server.PrevTransferInSnapshot, raw)
	}
	if server.CumulativeNetInTransfer != 5000 || state.NetInTransfer != 5000 {
		t.Fatalf("cumulative/state = %d/%d, want 5000/5000", server.CumulativeNetInTransfer, state.NetInTransfer)
	}
}
