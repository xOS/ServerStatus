package rpc

import (
	"fmt"
	"net"

	"google.golang.org/grpc"

	pb "github.com/xos/serverstatus/proto"
	rpcService "github.com/xos/serverstatus/service/rpc"
)

func ServeRPC(port uint) {
	server := grpc.NewServer()
	pb.RegisterServerServiceServer(server, &rpcService.ServerHandler{
		Auth: &rpcService.AuthHandler{},
	})
	listen, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		panic(err)
	}
	server.Serve(listen)
}
