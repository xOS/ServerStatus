package singleton

import (
	"github.com/xos/serverstatus/pkg/utils"
)

/*
	该文件保存了一些工具函数
	IPDesensitize				根据用户设置选择是否对IP进行打码处理 返回处理后的IP(关闭打码则返回原IP)
*/

// IPDesensitize 根据设置选择是否对IP进行打码处理 返回处理后的IP(关闭打码则返回原IP)
func IPDesensitize(ip string) string {
	if Conf.EnablePlainIPInNotification {
		return ip
	}
	return utils.IPDesensitize(ip)
}