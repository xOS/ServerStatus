package webhook

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/libdns/libdns"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/utils"
)

const (
	_ = iota
	methodGET
	methodPOST
	methodPATCH
	methodDELETE
	methodPUT
)

const (
	_ = iota
	requestTypeJSON
	requestTypeForm
)

var requestTypes = map[uint8]string{
	methodGET:    "GET",
	methodPOST:   "POST",
	methodPATCH:  "PATCH",
	methodDELETE: "DELETE",
	methodPUT:    "PUT",
}

// NotifyDDNSChangeFunc 是DDNS变更通知回调函数类型
type NotifyDDNSChangeFunc func(serverName string, serverID uint64, domain string, recordType string, oldIP string, newIP string)

// Internal use
type Provider struct {
	ipAddr     string
	ipType     string
	recordType string
	domain     string
	ServerName string // 服务器名称 (导出)
	ServerID   uint64 // 服务器ID (导出)

	DDNSProfile       *model.DDNSProfile
	NotifyChangeFunc  NotifyDDNSChangeFunc // 通知回调函数
}

func (provider *Provider) SetRecords(ctx context.Context, zone string,
	recs []libdns.Record) ([]libdns.Record, error) {
	for _, rec := range recs {
		provider.recordType = rec.Type
		provider.ipType = recordToIPType(provider.recordType)
		provider.ipAddr = rec.Value
		provider.domain = fmt.Sprintf("%s.%s", rec.Name, strings.TrimSuffix(zone, "."))

		req, err := provider.prepareRequest(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to update a domain: %s. Cause by: %v", provider.domain, err)
		}
		if _, err := utils.HttpClient.Do(req); err != nil {
			return nil, fmt.Errorf("failed to update a domain: %s. Cause by: %v", provider.domain, err)
		}

		// 发送DDNS记录变更通知（如果有服务器名称）
		// 注意：IP变更检测逻辑已移至主服务中的数据库比较，这里只负责执行webhook调用后的通知
		if provider.ServerName != "" && provider.NotifyChangeFunc != nil {
			provider.NotifyChangeFunc(
				provider.ServerName,
				provider.ServerID,
				provider.domain,
				provider.recordType,
				"", // 旧IP在主服务中已经处理，这里传空字符串
				provider.ipAddr,
			)
		}
	}

	return recs, nil
}

func (provider *Provider) prepareRequest(ctx context.Context) (*http.Request, error) {
	u, err := provider.reqUrl()
	if err != nil {
		return nil, err
	}

	body, err := provider.reqBody()
	if err != nil {
		return nil, err
	}

	headers, err := utils.GjsonParseStringMap(
		provider.formatWebhookString(provider.DDNSProfile.WebhookHeaders))
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, requestTypes[provider.DDNSProfile.WebhookMethod], u.String(), strings.NewReader(body))
	if err != nil {
		return nil, err
	}

	provider.setContentType(req)

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	return req, nil
}

func (provider *Provider) setContentType(req *http.Request) {
	if provider.DDNSProfile.WebhookMethod == methodGET {
		return
	}
	if provider.DDNSProfile.WebhookRequestType == requestTypeForm {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
}

func (provider *Provider) reqUrl() (*url.URL, error) {
	formattedUrl := strings.ReplaceAll(provider.DDNSProfile.WebhookURL, "#", "%23")

	u, err := url.Parse(formattedUrl)
	if err != nil {
		return nil, err
	}

	// Only handle queries here
	q := u.Query()
	for p, vals := range q {
		for n, v := range vals {
			vals[n] = provider.formatWebhookString(v)
		}
		q[p] = vals
	}

	u.RawQuery = q.Encode()
	return u, nil
}

func (provider *Provider) reqBody() (string, error) {
	if provider.DDNSProfile.WebhookMethod == methodGET ||
		provider.DDNSProfile.WebhookMethod == methodDELETE {
		return "", nil
	}

	switch provider.DDNSProfile.WebhookRequestType {
	case requestTypeJSON:
		return provider.formatWebhookString(provider.DDNSProfile.WebhookRequestBody), nil
	case requestTypeForm:
		data, err := utils.GjsonParseStringMap(provider.DDNSProfile.WebhookRequestBody)
		if err != nil {
			return "", err
		}
		params := url.Values{}
		for k, v := range data {
			params.Add(k, provider.formatWebhookString(v))
		}
		return params.Encode(), nil
	default:
		return "", errors.New("request type not supported")
	}
}

func (provider *Provider) formatWebhookString(s string) string {
	r := strings.NewReplacer(
		"#ip#", provider.ipAddr,
		"#domain#", provider.domain,
		"#type#", provider.ipType,
		"#record#", provider.recordType,
		"#access_id#", provider.DDNSProfile.AccessID,
		"#access_secret#", provider.DDNSProfile.AccessSecret,
		"\r", "",
	)

	result := r.Replace(strings.TrimSpace(s))
	return result
}

func recordToIPType(record string) string {
	switch record {
	case "A":
		return "ipv4"
	case "AAAA":
		return "ipv6"
	default:
		return ""
	}
}

// sendDDNSChangeNotification 发送DDNS记录变更通知
func (provider *Provider) sendDDNSChangeNotification() {
	if provider.DDNSProfile == nil || provider.ServerID == 0 || provider.ServerName == "" || provider.NotifyChangeFunc == nil {
		return // 缺少必要信息，无法发送通知
	}

	// 调用通知回调函数
	oldIP := "" // 无法获取旧IP，传空字符串
	provider.NotifyChangeFunc(
		provider.ServerName,
		provider.ServerID,
		provider.domain,
		provider.recordType,
		oldIP,
		provider.ipAddr,
	)
}

// SendTestNotification 发送测试通知（仅用于测试）
func (provider *Provider) SendTestNotification(testDomain, testRecordType, testOldIP, testNewIP string) {
	if provider.NotifyChangeFunc == nil || provider.ServerName == "" || provider.ServerID == 0 {
		fmt.Println("警告：无法发送测试通知，缺少必要信息")
		return
	}

	provider.NotifyChangeFunc(
		provider.ServerName,
		provider.ServerID,
		testDomain,
		testRecordType,
		testOldIP,
		testNewIP,
	)
}
