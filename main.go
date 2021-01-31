package main

import (
	"bufio"
	"context"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/go-connections/nat"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/scp-fs2open/CommnodeWorker/fsoApi"
	pb "github.com/scp-fs2open/CommnodeWorker/grpc"

	"github.com/docker/cli/cli/connhelper"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

const (
	grpcPort       = ":50051"
	containerLabel = "fso_server"
)

func readDockerReader(readCloser io.ReadCloser) error {
	scanner := bufio.NewScanner(readCloser)

	for scanner.Scan() {
		log.Printf("Docker: %v", scanner.Text())
	}

	if scanner.Err() != nil {
		return scanner.Err()
	}

	return readCloser.Close()
}

type workerServer struct {
	pb.UnimplementedCommNodeWorkerServer

	dockerClient client.APIClient
}

func (s *workerServer) StartServer(in *pb.StartRequest, stream pb.CommNodeWorker_StartServerServer) error {
	log.Printf("Starting server with name: %v", in.GetName())

	imageName := "scpfs2open/fso-standalone:release"

	err := stream.Send(&pb.ServerEvent{Type: pb.ServerEvent_ContainerImagePull, Message: imageName})
	if err != nil {
		return err
	}

	//closer, err := s.dockerClient.ImagePull(stream.Context(), imageName, types.ImagePullOptions{})
	//if err != nil {
	//	return err
	//}
	//if err = readDockerReader(closer); err != nil {
	//	return err
	//}

	err = stream.Send(&pb.ServerEvent{Type: pb.ServerEvent_ContainerStart, Message: imageName})
	if err != nil {
		return err
	}

	containerConfig := &container.Config{
		Image: imageName,
		ExposedPorts: nat.PortSet{
			"8080/tcp": struct{}{},
			"7809/udp": struct{}{},
		},
		AttachStdout: true,
		AttachStderr: true,
		Volumes: map[string]struct{}{
			"/fso": {},
		},
		Labels: map[string]string{
			containerLabel: "",
		},
		Cmd: []string{
			"-port",
			"7809",
		},
	}
	hostConfig := &container.HostConfig{
		AutoRemove: true,
		PortBindings: nat.PortMap{
			"8080/tcp": []nat.PortBinding{
				{
					HostIP:   "127.0.0.1",
					HostPort: "8080",
				},
			},
			"7809/udp": []nat.PortBinding{
				{
					HostIP:   "0.0.0.0",
					HostPort: "7809",
				},
			},
		},
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: "/data/fso/fs2",
				Target: "/fso",
			},
		},
	}
	response, err := s.dockerClient.ContainerCreate(stream.Context(), containerConfig, hostConfig, nil, nil, "")
	if err != nil {
		return err
	}

	if err := s.dockerClient.ContainerStart(stream.Context(), response.ID, types.ContainerStartOptions{}); err != nil {
		return err
	}

	err = stream.Send(&pb.ServerEvent{Type: pb.ServerEvent_SettingUpServer, Message: imageName})
	if err != nil {
		return err
	}

	fsoClient := fsoApi.NewClient(8080)

	err = fsoClient.WaitForOnline(stream.Context(), time.Second*5)
	if err != nil {
		return err
	}

	serverName := "CommNode server " + in.Name
	err = fsoClient.SetServerName(stream.Context(), serverName)
	if err != nil {
		return err
	}

	err = stream.Send(&pb.ServerEvent{Type: pb.ServerEvent_ServerReady, Message: serverName})
	if err != nil {
		return err
	}
	return nil
}

func getDockerOptions() ([]client.Opt, error) {
	host := os.Getenv("DOCKER_HOST")

	var clientOpts []client.Opt

	if strings.HasPrefix(host, "ssh://") {
		helper, err := connhelper.GetConnectionHelper(host)

		if err != nil {
			return nil, err
		}

		httpClient := &http.Client{
			// No tls
			// No proxy
			Transport: &http.Transport{
				DialContext: helper.Dialer,
			},
		}

		clientOpts = append(clientOpts,
			client.WithHTTPClient(httpClient),
			client.WithHost(helper.Host),
			client.WithDialContext(helper.Dialer),
		)
	} else if len(host) > 0 {
		clientOpts = append(clientOpts,
			client.WithHost(host))
	}

	version := os.Getenv("DOCKER_API_VERSION")

	if version != "" {
		clientOpts = append(clientOpts, client.WithVersion(version))
	} else {
		clientOpts = append(clientOpts, client.WithAPIVersionNegotiation())
	}

	return clientOpts, nil
}

func stopOldContainers(docker *client.Client) error {
	containers, err := docker.ContainerList(context.Background(), types.ContainerListOptions{
		Filters: filters.NewArgs(filters.KeyValuePair{
			Key:   "label",
			Value: containerLabel,
		}),
	})

	if err != nil {
		return err
	}

	for _, fsoContainer := range containers {
		log.Printf("Stopping container %v...", fsoContainer.ID)
		err := docker.ContainerStop(context.Background(), fsoContainer.ID, nil)
		if err != nil {
			return err
		}
	}

	return nil
}

func main() {
	dockerOpts, err := getDockerOptions()
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

	err = stopOldContainers(dockerClient)
	if err != nil {
		panic(err)
	}

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
	pb.RegisterCommNodeWorkerServer(s, &workerServer{dockerClient: dockerClient})
	reflection.Register(s)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
