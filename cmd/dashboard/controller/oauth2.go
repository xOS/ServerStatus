package controller

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/xos/serverstatus/db"
	"github.com/xos/serverstatus/pkg/oidc/cloudflare"
	myOidc "github.com/xos/serverstatus/pkg/oidc/general"

	"code.gitea.io/sdk/gitea"
	"github.com/gin-gonic/gin"
	GitHubAPI "github.com/google/go-github/v47/github"
	"github.com/patrickmn/go-cache"
	"github.com/xanzy/go-gitlab"
	"golang.org/x/oauth2"
	GitHubOauth2 "golang.org/x/oauth2/github"
	GitlabOauth2 "golang.org/x/oauth2/gitlab"

	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/mygin"
	"github.com/xos/serverstatus/pkg/utils"
	"github.com/xos/serverstatus/service/singleton"
)

type oauth2controller struct {
	r            gin.IRoutes
	oidcProvider *oidc.Provider
}

func (oa *oauth2controller) serve() {
	oa.r.GET("/oauth2/login", oa.login)
	oa.r.GET("/oauth2/callback", oa.callback)
}

func (oa *oauth2controller) getCommonOauth2Config(c *gin.Context) *oauth2.Config {
	if singleton.Conf.Oauth2.Type == model.ConfigTypeGitee {
		return &oauth2.Config{
			ClientID:     singleton.Conf.Oauth2.ClientID,
			ClientSecret: singleton.Conf.Oauth2.ClientSecret,
			Scopes:       []string{},
			Endpoint: oauth2.Endpoint{
				AuthURL:  "https://gitee.com/oauth/authorize",
				TokenURL: "https://gitee.com/oauth/token",
			},
			RedirectURL: oa.getRedirectURL(c),
		}
	} else if singleton.Conf.Oauth2.Type == model.ConfigTypeGitlab {
		return &oauth2.Config{
			ClientID:     singleton.Conf.Oauth2.ClientID,
			ClientSecret: singleton.Conf.Oauth2.ClientSecret,
			Scopes:       []string{"read_user", "read_api"},
			Endpoint:     GitlabOauth2.Endpoint,
			RedirectURL:  oa.getRedirectURL(c),
		}
	} else if singleton.Conf.Oauth2.Type == model.ConfigTypeJihulab {
		return &oauth2.Config{
			ClientID:     singleton.Conf.Oauth2.ClientID,
			ClientSecret: singleton.Conf.Oauth2.ClientSecret,
			Scopes:       []string{"read_user", "read_api"},
			Endpoint: oauth2.Endpoint{
				AuthURL:  "https://jihulab.com/oauth/authorize",
				TokenURL: "https://jihulab.com/oauth/token",
			},
			RedirectURL: oa.getRedirectURL(c),
		}
	} else if singleton.Conf.Oauth2.Type == model.ConfigTypeGitea {
		return &oauth2.Config{
			ClientID:     singleton.Conf.Oauth2.ClientID,
			ClientSecret: singleton.Conf.Oauth2.ClientSecret,
			Endpoint: oauth2.Endpoint{
				AuthURL:  fmt.Sprintf("%s/login/oauth/authorize", singleton.Conf.Oauth2.Endpoint),
				TokenURL: fmt.Sprintf("%s/login/oauth/access_token", singleton.Conf.Oauth2.Endpoint),
			},
			RedirectURL: oa.getRedirectURL(c),
		}
	} else if singleton.Conf.Oauth2.Type == model.ConfigTypeCloudflare {
		return &oauth2.Config{
			ClientID:     singleton.Conf.Oauth2.ClientID,
			ClientSecret: singleton.Conf.Oauth2.ClientSecret,
			Scopes:       []string{"openid", "email", "profile", "groups"},
			Endpoint: oauth2.Endpoint{
				AuthURL:  fmt.Sprintf("%s/cdn-cgi/access/sso/oidc/%s/authorization", singleton.Conf.Oauth2.Endpoint, singleton.Conf.Oauth2.ClientID),
				TokenURL: fmt.Sprintf("%s/cdn-cgi/access/sso/oidc/%s/token", singleton.Conf.Oauth2.Endpoint, singleton.Conf.Oauth2.ClientID),
			},
			RedirectURL: oa.getRedirectURL(c),
		}
	} else if singleton.Conf.Oauth2.Type == model.ConfigTypeOidc {
		var err error
		oa.oidcProvider, err = oidc.NewProvider(c.Request.Context(), singleton.Conf.Oauth2.OidcIssuer)
		if err != nil {
			mygin.ShowErrorPage(c, mygin.ErrInfo{
				Code:  http.StatusBadRequest,
				Title: fmt.Sprintf("Cannot get OIDC infomaion from issuer from %s", singleton.Conf.Oauth2.OidcIssuer),
				Msg:   err.Error(),
			}, true)
			return nil
		}
		scopes := strings.Split(singleton.Conf.Oauth2.OidcScopes, ",")
		scopes = append(scopes, oidc.ScopeOpenID)
		uniqueScopes := removeDuplicates(scopes)
		return &oauth2.Config{
			ClientID:     singleton.Conf.Oauth2.ClientID,
			ClientSecret: singleton.Conf.Oauth2.ClientSecret,
			Scopes:       uniqueScopes,
			Endpoint:     oa.oidcProvider.Endpoint(),
			RedirectURL:  oa.getRedirectURL(c),
		}
	} else {
		return &oauth2.Config{
			ClientID:     singleton.Conf.Oauth2.ClientID,
			ClientSecret: singleton.Conf.Oauth2.ClientSecret,
			Scopes:       []string{},
			Endpoint:     GitHubOauth2.Endpoint,
		}
	}
}

