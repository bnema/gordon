package docker

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

type Config struct {
	Sock         string
	PodmanEnable bool
}

var (
	dockerCli     *client.Client
	currentConfig *Config
)

func InitializeClient(config *Config) error {
	if config.Sock == "" {
		return fmt.Errorf("sock field in Config is empty")
	}

	if _, err := os.Stat(config.Sock); os.IsNotExist(err) {
		return err
	}

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

func CheckIfInitialized() error {
	if currentConfig == nil || currentConfig.Sock == "" {
		return fmt.Errorf("docker client is not initialized or configuration is missing")
	}

	socketPath := currentConfig.Sock
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return fmt.Errorf("sock file does not exist: %s", socketPath)
	}

	if dockerCli == nil {
		return fmt.Errorf("docker client is not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := dockerCli.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return fmt.Errorf("cannot connect to Docker daemon: %s", err)
	}

	return nil
}

func ListContainers(ctx context.Context) ([]types.Container, error) {
	if dockerCli == nil {
		return nil, fmt.Errorf("Docker client is not initialized")
	}

	containers, err := dockerCli.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("error listing containers: %w", err)
	}

	return containers, nil
}
