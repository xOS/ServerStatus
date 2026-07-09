package controller

import (
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xos/serverstatus/db"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/mygin"
	"github.com/xos/serverstatus/pkg/utils"
	"github.com/xos/serverstatus/service/singleton"
)

type apiV1 struct {
	r gin.IRouter
}

// monitor API 短时缓存（包级别）
var (
	monitorCacheMu sync.Mutex
	monitorCache   = map[string]struct {
		ts   time.Time
		data []byte // pre-encoded JSON payload
		dur  time.Duration
	}{}
	// serverList/serverDetails 短时缓存
	listCacheMu sync.Mutex
	listCache   = map[string]struct {
		ts   time.Time
		data []byte
	}{}
	monitorConfigCacheMu sync.Mutex
	monitorConfigCache   struct {
		ts   time.Time
		data []byte
	}
	invalidNetworkLatencyValues = [...]float64{0, 450}
)

func (v *apiV1) serve() {
	r := v.r.Group("")
	// 强制认证的 API
	r.Use(mygin.Authorize(mygin.AuthorizeOption{
		MemberOnly: true,
		IsPage:     false,
		Msg:        "访问此接口需要认证",
		Btn:        "点此登录",
		Redirect:   "/login",
	}))
	r.POST("/server/register", v.RegisterServer)

	// 公开的 Server API
	publicServer := v.r.Group("server")
	publicServer.Use(mygin.Authorize(mygin.AuthorizeOption{
		MemberOnly: false,
		IsPage:     false,
	}))
	publicServer.GET("/list", v.serverList)
	publicServer.GET("/details", v.serverDetails)

	br := v.r.Group("bootstrap")
	br.Use(mygin.Authorize(mygin.AuthorizeOption{
		MemberOnly: false,
		IsPage:     false,
	}))
	br.GET("", v.bootstrap)

	// 不强制认证的 API
	mr := v.r.Group("monitor")
	mr.Use(mygin.Authorize(mygin.AuthorizeOption{
		MemberOnly: false,
		IsPage:     false,
		Msg:        "访问此接口需要认证",
		Btn:        "点此登录",
		Redirect:   "/login",
	}))
	mr.Use(mygin.ValidateViewPassword(mygin.ValidateViewPasswordOption{
		IsPage:        false,
		AbortWhenFail: true,
	}))
	mr.GET("/configs", v.monitorConfigs)
	mr.GET("/:id", v.monitorHistoriesById)

	sr := v.r.Group("service")
	sr.Use(mygin.Authorize(mygin.AuthorizeOption{
		MemberOnly: false,
		IsPage:     false,
	}))
	sr.Use(mygin.ValidateViewPassword(mygin.ValidateViewPasswordOption{
		IsPage:        false,
		AbortWhenFail: true,
	}))
	sr.GET("", v.serviceStatus)

	// Frontend Profile API (提供原先由 CommonEnvironment 注入的全局配置)
	cr := v.r.Group("profile")
	cr.Use(mygin.Authorize(mygin.AuthorizeOption{
		MemberOnly: false,
		IsPage:     false,
	}))
	cr.GET("", v.profile)
}

// profile 返回前端 SPA 初始化所需的全局配置与用户信息
func (v *apiV1) profile(c *gin.Context) {
	WriteJSON(c, 200, profilePayload(c))
}

func (v *apiV1) bootstrap(c *gin.Context) {
	WriteJSON(c, 200, gin.H{
		"profile":     profilePayload(c),
		"servers":     frontendServerList(c, c.Query("tag")),
		"trafficData": buildTrafficData(),
		"now":         time.Now().UnixMilli(),
	})
}

