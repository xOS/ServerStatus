package singleton

import (
	"log"
	"sync"
	"time"

	"github.com/xos/serverstatus/db"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/utils"
	"gorm.io/gorm"
)

var (
	ApiTokenList         = make(map[string]*model.ApiToken)
	UserIDToApiTokenList = make(map[uint64][]string)
	ApiLock              sync.RWMutex

	ServerAPI  = &ServerAPIService{}
	MonitorAPI = &MonitorAPIService{}
)

type ServerAPIService struct{}

// CommonResponse 常规返回结构 包含状态码 和 状态信息
type CommonResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type RegisterServer struct {
	Name         string
	Tag          string
	Note         string
	HideForGuest string
}

type ServerRegisterResponse struct {
	CommonResponse
	Secret string `json:"secret"`
}

type CommonServerInfo struct {
	ID           uint64 `json:"id"`
	Name         string `json:"name"`
	Tag          string `json:"tag"`
	LastActive   int64  `json:"last_active"`
	IPV4         string `json:"ipv4"`
	IPV6         string `json:"ipv6"`
	ValidIP      string `json:"valid_ip"`
	DisplayIndex int    `json:"display_index"`
	HideForGuest bool   `json:"hide_for_guest"`
}

// StatusResponse 服务器状态子结构 包含服务器信息与状态信息
type StatusResponse struct {
	CommonServerInfo
	Host   *model.Host      `json:"host"`
	Status *model.HostState `json:"status"`
}

// ServerStatusResponse 服务器状态返回结构 包含常规返回结构 和 服务器状态子结构
type ServerStatusResponse struct {
	CommonResponse
	Result []*StatusResponse `json:"result"`
}

// ServerInfoResponse 服务器信息返回结构 包含常规返回结构 和 服务器信息子结构
type ServerInfoResponse struct {
	CommonResponse
	Result []*CommonServerInfo `json:"result"`
}

type MonitorAPIService struct {
}

type MonitorInfoResponse struct {
	CommonResponse
	Result []*MonitorInfo `json:"result"`
}

type MonitorInfo struct {
	MonitorID   uint64    `json:"monitor_id"`
	ServerID    uint64    `json:"server_id"`
	MonitorName string    `json:"monitor_name"`
	ServerName  string    `json:"server_name"`
	CreatedAt   []int64   `json:"created_at"`
	AvgDelay    []float32 `json:"avg_delay"`
}

type TrafficDisplayData struct {
	ServerID   uint64  `json:"server_id"`
	ServerName string  `json:"server_name"`
	Used       string  `json:"used"`        // 已用流量（格式化后）
	Total      string  `json:"total"`       // 总流量（格式化后）
	UsedBytes  uint64  `json:"used_bytes"`  // 已用字节数
	TotalBytes uint64  `json:"total_bytes"` // 总字节数
	Percent    float64 `json:"percent"`     // 使用百分比
	CycleName  string  `json:"cycle_name"`  // 周期名称
}

func InitAPI() {
	ApiTokenList = make(map[string]*model.ApiToken)
	UserIDToApiTokenList = make(map[uint64][]string)
}

func loadAPI() {
	InitAPI()
	var tokenList []*model.ApiToken

	// 根据数据库类型选择不同的加载方式
	if Conf.DatabaseType == "badger" {
		// 使用 BadgerDB 加载API令牌
		if db.DB != nil {
			// 使用ApiTokenOps加载API令牌
			apiTokenOps := db.NewApiTokenOps(db.DB)
			var err error
			tokenList, err = apiTokenOps.GetAllApiTokens()
			if err != nil {
				log.Printf("从 BadgerDB 加载API令牌失败: %v", err)
				return
			}
			log.Printf("从 BadgerDB 加载了 %d 个API令牌", len(tokenList))
		} else {
			log.Println("警告: BadgerDB 未初始化")
			return
		}
	} else {
		// 使用 GORM (SQLite) 加载API令牌
		if DB != nil {
			DB.Find(&tokenList)
			log.Printf("从 SQLite 加载了 %d 个API令牌", len(tokenList))
		} else {
			log.Println("警告: SQLite 数据库未初始化")
			return
		}
	}

	// 加载到内存中
	ApiLock.Lock()
	defer ApiLock.Unlock()

	validTokenCount := 0
	for _, token := range tokenList {
		if token != nil && token.Token != "" && token.ID > 0 {
			ApiTokenList[token.Token] = token
			UserIDToApiTokenList[token.UserID] = append(UserIDToApiTokenList[token.UserID], token.Token)
			validTokenCount++
			log.Printf("加载API令牌: %s (Note: %s)", token.Token, token.Note)
		} else if token != nil {
			log.Printf("警告: 发现空Token的API令牌记录 (ID: %d, Note: %s)", token.ID, token.Note)
		}
	}

	log.Printf("BadgerDB模式: 加载了 %d 个有效API令牌", validTokenCount)

	log.Printf("API令牌加载完成，共加载 %d 个令牌", len(ApiTokenList))
}

