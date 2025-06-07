package controller

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jinzhu/copier"
	"golang.org/x/net/idna"
	"gorm.io/gorm"

	"github.com/xos/serverstatus/db"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/mygin"
	"github.com/xos/serverstatus/pkg/utils"
	"github.com/xos/serverstatus/proto"
	"github.com/xos/serverstatus/service/singleton"
)

type memberAPI struct {
	r gin.IRouter
}

func (ma *memberAPI) serve() {
	// 需要登录的 API
	mr := ma.r.Group("")
	mr.Use(mygin.Authorize(mygin.AuthorizeOption{
		MemberOnly: true,
		IsPage:     false,
		Msg:        "访问此接口需要登录",
		Btn:        "点此登录",
		Redirect:   "/login",
	}))

	// 搜索 API 现在需要登录
	mr.GET("/search-server", ma.searchServer)
	mr.GET("/search-tasks", ma.searchTask)
	mr.GET("/search-ddns", ma.searchDDNS)
	mr.POST("/server", ma.addOrEditServer)
	mr.POST("/monitor", ma.addOrEditMonitor)
	mr.POST("/traffic", ma.addOrEditAlertRule)
	mr.POST("/cron", ma.addOrEditCron)
	mr.GET("/cron/:id/manual", ma.manualTrigger)
	mr.POST("/force-update", ma.forceUpdate)
	mr.POST("/batch-update-server-group", ma.batchUpdateServerGroup)
	mr.POST("/batch-delete-server", ma.batchDeleteServer)
	mr.POST("/notification", ma.addOrEditNotification)
	mr.POST("/ddns", ma.addOrEditDDNS)
	mr.POST("/nat", ma.addOrEditNAT)
	mr.POST("/alert-rule", ma.addOrEditAlertRule)
	mr.GET("/alert-rule/:id", ma.getAlertRule)
	mr.POST("/setting", ma.updateSetting)
	mr.DELETE("/:model/:id", ma.delete)
	mr.POST("/logout", ma.logout)
	mr.GET("/token", ma.getToken)
	mr.POST("/token", ma.issueNewToken)
	mr.DELETE("/token/:token", ma.deleteToken)

	// API
	v1 := ma.r.Group("v1")
	{
		apiv1 := &apiV1{v1}
		apiv1.serve()
	}
}

type apiResult struct {
	Token string `json:"token"`
	Note  string `json:"note"`
}

// getToken 获取 Token
func (ma *memberAPI) getToken(c *gin.Context) {
	u := c.MustGet(model.CtxKeyAuthorizedUser).(*model.User)
	singleton.ApiLock.RLock()
	defer singleton.ApiLock.RUnlock()

	tokenList := singleton.UserIDToApiTokenList[u.ID]
	res := make([]*apiResult, len(tokenList))
	for i, token := range tokenList {
		res[i] = &apiResult{
			Token: token,
			Note:  singleton.ApiTokenList[token].Note,
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "success",
		"result":  res,
	})
}

type TokenForm struct {
	Note string
}

// issueNewToken 生成新的 token
func (ma *memberAPI) issueNewToken(c *gin.Context) {
	u := c.MustGet(model.CtxKeyAuthorizedUser).(*model.User)
	tf := &TokenForm{}
	err := c.ShouldBindJSON(tf)
	if err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("请求错误：%s", err),
		})
		return
	}
	secureToken, err := utils.GenerateRandomString(32)
	if err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("请求错误：%s", err),
		})
		return
	}
	token := &model.ApiToken{
		UserID: u.ID,
		Token:  secureToken,
		Note:   tf.Note,
	}

	// BadgerDB 模式下使用 BadgerDB 操作
	if singleton.Conf.DatabaseType == "badger" {
		// 为新API令牌生成ID
		token.ID = uint64(time.Now().UnixNano())
		// 使用ApiTokenOps保存
		apiTokenOps := db.NewApiTokenOps(db.DB)
		err = apiTokenOps.SaveApiToken(token)
		if err != nil {
			c.JSON(http.StatusOK, model.Response{
				Code:    http.StatusBadRequest,
				Message: fmt.Sprintf("保存API令牌失败：%s", err),
			})
			return
		}
		log.Printf("API Token已保存到BadgerDB: %s (Note: %s)", token.Token, token.Note)
	} else if singleton.DB != nil {
		err = singleton.DB.Create(token).Error
		if err != nil {
			c.JSON(http.StatusOK, model.Response{
				Code:    http.StatusBadRequest,
				Message: fmt.Sprintf("保存API令牌失败：%s", err),
			})
			return
		}
	} else {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: "数据库未初始化",
		})
		return
	}

	singleton.ApiLock.Lock()
	singleton.ApiTokenList[token.Token] = token
	singleton.UserIDToApiTokenList[u.ID] = append(singleton.UserIDToApiTokenList[u.ID], token.Token)
	singleton.ApiLock.Unlock()

	c.JSON(http.StatusOK, model.Response{
		Code:    http.StatusOK,
		Message: "success",
		Result: map[string]string{
			"token": token.Token,
			"note":  token.Note,
		},
	})
}

// deleteToken 删除 token
func (ma *memberAPI) deleteToken(c *gin.Context) {
	token := c.Param("token")
	if token == "" {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: "token 不能为空",
		})
		return
	}
	singleton.ApiLock.Lock()
	defer singleton.ApiLock.Unlock()
	if _, ok := singleton.ApiTokenList[token]; !ok {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: "token 不存在",
		})
		return
	}
	// 获取Token对象用于后续清理
	tokenObj := singleton.ApiTokenList[token]

	// 在数据库中删除该Token
	var err error
	if singleton.Conf.DatabaseType == "badger" {
		// 在BadgerDB模式下，需要删除所有匹配的Token记录（解决重复记录问题）
		if tokenObj != nil && db.DB != nil {
			apiTokenOps := db.NewApiTokenOps(db.DB)

			// 先获取所有API Token，找到所有匹配的记录
			allTokens, getAllErr := apiTokenOps.GetAllApiTokens()
			if getAllErr != nil {
				log.Printf("获取所有API令牌失败: %v", getAllErr)
				err = getAllErr
			} else {
				// 删除所有匹配的Token记录
				deletedCount := 0
				for _, t := range allTokens {
					if t.Token == token {
						if deleteErr := apiTokenOps.DeleteApiToken(t.ID); deleteErr != nil {
							log.Printf("删除API令牌记录失败 (ID: %d): %v", t.ID, deleteErr)
						} else {
							deletedCount++
							log.Printf("BadgerDB: 成功删除API令牌记录 %s (ID: %d, Note: %s)", token, t.ID, t.Note)
						}
					}
				}
				if deletedCount > 1 {
					log.Printf("BadgerDB: 发现并删除了 %d 个重复的API令牌记录: %s", deletedCount, token)
				}
				if deletedCount == 0 {
					err = fmt.Errorf("未找到要删除的API令牌记录")
				}
			}
		}
	} else if singleton.DB != nil {
		err = singleton.DB.Unscoped().Delete(&model.ApiToken{}, "token = ?", token).Error
		if err != nil {
			c.JSON(http.StatusOK, model.Response{
				Code:    http.StatusBadRequest,
				Message: fmt.Sprintf("删除API令牌失败：%s", err),
			})
			return
		}
		log.Printf("SQLite: 成功删除API令牌 %s", token)
	}

	// 无论数据库删除是否成功，都要清理内存，避免重新保存
	// 从UserIDToApiTokenList中删除该Token
	if tokenObj != nil {
		userTokens := singleton.UserIDToApiTokenList[tokenObj.UserID]
		for i, userToken := range userTokens {
			if userToken == token {
				// 从切片中删除该token
				singleton.UserIDToApiTokenList[tokenObj.UserID] = append(userTokens[:i], userTokens[i+1:]...)
				break
			}
		}
		// 如果用户没有其他token，删除整个条目
		if len(singleton.UserIDToApiTokenList[tokenObj.UserID]) == 0 {
			delete(singleton.UserIDToApiTokenList, tokenObj.UserID)
		}
	}

	// 在ApiTokenList中删除该Token
	delete(singleton.ApiTokenList, token)

	log.Printf("已从内存中清理API令牌: %s (数据库删除结果: %v)", token, err)

	// 如果数据库删除失败，记录错误但不影响响应
	if err != nil {
		log.Printf("警告: 数据库删除API令牌失败，但已从内存中清理: %v", err)
	}

	c.JSON(http.StatusOK, model.Response{
		Code:    http.StatusOK,
		Message: "success",
	})
}