func profilePayload(c *gin.Context) gin.H {
	data := gin.H{
		"Version":             singleton.Version,
		"CustomCode":          singleton.Conf.Site.CustomCode,
		"CustomCodeDashboard": singleton.Conf.Site.CustomCodeDashboard,
		"Conf": gin.H{
			"Site": gin.H{
				"Brand":               singleton.Conf.Site.Brand,
				"LogoURL":             singleton.Conf.Site.LogoURL,
				"CustomCode":          singleton.Conf.Site.CustomCode,
				"CustomCodeDashboard": singleton.Conf.Site.CustomCodeDashboard,
				"FooterYear":          singleton.Conf.Site.FooterYear,
				"FooterName":          singleton.Conf.Site.FooterName,
				"FooterURL":           singleton.Conf.Site.FooterURL,
			},
			"Login": gin.H{
				"EnableOAuth":  singleton.Conf.Login.EnableOAuth,
				"EnableAPIKey": singleton.Conf.Login.EnableAPIKey,
			},
		},
	}

	// 如果用户已登录，返回用户信息
	u, ok := c.Get(model.CtxKeyAuthorizedUser)
	if ok {
		if user, ok := u.(*model.User); ok && user != nil {
			data["Admin"] = gin.H{
				"ID":         user.ID,
				"Login":      user.Login,
				"Name":       user.Name,
				"AvatarURL":  user.AvatarURL,
				"SuperAdmin": user.SuperAdmin,
			}
		}
	}

	return data
}

// serverList 获取服务器列表 不传入Query参数则获取全部
// header: Authorization: Token
// query: tag (服务器分组)
func (v *apiV1) serverList(c *gin.Context) {
	tag := c.Query("tag")
	cacheKey := "serverList:role=" + frontendRole(c) + ":tag=" + tag
	listCacheMu.Lock()
	if ce, ok := listCache[cacheKey]; ok && time.Since(ce.ts) <= 500*time.Millisecond {
		payload := ce.data
		listCacheMu.Unlock()
		WriteJSONPayload(c, 200, payload)
		return
	}
	listCacheMu.Unlock()

	res := frontendServerList(c, tag)
	payload, err := utils.EncodeJSON(res)
	if err != nil {
		payload, _ = utils.EncodeJSON([]any{})
	}
	listCacheMu.Lock()
	listCache[cacheKey] = struct {
		ts   time.Time
		data []byte
	}{ts: time.Now(), data: payload}
	listCacheMu.Unlock()
	WriteJSONPayload(c, 200, payload)
}

func frontendRole(c *gin.Context) string {
	if u, ok := c.Get(model.CtxKeyAuthorizedUser); ok && u != nil {
		return "admin"
	}
	return "guest"
}

func frontendServerList(c *gin.Context, tag string) []*model.Server {
	var res []*model.Server
	singleton.ServerLock.RLock()
	singleton.SortedServerLock.RLock()
	if frontendRole(c) == "admin" {
		res = cloneServersForFrontend(singleton.SortedServerList, true)
	} else {
		res = cloneServersForFrontend(singleton.SortedServerListForGuest, true)
	}
	singleton.SortedServerLock.RUnlock()
	singleton.ServerLock.RUnlock()

	tag = strings.TrimSpace(tag)
	if tag == "" {
		return res
	}

	filtered := make([]*model.Server, 0, len(res))
	for _, server := range res {
		if server != nil && server.Tag == tag {
			filtered = append(filtered, server)
		}
	}
	return filtered
}

// serverDetails 获取服务器信息 不传入Query参数则获取全部
// header: Authorization: Token
// query: id (服务器ID，逗号分隔，优先级高于tag查询)
// query: tag (服务器分组)
func (v *apiV1) serverDetails(c *gin.Context) {
	var idList []uint64
	idListStr := strings.Split(c.Query("id"), ",")
	if c.Query("id") != "" {
		idList = make([]uint64, len(idListStr))
		for i, v := range idListStr {
			id, _ := strconv.ParseUint(v, 10, 64)
			idList[i] = id
		}
	}
	tag := c.Query("tag")
	cacheKey := "serverDetails:id=" + c.Query("id") + "&tag=" + tag
	listCacheMu.Lock()
	if ce, ok := listCache[cacheKey]; ok && time.Since(ce.ts) <= 500*time.Millisecond {
		payload := ce.data
		listCacheMu.Unlock()
		WriteJSONPayload(c, 200, payload)
		return
	}
	listCacheMu.Unlock()

	var res interface{}
	if tag != "" {
		res = singleton.ServerAPI.GetStatusByTag(tag)
	} else if len(idList) != 0 {
		res = singleton.ServerAPI.GetStatusByIDList(idList)
	} else {
		res = singleton.ServerAPI.GetAllStatus()
	}
	payload, err := utils.EncodeJSON(res)
	if err != nil {
		payload, _ = utils.EncodeJSON([]any{})
	}
	listCacheMu.Lock()
	listCache[cacheKey] = struct {
		ts   time.Time
		data []byte
	}{ts: time.Now(), data: payload}
	listCacheMu.Unlock()
	WriteJSONPayload(c, 200, payload)
}

