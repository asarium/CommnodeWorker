package servers

import (
	"context"
	"sync"
	"time"
)

type ServerManager struct {
	portMutex sync.Mutex
	freePorts []int32
	nextPort  int32

	managerContext context.Context

	shutdownServers chan struct{}
}

func NewServerManager() *ServerManager {
	return &ServerManager{
		freePorts:      make([]int32, 0),
		nextPort:       0,
		managerContext: context.Background(),
		shutdownServers: make(chan struct{}),
	}
}

func (s *ServerManager) allocatePort() int32 {
	s.portMutex.Lock()
	defer s.portMutex.Unlock()

	if len(s.freePorts) > 0 {
		// Take a port from the free list
		port := s.freePorts[len(s.freePorts)-1]
		s.freePorts = s.freePorts[:len(s.freePorts)-1]
		return port
	}

	// Use a new port
	port := s.nextPort
	s.nextPort += 1
	return port
}

func (s *ServerManager) freePort(port int32) {
	s.portMutex.Lock()
	defer s.portMutex.Unlock()

	s.freePorts = append(s.freePorts, port)
}

func (s *ServerManager) CreateServer() *Server {
	return &Server{
		PortOffset:     s.allocatePort(),
		serverContext:  s.managerContext,
		lastPlayerTime: time.Now(),
		shutdown:       s.shutdownServers,
		freePortCb: func(port int32) {
			s.freePort(port)
		},
	}
}

func (s *ServerManager) Shutdown() {
	// This will cause all the manage loops to exit and shut down their servers
	close(s.shutdownServers)
}