func (ma *memberAPI) delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id < 1 {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: "错误的 Server ID",
		})
		return
	}

	var err error
	switch c.Param("model") {
	case "server":
		// BadgerDB 模式下使用 BadgerDB 操作
		if singleton.Conf.DatabaseType == "badger" {
			err = db.DB.DeleteModel("server", id)
			// BadgerDB 模式下暂不支持复杂的监控历史删除
		} else if singleton.DB != nil {
			err = singleton.DB.Transaction(func(tx *gorm.DB) error {
				err = singleton.DB.Unscoped().Delete(&model.Server{}, "id = ?", id).Error
				if err != nil {
					return err
				}
				err = singleton.DB.Unscoped().Delete(&model.MonitorHistory{}, "server_id = ?", id).Error
				if err != nil {
					return err
				}
				return nil
			})
		} else {
			err = errors.New("数据库未初始化")
		}
		if err == nil {
			// 删除服务器
			singleton.ServerLock.Lock()
			onServerDelete(id)
			singleton.ServerLock.Unlock()
			singleton.ReSortServer()
		}
	case "notification":
		// BadgerDB 模式下使用 BadgerDB 操作
		if singleton.Conf.DatabaseType == "badger" {
			err = db.DB.DeleteModel("notification", id)
		} else if singleton.DB != nil {
			err = singleton.DB.Unscoped().Delete(&model.Notification{}, "id = ?", id).Error
		} else {
			err = errors.New("数据库未初始化")
		}
		if err == nil {
			singleton.OnDeleteNotification(id)
		}
	case "ddns":
		// BadgerDB 模式下使用 BadgerDB 操作
		if singleton.Conf.DatabaseType == "badger" {
			err = db.DB.DeleteModel("ddns_profile", id)
		} else if singleton.DB != nil {
			err = singleton.DB.Unscoped().Delete(&model.DDNSProfile{}, "id = ?", id).Error
		} else {
			err = errors.New("数据库未初始化")
		}
		if err == nil {
			singleton.OnDDNSUpdate()
		}
	case "nat":
		// BadgerDB 模式下使用 BadgerDB 操作
		if singleton.Conf.DatabaseType == "badger" {
			err = db.DB.DeleteModel("nat", id)
		} else if singleton.DB != nil {
			err = singleton.DB.Unscoped().Delete(&model.NAT{}, "id = ?", id).Error
		} else {
			err = errors.New("数据库未初始化")
		}
		if err == nil {
			singleton.OnNATUpdate()
		}
	case "monitor":
		// BadgerDB 模式下使用 BadgerDB 操作
		if singleton.Conf.DatabaseType == "badger" {
			err = db.DB.DeleteModel("monitor", id)
			// BadgerDB 模式下暂不支持复杂的监控历史删除
		} else if singleton.DB != nil {
			err = singleton.DB.Unscoped().Delete(&model.Monitor{}, "id = ?", id).Error
			if err == nil {
				err = singleton.DB.Unscoped().Delete(&model.MonitorHistory{}, "monitor_id = ?", id).Error
			}
		} else {
			err = errors.New("数据库未初始化")
		}
		if err == nil {
			singleton.ServiceSentinelShared.OnMonitorDelete(id)
		}
	case "cron":
		// BadgerDB 模式下使用 BadgerDB 操作
		if singleton.Conf.DatabaseType == "badger" {
			err = db.DB.DeleteModel("cron", id)
		} else if singleton.DB != nil {
			err = singleton.DB.Unscoped().Delete(&model.Cron{}, "id = ?", id).Error
		} else {
			err = errors.New("数据库未初始化")
		}
		if err == nil {
			singleton.CronLock.RLock()
			defer singleton.CronLock.RUnlock()
			cr := singleton.Crons[id]
			if cr != nil && cr.CronJobID != 0 {
				singleton.Cron.Remove(cr.CronJobID)
			}
			delete(singleton.Crons, id)
		}
	case "alert-rule":
		// BadgerDB 模式下使用 BadgerDB 操作
		if singleton.Conf.DatabaseType == "badger" {
			err = db.DB.DeleteModel("alert_rule", id)
		} else if singleton.DB != nil {
			err = singleton.DB.Unscoped().Delete(&model.AlertRule{}, "id = ?", id).Error
		} else {
			err = errors.New("数据库未初始化")
		}
		if err == nil {
			singleton.OnDeleteAlert(id)
		}
	}
	if err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("数据库错误：%s", err),
		})
		return
	}
	c.JSON(http.StatusOK, model.Response{
		Code: http.StatusOK,
	})
}

type searchResult struct {
	Name  string `json:"name,omitempty"`
	Value uint64 `json:"value,omitempty"`
	Text  string `json:"text,omitempty"`
}

func (ma *memberAPI) searchServer(c *gin.Context) {
	var resp []searchResult
	word := c.Query("word")

	// 根据数据库类型选择不同的查询方式
	if singleton.Conf.DatabaseType == "badger" {
		// 使用BadgerDB和内存中的服务器列表
		singleton.ServerLock.RLock()
		defer singleton.ServerLock.RUnlock()

		for _, server := range singleton.ServerList {
			if server == nil {
				continue
			}

			// 搜索逻辑：ID匹配或名称/标签/备注包含关键词
			if word == "" ||
				fmt.Sprintf("%d", server.ID) == word ||
				strings.Contains(strings.ToLower(server.Name), strings.ToLower(word)) ||
				strings.Contains(strings.ToLower(server.Tag), strings.ToLower(word)) ||
				strings.Contains(strings.ToLower(server.Note), strings.ToLower(word)) {

				resp = append(resp, searchResult{
					Value: server.ID,
					Name:  server.Name,
					Text:  server.Name,
				})
			}
		}
	} else {
		// 使用SQLite
		var servers []model.Server
		likeWord := "%" + word + "%"
		singleton.DB.Select("id,name").Where("id = ? OR name LIKE ? OR tag LIKE ? OR note LIKE ?",
			word, likeWord, likeWord, likeWord).Find(&servers)

		for i := 0; i < len(servers); i++ {
			resp = append(resp, searchResult{
				Value: servers[i].ID,
				Name:  servers[i].Name,
				Text:  servers[i].Name,
			})
		}
	}

	c.JSON(http.StatusOK, map[string]interface{}{
		"success": true,
		"results": resp,
	})
}