// RegisterServer adds a server and responds with the full ServerRegisterResponse
// header: Authorization: Token
// body: RegisterServer
// response: ServerRegisterResponse or Secret string
func (v *apiV1) RegisterServer(c *gin.Context) {
	var rs singleton.RegisterServer
	// Attempt to bind JSON to RegisterServer struct
	if err := c.ShouldBindJSON(&rs); err != nil {
		WriteJSON(c, 400, singleton.ServerRegisterResponse{
			CommonResponse: singleton.CommonResponse{
				Code:    400,
				Message: "Parse JSON failed",
			},
		})
		return
	}
	// Check if simple mode is requested
	simple := c.Query("simple") == "true" || c.Query("simple") == "1"
	// Set defaults if fields are empty
	if rs.Name == "" {
		rs.Name = c.ClientIP()
	}
	if rs.Tag == "" {
		rs.Tag = "AutoRegister"
	}
	if rs.HideForGuest == "" {
		rs.HideForGuest = "on"
	}
	// Call the Register function and get the response
	response := singleton.ServerAPI.Register(&rs)
	// Respond with Secret only if in simple mode, otherwise full response
	if simple {
		WriteJSON(c, response.Code, response.Secret)
	} else {
		WriteJSON(c, response.Code, response)
	}
}

func (v *apiV1) monitorHistoriesById(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		WriteJSON(c, 400, gin.H{"code": 400, "message": "id参数错误"})
		return
	}
	server, ok := singleton.ServerList[id]
	if !ok {
		WriteJSON(c, 404, gin.H{
			"code":    404,
			"message": "id不存在",
		})
		return
	}

	_, isMember := c.Get(model.CtxKeyAuthorizedUser)
	_, isViewPasswordVerfied := c.Get(model.CtxKeyViewPasswordVerified)
	authorized := isMember || isViewPasswordVerfied

	if server.HideForGuest && !authorized {
		WriteJSON(c, 403, gin.H{"code": 403, "message": "需要认证"})
		return
	}

	// 解析时间范围参数，默认72小时=3天（向后兼容原逻辑）
	rangeParam := strings.ToLower(strings.TrimSpace(c.Query("range")))
	// 默认3天
	duration := 72 * time.Hour
	switch rangeParam {
	case "24h", "24", "1d":
		duration = 24 * time.Hour
	case "72h", "72", "3d", "":
		duration = 72 * time.Hour
	}

	// 根本性能优化：使用正确的高效查询方法，利用BadgerDB的时间索引
	if singleton.Conf.DatabaseType == "badger" {
		if db.DB != nil {
			// 使用range参数决定时间范围
			endTime := time.Now()
			startTime := endTime.Add(-duration)

			// 命中短时缓存则直接返回（预编码 JSON）
			monitorCacheKey := fmt.Sprintf("%d:%s", server.ID, rangeParam)
			monitorCacheMu.Lock()
			if ce, ok := monitorCache[monitorCacheKey]; ok {
				if time.Since(ce.ts) <= 30*time.Second && ce.dur == duration {
					payload := ce.data
					monitorCacheMu.Unlock()
					c.Status(200)
					c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
					if _, gz, _ := utils.GzipIfAccepted(c.Writer, c.Request, payload); !gz {
						_, _ = c.Writer.Write(payload)
					}
					return
				}
			}
			monitorCacheMu.Unlock()

			// 获取该服务器的监控配置，展示所有监控器
			monitors := singleton.ServiceSentinelShared.Monitors()
			monitorNames := monitorNameMap(monitors)
			var networkHistories []*model.MonitorHistory

			if monitors != nil {
				monitorOps := db.NewMonitorHistoryOps(db.DB)

				// 使用并发查询提升性能，同时保持数据完整性
				type monitorResult struct {
					histories []*model.MonitorHistory
					err       error
				}

				// 创建通道收集结果 + 控制并发
				resultChan := make(chan monitorResult, len(monitors))
				activeQueries := 0
				// 限制并发，避免在低配机上压爆 CPU
				sem := make(chan struct{}, 4)

				// 并发查询当前服务器实际参与的 ICMP/TCP 监控器
				for _, monitor := range monitors {
					if monitorAppliesToServer(monitor, server.ID) && (monitor.Type == model.TaskTypeICMPPing || monitor.Type == model.TaskTypeTCPPing) {
						activeQueries++
						go func(monitorID uint64, monitorDuration uint64) {
							sem <- struct{}{}
							defer func() { <-sem }()
							// 使用反向限量，优先拿最近的数据；limit 按监控周期动态计算，避免 72h 被截断导致统计失真
							allHistories, err := monitorOps.GetMonitorHistoriesByServerAndMonitorRangeReverseLimit(
								server.ID, monitorID, startTime, endTime, networkHistoryQueryLimit(duration, monitorDuration),
							)

							resultChan <- monitorResult{
								histories: allHistories,
								err:       err,
							}
						}(monitor.ID, monitor.Duration)
					}
				}

				// 收集所有并发查询结果
				for i := 0; i < activeQueries; i++ {
					result := <-resultChan
					if result.err != nil {
						log.Printf("并发查询监控历史记录失败: %v", result.err)
						continue
					}
					networkHistories = append(networkHistories, result.histories...)
				}
			}

			// 将多个监控器结果汇总后统一按时间倒序
			sort.Slice(networkHistories, func(i, j int) bool {
				return networkHistories[i].CreatedAt.After(networkHistories[j].CreatedAt)
			})

			summary := monitorHistorySummary(networkHistories)
			chartHistories := sampleMonitorHistories(networkHistories, 5000)

			// 预编码 JSON，减少后续重复编码
			payload, err := utils.EncodeJSON(networkMonitorHistoryResponse{
				Data:    monitorHistoryPayload(chartHistories, monitorNames),
				Summary: summary,
			})
			if err != nil {
				// 回退到空数组
				payload, _ = utils.EncodeJSON([]any{})
			}

			// 写入短时缓存
			monitorCacheMu.Lock()
			monitorCache[monitorCacheKey] = struct {
				ts   time.Time
				data []byte
				dur  time.Duration
			}{ts: time.Now(), data: payload, dur: duration}
			monitorCacheMu.Unlock()

			log.Printf("API /monitor/%d 返回 %d/%d 条记录（范围: %v，所有监控器）", server.ID, len(chartHistories), len(networkHistories), duration)
			c.Status(200)
			c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			if _, gz, _ := utils.GzipIfAccepted(c.Writer, c.Request, payload); !gz {
				_, _ = c.Writer.Write(payload)
			}
		} else {
			payload, _ := utils.EncodeJSON([]any{})
			c.Status(200)
			c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			if _, gz, _ := utils.GzipIfAccepted(c.Writer, c.Request, payload); !gz {
				_, _ = c.Writer.Write(payload)
			}
		}
	} else {
		// SQLite 模式下恢复原始查询逻辑
		if singleton.DB != nil {
			var networkHistories []*model.MonitorHistory

			// 使用range参数决定时间范围（与Badger保持一致）
			endTime := time.Now()
			startTime := endTime.Add(-duration)

			err := singleton.DB.Where("server_id = ? AND created_at >= ? AND created_at < ? AND monitor_id IN (SELECT id FROM monitors WHERE type IN (?, ?))",
				server.ID, startTime, endTime, model.TaskTypeICMPPing, model.TaskTypeTCPPing).
				Order("created_at DESC").
				Find(&networkHistories).Error

			var payload []byte
			if err != nil {
				payload, _ = utils.EncodeJSON([]any{})
			} else {
				summary := monitorHistorySummary(networkHistories)
				chartHistories := sampleMonitorHistories(networkHistories, 5000)
				payload, _ = utils.EncodeJSON(networkMonitorHistoryResponse{
					Data:    monitorHistoryPayload(chartHistories, currentMonitorNameMap()),
					Summary: summary,
				})
			}
			c.Status(200)
			c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			if _, gz, _ := utils.GzipIfAccepted(c.Writer, c.Request, payload); !gz {
				_, _ = c.Writer.Write(payload)
			}
		} else {
			payload, _ := utils.EncodeJSON([]any{})
			c.Status(200)
			c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			if _, gz, _ := utils.GzipIfAccepted(c.Writer, c.Request, payload); !gz {
				_, _ = c.Writer.Write(payload)
			}
		}
	}
}

