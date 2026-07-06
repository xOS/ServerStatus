package controller

import (
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"github.com/hashicorp/go-uuid"
	"golang.org/x/time/rate"

	"github.com/xos/serverstatus/db"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/mygin"
	"github.com/xos/serverstatus/pkg/utils"
	"github.com/xos/serverstatus/proto"
	"github.com/xos/serverstatus/service/rpc"
	"github.com/xos/serverstatus/service/singleton"
)

const (
	apiV1Group    = "v1"
	adminAPIGroup = apiV1Group + "/admin"

	apiV1Prefix       = "/api/v1"
	authAPIPrefix     = apiV1Prefix + "/auth"
	oauthLoginPath    = authAPIPrefix + "/oauth2/login"
	oauthCallbackPath = authAPIPrefix + "/oauth2/callback"
)

type rateEntry struct {
	limiter *rate.Limiter
	seen    time.Time
}

var (
	requestLimitersMu sync.Mutex
	requestLimiters   = make(map[string]*rateEntry)
)

type authAPI struct {
	r gin.IRouter
}

func (a *authAPI) serve() {
	guest := a.r.Group("")
	guest.Use(mygin.Authorize(mygin.AuthorizeOption{
		GuestOnly: true,
		IsPage:    true,
		Msg:       "您已登录",
		Btn:       "返回首页",
		Redirect:  "/",
	}))
	oauth := &oauth2controller{r: guest}
	oauth.serve()

	optional := a.r.Group("")
	optional.Use(mygin.Authorize(mygin.AuthorizeOption{}))
	optional.GET("/logout", a.logout)
	optional.POST("/logout", a.logout)
}

func (a *authAPI) logout(c *gin.Context) {
	if rawUser, ok := c.Get(model.CtxKeyAuthorizedUser); ok {
		if user, ok := rawUser.(*model.User); ok {
			expireUserLoginToken(user)
		}
	}

	clearSecureCookie(c, singleton.Conf.Site.CookieName)
	clearSecureCookie(c, singleton.Conf.Site.CookieName+"-sk")

	if c.Request.Method == http.MethodGet {
		if oidcLogoutURL := singleton.Conf.Oauth2.OidcLogoutURL; oidcLogoutURL != "" {
			c.Redirect(http.StatusFound, oidcLogoutURL)
			return
		}
		c.Redirect(http.StatusFound, "/login")
		return
	}

	WriteJSON(c, http.StatusOK, model.Response{
		Code:    http.StatusOK,
		Message: "success",
	})
}

func expireUserLoginToken(user *model.User) {
	if user == nil || user.ID == 0 {
		return
	}

	user.Token = ""
	user.TokenExpired = time.Now()
	if singleton.Conf.DatabaseType == "badger" {
		if db.DB == nil {
			return
		}
		if err := db.DB.SaveModel("user", user.ID, user); err != nil {
			log.Printf("更新用户登出状态到BadgerDB失败: %v", err)
		}
		return
	}

	if singleton.DB != nil {
		singleton.DB.Model(user).UpdateColumns(model.User{
			Token:        "",
			TokenExpired: time.Now(),
		})
	}
}

// WriteJSON encodes v to JSON once and writes with proper headers, using gzip when accepted.
func WriteJSON(c *gin.Context, status int, v interface{}) {
	payload, err := utils.EncodeJSON(v)
	if err != nil {
		// best-effort empty array fallback
		payload, _ = utils.EncodeJSON([]any{})
	}
	WriteJSONPayload(c, status, payload)
}

// WriteJSONPayload writes a pre-encoded JSON payload with gzip when client accepts it.
func WriteJSONPayload(c *gin.Context, status int, payload []byte) {
	c.Status(status)
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	if _, gz, _ := utils.GzipIfAccepted(c.Writer, c.Request, payload); !gz {
		_, _ = c.Writer.Write(payload)
	}
}

