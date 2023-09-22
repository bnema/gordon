package docker

import (
	"context"
	"time"

	"github.com/docker/docker/api/types"
)

func init() {
}

func ListRunningContainers() ([]types.Container, error) {
	// Check if the Docker client has been initialized
	CheckIfInitialized()
	// List containers using the Docker client
	containers, err := dockerCli.ContainerList(context.Background(), types.ContainerListOptions{})
	if err != nil {
		return nil, err
	}

	return containers, nil
}

// StopContainer attempts to stop a container gracefully with a timeout
// If it doesn't stop, it sets a flag that can be checked to prompt the user.
func StopContainerGracefully(containerID string, timeoutDuration time.Duration) (bool, error) {

	// Start by sending a SIGTERM
	err := dockerCli.ContainerKill(context.Background(), containerID, "SIGTERM")
	if err != nil {
		return false, err
	}

	// Initialize a ticker for timeout
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for elapsed := 0; elapsed < int(timeoutDuration.Seconds()); elapsed++ {
		select {
		case <-ticker.C:
			// Check if the container is still running
			container, err := dockerCli.ContainerInspect(context.Background(), containerID)
			if err != nil {
				return false, err
			}

			// If the container is not running, return true indicating it was stopped
			if !container.State.Running {
				return true, nil
			}
		}
	}

	// Return false, signaling that the container needs to be force-stopped
	return false, nil
}

func StopContainerRagefully(containerID string) error {
	// Start by sending a SIGKILL
	err := dockerCli.ContainerKill(context.Background(), containerID, "SIGKILL")
	if err != nil {
		return err
	}

	return nil
}

func DeleteContainer(containerID string) error {

	// Delete container using the Docker client
	err := dockerCli.ContainerRemove(context.Background(), containerID, types.ContainerRemoveOptions{})
	if err != nil {
		return err
	}

	return nil
}
