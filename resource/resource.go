package resource

import (
	"github.com/xos/serverstatus/pkg/utils"
)

var StaticFS *utils.HybridFS

func init() {
	var err error
	// 只用本地目录，不用 embed.FS，第二个参数必须为类型为 string 的路径
	StaticFS, err = utils.NewHybridFS(nil, "", "resource/static/custom")
	if err != nil {
		panic(err)
	}
}
