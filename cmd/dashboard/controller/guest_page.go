package controller

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nicksnyder/go-i18n/v2/i18n"
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

	// 调试模式下的简单登录
	if singleton.Conf.Debug {
		gr.POST("/debug-login", gp.debugLogin)
	}

	oauth := &oauth2controller{
		r: gr,
	}
	oauth.serve()
}

func (gp *guestPage) login(c *gin.Context) {
	if singleton.Conf.Oauth2.OidcAutoLogin {
		c.Redirect(http.StatusFound, "/oauth2/login")
		return
	}
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
	c.HTML(http.StatusOK, "dashboard-"+singleton.Conf.Site.DashboardTheme+"/login", mygin.CommonEnvironment(c, gin.H{
		"Title":            singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{MessageID: "Login"}),
		"LoginType":        LoginType,
		"RegistrationLink": RegistrationLink,
	}))
}

func (gp *guestPage) debugLogin(c *gin.Context) {
	if !singleton.Conf.Debug {
		c.JSON(http.StatusForbidden, gin.H{"error": "Debug mode not enabled"})
		return
	}

	// 设置 admin token cookie
	c.SetCookie(singleton.Conf.Site.CookieName, "admin", 3600*24*30, "/", "", false, false)
	c.JSON(http.StatusOK, gin.H{"message": "Debug login successful"})
}
