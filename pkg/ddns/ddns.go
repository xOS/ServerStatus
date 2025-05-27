package ddns

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/libdns/libdns"
	"github.com/miekg/dns"

	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/utils"
)

var (
	dnsTimeOut       = 10 * time.Second
	customDNSServers []string
)

type IP struct {
	Ipv4Addr string
	Ipv6Addr string
}

type Provider struct {
	ctx        context.Context
	ipAddr     string
	recordType string
	domain     string
	prefix     string
	zone       string

	DDNSProfile *model.DDNSProfile
	IPAddrs     *IP
	Setter      libdns.RecordSetter

	// 新增字段用于DDNS变更通知
	ServerName     string
	ServerID       uint64
	NotifyCallback func(serverName string, serverID uint64, domain string, recordType string, oldIP string, newIP string) // 通知回调函数
}

func InitDNSServers(s string) {
	if s != "" {
		customDNSServers = strings.Split(s, ",")
	}
}

func (provider *Provider) UpdateDomain(ctx context.Context) {
	provider.ctx = ctx
	for _, domain := range provider.DDNSProfile.Domains {
		for retries := 0; retries < int(provider.DDNSProfile.MaxRetries); retries++ {
			provider.domain = domain
			log.Printf("NG>> 正在尝试更新域名(%s)DDNS(%d/%d)", provider.domain, retries+1, provider.DDNSProfile.MaxRetries)
			if err := provider.updateDomain(); err != nil {
				log.Printf("NG>> 尝试更新域名(%s)DDNS失败: %v", provider.domain, err)
			} else {
				log.Printf("NG>> 尝试更新域名(%s)DDNS成功", provider.domain)
				break
			}
		}
	}
}

func (provider *Provider) updateDomain() error {
	var err error
	provider.prefix, provider.zone, err = splitDomainSOA(provider.domain)
	if err != nil {
		return err
	}

	// 当IPv4和IPv6同时成功才算作成功
	if *provider.DDNSProfile.EnableIPv4 {
		provider.recordType = getRecordString(true)
		provider.ipAddr = provider.IPAddrs.Ipv4Addr
		if err = provider.addDomainRecord(); err != nil {
			return err
		}
	}

	if *provider.DDNSProfile.EnableIPv6 {
		provider.recordType = getRecordString(false)
		provider.ipAddr = provider.IPAddrs.Ipv6Addr
		if err = provider.addDomainRecord(); err != nil {
			return err
		}
	}

	return nil
}

func (provider *Provider) addDomainRecord() error {
	// 查询当前DNS记录的IP地址
	currentIP, err := provider.queryCurrentDNSRecord()
	if err != nil {
		log.Printf("查询当前DNS记录失败: %v", err)
		// 如果查询失败，继续更新，但使用空字符串作为旧IP
		currentIP = ""
	}

	// 检查IP是否实际发生了变化
	if currentIP != "" && currentIP == provider.ipAddr {
		log.Printf("域名 %s 的 %s 记录IP未发生变化 (%s)，跳过更新", provider.domain, provider.recordType, currentIP)
		return nil // IP没有变化，不需要更新
	}

	_, err = provider.Setter.SetRecords(provider.ctx, provider.zone,
		[]libdns.Record{
			{
				Type:  provider.recordType,
				Name:  provider.prefix,
				Value: provider.ipAddr,
				TTL:   time.Minute,
			},
		})

	// 如果DDNS更新成功且有通知回调，发送DDNS变更通知
	if err == nil && provider.NotifyCallback != nil && provider.ServerName != "" {
		go provider.NotifyCallback(provider.ServerName, provider.ServerID, provider.domain, provider.recordType, currentIP, provider.ipAddr)
	}

	return err
}

func splitDomainSOA(domain string) (prefix string, zone string, err error) {
	c := &dns.Client{Timeout: dnsTimeOut}

	domain += "."
	indexes := dns.Split(domain)

	servers := utils.DNSServers
	if len(customDNSServers) > 0 {
		servers = customDNSServers
	}

	var r *dns.Msg
	for _, idx := range indexes {
		m := new(dns.Msg)
		m.SetQuestion(domain[idx:], dns.TypeSOA)

		for _, server := range servers {
			r, _, err = c.Exchange(m, server)
			if err != nil {
				return
			}
			if len(r.Answer) > 0 {
				if soa, ok := r.Answer[0].(*dns.SOA); ok {
					zone = soa.Hdr.Name
					prefix = libdns.RelativeName(domain, zone)
					return
				}
			}
		}
	}

	return "", "", fmt.Errorf("SOA record not found for domain: %s", domain)
}

func getRecordString(isIpv4 bool) string {
	if isIpv4 {
		return "A"
	}
	return "AAAA"
}

// queryCurrentDNSRecord 查询域名当前的DNS记录IP地址
func (provider *Provider) queryCurrentDNSRecord() (string, error) {
	c := &dns.Client{Timeout: dnsTimeOut}

	servers := utils.DNSServers
	if len(customDNSServers) > 0 {
		servers = customDNSServers
	}

	// 构建查询消息
	m := new(dns.Msg)
	var qtype uint16
	if provider.recordType == "A" {
		qtype = dns.TypeA
	} else if provider.recordType == "AAAA" {
		qtype = dns.TypeAAAA
	} else {
		return "", fmt.Errorf("不支持的记录类型: %s", provider.recordType)
	}

	m.SetQuestion(dns.Fqdn(provider.domain), qtype)

	// 尝试每个DNS服务器
	for _, server := range servers {
		r, _, err := c.Exchange(m, server)
		if err != nil {
			continue
		}

		// 解析响应
		if len(r.Answer) > 0 {
			for _, ans := range r.Answer {
				if provider.recordType == "A" {
					if a, ok := ans.(*dns.A); ok {
						return a.A.String(), nil
					}
				} else if provider.recordType == "AAAA" {
					if aaaa, ok := ans.(*dns.AAAA); ok {
						return aaaa.AAAA.String(), nil
					}
				}
			}
		}
	}

	return "", fmt.Errorf("未找到域名 %s 的 %s 记录", provider.domain, provider.recordType)
}
