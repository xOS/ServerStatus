package rpc

import (
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/xos/serverstatus/model"
	pb "github.com/xos/serverstatus/proto"
	rpcService "github.com/xos/serverstatus/service/rpc"
	"github.com/xos/serverstatus/service/singleton"
)

// isContextCanceledError 检查是否为context canceled错误
func isContextCanceledError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "context canceled") ||
		strings.Contains(errStr, "context deadline exceeded") ||
		strings.Contains(errStr, "Canceled desc = context canceled")
}

// isGRPCTransportError 检查是否为gRPC传输错误
func isGRPCTransportError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "transport: SendHeader called multiple times") ||
		strings.Contains(errStr, "transport: connection is closing") ||
		strings.Contains(errStr, "transport is closing") || // 添加这个错误类型
		strings.Contains(errStr, "transport: the stream is done") ||
		strings.Contains(errStr, "rpc error: code = Unavailable desc = transport is closing") || // 完整匹配
		strings.Contains(errStr, "rpc error: code = Internal desc = transport:")
}

func ServeRPC(port uint) {
	// 配置 gRPC 服务器选项，防止 goroutine 泄漏和连接问题
	opts := []grpc.ServerOption{
		// 优化 keepalive 参数：使用长连接策略，减少重连
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     30 * time.Minute, // 空闲30分钟后断开（防止僵尸连接）
			MaxConnectionAge:      2 * time.Hour,    // 连接最长2小时（定期刷新，防止资源泄漏）
			MaxConnectionAgeGrace: 30 * time.Second, // 优雅关闭时间30秒
			Time:                  30 * time.Second, // 每30秒发送心跳
			Timeout:               10 * time.Second, // 心跳超时10秒
		}),
		// 优化 keepalive 强制策略
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second, // 最小心跳间隔10秒
			PermitWithoutStream: true,             // 允许没有活跃流时发送keepalive
		}),
		// 设置最大接收消息大小
		grpc.MaxRecvMsgSize(4 * 1024 * 1024), // 4MB
		// 设置最大发送消息大小
		grpc.MaxSendMsgSize(4 * 1024 * 1024), // 4MB
		// 设置连接超时
		grpc.ConnectionTimeout(30 * time.Second),
	}

	server := grpc.NewServer(opts...)
	rpcService.ServerHandlerSingleton = rpcService.NewServerHandler()
	pb.RegisterServerServiceServer(server, rpcService.ServerHandlerSingleton)

	listen, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Printf("gRPC服务器监听端口 %d 失败: %v", port, err)
		panic(err)
	}

	log.Printf("gRPC服务器启动在端口 %d，配置了连接管理和超时控制", port)

	if err := server.Serve(listen); err != nil {
		log.Printf("gRPC服务器运行错误: %v", err)
	}
}

func DispatchTask(serviceSentinelDispatchBus <-chan model.Monitor) {
	workedServerIndex := 0
	for task := range serviceSentinelDispatchBus {
		round := 0
		endIndex := workedServerIndex
		singleton.SortedServerLock.RLock()

		// 安全检查：确保SortedServerList不为空
		if len(singleton.SortedServerList) == 0 {
			singleton.SortedServerLock.RUnlock()
			continue
		}

		// 如果已经轮了一整圈又轮到自己，没有合适机器去请求，跳出循环
		for round < 1 || workedServerIndex < endIndex {
			// 如果到了圈尾，再回到圈头，圈数加一，游标重置
			if workedServerIndex >= len(singleton.SortedServerList) {
				workedServerIndex = 0
				round++
				continue
			}

			// 安全检查：确保服务器不为nil
			currentServer := singleton.SortedServerList[workedServerIndex]
			if currentServer == nil {
				workedServerIndex++
				continue
			}

			// 如果服务器不在线，跳过这个服务器
			if currentServer.TaskStream == nil {
				workedServerIndex++
				continue
			}

			// 安全检查：确保SkipServers不为nil
			skipServers := task.SkipServers
			if skipServers == nil {
				skipServers = make(map[uint64]bool)
			}

			// 如果此任务不可使用此服务器请求，跳过这个服务器（有些 IPv6 only 开了 NAT64 的机器请求 IPv4 总会出问题）
			if (task.Cover == model.MonitorCoverAll && skipServers[currentServer.ID]) ||
				(task.Cover == model.MonitorCoverIgnoreAll && !skipServers[currentServer.ID]) {
				workedServerIndex++
				continue
			}

			// 安全发送任务的辅助函数，防止竞态条件下的nil pointer dereference
			sendTask := func() error {
				// 再次检查TaskStream是否为nil（防止竞态条件）
				stream := currentServer.TaskStream
				if stream == nil {
					return nil // 连接已断开，静默跳过
				}
				return stream.Send(task.PB())
			}

			if task.Cover == model.MonitorCoverIgnoreAll && skipServers[currentServer.ID] {
				if err := sendTask(); err != nil {
					// 清理失效的连接
					currentServer.TaskStream = nil
					// 只在非正常网络错误时记录日志
					if !isContextCanceledError(err) && !isGRPCTransportError(err) {
						log.Printf("DispatchTask: 发送任务到服务器 %d 失败: %v", currentServer.ID, err)
					}
				}
				workedServerIndex++
				continue
			}
			if task.Cover == model.MonitorCoverAll && !skipServers[currentServer.ID] {
				if err := sendTask(); err != nil {
					// 清理失效的连接
					currentServer.TaskStream = nil
					// 只在非正常网络错误时记录日志
					if !isContextCanceledError(err) && !isGRPCTransportError(err) {
						log.Printf("DispatchTask: 发送任务到服务器 %d 失败: %v", currentServer.ID, err)
					}
				}
				workedServerIndex++
				continue
			}
			// 找到合适机器执行任务，跳出循环
			if err := sendTask(); err != nil {
				// 清理失效的连接
				currentServer.TaskStream = nil
				// 只在非正常网络错误时记录日志
				if !isContextCanceledError(err) && !isGRPCTransportError(err) {
					log.Printf("DispatchTask: 发送任务到服务器 %d 失败: %v", currentServer.ID, err)
				}
			}
			workedServerIndex++
			break
		}
		singleton.SortedServerLock.RUnlock()
	}
}

func DispatchKeepalive() {
	singleton.Cron.AddFunc("@every 60s", func() {
		singleton.SortedServerLock.RLock()
		defer singleton.SortedServerLock.RUnlock()
		for i := 0; i < len(singleton.SortedServerList); i++ {
			server := singleton.SortedServerList[i]
			if server == nil {
				continue
			}
			// 防止竞态条件下的nil pointer dereference
			stream := server.TaskStream
			if stream == nil {
				continue
			}
			stream.Send(&pb.Task{Type: model.TaskTypeKeepalive})
		}
	})
}
