package rpc

import (
	"fmt"
	"net"

	"google.golang.org/grpc"

	"github.com/xos/serverstatus/model"
	pb "github.com/xos/serverstatus/proto"
	rpcService "github.com/xos/serverstatus/service/rpc"
	"github.com/xos/serverstatus/service/singleton"
)

func ServeRPC(port uint) {
	server := grpc.NewServer()
	pb.RegisterProbeServiceServer(server, &rpcService.ProbeHandler{
		Auth: &rpcService.AuthHandler{},
	})
	listen, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		panic(err)
	}
	server.Serve(listen)
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
