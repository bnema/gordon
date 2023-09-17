package docker

import (
	"fmt"
	"log"

	"github.com/docker/docker/client"
)

type Config struct {
	DockerSock   string
	PodmanEnable bool
	PodmanSock   string
}

var dockerClient *client.Client

func InitializeDockerClient(config *Config) {
	// Based on configuration, decide whether to use Docker or Podman
	socketPath := config.DockerSock
	if config.PodmanEnable {
		socketPath = config.PodmanSock
	}

	// Initialize Docker client
	var err error
	dockerClient, err = client.NewClientWithOpts(client.WithHost(fmt.Sprintf("unix://%s", socketPath)), client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatalf("Error initializing Docker client: %v", err)
	}
}
