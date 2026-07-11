package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xos/serverstatus/pkg/mygin"
)

type memberPage struct {
	r *gin.Engine
}

func (mp *memberPage) serve() {
	mr := mp.r.Group("")
	mr.Use(mygin.Authorize(mygin.AuthorizeOption{
		MemberOnly: true,
		IsPage:     true,
		Msg:        "您无权访问",
		Btn:        "登录",
		Redirect:   "/login",
	}))
	mr.GET("/server", mp.server)
	mr.GET("/monitor", mp.monitor)
	mr.GET("/cron", mp.cron)
	mr.GET("/notification", mp.notification)
	mr.GET("/ddns", mp.ddns)
	mr.GET("/nat", mp.nat)
	mr.GET("/setting", mp.setting)
}

func (mp *memberPage) server(c *gin.Context) {
	c.Redirect(http.StatusFound, "/dashboard/server")
}

func (mp *memberPage) monitor(c *gin.Context) {
	c.Redirect(http.StatusFound, "/dashboard/monitor")
}

func (mp *memberPage) cron(c *gin.Context) {
	c.Redirect(http.StatusFound, "/dashboard/cron")
}

func (mp *memberPage) notification(c *gin.Context) {
	c.Redirect(http.StatusFound, "/dashboard/notification")
}

func (mp *memberPage) ddns(c *gin.Context) {
	c.Redirect(http.StatusFound, "/dashboard/ddns")
}

func (mp *memberPage) nat(c *gin.Context) {
	c.Redirect(http.StatusFound, "/dashboard/nat")
}

func (mp *memberPage) setting(c *gin.Context) {
	c.Redirect(http.StatusFound, "/dashboard/setting")
}
