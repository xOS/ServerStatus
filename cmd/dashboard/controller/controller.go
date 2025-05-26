package controller

import (
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/bytefmt"
	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/hashicorp/go-uuid"
	"github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/mygin"
	"github.com/xos/serverstatus/pkg/utils"
	"github.com/xos/serverstatus/proto"
	"github.com/xos/serverstatus/service/rpc"
	"github.com/xos/serverstatus/service/singleton"
)

// WebSocket相关
var (
	wsClients sync.Map // 存储WebSocket连接 [clientID]=>连接
	// 注：upgrader已在common_page.go中定义
)

func ServeWeb(port uint) *http.Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	if singleton.Conf.Debug {
		gin.SetMode(gin.DebugMode)
		pprof.Register(r)
	}
	r.Use(natGateway)
	tmpl := template.New("").Funcs(funcMap)
	var err error
	// 直接用本地模板目录
	tmpl, err = tmpl.ParseGlob("resource/template/**/*.html")
	if err != nil {
		panic(err)
	}
	tmpl = loadThirdPartyTemplates(tmpl)
	r.SetHTMLTemplate(tmpl)
	r.Use(mygin.RecordPath)
	// 直接用本地静态资源目录
	r.Static("/static", "resource/static")

	// WebSocket服务
	r.GET("/ws", func(c *gin.Context) {
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			log.Println("WebSocket连接失败:", err)
			return
		}

		// 生成唯一客户端ID
		clientID, _ := uuid.GenerateUUID()
		wsClients.Store(clientID, conn)

		// 连接关闭时清理
		conn.SetCloseHandler(func(code int, text string) error {
			wsClients.Delete(clientID)
			return nil
		})

		// 处理接收到的消息
		go handleWebSocketMessages(conn, clientID)
	})

	// 启动定时推送服务
	startWebSocketPushService()

	routers(r)
	page404 := func(c *gin.Context) {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusNotFound,
			Title: "该页面不存在",
			Msg:   "该页面内容可能已着陆火星",
			Link:  "/",
			Btn:   "返回首页",
		}, true)
	}
	r.NoRoute(page404)
	r.NoMethod(page404)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		ReadHeaderTimeout: time.Second * 5,
		Handler:           r,
	}
	return srv
}

func routers(r *gin.Engine) {
	// 通用页面
	cp := commonPage{r: r}
	cp.serve()
	// 游客页面
	gp := guestPage{r}
	gp.serve()
	// 会员页面
	mp := &memberPage{r}
	mp.serve()
	// API
	api := r.Group("api")
	{
		ma := &memberAPI{api}
		ma.serve()
	}
}

func loadThirdPartyTemplates(tmpl *template.Template) *template.Template {
	ret := tmpl
	themes, err := os.ReadDir("resource/template")
	if err != nil {
		log.Printf("NG>> Error reading themes folder: %v", err)
		return ret
	}
	for _, theme := range themes {
		if !theme.IsDir() {
			continue
		}

		themeDir := theme.Name()
		if themeDir == "theme-custom" {
			// for backward compatibility
			// note: will remove this in future versions
			ret = loadTemplates(ret, themeDir)
			continue
		}

		if strings.HasPrefix(themeDir, "dashboard-") {
			// load dashboard templates, ignore desc file
			ret = loadTemplates(ret, themeDir)
			continue
		}

		if !strings.HasPrefix(themeDir, "theme-") {
			log.Printf("NG>> Invalid theme name: %s", themeDir)
			continue
		}

		descPath := filepath.Join("resource", "template", themeDir, "theme.json")
		desc, err := os.ReadFile(filepath.Clean(descPath))
		if err != nil {
			log.Printf("NG>> Error opening %s config: %v", themeDir, err)
			continue
		}

		themeName, err := utils.GjsonGet(desc, "name")
		if err != nil {
			log.Printf("NG>> Error opening %s config: not a valid description file", theme.Name())
			continue
		}

		// load templates
		ret = loadTemplates(ret, themeDir)

		themeKey := strings.TrimPrefix(themeDir, "theme-")
		model.Themes[themeKey] = themeName.String()
	}

	return ret
}

func loadTemplates(tmpl *template.Template, themeDir string) *template.Template {
	// load templates
	templatePath := filepath.Join("resource", "template", themeDir, "*.html")
	t, err := tmpl.ParseGlob(templatePath)
	if err != nil {
		log.Printf("NG>> Error parsing templates %s: %v", themeDir, err)
		return tmpl
	}

	return t
}

