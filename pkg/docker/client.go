package docker

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

type ContainerEngineClient interface {
	InitializeClient(*Config) error
	// Add other methods that both Docker and Podman should implement
}
type Config struct {
	Sock         string
	PodmanEnable bool
	TLSConfig    *tls.Config
}

var (
	dockerCli     *client.Client
	currentConfig *Config
)

// DockerClient implements the ContainerEngineClient interface for Docker
type DockerClient struct{}

func (d *DockerClient) InitializeClient(config *Config) error {
	// Validate if the Sock field is not empty
	if config.Sock == "" {
		return fmt.Errorf("sock field in Config is empty")
	}

	// Check if the socket file exists if not return an error
	if _, err := os.Stat(config.Sock); os.IsNotExist(err) {
		return err
	}

	// Initialize Docker client
	cli, err := client.NewClientWithOpts(
		client.WithAPIVersionNegotiation(),
		client.WithHost("unix://"+config.Sock),
	)
	if err != nil {
		return fmt.Errorf("error initializing Docker client: %s", err)
	}
	dockerCli = cli
	currentConfig = config
	return nil
}

// CheckIfInitialized checks if the Docker client is initialized
func CheckIfInitialized() error {
	// Check if there is a socket file
	if currentConfig == nil || currentConfig.Sock == "" {
		return fmt.Errorf("docker client is not initialized or configuration is missing")
	}

	socketPath := currentConfig.Sock // Use the package-level variable
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return fmt.Errorf("sock file does not exist: %s", socketPath)
	}

	// Then we check if the dockerCli is nil
	if dockerCli == nil {
		return fmt.Errorf("docker client is not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// More stringent check: try to list containers
	_, err := dockerCli.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return fmt.Errorf("cannot connect to Docker daemon: %s", err)
	}
	return nil
}