func (ma *memberAPI) searchTask(c *gin.Context) {
	var resp []searchResult
	word := c.Query("word")

	// 根据数据库类型选择不同的查询方式
	if singleton.Conf.DatabaseType == "badger" {
		// 使用BadgerDB
		if db.DB != nil {
			cronOps := db.NewCronOps(db.DB)
			tasks, err := cronOps.GetAllCrons()
			if err != nil {
				log.Printf("searchTask: 查询任务失败: %v", err)
				c.JSON(http.StatusOK, map[string]interface{}{
					"success": true,
					"results": []searchResult{},
				})
				return
			}

			for _, task := range tasks {
				if task == nil {
					continue
				}

				// 搜索逻辑：ID匹配或名称包含关键词
				if word == "" ||
					fmt.Sprintf("%d", task.ID) == word ||
					strings.Contains(strings.ToLower(task.Name), strings.ToLower(word)) {

					resp = append(resp, searchResult{
						Value: task.ID,
						Name:  task.Name,
						Text:  task.Name,
					})
				}
			}
		}
	} else {
		// 使用SQLite
		var tasks []model.Cron
		likeWord := "%" + word + "%"
		singleton.DB.Select("id,name").Where("id = ? OR name LIKE ?",
			word, likeWord).Find(&tasks)

		for i := 0; i < len(tasks); i++ {
			resp = append(resp, searchResult{
				Value: tasks[i].ID,
				Name:  tasks[i].Name,
				Text:  tasks[i].Name,
			})
		}
	}

	c.JSON(http.StatusOK, map[string]interface{}{
		"success": true,
		"results": resp,
	})
}

func (ma *memberAPI) searchDDNS(c *gin.Context) {
	var resp []searchResult
	word := c.Query("word")

	// 根据数据库类型选择不同的查询方式
	if singleton.Conf.DatabaseType == "badger" {
		// 使用BadgerDB
		if db.DB != nil {
			ddnsOps := db.NewDDNSOps(db.DB)
			ddnsProfiles, err := ddnsOps.GetAllDDNSProfiles()
			if err != nil {
				log.Printf("searchDDNS: 查询DDNS配置失败: %v", err)
				c.JSON(http.StatusOK, map[string]interface{}{
					"success": true,
					"results": []searchResult{},
				})
				return
			}

			for _, ddns := range ddnsProfiles {
				if ddns == nil {
					continue
				}

				// 搜索逻辑：ID匹配或名称包含关键词
				if word == "" ||
					fmt.Sprintf("%d", ddns.ID) == word ||
					strings.Contains(strings.ToLower(ddns.Name), strings.ToLower(word)) {

					resp = append(resp, searchResult{
						Value: ddns.ID,
						Name:  ddns.Name,
						Text:  ddns.Name,
					})
				}
			}
		}
	} else {
		// 使用SQLite
		var ddns []model.DDNSProfile
		likeWord := "%" + word + "%"
		singleton.DB.Select("id,name").Where("id = ? OR name LIKE ?",
			word, likeWord).Find(&ddns)

		for i := 0; i < len(ddns); i++ {
			resp = append(resp, searchResult{
				Value: ddns[i].ID,
				Name:  ddns[i].Name,
				Text:  ddns[i].Name,
			})
		}
	}

	c.JSON(http.StatusOK, map[string]interface{}{
		"success": true,
		"results": resp,
	})
}

type serverForm struct {
	ID              uint64
	Name            string `binding:"required"`
	DisplayIndex    int
	Secret          string
	Tag             string
	Note            string
	PublicNote      string
	HideForGuest    string
	EnableDDNS      string
	DDNSProfilesRaw string
}

func (ma *memberAPI) addOrEditServer(c *gin.Context) {
	var sf serverForm
	var s model.Server
	var isEdit bool
	err := c.ShouldBindJSON(&sf)
	if err == nil {
		s.Name = sf.Name
		s.Secret = sf.Secret
		s.DisplayIndex = sf.DisplayIndex
		s.ID = sf.ID
		s.Tag = sf.Tag
		s.Note = sf.Note
		s.PublicNote = sf.PublicNote
		s.HideForGuest = sf.HideForGuest == "on"
		s.EnableDDNS = sf.EnableDDNS == "on"

		// 处理DDNSProfilesRaw，确保它是有效的JSON数组
		if sf.DDNSProfilesRaw == "" {
			sf.DDNSProfilesRaw = "[]"
		}
		s.DDNSProfilesRaw = sf.DDNSProfilesRaw

		// 尝试解析JSON，如果失败则设置为空数组
		err = utils.Json.Unmarshal([]byte(sf.DDNSProfilesRaw), &s.DDNSProfiles)
		if err != nil {
			log.Printf("解析DDNS配置失败（%s），重置为空数组: %v", sf.DDNSProfilesRaw, err)
			s.DDNSProfiles = []uint64{}
			s.DDNSProfilesRaw = "[]"
			err = nil
		}

		if err == nil {
			if s.ID == 0 {
				s.Secret, err = utils.GenerateRandomString(18)
				if err == nil {
					// BadgerDB 模式下使用 BadgerDB 操作
					if singleton.Conf.DatabaseType == "badger" {
						// 为新服务器生成ID
						if s.ID == 0 {
							s.ID = uint64(time.Now().UnixNano())
						}
						err = db.DB.SaveModel("server", s.ID, &s)
					} else if singleton.DB != nil {
						err = singleton.DB.Create(&s).Error
					} else {
						err = errors.New("数据库未初始化")
					}
				}
			} else {
				isEdit = true

				// 确保在编辑时不会丢失 Secret 字段
				if s.Secret == "" {
					singleton.ServerLock.RLock()
					if existingServer, exists := singleton.ServerList[s.ID]; exists {
						s.Secret = existingServer.Secret
						log.Printf("编辑服务器 %d 时保留现有Secret: %s", s.ID, s.Secret)
					}
					singleton.ServerLock.RUnlock()
				}

				// BadgerDB 模式下使用 BadgerDB 操作
				if singleton.Conf.DatabaseType == "badger" {
					// 使用ServerOps来确保Secret字段正确保存
					serverOps := db.NewServerOps(db.DB)
					err = serverOps.SaveServer(&s)
					if err == nil {
						log.Printf("服务器 %d 的Secret已保存到BadgerDB: %s", s.ID, s.Secret)
					}
				} else if singleton.DB != nil {
					err = singleton.DB.Save(&s).Error
				} else {
					err = errors.New("数据库未初始化")
				}
			}
		}
	}
	if err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("请求错误：%s", err),
		})
		return
	}
	if isEdit {
		singleton.ServerLock.Lock()
		s.CopyFromRunningServer(singleton.ServerList[s.ID])
		// 如果修改了 Secret
		if s.Secret != singleton.ServerList[s.ID].Secret {
			// 删除旧 Secret-ID 绑定关系
			singleton.SecretToID[s.Secret] = s.ID
			// 设置新的 Secret-ID 绑定关系
			delete(singleton.SecretToID, singleton.ServerList[s.ID].Secret)
		}
		// 如果修改了Tag
		oldTag := singleton.ServerList[s.ID].Tag
		newTag := s.Tag
		if newTag != oldTag {
			index := -1
			for i := 0; i < len(singleton.ServerTagToIDList[oldTag]); i++ {
				if singleton.ServerTagToIDList[oldTag][i] == s.ID {
					index = i
					break
				}
			}
			if index > -1 {
				// 删除旧 Tag-ID 绑定关系
				singleton.ServerTagToIDList[oldTag] = append(singleton.ServerTagToIDList[oldTag][:index], singleton.ServerTagToIDList[oldTag][index+1:]...)
				if len(singleton.ServerTagToIDList[oldTag]) == 0 {
					delete(singleton.ServerTagToIDList, oldTag)
				}
			}
			// 设置新的 Tag-ID 绑定关系
			singleton.ServerTagToIDList[newTag] = append(singleton.ServerTagToIDList[newTag], s.ID)
		}
		singleton.ServerList[s.ID] = &s
		singleton.ServerLock.Unlock()
	} else {
		s.Host = &model.Host{}
		s.State = &model.HostState{}
		s.TaskCloseLock = new(sync.Mutex)
		singleton.ServerLock.Lock()
		singleton.SecretToID[s.Secret] = s.ID
		singleton.ServerList[s.ID] = &s
		singleton.ServerTagToIDList[s.Tag] = append(singleton.ServerTagToIDList[s.Tag], s.ID)
		singleton.ServerLock.Unlock()
	}
	singleton.ReSortServer()
	c.JSON(http.StatusOK, model.Response{
		Code: http.StatusOK,
	})
}