// GetStatusByIDList 获取传入IDList的服务器状态信息
func (s *ServerAPIService) GetStatusByIDList(idList []uint64) *ServerStatusResponse {
	res := &ServerStatusResponse{}
	res.Result = make([]*StatusResponse, 0)

	ServerLock.RLock()
	defer ServerLock.RUnlock()

	for _, v := range idList {
		server := ServerList[v]
		if server == nil {
			continue
		}

		// 获取Host信息，优先使用内存中的数据
		host := server.Host
		if host == nil || (host.MemTotal == 0 && len(host.CPU) == 0) {
			// 尝试从数据库重新加载Host信息
			var hostJSONStr string
			if err := DB.Raw("SELECT host_json FROM servers WHERE id = ?", server.ID).Scan(&hostJSONStr).Error; err == nil && len(hostJSONStr) > 0 {
				tempHost := &model.Host{}
				if err := utils.Json.Unmarshal([]byte(hostJSONStr), tempHost); err == nil {
					tempHost.Initialize()
					host = tempHost
					server.Host = tempHost // 更新内存中的数据
				} else {
					log.Printf("API - 服务器 %s (ID: %d) 解析Host数据失败: %v", server.Name, server.ID, err)
				}
			}
		}

		// 如果仍然没有有效的Host数据，创建空的Host对象
		if host == nil {
			host = &model.Host{}
			host.Initialize()
		} else {
			// 确保已有的Host对象数组已初始化
			host.Initialize()
		}

		// 获取状态数据，优先使用当前状态，没有则使用离线前保存的最后状态
		state := server.State
		if state == nil && server.LastStateBeforeOffline != nil {
			state = server.LastStateBeforeOffline
		}

		ipv4, ipv6, validIP := utils.SplitIPAddr(host.IP)
		info := CommonServerInfo{
			ID:           server.ID,
			Name:         server.Name,
			Tag:          server.Tag,
			LastActive:   server.LastActive.Unix(),
			IPV4:         ipv4,
			IPV6:         ipv6,
			ValidIP:      validIP,
			DisplayIndex: server.DisplayIndex,
			HideForGuest: server.HideForGuest,
		}

		// 构建状态响应
		statusData := &StatusResponse{
			CommonServerInfo: info,
			Host:             host,
			Status:           state,
		}

		res.Result = append(res.Result, statusData)
	}
	res.CommonResponse = CommonResponse{
		Code:    0,
		Message: "success",
	}
	return res
}

// GetStatusByTag 获取传入分组的所有服务器状态信息
func (s *ServerAPIService) GetStatusByTag(tag string) *ServerStatusResponse {
	return s.GetStatusByIDList(ServerTagToIDList[tag])
}

// GetAllStatus 获取所有服务器状态信息
func (s *ServerAPIService) GetAllStatus() *ServerStatusResponse {
	res := &ServerStatusResponse{}
	res.Result = make([]*StatusResponse, 0)
	ServerLock.RLock()
	defer ServerLock.RUnlock()

	for _, v := range ServerList {
		// 获取Host信息，优先使用内存中的数据
		host := v.Host
		if host == nil || (host.MemTotal == 0 && len(host.CPU) == 0) {
			// 尝试从数据库重新加载Host信息
			var hostJSONStr string
			if err := DB.Raw("SELECT host_json FROM servers WHERE id = ?", v.ID).Scan(&hostJSONStr).Error; err == nil && len(hostJSONStr) > 0 {
				tempHost := &model.Host{}
				if err := utils.Json.Unmarshal([]byte(hostJSONStr), tempHost); err == nil {
					tempHost.Initialize()
					host = tempHost
					v.Host = tempHost // 更新内存中的数据
				} else {
					log.Printf("API - 服务器 %s (ID: %d) 解析Host数据失败: %v", v.Name, v.ID, err)
				}
			}
		}

		// 如果仍然没有有效的Host数据，创建空的Host对象
		if host == nil {
			host = &model.Host{}
			host.Initialize()
		} else {
			// 确保已有的Host对象数组已初始化
			host.Initialize()
		}

		// 获取状态数据，优先使用当前状态，没有则使用离线前保存的最后状态
		state := v.State
		if state == nil && v.LastStateBeforeOffline != nil {
			state = v.LastStateBeforeOffline
		}

		ipv4, ipv6, validIP := utils.SplitIPAddr(host.IP)
		info := CommonServerInfo{
			ID:           v.ID,
			Name:         v.Name,
			Tag:          v.Tag,
			LastActive:   v.LastActive.Unix(),
			IPV4:         ipv4,
			IPV6:         ipv6,
			ValidIP:      validIP,
			DisplayIndex: v.DisplayIndex,
			HideForGuest: v.HideForGuest,
		}

		// 构建状态响应
		statusData := &StatusResponse{
			CommonServerInfo: info,
			Host:             host,
			Status:           state,
		}

		res.Result = append(res.Result, statusData)
	}

	res.CommonResponse = CommonResponse{
		Code:    0,
		Message: "success",
	}
	return res
}

