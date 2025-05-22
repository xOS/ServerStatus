package singleton

import (
	"log"
	"sort"
	"sync"

	"github.com/jinzhu/copier"
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

		// 从数据库加载最后一次上报的Host信息
		var hostJSON []byte
		if err := DB.Raw("SELECT host_json FROM last_reported_host WHERE server_id = ?", innerS.ID).Scan(&hostJSON).Error; err == nil && len(hostJSON) > 0 {
			if err := utils.Json.Unmarshal(hostJSON, innerS.Host); err != nil {
				log.Printf("NG>> 解析服务器 %s 的Host数据失败: %v", innerS.Name, err)
			}
		}

		// 加载离线前的最后状态
		if innerS.LastStateJSON != "" {
			lastState := &model.HostState{}
			if err := utils.Json.Unmarshal([]byte(innerS.LastStateJSON), lastState); err == nil {
				// 设置离线前的最后状态
				innerS.LastStateBeforeOffline = lastState

				// 重要：同时设置当前状态为离线前的最后状态，确保API返回正确数据
				// 深拷贝lastState到State，避免引用同一个对象
				stateCopy := &model.HostState{}
				if copyErr := copier.Copy(stateCopy, lastState); copyErr == nil {
					innerS.State = stateCopy

					// 确保状态中的数据不为零
					if innerS.State.ProcessCount == 0 && lastState.ProcessCount > 0 {
						innerS.State.ProcessCount = lastState.ProcessCount
					}
					if innerS.State.CPU == 0 && lastState.CPU > 0 {
						innerS.State.CPU = lastState.CPU
					}
					if innerS.State.MemUsed == 0 && lastState.MemUsed > 0 {
						innerS.State.MemUsed = lastState.MemUsed
					}
					if innerS.State.DiskUsed == 0 && lastState.DiskUsed > 0 {
						innerS.State.DiskUsed = lastState.DiskUsed
					}
				} else {
					log.Printf("NG>> 复制服务器 %s 的状态数据失败: %v", innerS.Name, copyErr)
				}

				// 将保存的流量数据初始化到State中，确保显示流量数据
				innerS.State.NetInTransfer = innerS.CumulativeNetInTransfer
				innerS.State.NetOutTransfer = innerS.CumulativeNetOutTransfer
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

	// 仅在Debug模式下输出详细信息
	if Conf.Debug {
		printServerLoadSummary()
	}
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

// printServerLoadSummary 输出服务器状态加载情况的摘要信息
func printServerLoadSummary() {
	log.Println("NG>> 服务器状态加载情况:")

	// 统计信息
	loaded := 0
	withState := 0
	withHost := 0

	for _, server := range ServerList {
		if server.Host != nil && server.Host.MemTotal > 0 {
			withHost++
		}

		if server.State != nil &&
			(server.State.CPU > 0 ||
				server.State.MemUsed > 0 ||
				server.State.NetInTransfer > 0) {
			withState++
		}

		if server.LastStateBeforeOffline != nil {
			loaded++
		}
	}

	log.Printf("NG>> 总共加载了 %d 台服务器: 有Host信息=%d, 有State信息=%d, 有离线前状态=%d",
		len(ServerList), withHost, withState, loaded)
}
