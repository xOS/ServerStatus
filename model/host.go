package model

import (
	pb "github.com/xos/serverstatus/proto"
)

const (
	_ = iota

	MTReportHostState
)

type SensorTemperature struct {
	Name        string
	Temperature float64
}

type HostState struct {
	CPU            float64
	MemUsed        uint64
	SwapUsed       uint64
	DiskUsed       uint64
	NetInTransfer  uint64
	NetOutTransfer uint64
	NetInSpeed     uint64
	NetOutSpeed    uint64
	Uptime         uint64
	Load1          float64
	Load5          float64
	Load15         float64
	TcpConnCount   uint64
	UdpConnCount   uint64
	ProcessCount   uint64
	Temperatures   []SensorTemperature
	GPU            float64
}

func (s *HostState) PB() *pb.State {
	var ts []*pb.State_SensorTemperature
	for _, t := range s.Temperatures {
		ts = append(ts, &pb.State_SensorTemperature{
			Name:        t.Name,
			Temperature: t.Temperature,
		})
	}

	return &pb.State{
		Cpu:            s.CPU,
		MemUsed:        s.MemUsed,
		SwapUsed:       s.SwapUsed,
		DiskUsed:       s.DiskUsed,
		NetInTransfer:  s.NetInTransfer,
		NetOutTransfer: s.NetOutTransfer,
		NetInSpeed:     s.NetInSpeed,
		NetOutSpeed:    s.NetOutSpeed,
		Uptime:         s.Uptime,
		Load1:          s.Load1,
		Load5:          s.Load5,
		Load15:         s.Load15,
		TcpConnCount:   s.TcpConnCount,
		UdpConnCount:   s.UdpConnCount,
		ProcessCount:   s.ProcessCount,
		Temperatures:   ts,
		Gpu:            s.GPU,
	}
}

func PB2State(s *pb.State) HostState {
	var ts []SensorTemperature
	for _, t := range s.GetTemperatures() {
		ts = append(ts, SensorTemperature{
			Name:        t.GetName(),
			Temperature: t.GetTemperature(),
		})
	}

	return HostState{
		CPU:            s.GetCpu(),
		MemUsed:        s.GetMemUsed(),
		SwapUsed:       s.GetSwapUsed(),
		DiskUsed:       s.GetDiskUsed(),
		NetInTransfer:  s.GetNetInTransfer(),
		NetOutTransfer: s.GetNetOutTransfer(),
		NetInSpeed:     s.GetNetInSpeed(),
		NetOutSpeed:    s.GetNetOutSpeed(),
		Uptime:         s.GetUptime(),
		Load1:          s.GetLoad1(),
		Load5:          s.GetLoad5(),
		Load15:         s.GetLoad15(),
		TcpConnCount:   s.GetTcpConnCount(),
		UdpConnCount:   s.GetUdpConnCount(),
		ProcessCount:   s.GetProcessCount(),
		Temperatures:   ts,
		GPU:            s.GetGpu(),
	}
}

type Host struct {
	OS              string   `json:"OS,omitempty"`
	Platform        string   `json:"Platform,omitempty"`
	PlatformVersion string   `json:"PlatformVersion,omitempty"`
	CPU             []string `json:"CPU"`
	MemTotal        uint64   `json:"MemTotal"`
	DiskTotal       uint64   `json:"DiskTotal"`
	SwapTotal       uint64   `json:"SwapTotal"`
	Arch            string   `json:"Arch,omitempty"`
	Virtualization  string   `json:"Virtualization,omitempty"`
	BootTime        uint64   `json:"BootTime"`
	IP              string   `json:"-"`
	CountryCode     string   `json:"CountryCode,omitempty"`
	Version         string   `json:"Version,omitempty"`
	GPU             []string `json:"GPU"`
}

func (h *Host) Initialize() {
	if h.CPU == nil {
		h.CPU = []string{}
	}
	if h.GPU == nil {
		h.GPU = []string{}
	}
	// 确保其他字段有合理的默认值
	// if h.OS == "" {
	// 	h.OS = "Unknown"
	// }
	// if h.Platform == "" {
	// 	h.Platform = "Unknown"
	// }
	// if h.Arch == "" {
	// 	h.Arch = "Unknown"
	// }
}

func (h *Host) PB() *pb.Host {
	h.Initialize()

	return &pb.Host{
		Os:              h.OS,
		Platform:        h.Platform,
		PlatformVersion: h.PlatformVersion,
		Cpu:             h.CPU,
		MemTotal:        h.MemTotal,
		DiskTotal:       h.DiskTotal,
		SwapTotal:       h.SwapTotal,
		Arch:            h.Arch,
		Virtualization:  h.Virtualization,
		BootTime:        h.BootTime,
		Ip:              h.IP,
		CountryCode:     h.CountryCode,
		Version:         h.Version,
		Gpu:             h.GPU,
	}
}

func PB2Host(h *pb.Host) Host {
	host := Host{
		OS:              h.GetOs(),
		Platform:        h.GetPlatform(),
		PlatformVersion: h.GetPlatformVersion(),
		CPU:             h.GetCpu(),
		MemTotal:        h.GetMemTotal(),
		DiskTotal:       h.GetDiskTotal(),
		SwapTotal:       h.GetSwapTotal(),
		Arch:            h.GetArch(),
		Virtualization:  h.GetVirtualization(),
		BootTime:        h.GetBootTime(),
		IP:              h.GetIp(),
		CountryCode:     h.GetCountryCode(),
		Version:         h.GetVersion(),
		GPU:             h.GetGpu(),
	}

	host.Initialize()

	return host
}