// GetListByTag 获取传入分组的所有服务器信息
func (s *ServerAPIService) GetListByTag(tag string) *ServerInfoResponse {
	res := &ServerInfoResponse{}
	res.Result = make([]*CommonServerInfo, 0)

	ServerLock.RLock()
	defer ServerLock.RUnlock()
	for _, v := range ServerTagToIDList[tag] {
		host := ServerList[v].Host
		if host == nil {
			// 使用空对象而不是跳过
			host = &model.Host{}
		}
		ipv4, ipv6, validIP := utils.SplitIPAddr(host.IP)
		info := &CommonServerInfo{
			ID:         v,
			Name:       ServerList[v].Name,
			Tag:        ServerList[v].Tag,
			LastActive: ServerList[v].LastActive.Unix(),
			IPV4:       ipv4,
			IPV6:       ipv6,
			ValidIP:    validIP,
		}
		res.Result = append(res.Result, info)
	}
	res.CommonResponse = CommonResponse{
		Code:    0,
		Message: "success",
	}
	return res
}

// GetAllList 获取所有服务器信息
func (s *ServerAPIService) GetAllList() *ServerInfoResponse {
	res := &ServerInfoResponse{}
	res.Result = make([]*CommonServerInfo, 0)

	ServerLock.RLock()
	defer ServerLock.RUnlock()
	for _, v := range ServerList {
		host := v.Host
		if host == nil {
			// 使用空对象而不是跳过
			host = &model.Host{}
		}
		ipv4, ipv6, validIP := utils.SplitIPAddr(host.IP)
		info := &CommonServerInfo{
			ID:         v.ID,
			Name:       v.Name,
			Tag:        v.Tag,
			LastActive: v.LastActive.Unix(),
			IPV4:       ipv4,
			IPV6:       ipv6,
			ValidIP:    validIP,
		}
		res.Result = append(res.Result, info)
	}
	res.CommonResponse = CommonResponse{
		Code:    0,
		Message: "success",
	}
	return res
}
func (s *ServerAPIService) Register(rs *RegisterServer) *ServerRegisterResponse {
	var serverInfo model.Server
	var err error
	// Populate serverInfo fields
	serverInfo.Name = rs.Name
	serverInfo.Tag = rs.Tag
	serverInfo.Note = rs.Note
	serverInfo.HideForGuest = rs.HideForGuest == "on"
	// Generate a random secret
	serverInfo.Secret, err = utils.GenerateRandomString(18)
	if err != nil {
		return &ServerRegisterResponse{
			CommonResponse: CommonResponse{
				Code:    500,
				Message: "Generate secret failed: " + err.Error(),
			},
			Secret: "",
		}
	}
	// Attempt to save serverInfo in the database
	err = DB.Create(&serverInfo).Error
	if err != nil {
		return &ServerRegisterResponse{
			CommonResponse: CommonResponse{
				Code:    500,
				Message: "Database error: " + err.Error(),
			},
			Secret: "",
		}
	}

	serverInfo.Host = &model.Host{}
	serverInfo.State = &model.HostState{}
	serverInfo.TaskCloseLock = new(sync.Mutex)
	ServerLock.Lock()
	SecretToID[serverInfo.Secret] = serverInfo.ID
	ServerList[serverInfo.ID] = &serverInfo
	ServerTagToIDList[serverInfo.Tag] = append(ServerTagToIDList[serverInfo.Tag], serverInfo.ID)
	ServerLock.Unlock()
	ReSortServer()
	// Successful response
	return &ServerRegisterResponse{
		CommonResponse: CommonResponse{
			Code:    200,
			Message: "Server created successfully",
		},
		Secret: serverInfo.Secret,
	}
}