type monitorForm struct {
	ID                     uint64
	Name                   string
	Target                 string
	Type                   uint8
	Cover                  uint8
	Notify                 string
	NotificationTag        string
	SkipServersRaw         string
	Duration               uint64
	MinLatency             float32
	MaxLatency             float32
	LatencyNotify          string
	EnableTriggerTask      string
	EnableShowInService    string
	FailTriggerTasksRaw    string
	RecoverTriggerTasksRaw string
}

func (ma *memberAPI) addOrEditMonitor(c *gin.Context) {
	var mf monitorForm
	var m model.Monitor
	err := c.ShouldBindJSON(&mf)
	if err == nil {
		m.Name = mf.Name
		m.Target = strings.TrimSpace(mf.Target)
		m.Type = mf.Type
		m.ID = mf.ID
		m.SkipServersRaw = mf.SkipServersRaw
		m.Cover = mf.Cover
		m.Notify = mf.Notify == "on"
		m.NotificationTag = mf.NotificationTag
		m.Duration = mf.Duration
		m.LatencyNotify = mf.LatencyNotify == "on"
		m.MinLatency = mf.MinLatency
		m.MaxLatency = mf.MaxLatency
		m.EnableShowInService = mf.EnableShowInService == "on"
		m.EnableTriggerTask = mf.EnableTriggerTask == "on"
		m.RecoverTriggerTasksRaw = mf.RecoverTriggerTasksRaw
		m.FailTriggerTasksRaw = mf.FailTriggerTasksRaw

		// 处理 SkipServersRaw，确保它是有效的JSON数组
		if mf.SkipServersRaw == "" {
			mf.SkipServersRaw = "[]"
			m.SkipServersRaw = "[]"
		}

		// 尝试初始化跳过服务器列表，如果失败则设置为空数组
		err = m.InitSkipServers()
		if err != nil {
			log.Printf("解析监控器跳过服务器列表失败（%s），重置为空数组: %v", mf.SkipServersRaw, err)
			m.SkipServers = make(map[uint64]bool)
			m.SkipServersRaw = "[]"
			err = nil
		}
	}
	if err == nil {
		// 保证NotificationTag不为空
		if m.NotificationTag == "" {
			m.NotificationTag = "default"
		}
		err = utils.Json.Unmarshal([]byte(mf.FailTriggerTasksRaw), &m.FailTriggerTasks)
	}
	if err == nil {
		err = utils.Json.Unmarshal([]byte(mf.RecoverTriggerTasksRaw), &m.RecoverTriggerTasks)
	}
	if err == nil {
		if m.ID == 0 {
			// BadgerDB 模式下使用 BadgerDB 操作
			if singleton.Conf.DatabaseType == "badger" {
				// 为新监控器生成ID
				if m.ID == 0 {
					m.ID = uint64(time.Now().UnixNano())
				}
				err = db.DB.SaveModel("monitor", m.ID, &m)
			} else if singleton.DB != nil {
				err = singleton.DB.Create(&m).Error
			} else {
				err = errors.New("数据库未初始化")
			}
		} else {
			// BadgerDB 模式下使用 BadgerDB 操作
			if singleton.Conf.DatabaseType == "badger" {
				err = db.DB.SaveModel("monitor", m.ID, &m)
			} else if singleton.DB != nil {
				err = singleton.DB.Save(&m).Error
			} else {
				err = errors.New("数据库未初始化")
			}
		}
	}
	if err == nil {
		// BadgerDB 模式下跳过监控历史删除操作（暂不支持复杂查询）
		if singleton.Conf.DatabaseType != "badger" && singleton.DB != nil {
			if m.Cover == 0 {
				err = singleton.DB.Unscoped().Delete(&model.MonitorHistory{}, "monitor_id = ? and server_id in (?)", m.ID, strings.Split(m.SkipServersRaw[1:len(m.SkipServersRaw)-1], ",")).Error
			} else {
				err = singleton.DB.Unscoped().Delete(&model.MonitorHistory{}, "monitor_id = ? and server_id not in (?)", m.ID, strings.Split(m.SkipServersRaw[1:len(m.SkipServersRaw)-1], ",")).Error
			}
		}
	}
	if err == nil {
		err = singleton.ServiceSentinelShared.OnMonitorUpdate(m)
	}
	if err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("请求错误：%s", err),
		})
		return
	}
	c.JSON(http.StatusOK, model.Response{
		Code: http.StatusOK,
	})
}

type cronForm struct {
	ID              uint64
	TaskType        uint8 // 0:计划任务 1:触发任务
	Name            string
	Scheduler       string
	Command         string
	ServersRaw      string
	Cover           uint8
	PushSuccessful  string
	NotificationTag string
}