var funcMap = template.FuncMap{
	"tr": func(id string, dataAndCount ...interface{}) string {
		conf := i18n.LocalizeConfig{
			MessageID: id,
		}
		if len(dataAndCount) > 0 {
			conf.TemplateData = dataAndCount[0]
		}
		if len(dataAndCount) > 1 {
			conf.PluralCount = dataAndCount[1]
		}
		return singleton.Localizer.MustLocalize(&conf)
	},
	"toValMap": func(val interface{}) map[string]interface{} {
		return map[string]interface{}{
			"Value": val,
		}
	},
	"tf": func(t time.Time) string {
		return t.In(singleton.Loc).Format("01/02/2006 15:04:05")
	},
	"len": func(slice []interface{}) string {
		return strconv.Itoa(len(slice))
	},
	"safe": func(s string) template.HTML {
		return template.HTML(s) // #nosec
	},
	"tag": func(s string) template.HTML {
		return template.HTML(`<` + s + `>`) // #nosec
	},
	"stf": func(s uint64) string {
		return time.Unix(int64(s), 0).In(singleton.Loc).Format("01/02/2006 15:04")
	},
	"sf": func(duration uint64) string {
		return time.Duration(time.Duration(duration) * time.Second).String()
	},
	"sft": func(future time.Time) string {
		return time.Until(future).Round(time.Second).String()
	},
	"bf": func(b uint64) string {
		return bytefmt.ByteSize(b)
	},
	"ts": func(s string) string {
		return strings.TrimSpace(s)
	},
	"float32f": func(f float32) string {
		return fmt.Sprintf("%.3f", f)
	},
	"divU64": func(a, b uint64) float32 {
		if b == 0 {
			if a > 0 {
				return 100
			}
			return 0
		}
		if a == 0 {
			// 这是从未在线的情况
			return 0.00001 / float32(b) * 100
		}
		return float32(a) / float32(b) * 100
	},
	"div": func(a, b int) float32 {
		if b == 0 {
			if a > 0 {
				return 100
			}
			return 0
		}
		if a == 0 {
			// 这是从未在线的情况
			return 0.00001 / float32(b) * 100
		}
		return float32(a) / float32(b) * 100
	},
	"addU64": func(a, b uint64) uint64 {
		return a + b
	},
	"add": func(a, b int) int {
		return a + b
	},
	"TransLeftPercent": func(a, b float64) (n float64) {
		if b <= 0 {
			return 0
		}
		n = (a / b) * 100
		if n > 100 {
			n = 100
		}
		if n < 0 {
			n = 0
		}
		return math.Round(n*100) / 100
	},
	"TransUsedPercent": func(used, total float64) (n float64) {
		if total <= 0 {
			return 0
		}
		n = (used / total) * 100
		if n > 100 {
			n = 100
		}
		if n < 0 {
			n = 0
		}
		return math.Round(n*100) / 100
	},
	"TransLeft": func(a, b uint64) string {
		if a < b {
			return "0B"
		}
		return bytefmt.ByteSize(a - b)
	},
	"TransClassName": func(a float64) string {
		if a == 0 {
			return "offline"
		}
		if a > 50 {
			return "fine"
		}
		if a > 20 {
			return "warning"
		}
		if a > 0 {
			return "error"
		}
		return "offline"
	},
	"UintToFloat": func(a uint64) (n float64) {
		n, _ = strconv.ParseFloat((strconv.FormatUint(a, 10)), 64)
		return
	},
	"dayBefore": func(i int) string {
		year, month, day := time.Now().Date()
		today := time.Date(year, month, day, 0, 0, 0, 0, singleton.Loc)
		return today.AddDate(0, 0, i-29).Format("01/02")
	},
	"className": func(percent float32) string {
		if percent == 0 {
			return ""
		}
		if percent > 95 {
			return "good"
		}
		if percent > 80 {
			return "warning"
		}
		return "danger"
	},
	"statusName": func(val float32) string {
		return singleton.StatusCodeToString(singleton.GetStatusCode(val))
	},
}

