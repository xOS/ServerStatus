package cloudflare

import (
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/service/singleton"
)

type UserInfo struct {
	Sub    string   `json:"sub"`
	Email  string   `json:"email"`
	Name   string   `json:"name"`
	Groups []string `json:"groups"`
}

func (u UserInfo) MapToServerUser() model.User {
	var user model.User
	singleton.DB.Where("login = ?", u.Sub).First(&user)
	user.Login = u.Sub
	user.Email = u.Email
	user.Name = u.Name
	return user
}