func (ma *memberAPI) addOrEditCron(c *gin.Context) {
	var cf cronForm
	var cr model.Cron
	err := c.ShouldBindJSON(&cf)
	if err == nil {
		cr.TaskType = cf.TaskType
		cr.Name = cf.Name
		cr.Scheduler = cf.Scheduler
		cr.Command = cf.Command
		cr.ServersRaw = cf.ServersRaw
		cr.PushSuccessful = cf.PushSuccessful == "on"
		cr.NotificationTag = cf.NotificationTag
		cr.ID = cf.ID
		cr.Cover = cf.Cover

		// 处理 ServersRaw，确保它是有效的JSON数组
		if cf.ServersRaw == "" {
			cf.ServersRaw = "[]"
			cr.ServersRaw = "[]"
		}

		// 尝试解析JSON，如果失败则设置为空数组
		err = utils.Json.Unmarshal([]byte(cf.ServersRaw), &cr.Servers)
		if err != nil {
			log.Printf("解析计划任务服务器列表失败（%s），重置为空数组: %v", cf.ServersRaw, err)
			cr.Servers = []uint64{}
			cr.ServersRaw = "[]"
			err = nil
		}
	}

	// 计划任务类型不得使用触发服务器执行方式
	if cr.TaskType == model.CronTypeCronTask && cr.Cover == model.CronCoverAlertTrigger {
		err = errors.New("计划任务类型不得使用触发服务器执行方式")
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("请求错误：%s", err),
		})
		return
	}

	// BadgerDB 模式下使用不同的事务处理
	if singleton.Conf.DatabaseType == "badger" {
		if err == nil {
			// 保证NotificationTag不为空
			if cr.NotificationTag == "" {
				cr.NotificationTag = "default"
			}
			if cf.ID == 0 {
				// 为新计划任务生成ID
				if cr.ID == 0 {
					cr.ID = uint64(time.Now().UnixNano())
				}
				err = db.DB.SaveModel("cron", cr.ID, &cr)
			} else {
				err = db.DB.SaveModel("cron", cr.ID, &cr)
			}
		}
		if err == nil {
			// 对于计划任务类型，需要更新CronJob
			if cf.TaskType == model.CronTypeCronTask {
				cr.CronJobID, err = singleton.Cron.AddFunc(cr.Scheduler, singleton.CronTrigger(cr))
			}
		}
	} else {
		// SQLite 模式下使用事务
		if singleton.DB == nil {
			err = errors.New("数据库未初始化")
		} else {
			tx := singleton.DB.Begin()
			if err == nil {
				// 保证NotificationTag不为空
				if cr.NotificationTag == "" {
					cr.NotificationTag = "default"
				}
				if cf.ID == 0 {
					err = tx.Create(&cr).Error
				} else {
					err = tx.Save(&cr).Error
				}
			}
			if err == nil {
				// 对于计划任务类型，需要更新CronJob
				if cf.TaskType == model.CronTypeCronTask {
					cr.CronJobID, err = singleton.Cron.AddFunc(cr.Scheduler, singleton.CronTrigger(cr))
				}
			}
			if err == nil {
				err = tx.Commit().Error
			} else {
				tx.Rollback()
			}
		}
	}
	if err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("请求错误：%s", err),
		})
		return
	}

	singleton.CronLock.Lock()
	defer singleton.CronLock.Unlock()
	crOld := singleton.Crons[cr.ID]
	if crOld != nil && crOld.CronJobID != 0 {
		singleton.Cron.Remove(crOld.CronJobID)
	}

	delete(singleton.Crons, cr.ID)
	singleton.Crons[cr.ID] = &cr

	c.JSON(http.StatusOK, model.Response{
		Code: http.StatusOK,
	})
}

func (ma *memberAPI) manualTrigger(c *gin.Context) {
	var cr model.Cron
	cronID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: "无效的任务ID",
		})
		return
	}

	// BadgerDB 模式下使用 BadgerDB 操作
	if singleton.Conf.DatabaseType == "badger" {
		err = db.DB.FindModel(cronID, "cron", &cr)
	} else if singleton.DB != nil {
		err = singleton.DB.First(&cr, "id = ?", cronID).Error
	} else {
		err = errors.New("数据库未初始化")
	}

	if err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: err.Error(),
		})
		return
	}

	singleton.ManualTrigger(cr)

	c.JSON(http.StatusOK, model.Response{
		Code: http.StatusOK,
	})
}

type BatchUpdateServerGroupRequest struct {
	Servers []uint64
	Group   string
}

func (ma *memberAPI) batchUpdateServerGroup(c *gin.Context) {
	var req BatchUpdateServerGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: err.Error(),
		})
		return
	}

	// BadgerDB 模式下使用不同的更新方式
	if singleton.Conf.DatabaseType == "badger" {
		// 在BadgerDB模式下，逐个更新服务器
		for _, serverID := range req.Servers {
			var server model.Server
			if err := db.DB.FindModel(serverID, "server", &server); err != nil {
				c.JSON(http.StatusOK, model.Response{
					Code:    http.StatusBadRequest,
					Message: fmt.Sprintf("找不到服务器 %d: %v", serverID, err),
				})
				return
			}

			// 保留现有的 Secret 字段，避免被重置
			singleton.ServerLock.RLock()
			if existingServer, exists := singleton.ServerList[serverID]; exists && server.Secret == "" {
				server.Secret = existingServer.Secret
				log.Printf("批量更新服务器 %d 时保留现有Secret: %s", serverID, server.Secret)
			}
			singleton.ServerLock.RUnlock()

			server.Tag = req.Group
			// 使用ServerOps保存到数据库，确保Secret字段正确保存
			serverOps := db.NewServerOps(db.DB)
			if err := serverOps.SaveServer(&server); err != nil {
				c.JSON(http.StatusOK, model.Response{
					Code:    http.StatusBadRequest,
					Message: fmt.Sprintf("更新服务器 %d 失败: %v", serverID, err),
				})
				return
			}
			log.Printf("批量更新服务器 %d 的Secret已保存: %s", serverID, server.Secret)
		}
	} else if singleton.DB != nil {
		if err := singleton.DB.Model(&model.Server{}).Where("id in (?)", req.Servers).Update("tag", req.Group).Error; err != nil {
			c.JSON(http.StatusOK, model.Response{
				Code:    http.StatusBadRequest,
				Message: err.Error(),
			})
			return
		}
	} else {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: "数据库未初始化",
		})
		return
	}

	singleton.ServerLock.Lock()

	for i := 0; i < len(req.Servers); i++ {
		serverId := req.Servers[i]
		var s model.Server
		copier.Copy(&s, singleton.ServerList[serverId])
		s.Tag = req.Group
		// 如果修改了Ta
		oldTag := singleton.ServerList[serverId].Tag
		newTag := s.Tag
		if newTag != oldTag {
			index := -1
			for i := 0; i < len(singleton.ServerTagToIDList[oldTag]); i++ {
				if singleton.ServerTagToIDList[oldTag][i] == s.ID {
					index = i
					break
				}
			}
			if index > -1 {
				// 删除旧 Tag-ID 绑定关系
				singleton.ServerTagToIDList[oldTag] = append(singleton.ServerTagToIDList[oldTag][:index], singleton.ServerTagToIDList[oldTag][index+1:]...)
				if len(singleton.ServerTagToIDList[oldTag]) == 0 {
					delete(singleton.ServerTagToIDList, oldTag)
				}
			}
			// 设置新的 Tag-ID 绑定关系
			singleton.ServerTagToIDList[newTag] = append(singleton.ServerTagToIDList[newTag], s.ID)
		}
		singleton.ServerList[s.ID] = &s
	}

	singleton.ServerLock.Unlock()

	singleton.ReSortServer()

	c.JSON(http.StatusOK, model.Response{
		Code: http.StatusOK,
	})
}

