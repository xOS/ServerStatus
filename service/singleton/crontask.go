package singleton

import (
	"sync"

	"github.com/robfig/cron/v3"
	"github.com/xos/serverstatus/model"
)

var (
	Cron     *cron.Cron
	Crons    map[uint64]*model.Cron
	CronLock sync.RWMutex
)

func InitCronTask() {
	Cron = cron.New(cron.WithSeconds(), cron.WithLocation(Loc))
	Crons = make(map[uint64]*model.Cron)
}
