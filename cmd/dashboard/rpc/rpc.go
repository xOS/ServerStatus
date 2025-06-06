package rpc

import (
	"fmt"
	"log"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/xos/serverstatus/model"
	pb "github.com/xos/serverstatus/proto"
	rpcService "github.com/xos/serverstatus/service/rpc"
	"github.com/xos/serverstatus/service/singleton"
)

func ServeRPC(port uint) {
	// 配置 gRPC 服务器选项，防止 goroutine 泄漏
	opts := []grpc.ServerOption{
		// 设置 keepalive 参数，防止僵尸连接
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     5 * time.Minute,  // 连接空闲5分钟后关闭
			MaxConnectionAge:      10 * time.Minute, // 连接最大存活10分钟
			MaxConnectionAgeGrace: 30 * time.Second, // 优雅关闭等待30秒
			Time:                  30 * time.Second, // 每30秒发送keepalive ping
			Timeout:               5 * time.Second,  // keepalive ping超时5秒
		}),
		// 设置 keepalive 强制策略
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second, // 客户端最小keepalive间隔
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
		// 如果已经轮了一整圈又轮到自己，没有合适机器去请求，跳出循环
		for round < 1 || workedServerIndex < endIndex {
			// 如果到了圈尾，再回到圈头，圈数加一，游标重置
			if workedServerIndex >= len(singleton.SortedServerList) {
				workedServerIndex = 0
				round++
				continue
			}
			// 如果服务器不在线，跳过这个服务器
			if singleton.SortedServerList[workedServerIndex].TaskStream == nil {
				workedServerIndex++
				continue
			}
			// 如果此任务不可使用此服务器请求，跳过这个服务器（有些 IPv6 only 开了 NAT64 的机器请求 IPv4 总会出问题）
			if (task.Cover == model.MonitorCoverAll && task.SkipServers[singleton.SortedServerList[workedServerIndex].ID]) ||
				(task.Cover == model.MonitorCoverIgnoreAll && !task.SkipServers[singleton.SortedServerList[workedServerIndex].ID]) {
				workedServerIndex++
				continue
			}
			if task.Cover == model.MonitorCoverIgnoreAll && task.SkipServers[singleton.SortedServerList[workedServerIndex].ID] {
				singleton.SortedServerList[workedServerIndex].TaskStream.Send(task.PB())
				workedServerIndex++
				continue
			}
			if task.Cover == model.MonitorCoverAll && !task.SkipServers[singleton.SortedServerList[workedServerIndex].ID] {
				singleton.SortedServerList[workedServerIndex].TaskStream.Send(task.PB())
				workedServerIndex++
				continue
			}
			// 找到合适机器执行任务，跳出循环
			singleton.SortedServerList[workedServerIndex].TaskStream.Send(task.PB())
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
			if singleton.SortedServerList[i] == nil || singleton.SortedServerList[i].TaskStream == nil {
				continue
			}

			singleton.SortedServerList[i].TaskStream.Send(&pb.Task{Type: model.TaskTypeKeepalive})
		}
	})
}
