package singleton

import (
	"log"
	"sync"

	"github.com/xos/serverstatus/db"
	"github.com/xos/serverstatus/model"
)

var natCache = make(map[string]*model.NAT)
var natCacheRwLock = new(sync.RWMutex)

func initNAT() {
	OnNATUpdate()
}

func OnNATUpdate() {
	natCacheRwLock.Lock()
	defer natCacheRwLock.Unlock()
	var nats []*model.NAT

	// 根据数据库类型选择不同的加载方式
	if Conf.DatabaseType == "badger" {
		// 使用 BadgerDB 加载NAT配置
		if db.DB != nil {
			// 目前BadgerDB还没有NATOps实现，
			// 后续可以添加NATOps对象来处理NAT配置
			log.Println("BadgerDB: NAT功能暂不支持，跳过加载")
			return
		} else {
			log.Println("警告: BadgerDB 未初始化")
			return
		}
	} else {
		// 使用 GORM (SQLite) 加载NAT配置
		DB.Find(&nats)
	}

	natCache = make(map[string]*model.NAT)
	for i := 0; i < len(nats); i++ {
		natCache[nats[i].Domain] = nats[i]
	}
}

func GetNATConfigByDomain(domain string) *model.NAT {
	natCacheRwLock.RLock()
	defer natCacheRwLock.RUnlock()
	return natCache[domain]
}