func (oa *oauth2controller) getRedirectURL(c *gin.Context) string {
	scheme := "http://"
	referer := c.Request.Referer()
	if forwardedProto := c.Request.Header.Get("X-Forwarded-Proto"); forwardedProto == "https" || strings.HasPrefix(referer, "https://") {
		scheme = "https://"
	}
	return scheme + c.Request.Host + "/oauth2/callback"
}

func (oa *oauth2controller) login(c *gin.Context) {
	randomString, err := utils.GenerateRandomString(32)
	if err != nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusBadRequest,
			Title: "Something Wrong",
			Msg:   err.Error(),
		}, true)
		return
	}
	state, stateKey := randomString[:16], randomString[16:]
	singleton.Cache.Set(fmt.Sprintf("%s%s", model.CacheKeyOauth2State, stateKey), state, cache.DefaultExpiration)
	url := oa.getCommonOauth2Config(c).AuthCodeURL(state, oauth2.AccessTypeOnline)
	c.SetCookie(singleton.Conf.Site.CookieName+"-sk", stateKey, 60*5, "", "", false, false)
	c.HTML(http.StatusOK, "dashboard-"+singleton.Conf.Site.DashboardTheme+"/redirect", mygin.CommonEnvironment(c, gin.H{
		"URL": url,
	}))
}

