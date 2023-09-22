package docker

import (
	"context"
	"fmt"
	"log"

	"github.com/docker/docker/client"
)

type ContainerEngineClient interface {
	InitializeClient(*Config) error
	// Add other methods that both Docker and Podman should implement
}
type Config struct {
	Sock         string
	PodmanEnable bool
}

var dockerCli *client.Client

// DockerClient implements the ContainerEngineClient interface for Docker
type DockerClient struct{}

func (d *DockerClient) InitializeClient(config *Config) error {
	// Validate if the Sock field is not empty
	if config.Sock == "" {
		return fmt.Errorf("Sock field in Config is empty")
	}

	// Initialize Docker client
	cli, err := client.NewClientWithOpts(
		client.WithAPIVersionNegotiation(),
		client.WithHost("unix://"+config.Sock), // Prepend "unix://" to the Unix socket path
	)
	if err != nil {
		log.Printf("Error initializing Docker client: %s", err)
		return err
	}
	dockerCli = cli
	return nil
}

func CheckIfInitialized() error {
	if dockerCli == nil {
		return fmt.Errorf("Docker client is not initialized")
	}
	// ping the Docker daemon to check if it's running
	_, err := dockerCli.Ping(context.Background())
	if err != nil {
		return fmt.Errorf("Docker client is not initialized")
	}
	return nil
}
