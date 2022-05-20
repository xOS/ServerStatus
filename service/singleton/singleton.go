package singleton

import (
	"time"

	"github.com/patrickmn/go-cache"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/utils"
)

var Version = "v0.1.17"

var (
	Conf  *model.Config
	Cache *cache.Cache
	DB    *gorm.DB
	Loc   *time.Location
)

// Init 初始化singleton
func Init() {
	// 初始化时区至上海 UTF+8
	var err error
	Loc, err = time.LoadLocation("Asia/Shanghai")
	if err != nil {
		panic(err)
	}

	Cache = cache.New(5*time.Minute, 10*time.Minute)
}

// LoadSingleton 加载子服务并执行
func LoadSingleton() {
	LoadNotifications() // 加载通知服务
	LoadServers()       // 加载服务器列表
	LoadAPI()
}

// InitConfigFromPath 从给出的文件路径中加载配置
func InitConfigFromPath(path string) {
	Conf = &model.Config{}
	err := Conf.Read(path)
	if err != nil {
		panic(err)
	}
}

// InitDBFromPath 从给出的文件路径中加载数据库
func InitDBFromPath(path string) {
	var err error
	DB, err = gorm.Open(sqlite.Open(path), &gorm.Config{
		CreateBatchSize: 200,
	})
	if err != nil {
		panic(err)
	}
	if Conf.Debug {
		DB = DB.Debug()
	}
	err = DB.AutoMigrate(model.Server{}, model.User{},
		model.Notification{}, model.AlertRule{}, model.Monitor{}, model.ApiToken{})
	if err != nil {
		panic(err)
	}
}

// IPDesensitize 根据设置选择是否对IP进行打码处理 返回处理后的IP(关闭打码则返回原IP)
func IPDesensitize(ip string) string {
	if Conf.EnablePlainIPInNotification {
		return ip
	}
	return utils.IPDesensitize(ip)
}
