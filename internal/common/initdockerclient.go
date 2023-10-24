package common

import (
	"log"

	"github.com/bnema/gordon/pkg/docker"
)

func DockerInit(cc *ContainerEngineConfig) {
	dockerClient := docker.DockerClient{}
	err := dockerClient.InitializeClient(NewDockerConfig(cc))
	if err != nil {
		log.Printf("Error: %s", err)
	}
}

// NewDockerConfig creates and returns a new Docker client configuration
func NewDockerConfig(cc *ContainerEngineConfig) *docker.Config {
	if cc.Podman {
		return &docker.Config{
			Sock:         cc.PodmanSock,
			PodmanEnable: true,
		}
	}
	return &docker.Config{
		Sock:         cc.Sock,
		PodmanEnable: false,
	}
}
