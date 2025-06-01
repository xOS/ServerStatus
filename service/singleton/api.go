package singleton

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/xos/serverstatus/db"
	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/pkg/utils"
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
			// 目前BadgerDB还没有ApiTokenOps实现，
			// 后续可以添加ApiTokenOps对象来处理ApiToken
			log.Println("BadgerDB: API令牌功能暂不支持，跳过加载")
			return
		} else {
			log.Println("警告: BadgerDB 未初始化")
			return
		}
	} else {
		// 使用 GORM (SQLite) 加载API令牌
		DB.Find(&tokenList)
	}

	for _, token := range tokenList {
		ApiTokenList[token.Token] = token
		UserIDToApiTokenList[token.UserID] = append(UserIDToApiTokenList[token.UserID], token.Token)
	}
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

func (m *MonitorAPIService) GetMonitorHistories(query map[string]any) *MonitorInfoResponse {
	// 生成缓存键
	cacheKey := fmt.Sprintf("monitor_histories_%v", query)

	// 尝试从缓存获取
	if cached, found := Cache.Get(cacheKey); found {
		if cachedResponse, ok := cached.(*MonitorInfoResponse); ok {
			return cachedResponse
		}
	}

	var (
		resultMap        = make(map[uint64]*MonitorInfo)
		monitorHistories []*model.MonitorHistory
		sortedMonitorIDs []uint64
	)
	res := &MonitorInfoResponse{
		CommonResponse: CommonResponse{
			Code:    0,
			Message: "success",
		},
	}

	// 优化查询：减少查询时间范围，添加合理的限制
	timeRange := 24 * time.Hour // 从72小时减少到24小时
	queryLimit := 5000          // 限制最大记录数，防止数据过大

	if err := DB.Model(&model.MonitorHistory{}).
		Select("monitor_id, created_at, server_id, avg_delay").
		Where(query).
		Where("created_at >= ?", time.Now().Add(-timeRange)).
		Order("monitor_id, created_at DESC").
		Limit(queryLimit).
		Scan(&monitorHistories).Error; err != nil {
		res.CommonResponse = CommonResponse{
			Code:    500,
			Message: err.Error(),
		}
	} else {
		for _, history := range monitorHistories {
			infos, ok := resultMap[history.MonitorID]
			if !ok {
				// 安全检查：确保monitor和server存在
				var monitorName string
				var serverName string

				// 检查ServiceSentinelShared和monitors是否存在
				if ServiceSentinelShared != nil && ServiceSentinelShared.monitors != nil {
					if monitor, exists := ServiceSentinelShared.monitors[history.MonitorID]; exists && monitor != nil {
						monitorName = monitor.Name
					} else {
						monitorName = "Unknown Monitor"
					}
				} else {
					monitorName = "Unknown Monitor"
				}

				// 检查ServerList中是否存在该服务器
				ServerLock.RLock()
				if server, exists := ServerList[history.ServerID]; exists && server != nil {
					serverName = server.Name
				} else {
					// 如果内存中没有，尝试从数据库查询
					var dbServer model.Server
					if err := DB.Where("id = ?", history.ServerID).First(&dbServer).Error; err == nil {
						serverName = dbServer.Name
					} else {
						serverName = "Unknown Server"
					}
				}
				ServerLock.RUnlock()

				infos = &MonitorInfo{
					MonitorID:   history.MonitorID,
					ServerID:    history.ServerID,
					MonitorName: monitorName,
					ServerName:  serverName,
				}
				resultMap[history.MonitorID] = infos
				sortedMonitorIDs = append(sortedMonitorIDs, history.MonitorID)
			}
			// 修复时间戳计算：确保返回正确的毫秒时间戳
			timestampMs := history.CreatedAt.Truncate(time.Minute).Unix() * 1000
			infos.CreatedAt = append(infos.CreatedAt, timestampMs)
			infos.AvgDelay = append(infos.AvgDelay, history.AvgDelay)
		}
		for _, monitorID := range sortedMonitorIDs {
			res.Result = append(res.Result, resultMap[monitorID])
		}

		// 缓存结果（缓存2分钟）
		Cache.Set(cacheKey, res, 2*time.Minute)
	}
	return res
}
