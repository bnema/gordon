package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types"
)

func ListRunningContainers() ([]types.Container, error) {
	// Check if the Docker client has been initialized
	if dockerCli == nil {
		return nil, fmt.Errorf("Docker client has not been initialized")
	}

	// List containers using the Docker client
	containers, err := dockerCli.ContainerList(context.Background(), types.ContainerListOptions{})
	if err != nil {
		return nil, err
	}

	return containers, nil
}
