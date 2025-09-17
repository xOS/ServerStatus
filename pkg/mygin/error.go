package mygin

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xos/serverstatus/pkg/utils"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/service/singleton"
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
		c.HTML(i.Code, "dashboard-"+singleton.Conf.Site.DashboardTheme+"/error", CommonEnvironment(c, gin.H{
			"Code":  i.Code,
			"Title": i.Title,
			"Msg":   i.Msg,
			"Link":  i.Link,
			"Btn":   i.Btn,
		}))
	} else {
		writeJSON(c, http.StatusOK, model.Response{
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
