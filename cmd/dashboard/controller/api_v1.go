package controller

import (
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
	monitorCache   = map[uint64]struct {
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
)

func (v *apiV1) serve() {
	r := v.r.Group("")
	// 强制认证的 API
	r.Use(mygin.Authorize(mygin.AuthorizeOption{
		MemberOnly: true,
		AllowAPI:   true,
		IsPage:     false,
		Msg:        "访问此接口需要认证",
		Btn:        "点此登录",
		Redirect:   "/login",
	}))
	r.GET("/server/list", v.serverList)
	r.GET("/server/details", v.serverDetails)
	r.POST("/server/register", v.RegisterServer)
	// 不强制认证的 API
	mr := v.r.Group("monitor")
	mr.Use(mygin.Authorize(mygin.AuthorizeOption{
		MemberOnly: false,
		IsPage:     false,
		AllowAPI:   true,
		Msg:        "访问此接口需要认证",
		Btn:        "点此登录",
		Redirect:   "/login",
	}))
	mr.Use(mygin.ValidateViewPassword(mygin.ValidateViewPasswordOption{
		IsPage:        false,
		AbortWhenFail: true,
	}))
	mr.GET("/:id", v.monitorHistoriesById)
	mr.GET("/configs", v.monitorConfigs)
}

// serverList 获取服务器列表 不传入Query参数则获取全部
// header: Authorization: Token
// query: tag (服务器分组)
func (v *apiV1) serverList(c *gin.Context) {
	tag := c.Query("tag")
	cacheKey := "serverList:tag=" + tag
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
		res = singleton.ServerAPI.GetListByTag(tag)
	} else {
		res = singleton.ServerAPI.GetAllList()
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
			monitorCacheMu.Lock()
			if ce, ok := monitorCache[server.ID]; ok {
				if time.Since(ce.ts) <= 500*time.Millisecond && ce.dur == duration {
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

				// 并发查询所有ICMP/TCP监控器
				for _, monitor := range monitors {
					if monitor.Type == model.TaskTypeICMPPing || monitor.Type == model.TaskTypeTCPPing {
						activeQueries++
						go func(monitorID uint64) {
							sem <- struct{}{}
							defer func() { <-sem }()
							// 使用反向限量，优先拿最近的数据，最多取 6000 条/监控器，足够覆盖 72h
							allHistories, err := monitorOps.GetMonitorHistoriesByServerAndMonitorRangeReverseLimit(
								server.ID, monitorID, startTime, endTime, 6000,
							)

							resultChan <- monitorResult{
								histories: allHistories,
								err:       err,
							}
						}(monitor.ID)
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

			// 下采样：当点数超过 5000 时，按等间隔抽取，减少前端渲染负荷
			const maxPoints = 5000
			if len(networkHistories) > maxPoints {
				step := float64(len(networkHistories)) / float64(maxPoints)
				sampled := make([]*model.MonitorHistory, 0, maxPoints)
				for i := 0; i < maxPoints; i++ {
					idx := int(float64(i) * step)
					if idx >= len(networkHistories) {
						idx = len(networkHistories) - 1
					}
					sampled = append(sampled, networkHistories[idx])
				}
				networkHistories = sampled
			}

			// 预编码 JSON，减少后续重复编码
			payload, err := utils.EncodeJSON(networkHistories)
			if err != nil {
				// 回退到空数组
				payload, _ = utils.EncodeJSON([]any{})
			}

			// 写入短时缓存
			monitorCacheMu.Lock()
			monitorCache[server.ID] = struct {
				ts   time.Time
				data []byte
				dur  time.Duration
			}{ts: time.Now(), data: payload, dur: duration}
			monitorCacheMu.Unlock()

			log.Printf("API /monitor/%d 返回 %d 条记录（范围: %v，所有监控器）", server.ID, len(networkHistories), duration)
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
			startTime := time.Now().Add(-duration)

			err := singleton.DB.Where("server_id = ? AND created_at > ? AND monitor_id IN (SELECT id FROM monitors WHERE type IN (?, ?))",
				server.ID, startTime, model.TaskTypeICMPPing, model.TaskTypeTCPPing).
				Order("created_at DESC").
				Find(&networkHistories).Error

			var payload []byte
			if err != nil {
				payload, _ = utils.EncodeJSON([]any{})
			} else {
				// 与 Badger 分支一致：预编码 + gzip 按需
				payload, _ = utils.EncodeJSON(networkHistories)
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

func (v *apiV1) monitorConfigs(c *gin.Context) {
	// 获取监控配置列表
	if singleton.ServiceSentinelShared != nil {
		monitors := singleton.ServiceSentinelShared.Monitors()
		WriteJSON(c, 200, monitors)
	} else {
		WriteJSON(c, 200, []interface{}{})
	}
}
