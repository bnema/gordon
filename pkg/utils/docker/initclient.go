package docker

import (
	"context"
	"fmt"
	"log"

	"github.com/bnema/gordon/internal/app"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
)

var dockerClient *client.Client

func init() {
	// Get Docker configuration from App
	appInstance := app.NewDockerClient()
	dockerSocket := appInstance.DockerSock
	podmanEnable := appInstance.PodmanEnable
	podmanSocket := appInstance.PodmanSock

	// Based on configuration, decide whether to use Docker or Podman
	socketPath := dockerSocket
	if podmanEnable {
		socketPath = podmanSocket
	}

	// Initialize Docker client
	var err error
	dockerClient, err = client.NewClientWithOpts(client.WithHost(fmt.Sprintf("unix://%s", socketPath)), client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatalf("Error initializing Docker client: %v", err)
	}
}
