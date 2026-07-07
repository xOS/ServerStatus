package controller

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xos/serverstatus/model"
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
	legacyOauth := &oauth2controller{r: gr}
	legacyOauth.serve()

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
	return

	LoginType := "GitHub"
	RegistrationLink := "https://github.com/join"
	if singleton.Conf.Oauth2.Type == model.ConfigTypeGitee {
		LoginType = "Gitee"
		RegistrationLink = "https://gitee.com/signup"
	} else if singleton.Conf.Oauth2.Type == model.ConfigTypeGitlab {
		LoginType = "Gitlab"
		RegistrationLink = "https://gitlab.com/users/sign_up"
	} else if singleton.Conf.Oauth2.Type == model.ConfigTypeJihulab {
		LoginType = "Jihulab"
		RegistrationLink = "https://jihulab.com/users/sign_up"
	} else if singleton.Conf.Oauth2.Type == model.ConfigTypeGitea {
		LoginType = "Gitea"
		RegistrationLink = fmt.Sprintf("%s/user/sign_up", singleton.Conf.Oauth2.Endpoint)
	} else if singleton.Conf.Oauth2.Type == model.ConfigTypeCloudflare {
		LoginType = "Cloudflare"
		RegistrationLink = "https://dash.cloudflare.com/sign-up/teams"
	} else if singleton.Conf.Oauth2.Type == model.ConfigTypeOidc {
		LoginType = singleton.Conf.Oauth2.OidcDisplayName
		RegistrationLink = singleton.Conf.Oauth2.OidcRegisterURL
	}
	c.HTML(http.StatusOK, "dashboard-default/login", mygin.CommonEnvironment(c, gin.H{
		"Title":            "登录",
		"LoginType":        LoginType,
		"RegistrationLink": RegistrationLink,
	}))
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
