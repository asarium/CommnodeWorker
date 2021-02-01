package main

import (
	"context"
	"github.com/scp-fs2open/CommnodeWorker/docker"
	"github.com/scp-fs2open/CommnodeWorker/servers"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/scp-fs2open/CommnodeWorker/fsoApi"
	pb "github.com/scp-fs2open/CommnodeWorker/grpc"

	"github.com/docker/docker/client"
)

const (
	grpcPort = ":50051"
)

type workerServer struct {
	pb.UnimplementedCommNodeWorkerServer

	dockerClient client.APIClient

	serverManager *servers.ServerManager
}

func installInterruptHandler(handler func()) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func(){
		for _ = range c {
			handler()
		}
	}()
}

func (s *workerServer) StartServer(in *pb.StartRequest, stream pb.CommNodeWorker_StartServerServer) (err error) {
	log.Printf("Starting server with name: %v", in.GetName())

	// We need this quite early so do this first
	server := s.serverManager.CreateServer()
	defer func() {
		// If we error out of here we need to free the port again
		if err != nil {
			server.FreePort()
		}
	}()

	imageName := "scpfs2open/fso-standalone:release"
	serverContainer := docker.NewServerContainer(s.dockerClient, imageName, uint16(server.PortOffset))

	err = serverContainer.Start(stream.Context(), func(progressState uint32, message string) error {
		eventType := pb.ServerEvent_Invalid
		switch progressState {
		case docker.ProgressPulling:
			eventType = pb.ServerEvent_ContainerImagePull
		case docker.ProgressStarting:
			eventType = pb.ServerEvent_ContainerStart
		}

		err := stream.Send(&pb.ServerEvent{Type: eventType, Message: message})
		if err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return
	}

	err = stream.Send(&pb.ServerEvent{Type: pb.ServerEvent_SettingUpServer, Message: imageName})
	if err != nil {
		return
	}

	fsoClient := fsoApi.NewClient(serverContainer.ApiPort)

	err = fsoClient.WaitForOnline(stream.Context(), time.Second*5)
	if err != nil {
		return
	}

	serverName := "CommNode server " + in.Name
	err = fsoClient.SetServerName(stream.Context(), serverName)
	if err != nil {
		return
	}

	err = stream.Send(&pb.ServerEvent{Type: pb.ServerEvent_ServerReady, Message: serverName})
	if err != nil {
		return
	}

	// Kick of the management
	go server.ManageServer(serverContainer, fsoClient)

	return nil
}

func main() {
	dockerOpts, err := docker.GetDockerOptions()
	if err != nil {
		panic(err)
	}

	dockerClient, err := client.NewClientWithOpts(dockerOpts...)
	if err != nil {
		panic(err)
	}
	defer func() {
		err := dockerClient.Close()
		if err != nil {
			panic(err)
		}
	}()

	err = docker.StopOldContainers(dockerClient)
	if err != nil {
		panic(err)
	}

	serverManager := servers.NewServerManager()

	lis, err := net.Listen("tcp", grpcPort)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	unaryErrLogger := func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		resp, err := handler(ctx, req)
		if err != nil {
			log.Printf("method %q failed: %s", info.FullMethod, err)
		}
		return resp, err
	}
	streamErrLogger := func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		err := handler(srv, ss)
		if err != nil {
			log.Printf("method %q failed: %s", info.FullMethod, err)
		}
		return err
	}

	log.Print("Starting up gRPC server")
	s := grpc.NewServer(grpc.UnaryInterceptor(unaryErrLogger), grpc.StreamInterceptor(streamErrLogger))

	installInterruptHandler(func() {
		log.Printf("Caught interrupt. Shutting down...")
		serverManager.Shutdown()

		// Ensure we have some time to shut down servers
		time.Sleep(time.Second * 1)

		s.GracefulStop()
	})

	pb.RegisterCommNodeWorkerServer(s, &workerServer{dockerClient: dockerClient, serverManager: serverManager})
	reflection.Register(s)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
