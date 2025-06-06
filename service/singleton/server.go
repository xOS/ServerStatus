package singleton

import (
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/jinzhu/copier"
	"github.com/xos/serverstatus/db"
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

	var servers []*model.Server

	// 根据数据库类型选择不同的加载方式
	if Conf.DatabaseType == "badger" {
		// 使用 BadgerDB 加载服务器
		if db.DB != nil {
			log.Printf("使用BadgerDB加载服务器列表...")
			serverOps := db.NewServerOps(db.DB)
			var err error
			servers, err = serverOps.GetAllServers()
			if err != nil {
				log.Printf("从 BadgerDB 加载服务器列表失败: %v", err)
				// 检查具体错误类型，适当处理
				log.Printf("尝试修复BadgerDB服务器加载问题...")
				// 确保服务器列表为空数组而不是nil
				servers = []*model.Server{}
			} else {
				log.Printf("从 BadgerDB 加载了 %d 台服务器", len(servers))
				// 确保没有nil条目
				for i, s := range servers {
					if s == nil {
						log.Printf("警告: 服务器列表中第 %d 项为nil，已移除", i)
						// 移除nil条目
						servers = append(servers[:i], servers[i+1:]...)
					}
				}
			}
		} else {
			log.Println("警告: BadgerDB 未初始化，无法加载服务器")
			// 确保服务器列表为空数组而不是nil
			servers = []*model.Server{}
			return
		}
	} else {
		// 使用 GORM (SQLite) 加载服务器
		log.Printf("使用SQLite加载服务器列表...")
		var sqliteServers []model.Server
		DB.Find(&sqliteServers)
		for _, s := range sqliteServers {
			servers = append(servers, &s)
		}
		log.Printf("从 SQLite 加载了 %d 台服务器", len(servers))
	}

	// 清空当前服务器列表，确保是干净的状态
	ServerList = make(map[uint64]*model.Server)
	SecretToID = make(map[string]uint64)
	ServerTagToIDList = make(map[string][]uint64)

	for _, s := range servers {
		innerS := s
		// 如果是指针，不需要再取地址
		if innerS == nil {
			log.Printf("警告: 跳过空的服务器对象")
			continue
		}

		// 确保服务器有有效的ID
		if innerS.ID == 0 {
			log.Printf("警告: 跳过ID为0的服务器: %s", innerS.Name)
			continue
		}

		// 初始化基本对象
		if innerS.State == nil {
			innerS.State = &model.HostState{}
		}
		innerS.LastStateBeforeOffline = nil
		innerS.IsOnline = false // 初始状态为离线，等待agent报告

		// 从数据库恢复LastActive时间，使用LastOnline字段
		if !innerS.LastOnline.IsZero() {
			innerS.LastActive = innerS.LastOnline
		} else {
			// 如果没有LastOnline记录，设置为当前时间减去一个较大的值，表示很久没有活动
			innerS.LastActive = time.Now().Add(-24 * time.Hour)
		}

		// 初始化主机信息
		if innerS.Host == nil {
			innerS.Host = &model.Host{}
			innerS.Host.Initialize()
		}

		// 解析主机信息
		if innerS.HostJSON != "" && len(innerS.HostJSON) > 0 {
			host := &model.Host{}
			if err := utils.Json.Unmarshal([]byte(innerS.HostJSON), host); err != nil {
				log.Printf("解析服务器 %s 的Host数据失败: %v", innerS.Name, err)
				// 创建空的Host对象作为后备
				host = &model.Host{}
			}
			// 确保Host对象正确初始化
			host.Initialize()
			innerS.Host = host
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
					log.Printf("复制服务器 %s 的状态数据失败: %v", innerS.Name, copyErr)
				}

				// 将保存的流量数据初始化到State中，确保显示流量数据
				innerS.State.NetInTransfer = innerS.CumulativeNetInTransfer
				innerS.State.NetOutTransfer = innerS.CumulativeNetOutTransfer
			} else {
				log.Printf("解析服务器 %s 的最后状态失败: %v", innerS.Name, err)
			}
		}

		innerS.TaskCloseLock = new(sync.Mutex)

		// 将服务器添加到映射表
		log.Printf("添加服务器到列表: ID=%d, 名称=%s", innerS.ID, innerS.Name)
		ServerList[innerS.ID] = innerS

		// 处理Secret映射
		log.Printf("检查服务器 %s (ID: %d) 的Secret: '%s' (长度: %d)", innerS.Name, innerS.ID, innerS.Secret, len(innerS.Secret))
		if innerS.Secret != "" {
			log.Printf("服务器 %s (ID: %d) 已有Secret: %s", innerS.Name, innerS.ID, innerS.Secret)
			SecretToID[innerS.Secret] = innerS.ID
		} else {
			// 如果Secret为空，生成一个新的Secret
			log.Printf("服务器 %s (ID: %d) 缺少Secret，正在生成新的Secret...", innerS.Name, innerS.ID)
			newSecret, err := utils.GenerateRandomString(18)
			if err != nil {
				log.Printf("为服务器 %s (ID: %d) 生成Secret失败: %v", innerS.Name, innerS.ID, err)
			} else {
				innerS.Secret = newSecret
				SecretToID[innerS.Secret] = innerS.ID

				// 保存到数据库
				if Conf.DatabaseType == "badger" {
					if err := SaveServerToBadgerDB(innerS); err != nil {
						log.Printf("保存服务器 %s (ID: %d) 的新Secret到BadgerDB失败: %v", innerS.Name, innerS.ID, err)
					} else {
						log.Printf("已为服务器 %s (ID: %d) 生成并保存新Secret: %s", innerS.Name, innerS.ID, newSecret)
					}
				} else {
					if err := DB.Save(innerS).Error; err != nil {
						log.Printf("保存服务器 %s (ID: %d) 的新Secret到SQLite失败: %v", innerS.Name, innerS.ID, err)
					} else {
						log.Printf("已为服务器 %s (ID: %d) 生成并保存新Secret: %s", innerS.Name, innerS.ID, newSecret)
					}
				}
			}
		}

		// 处理标签映射
		if innerS.Tag != "" {
			ServerTagToIDList[innerS.Tag] = append(ServerTagToIDList[innerS.Tag], innerS.ID)
		}
	}

	log.Printf("服务器加载完成，共加载 %d 台服务器", len(ServerList))
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

	printServerLoadSummary()
}

