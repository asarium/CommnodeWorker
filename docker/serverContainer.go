package docker

import (
	"bufio"
	"context"
	"fmt"
	"github.com/docker/cli/cli/connhelper"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

func GetDockerOptions() ([]client.Opt, error) {
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

func StopOldContainers(docker *client.Client) error {
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

type ServerContainer struct {
	ApiPort uint16
	UdpPort uint16

	dockerClient client.APIClient
	imageName    string
	containerId  string
}

func NewServerContainer(dockerClient client.APIClient, imageName string, portOffset uint16) *ServerContainer {
	return &ServerContainer{
		ApiPort: baseApiPort + portOffset,
		UdpPort: baseUdpPort + portOffset,

		dockerClient: dockerClient,
		imageName:    imageName,
	}
}

const (
	ProgressPulling  = iota
	ProgressStarting = iota
)

const (
	baseApiPort    = 8080
	baseUdpPort    = 7808
	containerLabel = "fso_server"
)

type ContainerProgress = func(progressState uint32, message string) error

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

func (s *ServerContainer) Start(ctx context.Context, progressCb ContainerProgress) error {
	if err := progressCb(ProgressPulling, s.imageName); err != nil {
		return err
	}

	closer, err := s.dockerClient.ImagePull(ctx, s.imageName, types.ImagePullOptions{})
	if err != nil {
		return err
	}
	if err = readDockerReader(closer); err != nil {
		return err
	}

	if err := progressCb(ProgressStarting, s.imageName); err != nil {
		return err
	}

	tcpPortExpose := fmt.Sprintf("%v/tcp", s.ApiPort)
	udpPortExpose := fmt.Sprintf("%v/udp", s.UdpPort)

	timeout := 5

	containerConfig := &container.Config{
		Image:       s.imageName,
		StopTimeout: &timeout,
		ExposedPorts: nat.PortSet{
			nat.Port(tcpPortExpose): struct{}{},
			nat.Port(udpPortExpose): struct{}{},
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
			strconv.FormatUint(uint64(s.UdpPort), 10),
		},
	}
	hostConfig := &container.HostConfig{
		AutoRemove: true,
		PortBindings: nat.PortMap{
			nat.Port(tcpPortExpose): []nat.PortBinding{
				{
					HostIP:   "127.0.0.1",
					HostPort: strconv.FormatUint(uint64(s.ApiPort), 10),
				},
			},
			nat.Port(udpPortExpose): []nat.PortBinding{
				{
					HostIP:   "0.0.0.0",
					HostPort: strconv.FormatUint(uint64(s.UdpPort), 10),
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
	response, err := s.dockerClient.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, "")
	if err != nil {
		return err
	}

	if err := s.dockerClient.ContainerStart(ctx, response.ID, types.ContainerStartOptions{}); err != nil {
		return err
	}

	s.containerId = response.ID

	return nil
}

func (s *ServerContainer) WaitForNotRunning(ctx context.Context) <-chan int64 {
	statusCh, errCh := s.dockerClient.ContainerWait(ctx, s.containerId, container.WaitConditionNotRunning)

	signalChan := make(chan int64)
	go func() {
		select {
		case err := <-errCh:
			if err != nil {
				log.Printf("Caught error while setting up container exit channel: %v", err)
			}
			signalChan <- -1
		case waitStat := <-statusCh:
			if waitStat.Error != nil {
				log.Printf("Error on container exit: %v", waitStat.Error.Message)
				signalChan <- -1
			} else {
				signalChan <- waitStat.StatusCode
			}
		}

		close(signalChan)
	}()

	return signalChan
}

func (s *ServerContainer) StopContainer(ctx context.Context) error {
	return s.dockerClient.ContainerStop(ctx, s.containerId, nil)
}
