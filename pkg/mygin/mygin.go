package mygin

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/service/singleton"
)

var adminPage = map[string]bool{
	"/server":       true,
	"/monitor":      true,
	"/setting":      true,
	"/notification": true,
	"/ddns":         true,
	"/nat":          true,
	"/cron":         true,
	"/api":          true,
}

func CommonEnvironment(c *gin.Context, data map[string]interface{}) gin.H {
	data["MatchedPath"] = c.MustGet("MatchedPath")
	data["Version"] = singleton.Version
	data["Conf"] = singleton.Conf
	data["CustomCode"] = singleton.Conf.Site.CustomCode
	data["CustomCodeDashboard"] = singleton.Conf.Site.CustomCodeDashboard
	// 是否是管理页面
	data["IsAdminPage"] = adminPage[data["MatchedPath"].(string)]
	// 站点标题
	if t, has := data["Title"]; !has {
		data["Title"] = singleton.Conf.Site.Brand
	} else {
		data["Title"] = fmt.Sprintf("%s - %s", t, singleton.Conf.Site.Brand)
	}
	u, ok := c.Get(model.CtxKeyAuthorizedUser)
	if ok {
		data["Admin"] = u
	}
	data["LANG"] = map[string]string{
		"Add":          "添加",
		"Edit":         "修改",
		"AlarmRule":    "通知规则",
		"Notification": "通知方式",
		"Server":       "主机",
		"Monitor":      "服务监控",
		"Cron":         "计划任务",
	}
	return data
}

func RecordPath(c *gin.Context) {
	url := c.Request.URL.String()
	for _, p := range c.Params {
		url = strings.Replace(url, p.Value, ":"+p.Key, 1)
	}
	c.Set("MatchedPath", url)
}
