package mygin

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/xos/probe-lite/model"
	"github.com/xos/probe-lite/service/singleton"
)

var adminPage = map[string]bool{
	"/server":       true,
	"/setting":      true,
	"/notification": true,
}

func CommonEnvironment(c *gin.Context, data map[string]interface{}) gin.H {
	data["MatchedPath"] = c.MustGet("MatchedPath")
	data["Version"] = singleton.Version
	data["Conf"] = singleton.Conf
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
	return data
}

func RecordPath(c *gin.Context) {
	url := c.Request.URL.String()
	for _, p := range c.Params {
		url = strings.Replace(url, p.Value, ":"+p.Key, 1)
	}
	c.Set("MatchedPath", url)
}
