package mygin

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/service/singleton"
	"golang.org/x/crypto/bcrypt"
)

type ValidateViewPasswordOption struct {
	IsPage        bool
	AbortWhenFail bool
}

func ValidateViewPassword(opt ValidateViewPasswordOption) gin.HandlerFunc {
	return func(c *gin.Context) {
		if singleton.Conf.Site.ViewPassword == "" {
			return
		}
		_, authorized := c.Get(model.CtxKeyAuthorizedUser)
		if authorized {
			return
		}
		viewPassword, err := c.Cookie(singleton.Conf.Site.CookieName + "-vp")
		if err == nil {
			err = bcrypt.CompareHashAndPassword([]byte(viewPassword), []byte(singleton.Conf.Site.ViewPassword))
		}
		if err == nil {
			c.Set(model.CtxKeyViewPasswordVerified, true)
			return
		}
		if !opt.AbortWhenFail {
			return
		}
		if opt.IsPage {
			c.HTML(http.StatusOK, GetPreferredTheme(c, "/viewpassword"), CommonEnvironment(c, gin.H{
				"Title": "验证查看密码",
			}))

		} else {
			writeJSON(c, http.StatusOK, model.Response{
				Code:    http.StatusForbidden,
				Message: "访问受限",
			})
		}
		c.Abort()
	}
}