func natGateway(c *gin.Context) {
	natConfig := singleton.GetNATConfigByDomain(c.Request.Host)
	if natConfig == nil {
		return
	}

	singleton.ServerLock.RLock()
	server := singleton.ServerList[natConfig.ServerID]
	singleton.ServerLock.RUnlock()
	if server == nil || server.TaskStream == nil {
		c.Writer.WriteString("server not found or not connected")
		c.Abort()
		return
	}

	streamId, err := uuid.GenerateUUID()
	if err != nil {
		c.Writer.WriteString(fmt.Sprintf("stream id error: %v", err))
		c.Abort()
		return
	}

	rpc.ServerHandlerSingleton.CreateStream(streamId)
	defer rpc.ServerHandlerSingleton.CloseStream(streamId)

	taskData, err := utils.Json.Marshal(model.TaskNAT{
		StreamID: streamId,
		Host:     natConfig.Host,
	})
	if err != nil {
		c.Writer.WriteString(fmt.Sprintf("task data error: %v", err))
		c.Abort()
		return
	}

	if err := server.TaskStream.Send(&proto.Task{
		Type: model.TaskTypeNAT,
		Data: string(taskData),
	}); err != nil {
		c.Writer.WriteString(fmt.Sprintf("send task error: %v", err))
		c.Abort()
		return
	}

	w, err := utils.NewRequestWrapper(c.Request, c.Writer)
	if err != nil {
		c.Writer.WriteString(fmt.Sprintf("request wrapper error: %v", err))
		c.Abort()
		return
	}

	if err := rpc.ServerHandlerSingleton.UserConnected(streamId, w); err != nil {
		c.Writer.WriteString(fmt.Sprintf("user connected error: %v", err))
		c.Abort()
		return
	}

	rpc.ServerHandlerSingleton.StartStream(streamId, time.Second*10)
	c.Abort()
}

// pushServerStatusToClients 推送服务器状态和流量数据到WebSocket客户端
func pushServerStatusToClients() {
	singleton.ServerLock.RLock()
	defer singleton.ServerLock.RUnlock()

	// 构建服务器状态数据
	serverStatusData := make([]map[string]interface{}, 0)
	for _, server := range singleton.ServerList {
		serverData := map[string]interface{}{
			"ID":         server.ID,
			"Name":       server.Name,
			"IsOnline":   server.IsOnline,
			"LastActive": server.LastActive,
		}

		// 添加服务器状态数据
		if server.State != nil {
			serverData["State"] = server.State
		}

		serverStatusData = append(serverStatusData, serverData)
	}

	// 构建流量数据
	trafficData := buildTrafficData()

	// 构建完整的消息
	message := map[string]interface{}{
		"now":         time.Now().UnixMilli(),
		"servers":     serverStatusData,
		"trafficData": trafficData,
	}

	// 发送到所有客户端
	messageJSON, err := utils.Json.Marshal(message)
	if err == nil {
		// 发送到所有WebSocket客户端
		wsClients.Range(func(key, value interface{}) bool {
			if conn, ok := value.(*websocket.Conn); ok {
				conn.WriteMessage(websocket.TextMessage, messageJSON)
			}
			return true
		})
	}
}

// buildTrafficData 构建用于前端显示的流量数据
func buildTrafficData() []map[string]interface{} {
	trafficData := make([]map[string]interface{}, 0)

	// 遍历所有服务器，生成流量数据
	for _, server := range singleton.ServerList {
		// 跳过没有状态数据的服务器
		if server.State == nil {
			continue
		}

		// 计算已使用流量和总流量
		traffic := map[string]interface{}{
			"server_id":   server.ID,
			"server_name": server.Name,
			"cycle_name":  "Monthly", // 默认周期名称

			// 使用字节数据作为源
			"is_bytes_source": true,
			"used_bytes":      server.State.NetInTransfer + server.State.NetOutTransfer,
			"max_bytes":       uint64(1099511627776), // 默认1TB限额，可根据实际情况调整

			// 提供格式化的流量字符串，兼容旧版本
			"used_formatted": formatBytes(server.State.NetInTransfer + server.State.NetOutTransfer),
			"max_formatted":  "1TB",
		}

		// 计算使用百分比
		if traffic["max_bytes"].(uint64) > 0 {
			traffic["used_percent"] = float64(traffic["used_bytes"].(uint64)) / float64(traffic["max_bytes"].(uint64)) * 100
		}

		trafficData = append(trafficData, traffic)
	}

	return trafficData
}

// formatBytes 格式化字节大小为易读形式
func formatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// 启动WebSocket数据推送服务
func startWebSocketPushService() {
	// 每5秒推送一次数据
	go func() {
		for {
			time.Sleep(5 * time.Second)
			pushServerStatusToClients()
		}
	}()
}

// 处理从客户端收到的WebSocket消息
func handleWebSocketMessages(conn *websocket.Conn, clientID string) {
	for {
		messageType, p, err := conn.ReadMessage()
		if err != nil {
			log.Println("读取WebSocket消息失败:", err)
			wsClients.Delete(clientID)
			return
		}

		// 简单的ping-pong测试
		if messageType == websocket.TextMessage {
			var message map[string]interface{}
			if err := utils.Json.Unmarshal(p, &message); err == nil {
				if message["type"] == "ping" {
					conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"pong"}`))
				}
			}
		}
	}
}