func (oa *oauth2controller) callback(c *gin.Context) {
	var err error
	// 验证登录跳转时的 State
	stateKey, err := c.Cookie(singleton.Conf.Site.CookieName + "-sk")
	if err == nil {
		state, ok := singleton.Cache.Get(fmt.Sprintf("%s%s", model.CacheKeyOauth2State, stateKey))
		if !ok || state.(string) != c.Query("state") {
			err = errors.New("非法的登录方式")
		}
	}
	oauth2Config := oa.getCommonOauth2Config(c)
	ctx := context.Background()
	var otk *oauth2.Token
	if err == nil {
		otk, err = oauth2Config.Exchange(ctx, c.Query("code"))
	}

	var user model.User

	if err == nil {
		if singleton.Conf.Oauth2.Type == model.ConfigTypeGitlab || singleton.Conf.Oauth2.Type == model.ConfigTypeJihulab {
			var gitlabApiClient *gitlab.Client
			if singleton.Conf.Oauth2.Type == model.ConfigTypeGitlab {
				gitlabApiClient, err = gitlab.NewOAuthClient(otk.AccessToken)
			} else {
				gitlabApiClient, err = gitlab.NewOAuthClient(otk.AccessToken, gitlab.WithBaseURL("https://jihulab.com/api/v4/"))
			}
			var u *gitlab.User
			if err == nil {
				u, _, err = gitlabApiClient.Users.CurrentUser()
			}
			if err == nil {
				user = model.NewUserFromGitlab(u)
			}
		} else if singleton.Conf.Oauth2.Type == model.ConfigTypeGitea {
			var giteaApiClient *gitea.Client
			giteaApiClient, err = gitea.NewClient(singleton.Conf.Oauth2.Endpoint, gitea.SetToken(otk.AccessToken))
			var u *gitea.User
			if err == nil {
				u, _, err = giteaApiClient.GetMyUserInfo()
			}
			if err == nil {
				user = model.NewUserFromGitea(u)
			}
		} else if singleton.Conf.Oauth2.Type == model.ConfigTypeCloudflare {
			client := oauth2Config.Client(context.Background(), otk)
			resp, err := client.Get(fmt.Sprintf("%s/cdn-cgi/access/sso/oidc/%s/userinfo", singleton.Conf.Oauth2.Endpoint, singleton.Conf.Oauth2.ClientID))
			if err == nil {
				defer resp.Body.Close()
				var cloudflareUserInfo *cloudflare.UserInfo
				if err := utils.Json.NewDecoder(resp.Body).Decode(&cloudflareUserInfo); err == nil {
					user = cloudflareUserInfo.MapToServerUser()
				}
			}
		} else if singleton.Conf.Oauth2.Type == model.ConfigTypeOidc {
			userInfo, err := oa.oidcProvider.UserInfo(c.Request.Context(), oauth2.StaticTokenSource(otk))
			if err == nil {
				loginClaim := singleton.Conf.Oauth2.OidcLoginClaim
				groupClain := singleton.Conf.Oauth2.OidcGroupClaim
				adminGroups := strings.Split(singleton.Conf.Oauth2.AdminGroups, ",")
				autoCreate := singleton.Conf.Oauth2.OidcAutoCreate
				var oidceUserInfo *myOidc.UserInfo
				if err := userInfo.Claims(&oidceUserInfo); err == nil {
					user = oidceUserInfo.MapToServerUser(loginClaim, groupClain, adminGroups, autoCreate)
				}
			}
		} else {
			var client *GitHubAPI.Client
			oc := oauth2Config.Client(ctx, otk)
			if singleton.Conf.Oauth2.Type == model.ConfigTypeGitee {
				baseURL, _ := url.Parse("https://gitee.com/api/v5/")
				uploadURL, _ := url.Parse("https://gitee.com/api/v5/uploads/")
				client = GitHubAPI.NewClient(oc)
				client.BaseURL = baseURL
				client.UploadURL = uploadURL
			} else {
				client = GitHubAPI.NewClient(oc)
			}
			var gu *GitHubAPI.User
			gu, _, err = client.Users.Get(ctx, "")
			if err == nil {
				user = model.NewUserFromGitHub(gu)
			}
		}
	}

	if err == nil && user.Login == "" {
		err = errors.New("获取用户信息失败")
	}

	if err != nil || user.Login == "" {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusBadRequest,
			Title: "登录失败",
			Msg:   fmt.Sprintf("错误信息：%s", err),
		}, true)
		return
	}
	var isAdmin bool

	if user.SuperAdmin {
		isAdmin = true
	} else {
		for _, admin := range strings.Split(singleton.Conf.Oauth2.Admin, ",") {
			if admin != "" && strings.EqualFold(user.Login, admin) {
				isAdmin = true
				break
			}
		}
	}
	if !isAdmin {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusBadRequest,
			Title: "登录失败",
			Msg:   fmt.Sprintf("错误信息：%s", "该用户不是本站点管理员，无法登录"),
		}, true)
		return
	}
	user.Token, err = utils.GenerateRandomString(32)
	if err != nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusBadRequest,
			Title: "Something wrong",
			Msg:   err.Error(),
		}, true)
		return
	}
	user.TokenExpired = time.Now().AddDate(0, 2, 0)

	// 根据数据库类型选择不同的保存方式
	if singleton.Conf.DatabaseType == "badger" {
		// 使用BadgerDB保存用户信息
		if singleton.Conf.Debug {
			log.Printf("使用BadgerDB保存OAuth2用户信息: %s", user.Login)
		}

		// 检查BadgerDB是否已初始化
		if db.DB != nil {
			// 使用UserOps保存用户信息
			userOps := db.NewUserOps(db.DB)
			err = userOps.SaveUser(&user)
			if err != nil {
				mygin.ShowErrorPage(c, mygin.ErrInfo{
					Code:  http.StatusBadRequest,
					Title: "保存用户信息失败",
					Msg:   fmt.Sprintf("错误信息：%s", err),
				}, true)
				return
			}
		} else {
			// BadgerDB未初始化，但在调试模式下仍然允许继续
			if singleton.Conf.Debug {
				log.Printf("警告：BadgerDB未初始化，无法保存用户信息，但在调试模式下将继续执行")
			} else {
				mygin.ShowErrorPage(c, mygin.ErrInfo{
					Code:  http.StatusBadRequest,
					Title: "数据库错误",
					Msg:   "BadgerDB未初始化，无法保存用户信息",
				}, true)
				return
			}
		}
	} else {
		// 使用SQLite(GORM)保存用户信息
		if singleton.DB == nil {
			mygin.ShowErrorPage(c, mygin.ErrInfo{
				Code:  http.StatusBadRequest,
				Title: "数据库错误",
				Msg:   "SQLite数据库未初始化，无法保存用户信息",
			}, true)
			return
		}
		err = singleton.DB.Save(&user).Error
		if err != nil {
			mygin.ShowErrorPage(c, mygin.ErrInfo{
				Code:  http.StatusBadRequest,
				Title: "保存用户信息失败",
				Msg:   fmt.Sprintf("错误信息：%s", err),
			}, true)
			return
		}
	}

	c.SetCookie(singleton.Conf.Site.CookieName, user.Token, 60*60*24, "", "", false, false)
	c.HTML(http.StatusOK, "dashboard-"+singleton.Conf.Site.DashboardTheme+"/redirect", mygin.CommonEnvironment(c, gin.H{
		"URL": "/",
	}))
}

func removeDuplicates(elements []string) []string {
	encountered := map[string]bool{}
	result := []string{}

	for _, v := range elements {
		if !encountered[v] {
			encountered[v] = true
			result = append(result, v)
		}
	}
	return result
}