// printServerLoadSummary 输出服务器状态加载情况的摘要信息
func printServerLoadSummary() {
	// 统计信息
	loaded := 0
	withState := 0
	withHost := 0
	incompleteHost := 0
	noHost := 0

	for _, server := range ServerList {
		if server.Host != nil {
			if server.Host.MemTotal > 0 && len(server.Host.CPU) > 0 {
				withHost++
			} else {
				incompleteHost++
			}
		} else {
			noHost++
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

	// 服务器状态统计完成
}

// CleanupServerState 清理服务器状态
func CleanupServerState() {
	// 修复死锁问题：先复制需要清理的服务器列表，避免长时间持有ServerLock
	ServerLock.RLock()
	var serversToCleanup []*model.Server
	now := time.Now()

	for _, server := range ServerList {
		// 需要清理的条件：长时间离线或长时间未活动
		if (!server.IsOnline && now.Sub(server.LastActive) > 24*time.Hour) ||
			(server.IsOnline && now.Sub(server.LastActive) > 5*time.Minute) {
			serversToCleanup = append(serversToCleanup, server)
		}
	}
	ServerLock.RUnlock()

	// 处理需要清理的服务器，不持有ServerLock，避免死锁
	for _, server := range serversToCleanup {
		// 清理长时间离线的服务器状态
		if !server.IsOnline && now.Sub(server.LastActive) > 24*time.Hour {
			// 保存最后状态到数据库（仅在SQLite模式下）
			if server.State != nil && Conf.DatabaseType != "badger" && DB != nil {
				lastStateJSON, err := utils.Json.Marshal(server.State)
				if err == nil {
					DB.Model(server).Update("last_state_json", string(lastStateJSON))
				}
			}

			// 清理内存中的状态（无需锁，因为是单独操作）
			server.State = nil
			server.LastStateBeforeOffline = nil
			server.TaskStream = nil

			// 安全关闭通道，使用异步处理防止死锁
			if server.TaskClose != nil && server.TaskCloseLock != nil {
				go func(s *model.Server) {
					// 使用带超时的锁获取，避免永久阻塞
					done := make(chan bool, 1)
					go func() {
						s.TaskCloseLock.Lock()
						done <- true
					}()

					select {
					case <-done:
						defer s.TaskCloseLock.Unlock()
						if s.TaskClose != nil {
							// 使用非阻塞发送，避免死锁
							select {
							case s.TaskClose <- fmt.Errorf("server state cleanup"):
							default:
								// 通道可能已满或已关闭，直接关闭
							}
							close(s.TaskClose)
							s.TaskClose = nil
						}
					case <-time.After(100 * time.Millisecond):
						// 超时后跳过这次清理
						return
					}
				}(server)
			}
		}

		// 清理长时间未活动的连接
		if server.IsOnline && now.Sub(server.LastActive) > 5*time.Minute {
			// 使用异步处理防止死锁
			if server.TaskCloseLock != nil {
				go func(s *model.Server) {
					// 使用带超时的锁获取，避免永久阻塞
					done := make(chan bool, 1)
					go func() {
						s.TaskCloseLock.Lock()
						done <- true
					}()

					select {
					case <-done:
						defer s.TaskCloseLock.Unlock()
						if s.TaskClose != nil {
							// 使用非阻塞发送，避免死锁
							select {
							case s.TaskClose <- fmt.Errorf("connection timeout"):
							default:
								// 通道可能已满或已关闭，直接关闭
							}
							close(s.TaskClose)
							s.TaskClose = nil
						}
						s.TaskStream = nil
					case <-time.After(100 * time.Millisecond):
						// 超时后跳过这次清理
						return
					}
				}(server)
			}
		}
	}
}

// SafeCleanupServerState 安全的、同步的服务器状态清理函数
// 这个函数专门设计为防死锁，使用超时机制和非阻塞操作
func SafeCleanupServerState() {
	log.Printf("开始安全服务器状态清理...")

	// 使用带超时的Server锁获取
	type lockResult struct {
		acquired bool
		servers  []*model.Server
	}

	lockCh := make(chan lockResult, 1)

	// 尝试获取锁的goroutine
	go func() {
		result := lockResult{acquired: false}

		// 尝试在500ms内获取锁
		done := make(chan bool, 1)
		go func() {
			ServerLock.RLock()
			done <- true
		}()

		select {
		case <-done:
			// 成功获取锁，快速复制服务器列表
			defer ServerLock.RUnlock()

			now := time.Now()
			for _, server := range ServerList {
				if server != nil &&
					((!server.IsOnline && now.Sub(server.LastActive) > 12*time.Hour) ||
						(server.IsOnline && now.Sub(server.LastActive) > 3*time.Minute)) {
					result.servers = append(result.servers, server)
				}
			}
			result.acquired = true

		case <-time.After(500 * time.Millisecond):
			// 超时，跳过这次清理
			log.Printf("SafeCleanupServerState: 获取ServerLock超时，跳过清理")
		}

		lockCh <- result
	}()

	// 等待锁获取结果
	select {
	case result := <-lockCh:
		if !result.acquired {
			log.Printf("SafeCleanupServerState: 无法获取锁，清理跳过")
			return
		}

		log.Printf("SafeCleanupServerState: 发现 %d 个需要清理的服务器", len(result.servers))

		// 同步清理每个服务器，使用超时保护
		cleanedCount := 0
		skippedCount := 0

		for _, server := range result.servers {
			cleaned := cleanupSingleServerState(server)
			if cleaned {
				cleanedCount++
			} else {
				skippedCount++
			}
		}

		log.Printf("SafeCleanupServerState: 清理完成，成功清理 %d 个，跳过 %d 个", cleanedCount, skippedCount)

	case <-time.After(1 * time.Second):
		log.Printf("SafeCleanupServerState: 整体操作超时")
	}
}

// cleanupSingleServerState 清理单个服务器状态的同步函数
func cleanupSingleServerState(server *model.Server) bool {
	if server == nil {
		return false
	}

	now := time.Now()
	cleaned := false

	// 清理长时间离线的服务器状态
	if !server.IsOnline && now.Sub(server.LastActive) > 12*time.Hour {
		// 保存最后状态到数据库（非阻塞，仅在SQLite模式下）
		if server.State != nil && Conf.DatabaseType != "badger" && DB != nil {
			if lastStateJSON, err := utils.Json.Marshal(server.State); err == nil {
				// 使用非阻塞的数据库更新
				go func() {
					defer func() {
						if r := recover(); r != nil {
							log.Printf("保存服务器状态时panic: %v", r)
						}
					}()
					DB.Model(server).Update("last_state_json", string(lastStateJSON))
				}()
			}
		}

		// 清理内存状态
		server.State = nil
		server.LastStateBeforeOffline = nil
		cleaned = true
	}

	// 清理任务连接（使用超时锁）
	if server.TaskCloseLock != nil {
		lockAcquired := make(chan bool, 1)

		// 尝试获取TaskCloseLock
		go func() {
			server.TaskCloseLock.Lock()
			lockAcquired <- true
		}()

		select {
		case <-lockAcquired:
			defer server.TaskCloseLock.Unlock()

			if server.TaskClose != nil {
				// 非阻塞发送关闭信号
				select {
				case server.TaskClose <- fmt.Errorf("safe cleanup"):
					log.Printf("向服务器 %s 发送关闭信号", server.Name)
				default:
					log.Printf("服务器 %s 的任务通道已满或已关闭，直接关闭", server.Name)
				}

				// 安全关闭通道
				close(server.TaskClose)
				server.TaskClose = nil
			}

			server.TaskStream = nil
			cleaned = true

		case <-time.After(100 * time.Millisecond):
			log.Printf("获取服务器 %s 的TaskCloseLock超时，跳过任务清理", server.Name)
		}
	}

	return cleaned
}

// UpdateServer 更新服务器信息
func UpdateServer(s *model.Server) error {
	defer ServerLock.Unlock()
	ServerLock.Lock()

	s.LastActive = time.Now()
	s.IsOnline = true

	if s.State != nil {
		// 获取当前上报的原始流量数据
		currentInTransfer := s.State.NetInTransfer
		currentOutTransfer := s.State.NetOutTransfer

		// 保存原始流量数据用于计算增量
		originalIn := currentInTransfer
		originalOut := currentOutTransfer

		// 获取之前存储的快照值
		var prevIn, prevOut uint64
		if server, ok := ServerList[s.ID]; ok && server != nil {
			prevIn = uint64(server.PrevTransferInSnapshot)
			prevOut = uint64(server.PrevTransferOutSnapshot)
		}

		// 计算增量并更新累计流量
		if originalIn < prevIn {
			// 流量回退，更新基准点但不增加累计流量
			s.PrevTransferInSnapshot = int64(originalIn)
		} else {
			// 正常增量
			increase := originalIn - prevIn
			// 检查是否会发生溢出
			if s.CumulativeNetInTransfer > 0 &&
				increase > ^uint64(0)-s.CumulativeNetInTransfer {
				// 如果会发生溢出，保持当前值不变
				log.Printf("警告：服务器 %s 入站流量累计值即将溢出，保持当前值", s.Name)
			} else {
				s.CumulativeNetInTransfer += increase
			}
			s.PrevTransferInSnapshot = int64(originalIn)
		}

		if originalOut < prevOut {
			// 流量回退，更新基准点但不增加累计流量
			s.PrevTransferOutSnapshot = int64(originalOut)
		} else {
			// 正常增量
			increase := originalOut - prevOut
			// 检查是否会发生溢出
			if s.CumulativeNetOutTransfer > 0 &&
				increase > ^uint64(0)-s.CumulativeNetOutTransfer {
				// 如果会发生溢出，保持当前值不变
				log.Printf("警告：服务器 %s 出站流量累计值即将溢出，保持当前值", s.Name)
			} else {
				s.CumulativeNetOutTransfer += increase
			}
			s.PrevTransferOutSnapshot = int64(originalOut)
		}

		// 更新显示的流量值（只显示累计流量，不加原始流量）
		s.State.NetInTransfer = s.CumulativeNetInTransfer
		s.State.NetOutTransfer = s.CumulativeNetOutTransfer

		// 在ServerList中同步更新
		if server, ok := ServerList[s.ID]; ok && server != nil {
			server.CumulativeNetInTransfer = s.CumulativeNetInTransfer
			server.CumulativeNetOutTransfer = s.CumulativeNetOutTransfer
			server.PrevTransferInSnapshot = s.PrevTransferInSnapshot
			server.PrevTransferOutSnapshot = s.PrevTransferOutSnapshot
		}

		// 定期保存到数据库（改为10分钟间隔）
		shouldSave := false
		if server, ok := ServerList[s.ID]; ok && server != nil {
			shouldSave = time.Since(server.LastFlowSaveTime).Minutes() > 10
		} else {
			shouldSave = true // 首次保存
		}

		if shouldSave && Conf.DatabaseType != "badger" && DB != nil {
			updateSQL := `UPDATE servers SET
							cumulative_net_in_transfer = ?,
							cumulative_net_out_transfer = ?,
							last_active = ?
							WHERE id = ?`

			result := DB.Exec(updateSQL,
				s.CumulativeNetInTransfer,
				s.CumulativeNetOutTransfer,
				s.LastActive,
				s.ID)

			if result.Error != nil {
				log.Printf("更新服务器 %s 的流量数据失败: %v", s.Name, result.Error)
				return result.Error
			}

			// 更新最后保存时间
			if server, ok := ServerList[s.ID]; ok && server != nil {
				server.LastFlowSaveTime = time.Now()
			}
		} else if shouldSave && Conf.DatabaseType == "badger" && db.DB != nil {
			// BadgerDB模式下，保存流量数据到BadgerDB
			serverOps := db.NewServerOps(db.DB)
			if serverOps != nil {
				// 获取当前数据库中的服务器数据
				dbServer, err := serverOps.GetServer(s.ID)
				if err == nil && dbServer != nil {
					// 更新累计流量数据
					dbServer.CumulativeNetInTransfer = s.CumulativeNetInTransfer
					dbServer.CumulativeNetOutTransfer = s.CumulativeNetOutTransfer

					// 更新服务器状态信息
					dbServer.LastActive = s.LastActive
					dbServer.IsOnline = s.IsOnline

					// 保存Host信息
					if s.Host != nil {
						dbServer.Host = s.Host
					}

					// 保存最后状态
					if s.State != nil {
						if lastStateJSON, err := utils.Json.Marshal(s.State); err == nil {
							dbServer.LastStateJSON = string(lastStateJSON)
						}
					}

					// 保存回数据库
					if err := serverOps.SaveServer(dbServer); err != nil {
						log.Printf("BadgerDB: 保存服务器 %s 的数据失败: %v", s.Name, err)
					} else {
						// 更新最后保存时间
						if server, ok := ServerList[s.ID]; ok && server != nil {
							server.LastFlowSaveTime = time.Now()
						}
					}
				}
			}
		}

		// 更新前端显示的流量统计
		UpdateTrafficStats(s.ID, s.CumulativeNetInTransfer, s.CumulativeNetOutTransfer)
	}

	// 更新内存中的服务器信息
	ServerList[s.ID] = s
	return nil
}