type networkMonitorHistoryResponse struct {
	Data    []networkMonitorHistoryItem `json:"data"`
	Summary networkMonitorSummary       `json:"summary"`
}

type networkMonitorHistoryItem struct {
	CreatedAt   time.Time `json:"created_at"`
	MonitorID   uint64    `json:"monitor_id"`
	MonitorName string    `json:"monitor_name,omitempty"`
	ServerID    uint64    `json:"server_id"`
	AvgDelay    float32   `json:"avg_delay"`
	Up          uint64    `json:"up"`
	Down        uint64    `json:"down"`
}

type networkMonitorSummary struct {
	Max          *float64 `json:"max"`
	Min          *float64 `json:"min"`
	Avg          *float64 `json:"avg"`
	P95          *float64 `json:"p95"`
	Loss         *float64 `json:"loss"`
	Count        int      `json:"count"`
	LatencyCount int      `json:"latency_count"`
}

func networkHistoryQueryLimit(duration time.Duration, monitorDuration uint64) int {
	interval := time.Duration(monitorDuration) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	limit := int(duration/interval) + 512
	if limit < 6000 {
		return 6000
	}
	if limit > 60000 {
		return 60000
	}
	return limit
}

func sampleMonitorHistories(histories []*model.MonitorHistory, maxPoints int) []*model.MonitorHistory {
	if maxPoints <= 0 || len(histories) <= maxPoints {
		return histories
	}

	groups := make(map[uint64][]*model.MonitorHistory)
	order := make([]uint64, 0)
	for _, history := range histories {
		if history == nil {
			continue
		}
		if _, ok := groups[history.MonitorID]; !ok {
			order = append(order, history.MonitorID)
		}
		groups[history.MonitorID] = append(groups[history.MonitorID], history)
	}
	if len(order) == 0 {
		return []*model.MonitorHistory{}
	}

	if len(order) >= maxPoints {
		sampled := make([]*model.MonitorHistory, 0, maxPoints)
		for _, monitorID := range order {
			group := groups[monitorID]
			if len(group) > 0 {
				sampled = append(sampled, group[0])
			}
		}
		sort.Slice(sampled, func(i, j int) bool {
			return sampled[i].CreatedAt.After(sampled[j].CreatedAt)
		})
		if len(sampled) > maxPoints {
			return sampled[:maxPoints]
		}
		return sampled
	}

	perGroupLimit := maxPoints / len(order)
	remainder := maxPoints % len(order)
	sampled := make([]*model.MonitorHistory, 0, maxPoints)
	for index, monitorID := range order {
		limit := perGroupLimit
		if index < remainder {
			limit++
		}
		sampled = append(sampled, sampleMonitorHistoryGroup(groups[monitorID], limit)...)
	}

	sort.Slice(sampled, func(i, j int) bool {
		return sampled[i].CreatedAt.After(sampled[j].CreatedAt)
	})
	return sampled
}

