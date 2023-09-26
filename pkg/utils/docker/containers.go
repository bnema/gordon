package docker

import (
	"bytes"
	"context"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

type ContainerCommandParams struct {
	ContainerName string
	ContainerHost string
	Domain        string
	Https         bool
	EnvVar        string
	ImageName     string
	ImageID       string
	Ports         string
	Data          string
	TraefikLabels []string
	Network       string
	Restart       string
}

// ListRunningContainers lists all running containers
func ListRunningContainers() ([]types.Container, error) {
	// Check if the Docker client has been initialized
	CheckIfInitialized()
	// List containers using the Docker client:
	containers, err := dockerCli.ContainerList(context.Background(), types.ContainerListOptions{All: true})
	if err != nil {
		return nil, err
	}
	return containers, nil
}

// StopContainerGracefully stops a container by sending a SIGTERM and waiting for it to stop
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

// StopContainerRagefully stops a container by sending a SIGKILL
func StopContainerRagefully(containerID string) error {
	// Start by sending a SIGKILL
	err := dockerCli.ContainerKill(context.Background(), containerID, "SIGKILL")
	if err != nil {
		return err
	}

	return nil
}

// DeleteContainer deletes a container
func DeleteContainer(containerID string) error {

	// Delete container using the Docker client
	err := dockerCli.ContainerRemove(context.Background(), containerID, types.ContainerRemoveOptions{})
	if err != nil {
		return err
	}

	return nil
}

// StartContainer starts a container
func StartContainer(containerID string) error {

	// Start container using the Docker client
	err := dockerCli.ContainerStart(context.Background(), containerID, types.ContainerStartOptions{})
	if err != nil {
		return err
	}

	return nil
}

// CreateContainer creates a container with the given parameters
func CreateContainer(cmdParams ContainerCommandParams) error {
	// Initialize Docker client
	dockerCli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return err
	}

	// Prepare container port bindings
	portBindings := nat.PortMap{}
	hostConfig := &container.HostConfig{
		PortBindings: portBindings,
		RestartPolicy: container.RestartPolicy{
			Name: cmdParams.Restart,
		},
	}

	// Define port mapping
	parts := strings.Split(cmdParams.Ports, ":")
	if len(parts) == 2 {
		portBindings[nat.Port(parts[1])] = []nat.PortBinding{
			{HostIP: "0.0.0.0", HostPort: parts[0]},
		}
	}

	// Create container
	resp, err := dockerCli.ContainerCreate(
		context.Background(),
		&container.Config{
			Image:  cmdParams.ImageName,
			Labels: map[string]string{},
		},
		hostConfig,
		nil, nil,
		cmdParams.ContainerName,
	)
	if err != nil {
		return err
	}

	// Start container
	err = dockerCli.ContainerStart(context.Background(), resp.ID, types.ContainerStartOptions{})
	if err != nil {
		return err
	}

	return nil
}

// GetContainerInfo returns information about a container
func GetContainerInfo(containerID string) (types.ContainerJSON, error) {

	// Get container info using the Docker client
	containerInfo, err := dockerCli.ContainerInspect(context.Background(), containerID)
	if err != nil {
		return types.ContainerJSON{}, err
	}

	return containerInfo, nil
}

// GetContainerLogs returns the logs of a container
func GetContainerLogs(containerID string) (string, error) {

	// Get container logs using the Docker client
	containerLogs, err := dockerCli.ContainerLogs(context.Background(), containerID, types.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return "", err
	}

	// Read the logs
	buf := new(bytes.Buffer)
	buf.ReadFrom(containerLogs)
	containerLogsString := buf.String()

	return containerLogsString, nil
}
