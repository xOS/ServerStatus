package resource

import (
	"github.com/xos/serverstatus/pkg/utils"
)

var StaticFS *utils.HybridFS

// 不再 embed 静态资源
// var staticFS embed.FS

// 不再 embed 模板和本地化文件
// var TemplateFS embed.FS
// var I18nFS embed.FS

func init() {
	var err error
	// 只用本地目录，不用 embed.FS
	StaticFS, err = utils.NewHybridFS(nil, "", "resource/static/custom")
	if err != nil {
		panic(err)
	}
}

// 如果需要模板和本地化文件，也请用本地文件系统方式加载
// func IsTemplateFileExist(name string) bool {
// 	_, err := os.Open("resource/template/" + name)
// 	return err == nil
// }
