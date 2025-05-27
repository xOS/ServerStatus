package model

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	ProviderDummy = iota
	ProviderWebHook
	ProviderCloudflare
	ProviderTencentCloud
)

const (
	_Dummy        = "dummy"
	_WebHook      = "webhook"
	_Cloudflare   = "cloudflare"
	_TencentCloud = "tencentcloud"
)

var ProviderMap = map[uint8]string{
	ProviderDummy:        _Dummy,
	ProviderWebHook:      _WebHook,
	ProviderCloudflare:   _Cloudflare,
	ProviderTencentCloud: _TencentCloud,
}

var ProviderList = []DDNSProvider{
	{
		Name: _Dummy,
		ID:   ProviderDummy,
	},
	{
		Name:         _Cloudflare,
		ID:           ProviderCloudflare,
		AccessSecret: true,
	},
	{
		Name:         _TencentCloud,
		ID:           ProviderTencentCloud,
		AccessID:     true,
		AccessSecret: true,
	},
	// Least frequently used, always place this at the end
	{
		Name:               _WebHook,
		ID:                 ProviderWebHook,
		AccessID:           true,
		AccessSecret:       true,
		WebhookURL:         true,
		WebhookMethod:      true,
		WebhookRequestType: true,
		WebhookRequestBody: true,
		WebhookHeaders:     true,
	},
}

type DDNSProfile struct {
	Common
	EnableIPv4         *bool
	EnableIPv6         *bool
	MaxRetries         uint64
	Name               string
	Provider           uint8
	AccessID           string
	AccessSecret       string
	WebhookURL         string
	WebhookMethod      uint8
	WebhookRequestType uint8
	WebhookRequestBody string
	WebhookHeaders     string

	Domains    []string `gorm:"-"`
	DomainsRaw string
}

func (d DDNSProfile) TableName() string {
	return "ddns"
}

func (d *DDNSProfile) AfterFind(tx *gorm.DB) error {
	if d.DomainsRaw != "" {
		d.Domains = strings.Split(d.DomainsRaw, ",")
	}
	return nil
}

type DDNSProvider struct {
	Name               string
	ID                 uint8
	AccessID           bool
	AccessSecret       bool
	WebhookURL         bool
	WebhookMethod      bool
	WebhookRequestType bool
	WebhookRequestBody bool
	WebhookHeaders     bool
}

// DDNSRecordState 存储DDNS记录的当前状态，用于避免重复通知
type DDNSRecordState struct {
	Common
	ServerID   uint64    `gorm:"index:idx_ddns_record,unique" json:"server_id"`
	Domain     string    `gorm:"index:idx_ddns_record,unique;size:255" json:"domain"`
	RecordType string    `gorm:"index:idx_ddns_record,unique;size:10" json:"record_type"`
	LastIP     string    `gorm:"size:45" json:"last_ip"`                       // 上次记录的IP地址
	LastUpdate time.Time `gorm:"default:CURRENT_TIMESTAMP" json:"last_update"` // 上次更新时间
}

func (d DDNSRecordState) TableName() string {
	return "ddns_record_states"
}
