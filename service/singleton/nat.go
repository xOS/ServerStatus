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
			natOps := db.NewNATOps(db.DB)
			var err error
			nats, err = natOps.GetAllNATs()
			if err != nil {
				log.Printf("从BadgerDB加载NAT配置失败: %v", err)
				nats = []*model.NAT{}
			} else {
				log.Printf("从BadgerDB成功加载 %d 条NAT配置", len(nats))
			}
		} else {
			log.Println("警告: BadgerDB 未初始化")
			nats = []*model.NAT{}
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