func sampleMonitorHistoryGroup(histories []*model.MonitorHistory, limit int) []*model.MonitorHistory {
	if limit <= 0 || len(histories) == 0 {
		return []*model.MonitorHistory{}
	}
	if len(histories) <= limit {
		return histories
	}
	if limit == 1 {
		return histories[:1]
	}

	step := float64(len(histories)-1) / float64(limit-1)
	sampled := make([]*model.MonitorHistory, 0, limit)
	for i := 0; i < limit; i++ {
		idx := int(float64(i)*step + 0.5)
		if idx >= len(histories) {
			idx = len(histories) - 1
		}
		sampled = append(sampled, histories[idx])
	}
	return sampled
}

func monitorHistorySummary(histories []*model.MonitorHistory) networkMonitorSummary {
	summary := networkMonitorSummary{Count: len(histories)}
	values := make([]float64, 0, len(histories))
	var up, down uint64

	for _, history := range histories {
		if history == nil {
			continue
		}
		up += history.Up
		down += history.Down
		if isValidNetworkLatency(history) {
			values = append(values, float64(history.AvgDelay))
		}
	}

	total := up + down
	if total > 0 {
		loss := float64(down) / float64(total) * 100
		summary.Loss = &loss
	}
	summary.LatencyCount = len(values)
	if len(values) == 0 {
		return summary
	}

	sort.Float64s(values)
	min := values[0]
	max := values[len(values)-1]
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	avg := sum / float64(len(values))
	p95 := percentileFloat64(values, 0.95)
	summary.Min = &min
	summary.Max = &max
	summary.Avg = &avg
	summary.P95 = &p95
	return summary
}

