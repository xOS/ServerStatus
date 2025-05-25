package model

import (
	"fmt"
	"html/template"
	"log"
	"sync"
	"time"

	"github.com/xos/serverstatus/pkg/utils"
	pb "github.com/xos/serverstatus/proto"
	"gorm.io/gorm"
)

// State 服务器状态
type State struct {
	CPU            float64   `json:"cpu"`
	MemUsed        uint64    `json:"mem_used"`
	SwapUsed       uint64    `json:"swap_used"`
	DiskUsed       uint64    `json:"disk_used"`
	NetInTransfer  uint64    `json:"net_in_transfer"`
	NetOutTransfer uint64    `json:"net_out_transfer"`
	NetInSpeed     uint64    `json:"net_in_speed"`
	NetOutSpeed    uint64    `json:"net_out_speed"`
	Uptime         uint64    `json:"uptime"`
	Load1          float64   `json:"load1"`
	Load5          float64   `json:"load5"`
	Load15         float64   `json:"load15"`
	TcpConnCount   int64     `json:"tcp_conn_count"`
	UdpConnCount   int64     `json:"udp_conn_count"`
	ProcessCount   int64     `json:"process_count"`
	Temperatures   []float64 `json:"temperatures"`
}

type Server struct {
	ID                     uint64     `json:"id" gorm:"primaryKey"`
	Name                   string     `json:"name"`
	Secret                 string     `json:"secret"`
	Tag                    string     `json:"tag"`           // 分组名
	DisplayIndex           int        `json:"display_index"` // 展示排序，越大越靠前
	Host                   *Host      `json:"host" gorm:"-"`
	State                  *State     `json:"state" gorm:"-"`
	LastActive             time.Time  `json:"last_active"`
	Note                   string     `json:"note"`                  // 管理员可见备注
	PublicNote             string     `json:"public_note,omitempty"` // 公开备注
	EnableDDNS             bool       `json:"enable_ddns"`           // 启用DDNS
	DDNSProfilesRaw        string     `json:"ddns_profiles_raw" gorm:"default:'[]'"`
	HideForGuest           bool       `json:"hide_for_guest"`
	DDNSProfiles           []uint64   `gorm:"-" json:"-"`         // DDNS配置
	LastStateBeforeOffline *HostState `gorm:"-" json:"-"`         // 离线前最后一次状态
	IsOnline               bool       `gorm:"-" json:"is_online"` // 是否在线

	// 累计流量统计
	CumulativeNetInTransfer  uint64    `json:"cumulative_net_in_transfer" gorm:"default:0"`
	CumulativeNetOutTransfer uint64    `json:"cumulative_net_out_transfer" gorm:"default:0"`
	LastTrafficReset         time.Time `json:"last_traffic_reset"`

	// 持久化保存的最后状态
	LastStateJSON string    `gorm:"type:text" json:"-"` // 最后一次状态的JSON格式
	LastOnline    time.Time // 最后一次在线时间

	TaskClose     chan error                         `gorm:"-" json:"-"`
	TaskCloseLock *sync.Mutex                        `gorm:"-" json:"-"`
	TaskStream    pb.ServerService_RequestTaskServer `gorm:"-" json:"-"`

	PrevTransferInSnapshot  int64 `gorm:"-" json:"-"` // 上次数据点时的入站使用量
	PrevTransferOutSnapshot int64 `gorm:"-" json:"-"` // 上次数据点时的出站使用量
}

func (s *Server) CopyFromRunningServer(old *Server) {
	s.Host = old.Host
	s.State = old.State
	s.LastActive = old.LastActive
	s.TaskClose = old.TaskClose
	s.TaskCloseLock = old.TaskCloseLock
	s.TaskStream = old.TaskStream
	s.PrevTransferInSnapshot = old.PrevTransferInSnapshot
	s.PrevTransferOutSnapshot = old.PrevTransferOutSnapshot
	s.LastStateBeforeOffline = old.LastStateBeforeOffline
	s.IsOnline = old.IsOnline
	s.CumulativeNetInTransfer = old.CumulativeNetInTransfer
	s.CumulativeNetOutTransfer = old.CumulativeNetOutTransfer
}

func (s *Server) AfterFind(tx *gorm.DB) error {
	if s.DDNSProfilesRaw != "" {
		if err := utils.Json.Unmarshal([]byte(s.DDNSProfilesRaw), &s.DDNSProfiles); err != nil {
			log.Println("NG>> Server.AfterFind:", err)
			return nil
		}
	}
	return nil
}

func boolToString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func (s Server) MarshalForDashboard() template.JS {
	name, _ := utils.Json.Marshal(s.Name)
	tag, _ := utils.Json.Marshal(s.Tag)
	note, _ := utils.Json.Marshal(s.Note)
	secret, _ := utils.Json.Marshal(s.Secret)
	ddnsProfilesRaw, _ := utils.Json.Marshal(s.DDNSProfilesRaw)
	publicNote, _ := utils.Json.Marshal(s.PublicNote)
	return template.JS(fmt.Sprintf(`{"ID":%d,"Name":%s,"Secret":%s,"DisplayIndex":%d,"Tag":%s,"Note":%s,"HideForGuest": %s,"EnableDDNS": %s,"DDNSProfilesRaw": %s,"PublicNote": %s}`, s.ID, name, secret, s.DisplayIndex, tag, note, boolToString(s.HideForGuest), boolToString(s.EnableDDNS), ddnsProfilesRaw, publicNote))
}
