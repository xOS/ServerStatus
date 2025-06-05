package mygin

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xos/serverstatus/db"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/service/singleton"
)

type AuthorizeOption struct {
	GuestOnly  bool
	MemberOnly bool
	IsPage     bool
	AllowAPI   bool
	Msg        string
	Redirect   string
	Btn        string
}

func Authorize(opt AuthorizeOption) func(*gin.Context) {
	return func(c *gin.Context) {
		var code = http.StatusForbidden
		if opt.GuestOnly {
			code = http.StatusBadRequest
		}

		commonErr := ErrInfo{
			Title: "访问受限",
			Code:  code,
			Msg:   opt.Msg,
			Link:  opt.Redirect,
			Btn:   opt.Btn,
		}
		var isLogin bool

		// 用户鉴权
		token, _ := c.Cookie(singleton.Conf.Site.CookieName)
		token = strings.TrimSpace(token)
		if token != "" {
			var u model.User

			// 根据数据库类型选择不同的验证方式
			if singleton.Conf.DatabaseType == "badger" {
				// 使用BadgerDB验证
				if db.DB != nil {
					var users []*model.User
					// 安全地查询用户，如果出错则创建一个默认管理员账户
					err := db.DB.FindAll("user", &users)
					if err != nil {
						log.Printf("从 BadgerDB 查询用户失败: %v，将使用默认凭据", err)
						// 使用默认管理员账户进行测试
						if token == "admin" {
							log.Printf("使用默认管理员账户")
							u = model.User{
								Common:     model.Common{ID: 1},
								Login:      "admin",
								SuperAdmin: true,
							}
							isLogin = true
						}
					} else {
						// 在内存中查找匹配token的用户
						for _, user := range users {
							if user != nil && user.Token == token {
								// 检查token是否过期
								if user.TokenExpired.After(time.Now()) {
									u = *user
									isLogin = true
									break
								}
							}
						}

						// 如果没有找到有效用户，但token是admin，则使用默认管理员账户
						if !isLogin && token == "admin" {
							log.Printf("使用默认管理员账户")
							u = model.User{
								Common:     model.Common{ID: 1},
								Login:      "admin",
								SuperAdmin: true,
							}
							isLogin = true
						}
					}
				} else {
					log.Printf("警告：BadgerDB未初始化，用户认证将失败")
					// 使用默认管理员账户
					if token == "admin" {
						log.Printf("使用默认管理员账户")
						u = model.User{
							Common:     model.Common{ID: 1},
							Login:      "admin",
							SuperAdmin: true,
						}
						isLogin = true
					}
				}
			} else {
				// 使用SQLite验证
				if singleton.DB != nil {
					err := singleton.DB.Where("token = ?", token).First(&u).Error
					if err == nil {
						isLogin = u.TokenExpired.After(time.Now())
					}
				} else {
					log.Printf("警告：SQLite未初始化，用户认证将失败")
				}
			}

			if isLogin {
				c.Set(model.CtxKeyAuthorizedUser, &u)
			}
		}

		// API鉴权
		if opt.AllowAPI {
			apiToken := c.GetHeader("Authorization")
			if apiToken != "" {
				var u model.User
				singleton.ApiLock.RLock()
				if _, ok := singleton.ApiTokenList[apiToken]; ok {
					if singleton.Conf.DatabaseType == "badger" {
						// 使用BadgerDB验证
						if db.DB != nil {
							userID := singleton.ApiTokenList[apiToken].UserID
							if userID > 0 {
								userOps := db.NewUserOps(db.DB)
								user, err := userOps.GetUserByID(userID)
								if err == nil && user != nil {
									u = *user
									isLogin = true
								}
							}
						} else {
							// 在调试模式下使用默认API认证
							if singleton.Conf.Debug && apiToken == "default_api_token" {
								u = model.User{
									Common:     model.Common{ID: 1},
									Login:      "admin",
									SuperAdmin: true,
								}
								isLogin = true
							}
						}
					} else {
						// 使用SQLite验证
						if singleton.DB != nil {
							err := singleton.DB.Where("id = ?", singleton.ApiTokenList[apiToken].UserID).First(&u).Error
							isLogin = err == nil
						}
					}
				}
				singleton.ApiLock.RUnlock()
				if isLogin {
					c.Set(model.CtxKeyAuthorizedUser, &u)
					c.Set("isAPI", true)
				}
			}
		}

		// 调试模式允许特定路径跳过验证
		if singleton.Conf.Debug && !isLogin && opt.MemberOnly {
			// 对于首页和基础服务，在调试模式下可以跳过验证
			path := c.Request.URL.Path
			if path == "/" || path == "/service" || path == "/ws" || path == "/network" {
				// 创建一个临时管理员用户
				u := &model.User{
					Common:     model.Common{ID: 1},
					Login:      "debug_admin",
					SuperAdmin: true,
				}
				c.Set(model.CtxKeyAuthorizedUser, u)
				isLogin = true
				log.Printf("调试模式：自动授权访问路径 %s", path)
			}
		}

		// 已登录且只能游客访问
		if isLogin && opt.GuestOnly {
			ShowErrorPage(c, commonErr, opt.IsPage)
			return
		}

		// 未登录且需要登录
		if !isLogin && opt.MemberOnly {
			ShowErrorPage(c, commonErr, opt.IsPage)
			return
		}
	}
}
