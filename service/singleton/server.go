package singleton

import (
	"log"
	"sort"
	"sync"

	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/utils"
)

var (
	ServerList        map[uint64]*model.Server // [ServerID] -> model.Server
	SecretToID        map[string]uint64        // [ServerSecret] -> ServerID
	ServerTagToIDList map[string][]uint64      // [ServerTag] -> ServerID
	ServerLock        sync.RWMutex

	SortedServerList         []*model.Server // 用于存储服务器列表的 slice，按照服务器 ID 排序
	SortedServerListForGuest []*model.Server
	SortedServerLock         sync.RWMutex
)

// InitServer 初始化 ServerID <-> Secret 的映射
func InitServer() {
	ServerList = make(map[uint64]*model.Server)
	SecretToID = make(map[string]uint64)
	ServerTagToIDList = make(map[string][]uint64)
}

// loadServers 加载服务器列表并根据ID排序
func loadServers() {
	InitServer()
	var servers []model.Server
	DB.Find(&servers)
	for _, s := range servers {
		innerS := s
		innerS.Host = &model.Host{}
		innerS.State = &model.HostState{}
		innerS.LastStateBeforeOffline = nil
		innerS.IsOnline = false // 初始状态为离线，等待agent报告

		// 加载离线前的最后状态
		if innerS.LastStateJSON != "" {
			lastState := &model.HostState{}
			if err := utils.Json.Unmarshal([]byte(innerS.LastStateJSON), lastState); err == nil {
				innerS.LastStateBeforeOffline = lastState

				// 将保存的流量数据初始化到State中，确保显示流量数据
				if innerS.State != nil {
					innerS.State.NetInTransfer = innerS.CumulativeNetInTransfer
					innerS.State.NetOutTransfer = innerS.CumulativeNetOutTransfer
				}

				log.Printf("NG>> 服务器 %s 加载了离线前的最后状态，累计流量入站: %d, 出站: %d",
					innerS.Name, innerS.CumulativeNetInTransfer, innerS.CumulativeNetOutTransfer)
			} else {
				log.Printf("NG>> 解析服务器 %s 的最后状态失败: %v", innerS.Name, err)
			}
		}

		innerS.TaskCloseLock = new(sync.Mutex)
		ServerList[innerS.ID] = &innerS
		SecretToID[innerS.Secret] = innerS.ID
		ServerTagToIDList[innerS.Tag] = append(ServerTagToIDList[innerS.Tag], innerS.ID)
	}
	ReSortServer()
}

// ReSortServer 根据服务器ID 对服务器列表进行排序（ID越大越靠前）
func ReSortServer() {
	ServerLock.RLock()
	defer ServerLock.RUnlock()
	SortedServerLock.Lock()
	defer SortedServerLock.Unlock()

	SortedServerList = []*model.Server{}
	SortedServerListForGuest = []*model.Server{}
	for _, s := range ServerList {
		SortedServerList = append(SortedServerList, s)
		if !s.HideForGuest {
			SortedServerListForGuest = append(SortedServerListForGuest, s)
		}
	}

	// 按照服务器 ID 排序的具体实现（ID越大越靠前）
	sort.SliceStable(SortedServerList, func(i, j int) bool {
		if SortedServerList[i].DisplayIndex == SortedServerList[j].DisplayIndex {
			return SortedServerList[i].ID < SortedServerList[j].ID
		}
		return SortedServerList[i].DisplayIndex > SortedServerList[j].DisplayIndex
	})

	sort.SliceStable(SortedServerListForGuest, func(i, j int) bool {
		if SortedServerListForGuest[i].DisplayIndex == SortedServerListForGuest[j].DisplayIndex {
			return SortedServerListForGuest[i].ID < SortedServerListForGuest[j].ID
		}
		return SortedServerListForGuest[i].DisplayIndex > SortedServerListForGuest[j].DisplayIndex
	})
}