// GetMonitorHistories 获取监控记录
func (m *MonitorAPIService) GetMonitorHistories(search map[string]any) []*model.MonitorHistory {
	// 检查是否使用BadgerDB
	if Conf != nil && Conf.DatabaseType == "badger" {
		log.Printf("MonitorAPIService.GetMonitorHistories: BadgerDB模式，查询监控历史记录")

		// 从BadgerDB获取监控历史记录
		if db.DB != nil {
			// 获取服务器ID
			var serverID uint64
			if sid, ok := search["server_id"]; ok {
				if sidVal, ok := sid.(uint64); ok {
					serverID = sidVal
				}
			}

			if serverID == 0 {
				log.Printf("MonitorAPIService.GetMonitorHistories: 无效的服务器ID")
				return make([]*model.MonitorHistory, 0)
			}

			// 获取时间范围（默认最近30天）
			endTime := time.Now()
			startTime := endTime.AddDate(0, 0, -30)

			// 从BadgerDB查询监控历史记录
			// 注意：这里需要查询所有监控历史记录，然后按server_id过滤
			monitorOps := db.NewMonitorHistoryOps(db.DB)

			// 获取所有监控历史记录并过滤
			allHistories, err := monitorOps.GetAllMonitorHistoriesInRange(startTime, endTime)
			if err != nil {
				log.Printf("MonitorAPIService.GetMonitorHistories: 从BadgerDB查询失败: %v", err)
				return make([]*model.MonitorHistory, 0)
			}

			// 过滤出指定服务器的记录
			var histories []*model.MonitorHistory
			for _, history := range allHistories {
				if history.ServerID == serverID {
					histories = append(histories, history)
				}
			}

			log.Printf("MonitorAPIService.GetMonitorHistories: 从BadgerDB获取到 %d 条监控历史记录 (服务器ID: %d)", len(histories), serverID)
			return histories
		} else {
			log.Printf("MonitorAPIService.GetMonitorHistories: BadgerDB未初始化")
			return make([]*model.MonitorHistory, 0)
		}
	}

	// 原有的SQLite查询逻辑
	var mhs []*model.MonitorHistory
	if DB == nil {
		log.Printf("MonitorAPIService.GetMonitorHistories: DB未初始化，返回空数组")
		return make([]*model.MonitorHistory, 0)
	}

	// 根据数据库类型选择不同的查询方式
	if Conf.DatabaseType == "badger" {
		// 使用BadgerDB查询监控历史记录
		if db.DB != nil {
			// 获取服务器ID
			var serverID uint64
			if sid, ok := search["server_id"]; ok {
				if sidVal, ok := sid.(uint64); ok {
					serverID = sidVal
				}
			}

			if serverID == 0 {
				log.Printf("MonitorAPIService.GetMonitorHistories: 无效的服务器ID")
				return make([]*model.MonitorHistory, 0)
			}

			// 获取时间范围（默认最近30天）
			endTime := time.Now()
			startTime := endTime.AddDate(0, 0, -30)

			// 从BadgerDB查询监控历史记录
			monitorOps := db.NewMonitorHistoryOps(db.DB)

			// 获取所有监控历史记录并过滤
			allHistories, err := monitorOps.GetAllMonitorHistoriesInRange(startTime, endTime)
			if err != nil {
				log.Printf("MonitorAPIService.GetMonitorHistories: 从BadgerDB查询失败: %v", err)
				return make([]*model.MonitorHistory, 0)
			}

			// 过滤出指定服务器的记录
			var filteredHistories []*model.MonitorHistory
			for _, h := range allHistories {
				if h != nil && h.ServerID == serverID {
					filteredHistories = append(filteredHistories, h)
				}
			}

			log.Printf("BadgerDB: 为服务器 %d 找到 %d 条监控历史记录", serverID, len(filteredHistories))
			return filteredHistories
		} else {
			log.Printf("BadgerDB未初始化，返回空监控记录")
			return make([]*model.MonitorHistory, 0)
		}
	} else if DB != nil {
		// SQLite模式
		if err := DB.Model(&model.MonitorHistory{}).
			Where(search).
			FindInBatches(&mhs, 100, func(tx *gorm.DB, batch int) error {
				return nil
			}).Error; err != nil {
			log.Printf("获取监控记录失败: %v", err)
			// 返回空数组而不是nil
			return make([]*model.MonitorHistory, 0)
		}
	} else {
		// 数据库未初始化，返回空数组
		log.Printf("数据库未初始化，返回空监控记录")
		return make([]*model.MonitorHistory, 0)
	}

	// 确保返回非nil值
	if mhs == nil {
		return make([]*model.MonitorHistory, 0)
	}
	return mhs
}
