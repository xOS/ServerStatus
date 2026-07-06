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
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>验证查看密码</title><style>body{margin:0;min-height:100vh;display:grid;place-items:center;background:#f5f7fb;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:#1f2937}.panel{width:min(360px,calc(100% - 32px));padding:22px;border:1px solid rgba(255,255,255,.72);border-radius:12px;background:rgba(255,255,255,.78);box-shadow:0 6px 18px rgba(15,23,42,.08);backdrop-filter:blur(12px)}h1{margin:0 0 14px;font-size:20px}input{width:100%;height:36px;margin-bottom:12px;padding:0 10px;border:1px solid rgba(34,36,38,.15);border-radius:8px;background:rgba(255,255,255,.86)}button{height:34px;padding:0 14px;border:0;border-radius:999px;background:#03a9f4;color:#fff;font-weight:700;cursor:pointer}</style></head><body><form class="panel" method="post" action="/view-password"><h1>验证查看密码</h1><input name="Password" type="password" autocomplete="current-password" autofocus><button type="submit">确认</button></form></body></html>`))

		} else {
			writeJSON(c, http.StatusForbidden, model.Response{
				Code:    http.StatusForbidden,
				Message: "访问受限",
			})
		}
		c.Abort()
	}
}