func isValidNetworkLatency(history *model.MonitorHistory) bool {
	if history == nil {
		return false
	}
	value := float64(history.AvgDelay)
	if value < 0 || isInvalidNetworkLatency(value) {
		return false
	}
	if history.Up > 0 {
		return true
	}
	return history.Up == 0 && history.Down == 0
}

func isInvalidNetworkLatency(value float64) bool {
	for _, invalid := range invalidNetworkLatencyValues {
		diff := value - invalid
		if diff < 0 {
			diff = -diff
		}
		if diff < 0.0001 {
			return true
		}
	}
	return false
}

func percentileFloat64(sortedValues []float64, ratio float64) float64 {
	if len(sortedValues) == 0 {
		return 0
	}
	if ratio < 0 {
		ratio = 0
	} else if ratio > 1 {
		ratio = 1
	}
	index := float64(len(sortedValues)-1) * ratio
	lower := int(index)
	upper := lower
	if float64(lower) < index {
		upper++
	}
	if lower == upper {
		return sortedValues[lower]
	}
	weight := index - float64(lower)
	return sortedValues[lower]*(1-weight) + sortedValues[upper]*weight
}

func currentMonitorNameMap() map[uint64]string {
	if singleton.ServiceSentinelShared == nil {
		return map[uint64]string{}
	}
	return monitorNameMap(singleton.ServiceSentinelShared.Monitors())
}

func monitorNameMap(monitors []*model.Monitor) map[uint64]string {
	names := make(map[uint64]string, len(monitors))
	for _, monitor := range monitors {
		if monitor == nil {
			continue
		}
		names[monitor.ID] = monitor.Name
	}
	return names
}

func monitorAppliesToServer(monitor *model.Monitor, serverID uint64) bool {
	if monitor == nil {
		return false
	}
	if monitor.SkipServers == nil {
		_ = monitor.InitSkipServers()
	}
	if monitor.Cover == model.MonitorCoverAll {
		return !monitor.SkipServers[serverID]
	}
	return monitor.Cover == model.MonitorCoverIgnoreAll && monitor.SkipServers[serverID]
}

func monitorHistoryPayload(histories []*model.MonitorHistory, monitorNames map[uint64]string) []networkMonitorHistoryItem {
	items := make([]networkMonitorHistoryItem, 0, len(histories))
	for _, history := range histories {
		if history == nil {
			continue
		}
		items = append(items, networkMonitorHistoryItem{
			CreatedAt:   history.CreatedAt,
			MonitorID:   history.MonitorID,
			MonitorName: monitorNames[history.MonitorID],
			ServerID:    history.ServerID,
			AvgDelay:    history.AvgDelay,
			Up:          history.Up,
			Down:        history.Down,
		})
	}
	return items
}

