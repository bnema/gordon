package docker

import (
	"log"
)

type ContainerEngineClient interface {
	InitializeClient(*Config) error
	// Add other methods that both Docker and Podman should implement
}
type Config struct {
	Sock         string
	PodmanEnable bool
}

var engineClient ContainerEngineClient

// DockerClient implements the ContainerEngineClient interface for Docker
type DockerClient struct{}

func (d *DockerClient) InitializeClient(config *Config) error {
	// Initialize Docker client here
	return nil // Return an error if something goes wrong
}

// PodmanClient implements the ContainerEngineClient interface for Podman
type PodmanClient struct{}

func (p *PodmanClient) InitializeClient(config *Config) error {
	// Initialize Podman client here
	return nil // Return an error if something goes wrong
}
func InitializeEngineClient(config *Config) {
	if config.PodmanEnable {
		engineClient = &PodmanClient{}
	} else {
		engineClient = &DockerClient{}
	}
	if err := engineClient.InitializeClient(config); err != nil {
		log.Fatalf("Error initializing container engine client: %v", err)
	}
}
