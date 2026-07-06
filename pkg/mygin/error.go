package mygin

import (
	"html"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/utils"
)

type ErrInfo struct {
	Code  int
	Title string
	Msg   string
	Link  string
	Btn   string
}

func ShowErrorPage(c *gin.Context, i ErrInfo, isPage bool) {
	if isPage {
		title := html.EscapeString(i.Title)
		msg := html.EscapeString(i.Msg)
		link := html.EscapeString(i.Link)
		btn := html.EscapeString(i.Btn)
		if btn == "" {
			btn = "返回"
		}
		if link == "" {
			link = "/"
		}
		c.Data(i.Code, "text/html; charset=utf-8", []byte(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>`+title+`</title><style>body{margin:0;min-height:100vh;display:grid;place-items:center;background:#f5f7fb;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:#1f2937}.panel{width:min(420px,calc(100% - 32px));padding:24px;border:1px solid rgba(255,255,255,.72);border-radius:12px;background:rgba(255,255,255,.78);box-shadow:0 6px 18px rgba(15,23,42,.08);backdrop-filter:blur(12px)}h1{margin:0 0 10px;font-size:20px}p{margin:0 0 18px;color:#64748b;line-height:1.6}a{display:inline-flex;align-items:center;height:34px;padding:0 14px;border-radius:999px;background:#03a9f4;color:#fff;text-decoration:none;font-weight:700}</style></head><body><main class="panel"><h1>`+title+`</h1><p>`+msg+`</p><a href="`+link+`">`+btn+`</a></main></body></html>`))
	} else {
		writeJSON(c, i.Code, model.Response{
			Code:    i.Code,
			Message: i.Msg,
		})
	}
	c.Abort()
}

// writeJSON 统一 JSON 输出路径（mygin 内部使用）
func writeJSON(c *gin.Context, status int, v interface{}) {
	payload, err := utils.EncodeJSON(v)
	if err != nil {
		// 降级兜底
		payload, _ = utils.EncodeJSON(model.Response{Code: status, Message: http.StatusText(status)})
	}
	c.Status(status)
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	if _, gz, _ := utils.GzipIfAccepted(c.Writer, c.Request, payload); !gz {
		_, _ = c.Writer.Write(payload)
	}
}