func (ma *memberAPI) forceUpdate(c *gin.Context) {
	var forceUpdateServers []uint64
	if err := c.ShouldBindJSON(&forceUpdateServers); err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: err.Error(),
		})
		return
	}

	var executeResult bytes.Buffer

	for i := 0; i < len(forceUpdateServers); i++ {
		singleton.ServerLock.RLock()
		server := singleton.ServerList[forceUpdateServers[i]]
		singleton.ServerLock.RUnlock()
		if server != nil && server.TaskStream != nil {
			if err := server.TaskStream.Send(&proto.Task{
				Type: model.TaskTypeUpgrade,
			}); err != nil {
				executeResult.WriteString(fmt.Sprintf("%d 下发指令失败 %+v<br/>", forceUpdateServers[i], err))
			} else {
				executeResult.WriteString(fmt.Sprintf("%d 下发指令成功<br/>", forceUpdateServers[i]))
			}
		} else {
			executeResult.WriteString(fmt.Sprintf("%d 离线<br/>", forceUpdateServers[i]))
		}
	}

	c.JSON(http.StatusOK, model.Response{
		Code:    http.StatusOK,
		Message: executeResult.String(),
	})
}

type notificationForm struct {
	ID            uint64
	Name          string
	Tag           string // 分组名
	URL           string
	RequestMethod int
	RequestType   int
	RequestHeader string
	RequestBody   string
	VerifySSL     string
	SkipCheck     string
}

func (ma *memberAPI) addOrEditNotification(c *gin.Context) {
	var nf notificationForm
	var n model.Notification
	err := c.ShouldBindJSON(&nf)
	if err == nil {
		n.Name = nf.Name
		n.Tag = nf.Tag
		n.RequestMethod = nf.RequestMethod
		n.RequestType = nf.RequestType
		n.RequestHeader = nf.RequestHeader
		n.RequestBody = nf.RequestBody
		n.URL = nf.URL
		verifySSL := nf.VerifySSL == "on"
		n.VerifySSL = &verifySSL
		n.ID = nf.ID
		ns := model.NotificationServerBundle{
			Notification: &n,
			Server:       nil,
			Loc:          singleton.Loc,
		}
		// 勾选了跳过检查
		if nf.SkipCheck != "on" {
			err = ns.Send("这是测试消息")
		}
	}
	if err == nil {
		// 保证Tag不为空
		if n.Tag == "" {
			n.Tag = "default"
		}
		if n.ID == 0 {
			// BadgerDB 模式下使用 BadgerDB 操作
			if singleton.Conf.DatabaseType == "badger" {
				// 为新通知配置生成ID
				if n.ID == 0 {
					n.ID = uint64(time.Now().UnixNano())
				}
				err = db.DB.SaveModel("notification", n.ID, &n)
			} else if singleton.DB != nil {
				err = singleton.DB.Create(&n).Error
			} else {
				err = errors.New("数据库未初始化")
			}
		} else {
			// BadgerDB 模式下使用 BadgerDB 操作
			if singleton.Conf.DatabaseType == "badger" {
				err = db.DB.SaveModel("notification", n.ID, &n)
			} else if singleton.DB != nil {
				err = singleton.DB.Save(&n).Error
			} else {
				err = errors.New("数据库未初始化")
			}
		}
	}
	if err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("请求错误：%s", err),
		})
		return
	}
	singleton.OnRefreshOrAddNotification(&n)
	c.JSON(http.StatusOK, model.Response{
		Code: http.StatusOK,
	})
}

type ddnsForm struct {
	ID                 uint64
	MaxRetries         uint64
	EnableIPv4         string
	EnableIPv6         string
	Name               string
	Provider           uint8
	DomainsRaw         string
	AccessID           string
	AccessSecret       string
	WebhookURL         string
	WebhookMethod      uint8
	WebhookRequestType uint8
	WebhookRequestBody string
	WebhookHeaders     string
}

func (ma *memberAPI) addOrEditDDNS(c *gin.Context) {
	var df ddnsForm
	var p model.DDNSProfile
	err := c.ShouldBindJSON(&df)
	if err == nil {
		if df.MaxRetries < 1 || df.MaxRetries > 10 {
			err = errors.New("重试次数必须为大于 1 且不超过 10 的整数")
		}
	}
	if err == nil {
		p.Name = df.Name
		p.ID = df.ID
		enableIPv4 := df.EnableIPv4 == "on"
		enableIPv6 := df.EnableIPv6 == "on"
		p.EnableIPv4 = &enableIPv4
		p.EnableIPv6 = &enableIPv6
		p.MaxRetries = df.MaxRetries
		p.Provider = df.Provider
		p.DomainsRaw = df.DomainsRaw
		p.Domains = strings.Split(p.DomainsRaw, ",")
		p.AccessID = df.AccessID
		p.AccessSecret = df.AccessSecret
		p.WebhookURL = df.WebhookURL
		p.WebhookMethod = df.WebhookMethod
		p.WebhookRequestType = df.WebhookRequestType
		p.WebhookRequestBody = df.WebhookRequestBody
		p.WebhookHeaders = df.WebhookHeaders

		for n, domain := range p.Domains {
			// IDN to ASCII
			domainValid, domainErr := idna.Lookup.ToASCII(domain)
			if domainErr != nil {
				err = fmt.Errorf("域名 %s 解析错误: %v", domain, domainErr)
				break
			}
			p.Domains[n] = domainValid
		}
	}
	if err == nil {
		if p.ID == 0 {
			// BadgerDB 模式下使用 BadgerDB 操作
			if singleton.Conf.DatabaseType == "badger" {
				// 为新DDNS配置生成ID
				if p.ID == 0 {
					p.ID = uint64(time.Now().UnixNano())
				}
				err = db.DB.SaveModel("ddns_profile", p.ID, &p)
			} else if singleton.DB != nil {
				err = singleton.DB.Create(&p).Error
			} else {
				err = errors.New("数据库未初始化")
			}
		} else {
			// BadgerDB 模式下使用 BadgerDB 操作
			if singleton.Conf.DatabaseType == "badger" {
				err = db.DB.SaveModel("ddns_profile", p.ID, &p)
			} else if singleton.DB != nil {
				err = singleton.DB.Save(&p).Error
			} else {
				err = errors.New("数据库未初始化")
			}
		}
	}
	if err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("请求错误：%s", err),
		})
		return
	}
	singleton.OnDDNSUpdate()
	c.JSON(http.StatusOK, model.Response{
		Code: http.StatusOK,
	})
}

type natForm struct {
	ID       uint64
	Name     string
	ServerID uint64
	Host     string
	Domain   string
}

func (ma *memberAPI) addOrEditNAT(c *gin.Context) {
	var nf natForm
	var n model.NAT
	err := c.ShouldBindJSON(&nf)
	if err == nil {
		n.Name = nf.Name
		n.ID = nf.ID
		n.Domain = nf.Domain
		n.Host = nf.Host
		n.ServerID = nf.ServerID
	}
	if err == nil {
		if n.ID == 0 {
			// BadgerDB 模式下使用 BadgerDB 操作
			if singleton.Conf.DatabaseType == "badger" {
				// 为新NAT配置生成ID
				if n.ID == 0 {
					n.ID = uint64(time.Now().UnixNano())
				}
				err = db.DB.SaveModel("nat", n.ID, &n)
			} else if singleton.DB != nil {
				err = singleton.DB.Create(&n).Error
			} else {
				err = errors.New("数据库未初始化")
			}
		} else {
			// BadgerDB 模式下使用 BadgerDB 操作
			if singleton.Conf.DatabaseType == "badger" {
				err = db.DB.SaveModel("nat", n.ID, &n)
			} else if singleton.DB != nil {
				err = singleton.DB.Save(&n).Error
			} else {
				err = errors.New("数据库未初始化")
			}
		}
	}
	if err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("请求错误：%s", err),
		})
		return
	}
	singleton.OnNATUpdate()
	c.JSON(http.StatusOK, model.Response{
		Code: http.StatusOK,
	})
}

