package singleton

import (
	"fmt"
	"sync"
	"time"

	"github.com/libdns/cloudflare"
	tencentcloud "github.com/nezhahq/libdns-tencentcloud"

	"github.com/xos/serverstatus/model"
	ddns2 "github.com/xos/serverstatus/pkg/ddns"
	"github.com/xos/serverstatus/pkg/ddns/dummy"
	"github.com/xos/serverstatus/pkg/ddns/webhook"
)

var (
	ddnsCache     map[uint64]*model.DDNSProfile
	ddnsCacheLock sync.RWMutex
)

func initDDNS() {
	OnDDNSUpdate()
	OnNameserverUpdate()
}

func OnDDNSUpdate() {
	var ddns []*model.DDNSProfile
	DB.Find(&ddns)
	ddnsCacheLock.Lock()
	defer ddnsCacheLock.Unlock()
	ddnsCache = make(map[uint64]*model.DDNSProfile)
	for i := 0; i < len(ddns); i++ {
		ddnsCache[ddns[i].ID] = ddns[i]
	}
}

func OnNameserverUpdate() {
	ddns2.InitDNSServers(Conf.DNSServers)
}

func GetDDNSProvidersFromProfiles(profileId []uint64, ip *ddns2.IP) ([]*ddns2.Provider, error) {
	profiles := make([]*model.DDNSProfile, 0, len(profileId))
	ddnsCacheLock.RLock()
	for _, id := range profileId {
		if profile, ok := ddnsCache[id]; ok {
			profiles = append(profiles, profile)
		} else {
			ddnsCacheLock.RUnlock()
			return nil, fmt.Errorf("无法找到DDNS配置 ID %d", id)
		}
	}
	ddnsCacheLock.RUnlock()

	providers := make([]*ddns2.Provider, 0, len(profiles))
	for _, profile := range profiles {
		provider := &ddns2.Provider{DDNSProfile: profile, IPAddrs: ip}
		switch profile.Provider {
		case model.ProviderDummy:
			provider.Setter = &dummy.Provider{}
			providers = append(providers, provider)
		case model.ProviderWebHook:
			provider.Setter = &webhook.Provider{DDNSProfile: profile}
			providers = append(providers, provider)
		case model.ProviderCloudflare:
			provider.Setter = &cloudflare.Provider{APIToken: profile.AccessSecret}
			providers = append(providers, provider)
		case model.ProviderTencentCloud:
			provider.Setter = &tencentcloud.Provider{SecretId: profile.AccessID, SecretKey: profile.AccessSecret}
			providers = append(providers, provider)
		default:
			return nil, fmt.Errorf("无法找到配置的DDNS提供者ID %d", profile.Provider)
		}
	}
	return providers, nil
}

// 新增：DDNS变更通知回调函数
func DDNSChangeNotificationCallback(serverName string, serverID uint64, domain string, recordType string, oldIP string, newIP string) {
	if !Conf.EnableIPChangeNotification {
		return
	}

	// 构建DDNS变更通知消息
	changeTime := time.Now().Format("2006-01-02 15:04:05")
	message := fmt.Sprintf(
		"[DDNS记录变更] 服务器: %s, 域名: %s, 记录类型: %s, IP变更: %s => %s, 变更时间: %s",
		serverName, domain, recordType, IPDesensitize(oldIP), IPDesensitize(newIP), changeTime,
	)

	// 使用与IP变更相同的通知组和静音机制
	muteLabel := NotificationMuteLabel.DDNSChanged(serverID, domain)
	SendNotification(Conf.IPChangeNotificationTag, message, muteLabel)
}

// 修改原有函数以包含服务器信息和通知回调
func GetDDNSProvidersFromProfilesWithServer(profileId []uint64, ip *ddns2.IP, serverName string, serverID uint64) ([]*ddns2.Provider, error) {
	providers, err := GetDDNSProvidersFromProfiles(profileId, ip)
	if err != nil {
		return nil, err
	}

	// 为每个provider添加服务器信息和通知回调
	for _, provider := range providers {
		provider.ServerName = serverName
		provider.ServerID = serverID
		provider.NotifyCallback = DDNSChangeNotificationCallback
	}

	return providers, nil
}
