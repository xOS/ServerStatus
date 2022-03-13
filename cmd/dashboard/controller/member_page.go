package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xos/probe-lite/model"
	"github.com/xos/probe-lite/pkg/mygin"
	"github.com/xos/probe-lite/service/singleton"
)

type memberPage struct {
	r *gin.Engine
}

func (mp *memberPage) serve() {
	mr := mp.r.Group("")
	mr.Use(mygin.Authorize(mygin.AuthorizeOption{
		Member:   true,
		IsPage:   true,
		Msg:      "此页面需要登录",
		Btn:      "点此登录",
		Redirect: "/login",
	}))
	mr.GET("/server", mp.server)
	mr.GET("/notification", mp.notification)
	mr.GET("/setting", mp.setting)
}

func (mp *memberPage) server(c *gin.Context) {
	singleton.SortedServerLock.RLock()
	defer singleton.SortedServerLock.RUnlock()
	c.HTML(http.StatusOK, "dashboard/server", mygin.CommonEnvironment(c, gin.H{
		"Title":   "服务器管理",
		"Servers": singleton.SortedServerList,
	}))
}

func (mp *memberPage) notification(c *gin.Context) {
	var nf []model.Notification
	singleton.DB.Find(&nf)
	var ar []model.AlertRule
	singleton.DB.Find(&ar)
	c.HTML(http.StatusOK, "dashboard/notification", mygin.CommonEnvironment(c, gin.H{
		"Title":         "报警通知",
		"Notifications": nf,
		"AlertRules":    ar,
	}))
}

func (mp *memberPage) setting(c *gin.Context) {
	c.HTML(http.StatusOK, "dashboard/setting", mygin.CommonEnvironment(c, gin.H{
		"Title": "系统设置",
	}))
}