type alertRuleForm struct {
	ID                     uint64
	Name                   string
	RulesRaw               string
	FailTriggerTasksRaw    string // 失败时触发的任务id
	RecoverTriggerTasksRaw string // 恢复时触发的任务id
	NotificationTag        string
	TriggerMode            int
	Enable                 string
}

func (ma *memberAPI) addOrEditAlertRule(c *gin.Context) {
	var arf alertRuleForm
	var r model.AlertRule
	err := c.ShouldBindJSON(&arf)
	if err == nil {
		// 清理 RulesRaw 中的千位分隔符和修复 ignore 字段格式
		cleanedRulesRaw := cleanNumbersInJSON(arf.RulesRaw)
		// 修复 ignore 字段的格式：将 {40:true} 转换为 {"40":true}
		cleanedRulesRaw = fixIgnoreFieldFormat(cleanedRulesRaw)
		err = utils.Json.Unmarshal([]byte(cleanedRulesRaw), &r.Rules)
		if err != nil {
			log.Printf("报警规则JSON解析失败: %v", err)
		}
	}
	if err == nil {
		if len(r.Rules) == 0 {
			err = errors.New("至少定义一条规则")
		} else {
			for i := 0; i < len(r.Rules); i++ {
				if !r.Rules[i].IsTransferDurationRule() {
					if r.Rules[i].Duration < 3 {
						err = errors.New("错误：Duration 至少为 3")
						break
					}
				} else {
					if r.Rules[i].CycleInterval < 1 {
						err = errors.New("错误: cycle_interval 至少为 1")
						break
					}
					if r.Rules[i].CycleStart == nil {
						err = errors.New("错误: cycle_start 未设置")
						break
					}
					if r.Rules[i].CycleStart.After(time.Now()) {
						err = errors.New("错误: cycle_start 是个未来值")
						break
					}
				}
			}
		}
	}
	if err == nil {
		r.Name = arf.Name
		r.RulesRaw = arf.RulesRaw
		r.FailTriggerTasksRaw = arf.FailTriggerTasksRaw
		r.RecoverTriggerTasksRaw = arf.RecoverTriggerTasksRaw
		r.NotificationTag = arf.NotificationTag
		enable := arf.Enable == "on"
		r.TriggerMode = arf.TriggerMode
		r.Enable = &enable
		r.ID = arf.ID
	}
	if err == nil {
		err = utils.Json.Unmarshal([]byte(arf.FailTriggerTasksRaw), &r.FailTriggerTasks)
	}
	if err == nil {
		err = utils.Json.Unmarshal([]byte(arf.RecoverTriggerTasksRaw), &r.RecoverTriggerTasks)
	}
	//保证NotificationTag不为空
	if err == nil {
		if r.NotificationTag == "" {
			r.NotificationTag = "default"
		}
		if r.ID == 0 {
			// BadgerDB 模式下使用 BadgerDB 操作
			if singleton.Conf.DatabaseType == "badger" {
				// 为新报警规则生成ID
				if r.ID == 0 {
					r.ID = uint64(time.Now().UnixNano())
				}
				err = db.DB.SaveModel("alert_rule", r.ID, &r)
			} else if singleton.DB != nil {
				err = singleton.DB.Create(&r).Error
			} else {
				err = errors.New("数据库未初始化")
			}
		} else {
			// BadgerDB 模式下使用 BadgerDB 操作
			if singleton.Conf.DatabaseType == "badger" {
				err = db.DB.SaveModel("alert_rule", r.ID, &r)
			} else if singleton.DB != nil {
				err = singleton.DB.Save(&r).Error
			} else {
				err = errors.New("数据库未初始化")
			}
		}
	}
	if err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("请求错误：%s", err),
		})
		return
	}
	singleton.OnRefreshOrAddAlert(r)
	c.JSON(http.StatusOK, model.Response{
		Code: http.StatusOK,
	})
}

func (ma *memberAPI) getAlertRule(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: "ID格式错误",
		})
		return
	}

	var alertRule model.AlertRule

	// BadgerDB 模式下使用 BadgerDB 操作
	if singleton.Conf.DatabaseType == "badger" {
		err = db.DB.FindModel(id, "alert_rule", &alertRule)
	} else if singleton.DB != nil {
		err = singleton.DB.First(&alertRule, "id = ?", id).Error
	} else {
		err = errors.New("数据库未初始化")
	}

	if err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusNotFound,
			Message: fmt.Sprintf("告警规则不存在：%s", err),
		})
		return
	}

	c.JSON(http.StatusOK, model.Response{
		Code:   http.StatusOK,
		Result: alertRule,
	})
}

type logoutForm struct {
	ID uint64
}

func (ma *memberAPI) logout(c *gin.Context) {
	admin := c.MustGet(model.CtxKeyAuthorizedUser).(*model.User)
	var lf logoutForm
	if err := c.ShouldBindJSON(&lf); err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("请求错误：%s", err),
		})
		return
	}
	if lf.ID != admin.ID {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("请求错误：%s", "用户ID不匹配"),
		})
		return
	}
	// BadgerDB 模式下使用 BadgerDB 操作
	if singleton.Conf.DatabaseType == "badger" {
		admin.Token = ""
		admin.TokenExpired = time.Now()
		if err := db.DB.SaveModel("user", admin.ID, admin); err != nil {
			log.Printf("更新用户登出状态到BadgerDB失败: %v", err)
		}
	} else if singleton.DB != nil {
		singleton.DB.Model(admin).UpdateColumns(model.User{
			Token:        "",
			TokenExpired: time.Now(),
		})
	}
	c.JSON(http.StatusOK, model.Response{
		Code: http.StatusOK,
	})

	if oidcLogoutUrl := singleton.Conf.Oauth2.OidcLogoutURL; oidcLogoutUrl != "" {
		// 重定向到 OIDC 退出登录地址。不知道为什么，这里的重定向不生效
		c.Redirect(http.StatusOK, oidcLogoutUrl)
	}
}

type settingForm struct {
	Title                   string
	Admin                   string
	Language                string
	Theme                   string
	DashboardTheme          string
	CustomCode              string
	CustomCodeDashboard     string
	CustomNameservers       string
	ViewPassword            string
	IgnoredIPNotification   string
	IPChangeNotificationTag string // IP变更提醒的通知组
	GRPCHost                string
	GRPCPort                uint
	Cover                   uint8

	EnableIPChangeNotification      string
	EnablePlainIPInNotification     string
	DisableSwitchTemplateInFrontend string
}

