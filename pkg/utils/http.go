package utils

import (
	"crypto/tls"
	"net/http"
	"time"
)

var (
	HttpClientSkipTlsVerify *http.Client
	HttpClient              *http.Client
)

func init() {
	HttpClientSkipTlsVerify = httpClient(_httpClient{
		Transport: httpTransport(_httpTransport{
			SkipVerifySSL: true,
		}),
	})
	HttpClient = httpClient(_httpClient{
		Transport: httpTransport(_httpTransport{
			SkipVerifySSL: false,
		}),
	})

	http.DefaultClient.Timeout = time.Minute * 10
}

type _httpTransport struct {
	SkipVerifySSL bool
}

func httpTransport(conf _httpTransport) *http.Transport {
	return &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: conf.SkipVerifySSL},
		Proxy:           http.ProxyFromEnvironment,
		// 根本修复：限制连接池大小，防止连接泄漏
		MaxIdleConns:        10,               // 最大空闲连接数
		MaxIdleConnsPerHost: 2,                // 每个host最大空闲连接数
		IdleConnTimeout:     30 * time.Second, // 空闲连接超时
		DisableKeepAlives:   false,            // 启用keep-alive但限制数量
		// 强制关闭连接，防止泄漏
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

type _httpClient struct {
	Transport *http.Transport
}

func httpClient(conf _httpClient) *http.Client {
	return &http.Client{
		Transport: conf.Transport,
		Timeout:   time.Minute * 10,
	}
}