func serveSPA(c *gin.Context) {
	const indexPath = "frontend/dist/index.html"
	if _, err := os.Stat(indexPath); err == nil {
		c.File(indexPath)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>ServerStatus</title></head><body><div id="app">新版前端尚未构建，请在 frontend 目录执行 npm run build。</div></body></html>`))
}

// handleBrokenPipe 中间件处理broken pipe错误
func handleBrokenPipe(c *gin.Context) {
	defer func() {
		if err := recover(); err != nil {
			// 检查是否为broken pipe错误
			if errStr := fmt.Sprintf("%v", err); strings.Contains(errStr, "broken pipe") ||
				strings.Contains(errStr, "connection reset") ||
				strings.Contains(errStr, "use of closed network connection") {
				// 静默处理broken pipe错误，不记录日志
				c.Abort()
				return
			}
			// 其他错误正常处理
			log.Printf("HTTP处理错误: %v", err)
			c.Abort()
		}
	}()
	c.Next()
}

func securityHeaders(c *gin.Context) {
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Frame-Options", "SAMEORIGIN")
	c.Header("Referrer-Policy", "same-origin")
	c.Header("Cross-Origin-Resource-Policy", "same-origin")
	c.Next()
}

func requestRateLimiter() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := clientIP(c) + ":" + limiterClass(c.Request)
		limit, burst := limiterSpec(c.Request)
		limiter := getRequestLimiter(key, limit, burst)
		if !limiter.Allow() {
			WriteJSON(c, http.StatusTooManyRequests, gin.H{
				"code":    http.StatusTooManyRequests,
				"message": "请求过于频繁，请稍后再试",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

func limiterClass(r *http.Request) string {
	path := r.URL.Path
	if strings.HasPrefix(path, authAPIPrefix) || path == "/login" || path == "/debug-login" {
		return "auth"
	}
	if path == apiV1Prefix+"/ws" {
		return "ws"
	}
	if r.Method != http.MethodGet && strings.HasPrefix(path, apiV1Prefix) {
		return "write"
	}
	if strings.HasPrefix(path, apiV1Prefix+"/admin") {
		return "admin"
	}
	if strings.HasPrefix(path, apiV1Prefix) {
		return "api"
	}
	return "page"
}

func limiterSpec(r *http.Request) (rate.Limit, int) {
	switch limiterClass(r) {
	case "auth":
		return rate.Every(6 * time.Second), 10
	case "ws":
		return rate.Every(3 * time.Second), 8
	case "write":
		return rate.Every(1 * time.Second), 30
	case "admin":
		return rate.Every(300 * time.Millisecond), 80
	case "api":
		return rate.Every(100 * time.Millisecond), 180
	default:
		return rate.Every(50 * time.Millisecond), 240
	}
}

func getRequestLimiter(key string, limit rate.Limit, burst int) *rate.Limiter {
	now := time.Now()
	requestLimitersMu.Lock()
	defer requestLimitersMu.Unlock()

	entry := requestLimiters[key]
	if entry == nil {
		entry = &rateEntry{limiter: rate.NewLimiter(limit, burst)}
		requestLimiters[key] = entry
	}
	entry.seen = now

	if len(requestLimiters) > 4096 {
		cutoff := now.Add(-10 * time.Minute)
		for cacheKey, cacheEntry := range requestLimiters {
			if cacheEntry.seen.Before(cutoff) {
				delete(requestLimiters, cacheKey)
			}
		}
	}

	return entry.limiter
}

func clientIP(c *gin.Context) string {
	ip := c.ClientIP()
	if ip == "" {
		ip, _, _ = net.SplitHostPort(c.Request.RemoteAddr)
	}
	if ip == "" {
		return "unknown"
	}
	return ip
}

func corsMiddleware(c *gin.Context) {
	origin := c.GetHeader("Origin")
	if origin == "" {
		c.Next()
		return
	}

	if isAllowedOrigin(c, origin) {
		c.Header("Vary", "Origin")
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
		c.Header("Access-Control-Max-Age", "86400")
	}

	if c.Request.Method == http.MethodOptions {
		if !isAllowedOrigin(c, origin) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.AbortWithStatus(http.StatusNoContent)
		return
	}

	c.Next()
}

func isAllowedOrigin(c *gin.Context, origin string) bool {
	originURL, err := url.Parse(origin)
	if err != nil || originURL.Scheme == "" || originURL.Host == "" {
		return false
	}
	if sameRequestHost(c.Request, originURL) {
		return true
	}

	for _, allowed := range strings.Split(singleton.Conf.Security.AllowedOrigins, ",") {
		allowed = strings.TrimSpace(strings.TrimRight(allowed, "/"))
		if allowed == "" {
			continue
		}
		if allowed == "*" {
			return singleton.Conf.Debug
		}
		if strings.EqualFold(allowed, strings.TrimRight(origin, "/")) {
			return true
		}
	}
	return false
}

func sameRequestHost(r *http.Request, originURL *url.URL) bool {
	if !strings.EqualFold(originURL.Host, r.Host) {
		return false
	}
	proto := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		proto = "https"
	}
	return strings.EqualFold(originURL.Scheme, proto)
}

func isAllowedWebSocketOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	originURL, err := url.Parse(origin)
	if err != nil || originURL.Scheme == "" || originURL.Host == "" {
		return false
	}
	if sameRequestHost(r, originURL) {
		return true
	}
	for _, allowed := range strings.Split(singleton.Conf.Security.AllowedOrigins, ",") {
		allowed = strings.TrimSpace(strings.TrimRight(allowed, "/"))
		if allowed == "" {
			continue
		}
		if allowed == "*" {
			return singleton.Conf.Debug
		}
		if strings.EqualFold(allowed, strings.TrimRight(origin, "/")) {
			return true
		}
	}
	return false
}

func setSecureCookie(c *gin.Context, name, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   requestIsHTTPS(c.Request),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSecureCookie(c *gin.Context, name string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   requestIsHTTPS(c.Request),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "",
		MaxAge:   -1,
		Secure:   requestIsHTTPS(c.Request),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func requestIsHTTPS(r *http.Request) bool {
	return singleton.Conf.TLS || r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// pprofAuthMiddleware pprof 认证中间件
// 与API接口采用相同的授权逻辑：支持Cookie和API Token认证，不要求管理员权限
func pprofAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 从上下文获取用户信息（由 mygin.Authorize 设置）
		user, exists := c.Get(model.CtxKeyAuthorizedUser)

		if !exists || user == nil {
			WriteJSON(c, http.StatusForbidden, gin.H{
				"error": "需要登录才能访问性能分析工具",
				"code":  403,
			})
			c.Abort()
			return
		}

		// 与API接口相同：只要通过认证即可访问，不要求管理员权限
		// 这样Cookie登录用户和API Token用户都能访问
		c.Next()
	}
}

func ServeWeb(port uint) *http.Server {
	gin.SetMode(gin.ReleaseMode)

	// 创建自定义的Gin引擎，过滤网络连接错误
	r := gin.New()
	_ = r.SetTrustedProxies(nil)

	// 全局并发限制器：限制同时在处理的请求数量，避免低配机被打满
	// 容量可通过环境变量 NG_MAX_INFLIGHT 配置，默认 64
	inflightCap := 64
	if v := os.Getenv("NG_MAX_INFLIGHT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			inflightCap = n
		}
	}
	inflight := make(chan struct{}, inflightCap)
	r.Use(func(c *gin.Context) {
		select {
		case inflight <- struct{}{}:
			defer func() { <-inflight }()
			c.Next()
		default:
			// 瞬时过载时快速失败，保护进程
			c.AbortWithStatus(http.StatusServiceUnavailable)
		}
	})

	// 添加自定义的日志中间件，过滤broken pipe等网络错误
	r.Use(gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		// 过滤broken pipe和connection reset错误
		if param.ErrorMessage != "" {
			errMsg := strings.ToLower(param.ErrorMessage)
			if strings.Contains(errMsg, "broken pipe") ||
				strings.Contains(errMsg, "connection reset") ||
				strings.Contains(errMsg, "use of closed network connection") ||
				strings.Contains(errMsg, "connection refused") {
				// 不记录这些正常的网络连接错误
				return ""
			}
		}

		// 正常的日志格式
		return fmt.Sprintf("[GIN] %v | %3d | %13v | %15s | %-7s %#v\n%s",
			param.TimeStamp.Format("2006/01/02 - 15:04:05"),
			param.StatusCode,
			param.Latency,
			param.ClientIP,
			param.Method,
			param.Path,
			param.ErrorMessage,
		)
	}))

	// 添加Recovery中间件，过滤网络连接错误
	r.Use(gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		if err, ok := recovered.(string); ok {
			errLower := strings.ToLower(err)
			if strings.Contains(errLower, "broken pipe") ||
				strings.Contains(errLower, "connection reset") ||
				strings.Contains(errLower, "use of closed network connection") {
				// 静默处理网络连接错误
				c.AbortWithStatus(http.StatusInternalServerError)
				return
			}
		}
		// 其他错误正常处理
		c.AbortWithStatus(http.StatusInternalServerError)
	}))

	if singleton.Conf.Debug {
		gin.SetMode(gin.DebugMode)
		// 为 pprof 添加认证保护，与API接口采用相同的授权模式
		pprofGroup := r.Group("/debug/pprof")
		pprofGroup.Use(mygin.Authorize(mygin.AuthorizeOption{
			MemberOnly: true,
			IsPage:     false,
			Msg:        "访问性能分析工具需要登录",
			Btn:        "点此登录",
			Redirect:   "/login",
		}))
		pprofGroup.Use(pprofAuthMiddleware())
		pprof.RouteRegister(pprofGroup, "")
	}

	// 首先添加全局panic恢复中间件
	r.Use(globalPanicRecovery())
	r.Use(natGateway)
	r.Use(securityHeaders)
	r.Use(requestRateLimiter())
	r.Use(handleBrokenPipe) // 添加broken pipe错误处理中间件
	r.Use(corsMiddleware)   // 添加CORS中间件处理OPTIONS请求
	// 全局请求体大小限制（默认2MiB，可通过环境变量 NG_MAX_BODY_BYTES 调整）
	maxBodyBytes := int64(2 << 20)
	if v := os.Getenv("NG_MAX_BODY_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			maxBodyBytes = n
		}
	}
	r.Use(func(c *gin.Context) {
		if c.Request != nil && c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
		}
		c.Next()
	})
	r.Use(mygin.RecordPath)
	// 直接用本地静态资源目录
	r.Static("/static", "resource/static")
	if _, err := os.Stat("frontend/dist/assets"); err == nil {
		r.Static("/assets", "frontend/dist/assets")
	}
	if _, err := os.Stat("frontend/dist/favicon.svg"); err == nil {
		r.StaticFile("/favicon.svg", "frontend/dist/favicon.svg")
	}

	routers(r)
	page404 := func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, apiV1Prefix) {
			WriteJSON(c, http.StatusNotFound, gin.H{
				"code":    http.StatusNotFound,
				"message": "not found",
			})
			return
		}
		if shouldServeSPA(c) {
			serveSPA(c)
			return
		}
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
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1MiB 头大小上限，避免过大头占用内存
		Handler:           r,
	}
	return srv
}

func shouldServeSPA(c *gin.Context) bool {
	if c.Request.Method != http.MethodGet {
		return false
	}
	path := c.Request.URL.Path
	if strings.HasPrefix(path, "/api") ||
		strings.HasPrefix(path, "/ws") ||
		strings.HasPrefix(path, "/static") ||
		strings.HasPrefix(path, "/assets") ||
		strings.HasPrefix(path, "/oauth2") {
		return false
	}
	return !strings.Contains(path, ".")
}

func routers(r *gin.Engine) {
	// 通用页面
	cp := commonPage{r: r}
	cp.serve()
	// 游客页面
	gp := guestPage{r}
	gp.serve()
	// 认证 API
	auth := &authAPI{r.Group(authAPIPrefix)}
	auth.serve()
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

// 全局panic恢复中间件
func globalPanicRecovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		// 记录详细的panic信息
		log.Printf("🚨 HTTP请求发生PANIC: %v", recovered)
		log.Printf("🚨 请求路径: %s %s", c.Request.Method, c.Request.URL.Path)
		log.Printf("🚨 客户端IP: %s", c.ClientIP())

		// 打印堆栈信息
		if gin.IsDebugging() {
			debug.PrintStack()
		}

		// 确保响应头没有被写入
		if !c.Writer.Written() {
			WriteJSON(c, http.StatusInternalServerError, gin.H{
				"error": "服务器内部错误，请稍后重试",
				"code":  500,
			})
		}
		c.Abort()
	})
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

// updateCycleStatsInfo 安全地更新周期统计信息，避免死锁
func updateCycleStatsInfo(cycleID uint64, from, to time.Time, max uint64, name string) {
	singleton.AlertsLock.Lock()
	defer singleton.AlertsLock.Unlock()
	if store, ok := singleton.AlertsCycleTransferStatsStore[cycleID]; ok && store != nil {
		store.From = from
		store.To = to
		store.Max = max
		store.Name = name
	}
}

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

		// 在读锁外部异步更新周期信息，避免死锁
		go updateCycleStatsInfo(cycleID, from, to, uint64(flowRule.Max), alert.Name)

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

	// 补充机制：为没有被警报规则覆盖的服务器创建默认流量数据（10TB月配额）
	// 获取所有已被警报规则覆盖的服务器ID
	coveredServerIDs := make(map[uint64]bool)
	for _, stats := range statsStore {
		for serverID := range stats.Transfer {
			coveredServerIDs[serverID] = true
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
