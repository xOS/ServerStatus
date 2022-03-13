package singleton

import (
	"sort"
	"sync"

	"github.com/patrickmn/go-cache"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"

	"github.com/xos/probe-lite/model"
	"github.com/xos/probe-lite/pkg/utils"
)

var Version = "v0.0.3"

var (
	Conf  *model.Config
	Cache *cache.Cache
	DB    *gorm.DB

	ServerList map[uint64]*model.Server
	SecretToID map[string]uint64
	ServerLock sync.RWMutex

	SortedServerList []*model.Server
	SortedServerLock sync.RWMutex
)

func ReSortServer() {
	ServerLock.RLock()
	defer ServerLock.RUnlock()
	SortedServerLock.Lock()
	defer SortedServerLock.Unlock()

	SortedServerList = []*model.Server{}
	for _, s := range ServerList {
		SortedServerList = append(SortedServerList, s)
	}

	sort.SliceStable(SortedServerList, func(i, j int) bool {
		if SortedServerList[i].DisplayIndex == SortedServerList[j].DisplayIndex {
			return SortedServerList[i].ID < SortedServerList[j].ID
		}
		return SortedServerList[i].DisplayIndex > SortedServerList[j].DisplayIndex
	})
}

// =============== Cron Mixin ===============

var CronLock sync.RWMutex
var Crons map[uint64]*model.Cron
var Cron *cron.Cron

func ManualTrigger(c model.Cron) {
	CronTrigger(c)()
}

func CronTrigger(cr model.Cron) func() {
	crIgnoreMap := make(map[uint64]bool)
	for j := 0; j < len(cr.Servers); j++ {
		crIgnoreMap[cr.Servers[j]] = true
	}
	return func() {
		ServerLock.RLock()
		defer ServerLock.RUnlock()
	}
}

func IPDesensitize(ip string) string {
	if Conf.EnablePlainIPInNotification {
		return ip
	}
	return utils.IPDesensitize(ip)
}
