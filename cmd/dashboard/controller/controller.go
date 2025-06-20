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
	"time"

	"code.cloudfoundry.org/bytefmt"
	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"github.com/hashicorp/go-uuid"
	"github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/mygin"
	"github.com/xos/serverstatus/pkg/utils"
	"github.com/xos/serverstatus/proto"
	"github.com/xos/serverstatus/service/rpc"
	"github.com/xos/serverstatus/service/singleton"
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

		// 处理公共模板目录
		if themeDir == "common" || themeDir == "component" {
			// load common/component templates
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

// buildTrafficData 构建用于前端显示的流量数据
// 返回的数据符合周期配置的cycle_start和cycle_unit
func buildTrafficData() []map[string]interface{} {
	singleton.AlertsLock.RLock()
	defer singleton.AlertsLock.RUnlock()

	// 创建一个AlertsCycleTransferStatsStore的深拷贝副本，确保并发安全
	var statsStore map[uint64]model.CycleTransferStats
	if singleton.AlertsCycleTransferStatsStore != nil {
		statsStore = make(map[uint64]model.CycleTransferStats)
		for cycleID, stats := range singleton.AlertsCycleTransferStatsStore {
			// 深拷贝每个CycleTransferStats
			newStats := model.CycleTransferStats{
				Name: stats.Name,
				Max:  stats.Max,
				From: stats.From,
				To:   stats.To,
			}

			// 深拷贝Transfer map
			if stats.Transfer != nil {
				newStats.Transfer = make(map[uint64]uint64)
				for serverID, transfer := range stats.Transfer {
					newStats.Transfer[serverID] = transfer
				}
			}

			// 深拷贝ServerName map
			if stats.ServerName != nil {
				newStats.ServerName = make(map[uint64]string)
				for serverID, name := range stats.ServerName {
					newStats.ServerName[serverID] = name
				}
			}

			// 深拷贝NextUpdate map
			if stats.NextUpdate != nil {
				newStats.NextUpdate = make(map[uint64]time.Time)
				for serverID, updateTime := range stats.NextUpdate {
					newStats.NextUpdate[serverID] = updateTime
				}
			}

			statsStore[cycleID] = newStats
		}
	}

	// 从statsStore构建流量数据
	var trafficData []map[string]interface{}

	if statsStore != nil {
		for cycleID, stats := range statsStore {
			// 查找对应的Alert规则，用于获取周期设置
			var alert *model.AlertRule
			for _, a := range singleton.Alerts {
				if a.ID == cycleID {
					alert = a
					break
				}
			}

			// 如果找不到Alert规则，跳过此项
			if alert == nil {
				continue
			}

			// 找到与流量相关的Rule
			var flowRule *model.Rule
			for i := range alert.Rules {
				if alert.Rules[i].IsTransferDurationRule() {
					flowRule = &alert.Rules[i]
					break
				}
			}

			// 如果没有流量相关规则，跳过
			if flowRule == nil {
				continue
			}

			// 确保周期开始和结束时间正确设置
			from := flowRule.GetTransferDurationStart()
			to := flowRule.GetTransferDurationEnd()

			// 更新stats中的周期时间，确保与规则一致
			stats.From = from
			stats.To = to
			stats.Max = uint64(flowRule.Max)
			stats.Name = alert.Name

			// 安全修复：使用同步更新替代异步goroutine，防止concurrent map writes
			// 同时确保数据不丢失
			func() {
				singleton.AlertsLock.Lock()
				defer singleton.AlertsLock.Unlock()
				if store, ok := singleton.AlertsCycleTransferStatsStore[cycleID]; ok && store != nil {
					// 安全地更新周期信息，确保数据一致性
					store.From = from
					store.To = to
					store.Max = uint64(flowRule.Max)
					store.Name = alert.Name
				}
			}()

			// 生成流量数据条目
			for serverID, transfer := range stats.Transfer {
				serverName := ""
				if stats.ServerName != nil {
					if name, exists := stats.ServerName[serverID]; exists {
						serverName = name
					}
				}

				// 如果没有名称，尝试从ServerList获取
				if serverName == "" {
					singleton.ServerLock.RLock()
					if server := singleton.ServerList[serverID]; server != nil {
						serverName = server.Name
					}
					singleton.ServerLock.RUnlock()
				}

				// 计算使用百分比
				usedPercent := float64(0)
				if stats.Max > 0 {
					usedPercent = (float64(transfer) / float64(stats.Max)) * 100
					usedPercent = math.Max(0, math.Min(100, usedPercent)) // 限制在0-100范围
				}

				// 获取周期单位和开始时间，用于前端展示
				cycleUnit := flowRule.CycleUnit

				// 构建完整的流量数据项，包含周期信息
				trafficItem := map[string]interface{}{
					"server_id":       serverID,
					"server_name":     serverName,
					"max_bytes":       stats.Max,
					"used_bytes":      transfer,
					"max_formatted":   formatBytes(stats.Max),
					"used_formatted":  formatBytes(transfer),
					"used_percent":    math.Round(usedPercent*100) / 100,
					"cycle_name":      stats.Name,
					"cycle_id":        strconv.FormatUint(cycleID, 10),
					"cycle_start":     stats.From.Format(time.RFC3339),
					"cycle_end":       stats.To.Format(time.RFC3339),
					"cycle_unit":      cycleUnit,
					"cycle_interval":  flowRule.CycleInterval,
					"is_bytes_source": true,
					"now":             time.Now().Unix() * 1000,
				}

				trafficData = append(trafficData, trafficItem)
			}
		}
	}

	// 补充机制：为没有被警报规则覆盖的服务器创建默认流量数据（10TB月配额）
	// 获取所有已被警报规则覆盖的服务器ID
	coveredServerIDs := make(map[uint64]bool)
	if statsStore != nil {
		for _, stats := range statsStore {
			for serverID := range stats.Transfer {
				coveredServerIDs[serverID] = true
			}
		}
	}

	// 为未覆盖的在线服务器创建默认配额
	singleton.ServerLock.RLock()
	for serverID, server := range singleton.ServerList {
		if server == nil || !server.IsOnline {
			continue
		}

		// 如果服务器已被警报规则覆盖，跳过
		if coveredServerIDs[serverID] {
			continue
		}

		// 创建默认的月流量配额（10TB = 10 * 1024^4 bytes）
		defaultQuota := uint64(10 * 1024 * 1024 * 1024 * 1024) // 10TB

		// 计算当前月的开始和结束时间（每月1号开始）
		now := time.Now()
		currentMonthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		nextMonthStart := currentMonthStart.AddDate(0, 1, 0)

		// 计算当月累积流量（模拟月度重置）
		var monthlyTransfer uint64

		// 如果服务器有最后活跃时间记录，且在当月内，使用累积流量
		if !server.LastActive.IsZero() && server.LastActive.After(currentMonthStart) {
			monthlyTransfer = server.CumulativeNetInTransfer + server.CumulativeNetOutTransfer
		} else {
			// 如果服务器在本月开始前就不活跃，或者没有记录，流量从0开始
			monthlyTransfer = 0
		}

		// 计算使用百分比
		usedPercent := float64(0)
		if defaultQuota > 0 {
			usedPercent = (float64(monthlyTransfer) / float64(defaultQuota)) * 100
			usedPercent = math.Max(0, math.Min(100, usedPercent))
		}

		// 构建默认流量数据项，显示月度配额
		trafficItem := map[string]interface{}{
			"server_id":       serverID,
			"server_name":     server.Name,
			"max_bytes":       defaultQuota,
			"used_bytes":      monthlyTransfer,
			"max_formatted":   formatBytes(defaultQuota),
			"used_formatted":  formatBytes(monthlyTransfer),
			"used_percent":    math.Round(usedPercent*100) / 100,
			"cycle_name":      "默认月流量配额",
			"cycle_id":        "default-monthly",
			"cycle_start":     currentMonthStart.Format(time.RFC3339),
			"cycle_end":       nextMonthStart.Format(time.RFC3339),
			"cycle_unit":      "month",
			"cycle_interval":  1,
			"is_bytes_source": true,
			"now":             time.Now().Unix() * 1000,
		}

		trafficData = append(trafficData, trafficItem)
	}
	singleton.ServerLock.RUnlock()

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
