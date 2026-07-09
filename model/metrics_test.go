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
