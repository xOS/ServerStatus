package webhook

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/libdns/libdns"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/utils"
	"github.com/xos/serverstatus/service/singleton"
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

// Internal use
type Provider struct {
	ipAddr     string
	ipType     string
	recordType string
	domain     string
	serverName string // 服务器名称
	serverID   uint64 // 服务器ID

	DDNSProfile *model.DDNSProfile
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
		if provider.serverName != "" {
			provider.sendDDNSChangeNotification()
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
	if provider.DDNSProfile == nil || provider.serverID == 0 || provider.serverName == "" {
		return // 缺少必要信息，无法发送通知
	}

	// 调用singleton包中的通知回调函数
	oldIP := "" // 无法获取旧IP，传空字符串
	singleton.DDNSChangeNotificationCallback(
		provider.serverName,
		provider.serverID,
		provider.domain,
		provider.recordType,
		oldIP,
		provider.ipAddr,
	)
}