func (v *apiV1) monitorConfigs(c *gin.Context) {
	monitorConfigCacheMu.Lock()
	if len(monitorConfigCache.data) > 0 && time.Since(monitorConfigCache.ts) <= 2*time.Second {
		payload := monitorConfigCache.data
		monitorConfigCacheMu.Unlock()
		WriteJSONPayload(c, 200, payload)
		return
	}
	monitorConfigCacheMu.Unlock()

	var payload []byte
	var err error
	if singleton.ServiceSentinelShared != nil {
		monitors := singleton.ServiceSentinelShared.Monitors()
		payload, err = utils.EncodeJSON(gin.H{
			"monitors":   monitors,
			"server_ids": monitoredServerIDs(monitors),
		})
	} else {
		payload, err = utils.EncodeJSON(gin.H{
			"monitors":   []interface{}{},
			"server_ids": []uint64{},
		})
	}
	if err != nil {
		payload, _ = utils.EncodeJSON(gin.H{
			"monitors":   []interface{}{},
			"server_ids": []uint64{},
		})
	}

	monitorConfigCacheMu.Lock()
	monitorConfigCache = struct {
		ts   time.Time
		data []byte
	}{ts: time.Now(), data: payload}
	monitorConfigCacheMu.Unlock()
	WriteJSONPayload(c, 200, payload)
}

func monitoredServerIDs(monitors []*model.Monitor) []uint64 {
	ids := make(map[uint64]struct{})
	singleton.ServerLock.RLock()
	serverIDs := make([]uint64, 0, len(singleton.ServerList))
	for id := range singleton.ServerList {
		serverIDs = append(serverIDs, id)
	}
	singleton.ServerLock.RUnlock()

	for _, monitor := range monitors {
		if monitor == nil || (monitor.Type != model.TaskTypeICMPPing && monitor.Type != model.TaskTypeTCPPing) {
			continue
		}
		if monitor.SkipServers == nil {
			_ = monitor.InitSkipServers()
		}
		for _, serverID := range serverIDs {
			if monitor.Cover == model.MonitorCoverAll {
				if !monitor.SkipServers[serverID] {
					ids[serverID] = struct{}{}
				}
				continue
			}
			if monitor.Cover == model.MonitorCoverIgnoreAll && monitor.SkipServers[serverID] {
				ids[serverID] = struct{}{}
			}
		}
	}

	result := make([]uint64, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (v *apiV1) serviceStatus(c *gin.Context) {
	type serviceItem struct {
		ID          uint64         `json:"ID"`
		Monitor     *model.Monitor `json:"Monitor"`
		CurrentUp   uint64         `json:"CurrentUp"`
		CurrentDown uint64         `json:"CurrentDown"`
		TotalUp     uint64         `json:"TotalUp"`
		TotalDown   uint64         `json:"TotalDown"`
		Uptime      float32        `json:"Uptime"`
		Delay       []float32      `json:"Delay"`
		Up          []int          `json:"Up"`
		Down        []int          `json:"Down"`
	}

	items := make([]serviceItem, 0)
	if singleton.ServiceSentinelShared != nil {
		singleton.AlertsLock.RLock()
		stats := singleton.ServiceSentinelShared.LoadStats()
		for id, item := range stats {
			if item == nil || item.Monitor == nil || !item.Monitor.EnableShowInService {
				continue
			}
			total := item.TotalUp + item.TotalDown
			uptime := float32(0)
			if total > 0 {
				uptime = float32(item.TotalUp) / float32(total) * 100
			}
			items = append(items, serviceItem{
				ID:          id,
				Monitor:     item.Monitor,
				CurrentUp:   item.CurrentUp,
				CurrentDown: item.CurrentDown,
				TotalUp:     item.TotalUp,
				TotalDown:   item.TotalDown,
				Uptime:      uptime,
				Delay:       float32Array(item.Delay),
				Up:          intArray(item.Up),
				Down:        intArray(item.Down),
			})
		}
		singleton.AlertsLock.RUnlock()
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Monitor == nil || items[j].Monitor == nil {
			return items[i].ID < items[j].ID
		}
		if items[i].Monitor.Name == items[j].Monitor.Name {
			return items[i].ID < items[j].ID
		}
		return items[i].Monitor.Name < items[j].Monitor.Name
	})

	WriteJSON(c, 200, gin.H{"Services": items})
}

func float32Array(input *[30]float32) []float32 {
	if input == nil {
		return []float32{}
	}
	output := make([]float32, 0, len(input))
	for _, value := range input {
		output = append(output, value)
	}
	return output
}

func intArray(input *[30]int) []int {
	if input == nil {
		return []int{}
	}
	output := make([]int, 0, len(input))
	for _, value := range input {
		output = append(output, value)
	}
	return output
}
