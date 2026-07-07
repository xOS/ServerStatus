package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xos/serverstatus/pkg/mygin"
	"github.com/xos/serverstatus/service/singleton"
)

type guestPage struct {
	r *gin.Engine
}

func (gp *guestPage) serve() {
	gr := gp.r.Group("")
	gr.Use(mygin.Authorize(mygin.AuthorizeOption{
		GuestOnly: true,
		IsPage:    true,
		Msg:       "您已登录",
		Btn:       "返回首页",
		Redirect:  "/",
	}))

	gr.GET("/login", gp.login)

	// 调试模式下的简单登录
	if singleton.Conf.Debug {
		gr.POST("/debug-login", gp.debugLogin)
	}

}

func (gp *guestPage) login(c *gin.Context) {
	if singleton.Conf.Oauth2.OidcAutoLogin {
		c.Redirect(http.StatusFound, oauthLoginPath)
		return
	}
	serveSPA(c)
}

func (gp *guestPage) debugLogin(c *gin.Context) {
	if !singleton.Conf.Debug {
		WriteJSON(c, http.StatusForbidden, gin.H{"error": "Debug mode not enabled"})
		return
	}

	// 设置 admin token cookie
	setSecureCookie(c, singleton.Conf.Site.CookieName, "admin", 3600*24*30)
	WriteJSON(c, http.StatusOK, gin.H{"message": "Debug login successful"})
}
