package resource

import (
    "net/http"
    "os"

    "github.com/xos/serverstatus/pkg/utils"
)

var StaticFS *utils.HybridFS

func init() {
    var err error
    localStaticPath := "./static" // 静态资源的本地路径
    if _, err := os.Stat(localStaticPath); os.IsNotExist(err) {
        panic("Static files directory does not exist: " + localStaticPath)
    }

    StaticFS, err = utils.NewHybridFS(nil, localStaticPath)
    if err != nil {
        panic(err)
    }
}

func ServeStaticFiles() http.Handler {
    return http.FileServer(http.Dir("./static"))
}
