package monitor

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"github.com/xos/serverstatus/pkg/utils"
)

type geoIP struct {
	CountryCode  string `json:"country_code,omitempty"`
	CountryCode2 string `json:"countryCode,omitempty"`
	IP           string `json:"ip,omitempty"`
	Query        string `json:"query,omitempty"`
}

func (ip *geoIP) Unmarshal(body []byte) error {
	if err := utils.Json.Unmarshal(body, ip); err != nil {
		return err
	}
	if ip.IP == "" && ip.Query != "" {
		ip.IP = ip.Query
	}
	if ip.CountryCode == "" && ip.CountryCode2 != "" {
		ip.CountryCode = ip.CountryCode2
	}
	return nil
}

var (
	geoIPApiList = []string{
		"http://ip.qste.com/json",
		"https://api.ip.sb/geoip",
		"https://ipapi.co/json",
		"http://ip-api.com/json/",
		"http://ip.nan.ge/json",
	}
	cachedIP, cachedCountry string
	httpClientV4            = utils.NewSingleStackHTTPClient(time.Second*20, time.Second*5, time.Second*10, false)
	httpClientV6            = utils.NewSingleStackHTTPClient(time.Second*20, time.Second*5, time.Second*10, true)
)

func UpdateIP() {
	for {
		ipv4 := fetchGeoIP(geoIPApiList, false)
		ipv6 := fetchGeoIP(geoIPApiList, true)

		if ipv4.IP == "" && ipv6.IP == "" {
			cachedIP = fmt.Sprintf("IPs[未获取到 IP]")
			time.Sleep(time.Second*30)
			continue
		} 
		if ipv4.IP != "" && ipv6.IP == "" {
			cachedIP = fmt.Sprintf("IPs[IPv4:%s]", ipv4.IP)
		} else if ipv4.IP == "" && ipv6.IP != "" {
			cachedIP = fmt.Sprintf("IPs[IPv6:[%s]]", ipv6.IP)
		} else {
			cachedIP = fmt.Sprintf("IPs[IPv4:%s,IPv6:[%s]]", ipv4.IP, ipv6.IP)
		} 
		if ipv4.CountryCode != "" {
			cachedCountry = ipv4.CountryCode
		} else if ipv6.CountryCode != "" {
			cachedCountry = ipv6.CountryCode
		}
		time.Sleep(time.Minute * 30)
	}
}

func fetchGeoIP(servers []string, isV6 bool) geoIP {
	var ip geoIP
	var resp *http.Response
	var err error
	// 双栈支持参差不齐，不能随机请求，有些 IPv6 取不到 IP
	for i := 0; i < len(servers); i++ {
		if isV6 {
			resp, err = httpClientV6.Get(servers[i])
		} else {
			resp, err = httpClientV4.Get(servers[i])
		}
		if err == nil {
			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				continue
			}
			resp.Body.Close()
			if err := ip.Unmarshal(body); err != nil {
				continue
			}
			// 没取到 v6 IP
			if isV6 && !strings.Contains(ip.IP, ":") {
				continue
			}
			// 没取到 v4 IP
			if !isV6 && !strings.Contains(ip.IP, ".") {
				continue
			}
			return ip
		}
	}
	return ip
}

func httpGetWithUA(client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/15.4 Safari/605.1.15")
	return client.Do(req)
}
