package servers

import (
	"context"
	"github.com/scp-fs2open/CommnodeWorker/docker"
	"github.com/scp-fs2open/CommnodeWorker/fsoApi"
	"log"
	"time"
)

const (
	// 5 Minutes should be enough for the requester to join a game
	noPlayerTimeout = time.Minute * 5
)

type freePortCallback = func(port int32)

type Server struct {
	PortOffset int32

	serverContext context.Context

	container *docker.ServerContainer

	serverApi *fsoApi.Client

	lastPlayerTime time.Time

	freePortCb freePortCallback

	shutdown <-chan struct{}
}

func (s *Server) stopServer() {
	log.Printf("Shutting down server")
	err := s.container.StopContainer(s.serverContext)
	if err != nil {
		log.Printf("Caught error while stopping container: %v", err)
	}
}

func (s *Server) checkPlayerCount() bool {
	log.Printf("Checking player status of server")
	players, err := s.serverApi.GetPlayers(s.serverContext)

	if err != nil {
		log.Printf("Caught error while checking player count: %v", err)
		return true
	}

	now := time.Now()

	// Check if we currently have some players
	if len(players) > 0 {
		// We are active!
		s.lastPlayerTime = now
		return true
	}

	// Check if we ran into our timeout
	if now.Sub(s.lastPlayerTime) <= noPlayerTimeout {
		// No players are active but we still have time left
		return true
	}

	// No one is there...
	s.stopServer()

	// Server was stopped, we can stop the management goroutine
	return false
}

func (s *Server) ManageServer(container *docker.ServerContainer, serverApi *fsoApi.Client) {
	s.container = container
	s.serverApi = serverApi

	containerExit := s.container.WaitForNotRunning(s.serverContext)

	alive := true
	for alive {
		alive = false

		select {
		case <-s.shutdown:
			// We were stopped forcefully so shut down the server
			s.stopServer()
			break
		case exitCode := <-containerExit:
			// Stop the management coroutine
			log.Printf("Container exited with code %v", exitCode)
			break
		case <-time.After(30 * time.Second):
			if !s.checkPlayerCount() {
				break
			}
			// This is the only case where we stay in the loop
			alive = true
		}
	}

	s.FreePort()
}

func (s *Server) FreePort() {
	go s.freePortCb(s.PortOffset)
	s.PortOffset = -1
}
