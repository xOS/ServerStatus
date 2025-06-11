package singleton

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/libdns/cloudflare"
	tencentcloud "github.com/nezhahq/libdns-tencentcloud"
	"gorm.io/gorm"

	"github.com/xos/serverstatus/db"
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
	log.Printf("初始化DDNS配置...")
	OnDDNSUpdate()
	OnNameserverUpdate()

	// 验证DDNS配置加载情况
	ddnsCacheLock.RLock()
	configCount := len(ddnsCache)
	ddnsCacheLock.RUnlock()

	log.Printf("DDNS初始化完成，加载了 %d 个配置", configCount)
}

func OnDDNSUpdate() {
	var ddns []*model.DDNSProfile

	// 根据数据库类型选择不同的加载方式
	if Conf.DatabaseType == "badger" {
		// 使用 BadgerDB 加载DDNS配置
		if db.DB != nil {
			ddnsOps := db.NewDDNSOps(db.DB)
			var err error
			ddns, err = ddnsOps.GetAllDDNSProfiles()
			if err != nil {
				log.Printf("从BadgerDB加载DDNS配置失败: %v", err)
				ddns = []*model.DDNSProfile{}
			}
			// 移除频繁的DDNS配置加载日志
		} else {
			log.Println("警告: BadgerDB 未初始化")
			ddns = []*model.DDNSProfile{}
		}
	} else {
		// 使用 GORM (SQLite) 加载DDNS配置
		DB.Find(&ddns)
	}

	ddnsCacheLock.Lock()
	defer ddnsCacheLock.Unlock()
	ddnsCache = make(map[uint64]*model.DDNSProfile)
	for i := 0; i < len(ddns); i++ {
		// 确保 BadgerDB 模式下 Domains 字段正确解析
		if ddns[i].DomainsRaw != "" && len(ddns[i].Domains) == 0 {
			ddns[i].Domains = strings.Split(ddns[i].DomainsRaw, ",")
		}
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
			// 验证 DDNS 配置的完整性
			if len(profile.Domains) == 0 {
				log.Printf("警告: DDNS配置 ID %d 没有配置域名", id)
				continue
			}
			profiles = append(profiles, profile)
		} else {
			ddnsCacheLock.RUnlock()
			return nil, fmt.Errorf("无法找到DDNS配置 ID %d", id)
		}
	}
	ddnsCacheLock.RUnlock()

	if len(profiles) == 0 {
		return nil, fmt.Errorf("没有有效的DDNS配置")
	}

	providers := make([]*ddns2.Provider, 0, len(profiles))
	for _, profile := range profiles {
		provider := &ddns2.Provider{DDNSProfile: profile, IPAddrs: ip}
		switch profile.Provider {
		case model.ProviderDummy:
			provider.Setter = &dummy.Provider{}
			providers = append(providers, provider)
		case model.ProviderWebHook:
			webhookProvider := &webhook.Provider{
				DDNSProfile:      profile,
				NotifyChangeFunc: DDNSChangeNotificationCallback, // 直接在这里设置回调函数
			}
			// 将来ServerName和ServerID会通过GetDDNSProvidersFromProfilesWithServer设置
			provider.Setter = webhookProvider
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

// DDNS变更通知回调函数 - 支持BadgerDB和SQLite
func DDNSChangeNotificationCallback(serverName string, serverID uint64, domain string, recordType string, oldIP string, newIP string) {
	if !Conf.EnableIPChangeNotification {
		return
	}

	// 根据数据库类型选择不同的处理方式
	if Conf.DatabaseType == "badger" {
		// 使用BadgerDB处理DDNS状态记录
		if db.DB != nil {
			ddnsOps := db.NewDDNSOps(db.DB)

			// 查询现有记录状态
			recordState, err := ddnsOps.GetDDNSRecordStateByParams(serverID, domain, recordType)

			if err != nil {
				// 记录不存在，创建新记录
				if err == db.ErrorNotFound {
					newRecordState := &model.DDNSRecordState{
						ServerID:   serverID,
						Domain:     domain,
						RecordType: recordType,
						LastIP:     newIP,
						LastUpdate: time.Now(),
					}
					if err := ddnsOps.CreateDDNSRecordState(newRecordState); err != nil {
						log.Printf("创建DDNS记录状态失败: %v", err)
						return
					}

					// 首次创建记录，发送通知（如果有旧IP信息）
					if oldIP != "" && oldIP != newIP {
						sendDDNSChangeNotification(serverName, domain, recordType, oldIP, newIP)
					} else {
						log.Printf("域名 %s 首次设置 %s 记录，IP: %s", domain, recordType, newIP)
					}
				} else {
					log.Printf("查询DDNS记录状态失败: %v", err)
				}
				return
			}

			// 检查IP是否实际发生了变化
			if recordState.LastIP == newIP {
				log.Printf("域名 %s 的 %s 记录IP未发生变化 (%s)，跳过通知", domain, recordType, newIP)
				return
			}

			// IP发生了变化，更新记录并发送通知
			oldLastIP := recordState.LastIP
			recordState.LastIP = newIP
			recordState.LastUpdate = time.Now()
			recordState.UpdatedAt = time.Now()
			if err := ddnsOps.SaveDDNSRecordState(recordState); err != nil {
				log.Printf("更新DDNS记录状态失败: %v", err)
				return
			}

			// 发送变更通知
			log.Printf("检测到IP变化: %s -> %s，发送DDNS变更通知", oldLastIP, newIP)
			sendDDNSChangeNotification(serverName, domain, recordType, oldLastIP, newIP)
		} else {
			log.Printf("BadgerDB未初始化，直接发送DDNS变更通知: 域名 %s 的 %s 记录IP变化 (%s -> %s)", domain, recordType, oldIP, newIP)
			// 直接发送通知，不保存状态
			sendDDNSChangeNotification(serverName, domain, recordType, oldIP, newIP)
		}
		return
	}

	// 以下是SQLite逻辑
	// 查询或创建DDNS记录状态
	if DB == nil {
		log.Printf("SQLite数据库未初始化，跳过DDNS记录状态更新")
		return
	}

	var recordState model.DDNSRecordState
	result := DB.Where("server_id = ? AND domain = ? AND record_type = ?", serverID, domain, recordType).First(&recordState)

	if result.Error != nil {
		// 记录不存在，创建新记录
		if result.Error == gorm.ErrRecordNotFound {
			recordState = model.DDNSRecordState{
				ServerID:   serverID,
				Domain:     domain,
				RecordType: recordType,
				LastIP:     newIP,
				LastUpdate: time.Now(),
			}
			if err := DB.Create(&recordState).Error; err != nil {
				log.Printf("创建DDNS记录状态失败: %v", err)
				return
			}

			// 首次创建记录，发送通知（如果有旧IP信息）
			if oldIP != "" && oldIP != newIP {
				sendDDNSChangeNotification(serverName, domain, recordType, oldIP, newIP)
			} else {
				log.Printf("域名 %s 首次设置 %s 记录，IP: %s", domain, recordType, newIP)
			}
		} else {
			log.Printf("查询DDNS记录状态失败: %v", result.Error)
		}
		return
	}

	// 检查IP是否实际发生了变化
	if recordState.LastIP == newIP {
		log.Printf("域名 %s 的 %s 记录IP未发生变化 (%s)，跳过通知", domain, recordType, newIP)
		return
	}

	// IP发生了变化，更新记录并发送通知
	oldLastIP := recordState.LastIP
	recordState.LastIP = newIP
	recordState.LastUpdate = time.Now()
	if err := DB.Save(&recordState).Error; err != nil {
		log.Printf("更新DDNS记录状态失败: %v", err)
		return
	}

	// 发送变更通知
	log.Printf("检测到IP变化: %s -> %s，发送DDNS变更通知", oldLastIP, newIP)
	sendDDNSChangeNotification(serverName, domain, recordType, oldLastIP, newIP)
}

// sendDDNSChangeNotification 发送DDNS变更通知
func sendDDNSChangeNotification(serverName string, domain string, recordType string, oldIP string, newIP string) {
	changeTime := time.Now().Format("2006-01-02 15:04:05")
	message := fmt.Sprintf(
		"[DDNS记录变更]\n服务器: %s\n域名: %s\n记录类型: %s\nIP变更: %s => %s\n变更时间: %s",
		serverName, domain, recordType, IPDesensitize(oldIP), IPDesensitize(newIP), changeTime,
	)

	// 使用与IP变更相同的通知组和静音机制
	muteLabel := NotificationMuteLabel.DDNSChanged(0, domain) // 使用0作为通用server ID
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

		// 特殊处理webhook.Provider，将服务器信息传递给它
		if provider.DDNSProfile.Provider == model.ProviderWebHook {
			if webhookProvider, ok := provider.Setter.(*webhook.Provider); ok {
				webhookProvider.ServerName = serverName
				webhookProvider.ServerID = serverID
				// 回调已在GetDDNSProvidersFromProfiles中设置
			}
		}
	}

	return providers, nil
}
