package docker

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/bnema/gordon/pkg/logger"
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
		logger.Error("Failed to initialize Docker client", "error", "sock field in Config is empty")
		return fmt.Errorf("sock field in Config is empty")
	}

	if _, err := os.Stat(config.Sock); os.IsNotExist(err) {
		logger.Error("Failed to initialize Docker client", "error", err)
		return err
	}

	// For Podman compatibility, we need to ensure we're using the right connection approach
	host := "unix://" + config.Sock
	logger.Debug("Setting up Docker client", "host", host)

	// Create the Docker client with appropriate options
	cli, err := client.NewClientWithOpts(
		client.WithAPIVersionNegotiation(),
		client.WithHost(host),
	)
	if err != nil {
		logger.Error("Failed to initialize Docker client", "error", err)
		return fmt.Errorf("error initializing Docker client: %s", err)
	}

	logger.Debug("Docker client initialized", "socket", config.Sock, "podman_enabled", config.PodmanEnable)
	dockerCli = cli
	currentConfig = config
	return nil
}

func CheckIfInitialized() error {
	if currentConfig == nil || currentConfig.Sock == "" {
		logger.Error("Docker client check failed", "error", "client not initialized or configuration missing")
		return fmt.Errorf("docker client is not initialized or configuration is missing")
	}

	socketPath := currentConfig.Sock
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		logger.Error("Docker client check failed", "error", "socket file not found", "path", socketPath)
		return fmt.Errorf("sock file does not exist: %s", socketPath)
	}

	if dockerCli == nil {
		logger.Error("Docker client check failed", "error", "client is nil")
		return fmt.Errorf("docker client is not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := dockerCli.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		logger.Error("Docker client check failed", "error", err)
		return fmt.Errorf("cannot connect to Docker daemon: %s", err)
	}

	logger.Debug("Docker client check successful")
	return nil
}

func ListContainers(ctx context.Context) ([]types.Container, error) {
	if dockerCli == nil {
		logger.Error("Failed to list containers", "error", "Docker client is not initialized")
		return nil, fmt.Errorf("Docker client is not initialized")
	}

	logger.Debug("Listing containers")
	containers, err := dockerCli.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		logger.Error("Failed to list containers", "error", err)
		return nil, fmt.Errorf("error listing containers: %w", err)
	}

	logger.Debug("Listed containers successfully", "count", len(containers))
	return containers, nil
}