func (ma *memberAPI) updateSetting(c *gin.Context) {
	var sf settingForm
	if err := c.ShouldBind(&sf); err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("请求错误：%s", err),
		})
		return
	}

	if _, yes := model.Themes[sf.Theme]; !yes {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("前台主题不存在：%s", sf.Theme),
		})
		return
	}

	if _, yes := model.DashboardThemes[sf.DashboardTheme]; !yes {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("后台主题不存在：%s", sf.DashboardTheme),
		})
		return
	}

	// 只检查本地文件是否存在，不再调用 resource.IsTemplateFileExist
	if !utils.IsFileExists("resource/template/theme-" + sf.Theme + "/home.html") {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("前台主题文件异常：%s", sf.Theme),
		})
		return
	}

	if !utils.IsFileExists("resource/template/dashboard-" + sf.DashboardTheme + "/setting.html") {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("后台主题文件异常：%s", sf.DashboardTheme),
		})
		return
	}

	singleton.Conf.Language = sf.Language
	singleton.Conf.EnableIPChangeNotification = sf.EnableIPChangeNotification == "on"
	singleton.Conf.EnablePlainIPInNotification = sf.EnablePlainIPInNotification == "on"
	singleton.Conf.DisableSwitchTemplateInFrontend = sf.DisableSwitchTemplateInFrontend == "on"
	singleton.Conf.Cover = sf.Cover
	singleton.Conf.GRPCHost = sf.GRPCHost
	singleton.Conf.GRPCPort = sf.GRPCPort
	singleton.Conf.IgnoredIPNotification = sf.IgnoredIPNotification
	singleton.Conf.IPChangeNotificationTag = sf.IPChangeNotificationTag
	singleton.Conf.Site.Brand = sf.Title
	singleton.Conf.Site.Theme = sf.Theme
	singleton.Conf.Site.DashboardTheme = sf.DashboardTheme
	singleton.Conf.Site.CustomCode = sf.CustomCode
	singleton.Conf.Site.CustomCodeDashboard = sf.CustomCodeDashboard
	singleton.Conf.DNSServers = sf.CustomNameservers
	singleton.Conf.Site.ViewPassword = sf.ViewPassword
	singleton.Conf.Oauth2.Admin = sf.Admin
	// 保证NotificationTag不为空
	if singleton.Conf.IPChangeNotificationTag == "" {
		singleton.Conf.IPChangeNotificationTag = "default"
	}
	if err := singleton.Conf.Save(); err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("请求错误：%s", err),
		})
		return
	}
	// 更新系统语言
	singleton.InitLocalizer()
	// 更新DNS服务器
	singleton.OnNameserverUpdate()
	c.JSON(http.StatusOK, model.Response{
		Code: http.StatusOK,
	})
}

func (ma *memberAPI) batchDeleteServer(c *gin.Context) {
	var servers []uint64
	if err := c.ShouldBindJSON(&servers); err != nil {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("JSON解码失败: %v", err),
		})
		return
	}

	if len(servers) == 0 {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: "没有选择要删除的服务器",
		})
		return
	}
	// BadgerDB 模式下使用不同的删除方式
	if singleton.Conf.DatabaseType == "badger" {
		// 在BadgerDB模式下，逐个删除服务器
		for _, serverID := range servers {
			if err := db.DB.DeleteModel("server", serverID); err != nil {
				c.JSON(http.StatusOK, model.Response{
					Code:    http.StatusBadRequest,
					Message: fmt.Sprintf("删除服务器 %d 失败: %v", serverID, err),
				})
				return
			}
		}
	} else if singleton.DB != nil {
		if err := singleton.DB.Unscoped().Delete(&model.Server{}, "id in (?)", servers).Error; err != nil {
			c.JSON(http.StatusOK, model.Response{
				Code:    http.StatusBadRequest,
				Message: err.Error(),
			})
			return
		}
	} else {
		c.JSON(http.StatusOK, model.Response{
			Code:    http.StatusBadRequest,
			Message: "数据库未初始化",
		})
		return
	}
	singleton.ServerLock.Lock()
	for i := 0; i < len(servers); i++ {
		id := servers[i]
		onServerDelete(id)
	}
	singleton.ServerLock.Unlock()
	singleton.ReSortServer()
	c.JSON(http.StatusOK, model.Response{
		Code: http.StatusOK,
	})
}

func onServerDelete(id uint64) {
	tag := singleton.ServerList[id].Tag
	delete(singleton.SecretToID, singleton.ServerList[id].Secret)
	delete(singleton.ServerList, id)
	index := -1
	for i := 0; i < len(singleton.ServerTagToIDList[tag]); i++ {
		if singleton.ServerTagToIDList[tag][i] == id {
			index = i
			break
		}
	}
	if index > -1 {

		singleton.ServerTagToIDList[tag] = append(singleton.ServerTagToIDList[tag][:index], singleton.ServerTagToIDList[tag][index+1:]...)
		if len(singleton.ServerTagToIDList[tag]) == 0 {
			delete(singleton.ServerTagToIDList, tag)
		}
	}

	singleton.AlertsLock.Lock()
	for i := 0; i < len(singleton.Alerts); i++ {
		if singleton.AlertsCycleTransferStatsStore[singleton.Alerts[i].ID] != nil {
			delete(singleton.AlertsCycleTransferStatsStore[singleton.Alerts[i].ID].ServerName, id)
			delete(singleton.AlertsCycleTransferStatsStore[singleton.Alerts[i].ID].Transfer, id)
			delete(singleton.AlertsCycleTransferStatsStore[singleton.Alerts[i].ID].NextUpdate, id)
		}
	}
	singleton.AlertsLock.Unlock()

	// BadgerDB 模式下暂不支持复杂的流量数据删除
	if singleton.Conf.DatabaseType != "badger" && singleton.DB != nil {
		singleton.DB.Unscoped().Delete(&model.Transfer{}, "server_id = ?", id)
	}
}

// cleanNumbersInJSON 清理JSON中带千位分隔符的数字
func cleanNumbersInJSON(jsonStr string) string {
	// 匹配引号内包含数字和逗号的字符串，更宽松的匹配
	// 这个正则表达式匹配引号内的数字，可能包含逗号（不管格式是否标准）
	re := regexp.MustCompile(`"(\d+(?:,\d+)*(?:\.\d+)?)"`)

	return re.ReplaceAllStringFunc(jsonStr, func(match string) string {
		// 移除引号
		numStr := match[1 : len(match)-1]
		// 移除所有逗号
		cleanNum := strings.ReplaceAll(numStr, ",", "")
		// 验证清理后的字符串是否为有效数字
		if matched, _ := regexp.MatchString(`^\d+(\.\d+)?$`, cleanNum); matched {
			// 返回清理后的数字（不带引号，因为它应该是数字类型）
			return cleanNum
		}
		// 如果不是有效数字，保持原样
		return match
	})
}

// fixIgnoreFieldFormat 修复 ignore 字段的格式，将 {40:true} 转换为 {"40":true}
func fixIgnoreFieldFormat(jsonStr string) string {
	// 匹配 ignore 字段中的数字键，将其转换为字符串键
	// 匹配模式：ignore":{数字:值,数字:值}
	re := regexp.MustCompile(`("ignore"\s*:\s*\{)([^}]+)(\})`)

	return re.ReplaceAllStringFunc(jsonStr, func(match string) string {
		// 提取 ignore 对象的内容
		parts := re.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}

		prefix := parts[1]  // "ignore":{
		content := parts[2] // 40:true,41:false
		suffix := parts[3]  // }

		// 修复内容中的数字键
		keyRe := regexp.MustCompile(`(\d+)(\s*:\s*)`)
		fixedContent := keyRe.ReplaceAllString(content, `"$1"$2`)

		return prefix + fixedContent + suffix
	})
}
