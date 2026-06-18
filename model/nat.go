package model

import (
	"fmt"
	"html/template"

	"github.com/xos/serverstatus/pkg/utils"
)

type NAT struct {
	Common
	Name     string
	ServerID uint64
	Host     string
	Domain   string `gorm:"unique"`
}

func (n NAT) MarshalForDashboard() template.JS {
	name, _ := utils.Json.Marshal(n.Name)
	host, _ := utils.Json.Marshal(n.Host)
	domain, _ := utils.Json.Marshal(n.Domain)
	return template.JS(fmt.Sprintf(`{"ID":"%d","Name":%s,"ServerID":%d,"Host":%s,"Domain":%s}`, n.ID, name, n.ServerID, host, domain))
}
