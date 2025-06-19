package controller

import (
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xos/serverstatus/db"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/mygin"
	"github.com/xos/serverstatus/service/singleton"
)

type apiV1 struct {
	r gin.IRouter
}

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
	if tag != "" {
		c.JSON(200, singleton.ServerAPI.GetListByTag(tag))
		return
	}
	c.JSON(200, singleton.ServerAPI.GetAllList())
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
	if tag != "" {
		c.JSON(200, singleton.ServerAPI.GetStatusByTag(tag))
		return
	}
	if len(idList) != 0 {
		c.JSON(200, singleton.ServerAPI.GetStatusByIDList(idList))
		return
	}
	c.JSON(200, singleton.ServerAPI.GetAllStatus())
}

// RegisterServer adds a server and responds with the full ServerRegisterResponse
// header: Authorization: Token
// body: RegisterServer
// response: ServerRegisterResponse or Secret string
func (v *apiV1) RegisterServer(c *gin.Context) {
	var rs singleton.RegisterServer
	// Attempt to bind JSON to RegisterServer struct
	if err := c.ShouldBindJSON(&rs); err != nil {
		c.JSON(400, singleton.ServerRegisterResponse{
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
		c.JSON(response.Code, response.Secret)
	} else {
		c.JSON(response.Code, response)
	}
}

func (v *apiV1) monitorHistoriesById(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.AbortWithStatusJSON(400, gin.H{"code": 400, "message": "id参数错误"})
		return
	}
	server, ok := singleton.ServerList[id]
	if !ok {
		c.AbortWithStatusJSON(404, gin.H{
			"code":    404,
			"message": "id不存在",
		})
		return
	}

	_, isMember := c.Get(model.CtxKeyAuthorizedUser)
	_, isViewPasswordVerfied := c.Get(model.CtxKeyViewPasswordVerified)
	authorized := isMember || isViewPasswordVerfied

	if server.HideForGuest && !authorized {
		c.AbortWithStatusJSON(403, gin.H{"code": 403, "message": "需要认证"})
		return
	}

	// 性能优化：根据数据库类型选择不同的查询方式，限制数据量
	if singleton.Conf.DatabaseType == "badger" {
		// BadgerDB 模式下使用 MonitorAPI，只查询最近7天的ICMP/TCP监控数据
		if singleton.MonitorAPI != nil {
			// 恢复3天数据展示，通过优化查询效率解决性能问题
			endTime := time.Now()
			startTime := endTime.AddDate(0, 0, -3) // 恢复3天数据

			if db.DB != nil {
				// 性能优化：直接按服务器ID和时间范围查询，避免查询所有记录
				monitorOps := db.NewMonitorHistoryOps(db.DB)

				// 预先获取监控配置
				monitors := singleton.ServiceSentinelShared.Monitors()
				monitorTypeMap := make(map[uint64]uint8)
				icmpTcpMonitorIDs := []uint64{}
				if monitors != nil {
					for _, monitor := range monitors {
						monitorTypeMap[monitor.ID] = monitor.Type
						if monitor.Type == model.TaskTypeICMPPing || monitor.Type == model.TaskTypeTCPPing {
							icmpTcpMonitorIDs = append(icmpTcpMonitorIDs, monitor.ID)
						}
					}
				}

				log.Printf("服务器 %d 的ICMP/TCP监控器ID列表: %v", server.ID, icmpTcpMonitorIDs)

				// 根本性能优化：使用高效的数据库查询，恢复合理的数据量
				var networkHistories []*model.MonitorHistory

				for _, monitorID := range icmpTcpMonitorIDs {
					// 使用优化的查询方法，每个监控器200条记录（3天数据）
					histories, err := monitorOps.GetMonitorHistoriesByServerAndMonitorOptimized(server.ID, monitorID, startTime, endTime, 200)
					if err != nil {
						log.Printf("查询监控器 %d 的历史记录失败: %v", monitorID, err)
						continue
					}

					log.Printf("监控器 %d 在服务器 %d 上找到 %d 条记录", monitorID, server.ID, len(histories))
					networkHistories = append(networkHistories, histories...)
				}

				log.Printf("服务器 %d 最终返回 %d 条ICMP/TCP监控记录", server.ID, len(networkHistories))
				c.JSON(200, networkHistories)
			} else {
				c.JSON(200, []any{})
			}
		} else {
			c.JSON(200, []any{})
		}
	} else {
		// SQLite 模式下直接查询ICMP/TCP监控数据，限制时间范围和数量
		if singleton.DB != nil {
			var networkHistories []*model.MonitorHistory

			// 性能优化：只查询最近7天的数据
			startTime := time.Now().AddDate(0, 0, -7)

			err := singleton.DB.Where("server_id = ? AND created_at > ? AND monitor_id IN (SELECT id FROM monitors WHERE type IN (?, ?))",
				server.ID, startTime, model.TaskTypeICMPPing, model.TaskTypeTCPPing).
				Order("created_at DESC").
				Limit(500). // 减少到500条记录
				Find(&networkHistories).Error

			if err != nil {
				log.Printf("查询网络监控历史记录失败: %v", err)
				c.JSON(200, []any{})
			} else {
				c.JSON(200, networkHistories)
			}
		} else {
			c.JSON(200, []any{})
		}
	}
}

func (v *apiV1) monitorConfigs(c *gin.Context) {
	// 获取监控配置列表
	if singleton.ServiceSentinelShared != nil {
		monitors := singleton.ServiceSentinelShared.Monitors()
		log.Printf("API返回监控配置数量: %d", len(monitors))
		if len(monitors) > 0 {
			for _, monitor := range monitors {
				log.Printf("监控配置: ID=%d, Name=%s, Type=%d", monitor.ID, monitor.Name, monitor.Type)
			}
		}
		c.JSON(200, monitors)
	} else {
		log.Printf("ServiceSentinelShared为nil，返回空数组")
		c.JSON(200, []interface{}{})
	}
}
