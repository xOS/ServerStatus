package controller

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xos/serverstatus/db"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/mygin"
	"github.com/xos/serverstatus/service/singleton"
)

type memberPage struct {
	r *gin.Engine
}

func (mp *memberPage) serve() {
	mr := mp.r.Group("")
	mr.Use(mygin.Authorize(mygin.AuthorizeOption{
		MemberOnly: true,
		IsPage:     true,
		Msg:        "您无权访问",
		Btn:        "登录",
		Redirect:   "/login",
	}))
	mr.GET("/server", mp.server)
	mr.GET("/monitor", mp.monitor)
	mr.GET("/cron", mp.cron)
	mr.GET("/notification", mp.notification)
	mr.GET("/ddns", mp.ddns)
	mr.GET("/nat", mp.nat)
	mr.GET("/setting", mp.setting)
	mr.GET("/api", mp.api)
}

func (mp *memberPage) api(c *gin.Context) {
	var tokens map[string]*model.ApiToken

	// 统一使用内存中的数据，确保删除操作的一致性
	// 这样可以避免页面显示已删除的token
	singleton.ApiLock.RLock()
	defer singleton.ApiLock.RUnlock()
	tokens = make(map[string]*model.ApiToken)
	for token, apiToken := range singleton.ApiTokenList {
		if apiToken != nil {
			tokens[token] = apiToken
		}
	}
	log.Printf("API页面: 使用内存中的 %d 个API令牌", len(tokens))

	c.HTML(http.StatusOK, "dashboard-default/api", mygin.CommonEnvironment(c, gin.H{
		"title":  "API 管理",
		"Tokens": tokens,
	}))
}

func (mp *memberPage) server(c *gin.Context) {
	singleton.SortedServerLock.RLock()
	defer singleton.SortedServerLock.RUnlock()
	c.HTML(http.StatusOK, "dashboard-default/server", mygin.CommonEnvironment(c, gin.H{
		"Title":   "服务器管理",
		"Servers": singleton.SortedServerList,
	}))
}

func (mp *memberPage) monitor(c *gin.Context) {
	c.HTML(http.StatusOK, "dashboard-default/monitor", mygin.CommonEnvironment(c, gin.H{
		"Title":    "服务监控",
		"Monitors": singleton.ServiceSentinelShared.Monitors(),
	}))
}

func (mp *memberPage) cron(c *gin.Context) {
	var crons []model.Cron

	// 根据数据库类型选择不同的查询方式
	if singleton.Conf.DatabaseType == "badger" {
		// 使用BadgerDB
		if db.DB != nil {
			cronOps := db.NewCronOps(db.DB)
			cronPtrs, err := cronOps.GetAllCrons()
			if err != nil {
				log.Printf("从BadgerDB查询定时任务失败: %v", err)
				crons = []model.Cron{}
			} else {
				// 转换指针数组为值数组
				for _, cronPtr := range cronPtrs {
					if cronPtr != nil {
						crons = append(crons, *cronPtr)
					}
				}
			}
		} else {
			crons = []model.Cron{}
		}
	} else {
		// 使用SQLite
		singleton.DB.Find(&crons)
	}

	c.HTML(http.StatusOK, "dashboard-default/cron", mygin.CommonEnvironment(c, gin.H{
		"Title": "计划任务",
		"Crons": crons,
	}))
}

func (mp *memberPage) notification(c *gin.Context) {
	var nf []model.Notification
	var ar []model.AlertRule

	// 根据数据库类型选择不同的查询方式
	if singleton.Conf.DatabaseType == "badger" {
		// 使用BadgerDB查询通知
		if db.DB != nil {
			notificationOps := db.NewNotificationOps(db.DB)
			nfPtrs, err := notificationOps.GetAllNotifications()
			if err != nil {
				log.Printf("从BadgerDB查询通知失败: %v", err)
				nf = []model.Notification{}
			} else {
				// 转换指针数组为值数组
				for _, nfPtr := range nfPtrs {
					if nfPtr != nil {
						nf = append(nf, *nfPtr)
					}
				}
			}

			// 查询通知规则
			alertOps := db.NewAlertRuleOps(db.DB)
			arPtrs, err := alertOps.GetAllAlertRules()
			if err != nil {
				log.Printf("从BadgerDB查询通知规则失败: %v", err)
				ar = []model.AlertRule{}
			} else {
				// 转换指针数组为值数组
				for _, arPtr := range arPtrs {
					if arPtr != nil {
						ar = append(ar, *arPtr)
					}
				}
			}
		} else {
			nf = []model.Notification{}
			ar = []model.AlertRule{}
		}
	} else {
		// 使用SQLite
		singleton.DB.Find(&nf)
		singleton.DB.Find(&ar)
	}

	c.HTML(http.StatusOK, "dashboard-default/notification", mygin.CommonEnvironment(c, gin.H{
		"Title":         "通知",
		"Notifications": nf,
		"AlertRules":    ar,
	}))
}

func (mp *memberPage) ddns(c *gin.Context) {
	var data []model.DDNSProfile

	// 根据数据库类型选择不同的查询方式
	if singleton.Conf.DatabaseType == "badger" {
		// 使用BadgerDB
		if db.DB != nil {
			ddnsOps := db.NewDDNSOps(db.DB)
			dataPtrs, err := ddnsOps.GetAllDDNSProfiles()
			if err != nil {
				log.Printf("从BadgerDB查询DDNS配置失败: %v", err)
				data = []model.DDNSProfile{}
			} else {
				// 转换指针数组为值数组
				for _, dataPtr := range dataPtrs {
					if dataPtr != nil {
						data = append(data, *dataPtr)
					}
				}
			}
		} else {
			data = []model.DDNSProfile{}
		}
	} else {
		// 使用SQLite
		if singleton.DB != nil {
			singleton.DB.Find(&data)
		} else {
			log.Printf("警告: SQLite数据库未初始化")
			data = []model.DDNSProfile{}
		}
	}

	c.HTML(http.StatusOK, "dashboard-default/ddns", mygin.CommonEnvironment(c, gin.H{
		"Title":        "DDNS",
		"DDNS":         data,
		"ProviderMap":  model.ProviderMap,
		"ProviderList": model.ProviderList,
	}))
}

func (mp *memberPage) nat(c *gin.Context) {
	var data []model.NAT

	// 根据数据库类型选择不同的查询方式
	if singleton.Conf.DatabaseType == "badger" {
		// 使用BadgerDB
		if db.DB != nil {
			natOps := db.NewNATOps(db.DB)
			dataPtrs, err := natOps.GetAllNATs()
			if err != nil {
				log.Printf("从BadgerDB查询NAT配置失败: %v", err)
				data = []model.NAT{}
			} else {
				// 转换指针数组为值数组
				for _, dataPtr := range dataPtrs {
					if dataPtr != nil {
						data = append(data, *dataPtr)
					}
				}
			}
		} else {
			data = []model.NAT{}
		}
	} else {
		// 使用SQLite
		if singleton.DB != nil {
			singleton.DB.Find(&data)
		} else {
			log.Printf("警告: SQLite数据库未初始化")
			data = []model.NAT{}
		}
	}

	c.HTML(http.StatusOK, "dashboard-default/nat", mygin.CommonEnvironment(c, gin.H{
		"Title": "NAT",
		"NAT":   data,
	}))
}

func (mp *memberPage) setting(c *gin.Context) {
	c.HTML(http.StatusOK, "dashboard-default/setting", mygin.CommonEnvironment(c, gin.H{
		"Title": "设置",
	}))
}
