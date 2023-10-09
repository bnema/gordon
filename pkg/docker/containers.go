package docker

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
)

type ContainerCommandParams struct {
	ContainerName string
	ContainerHost string
	Domain        string
	ServiceName   string
	IsSSL         bool
	EnvVar        string
	ImageName     string
	ImageID       string
	PortMappings  []PortMapping
	Volumes       []string
	Labels        []string
	Network       string
	Restart       string
	Environment   []string
}

type PortMapping struct {
	HostPort      string
	ContainerPort string
	Protocol      string
}

// ParsePortsSpecs receives a slice of strings in the format "hostPort:containerPort/Protocol" and returns a slice of PortMapping structs
func ParsePortsSpecs(portsSpecs []string) ([]PortMapping, error) {
	portMappings := make([]PortMapping, 0, len(portsSpecs))

	for _, portSpec := range portsSpecs {
		parts := strings.Split(portSpec, ":")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid port specification: %s. Format should be hostPort:containerPort[/Protocol]", portSpec)
		}

		// Validate hostPort and containerPort
		hostPort := parts[0]
		if _, err := strconv.Atoi(hostPort); err != nil {
			return nil, fmt.Errorf("invalid host port: %s. Must be a number", hostPort)
		}

		containerParts := strings.Split(parts[1], "/")
		containerPort := containerParts[0]
		if _, err := strconv.Atoi(containerPort); err != nil {
			return nil, fmt.Errorf("invalid exposed port: %s. Must be a number", containerPort)
		}

		// Default protocol is tcp
		protocol := "tcp"
		if len(containerParts) > 1 {
			protocol = containerParts[1]
		}

		portMappings = append(portMappings, PortMapping{
			HostPort:      hostPort,
			ContainerPort: containerPort,
			Protocol:      protocol,
		})
	}

	return portMappings, nil
}

func ContainerCommandParamsToConfig(cmdParams ContainerCommandParams) (*container.Config, error) {
	return &container.Config{
		Image:    cmdParams.ImageName,
		Hostname: cmdParams.ContainerHost,
		Env:      cmdParams.Environment,
		Labels:   map[string]string{},
		Volumes:  map[string]struct{}{},
	}, nil
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

// StopContainer try to stop a container gracefully, if it fails, it will stop it forcefully
func StopContainer(containerID string) error {
	StopContainerGracefully(containerID, 3*time.Second)
	StopContainerRagefully(containerID)
	return nil
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

// RenameContainer renames a container with the given name
func RenameContainer(containerID string, newName string) error {
	// Rename container using the Docker client
	err := dockerCli.ContainerRename(context.Background(), containerID, newName)
	if err != nil {
		return err
	}

	return nil
}

// RemoveContainer
func RemoveContainer(containerID string) error {
	err := dockerCli.ContainerRemove(context.Background(), containerID, types.ContainerRemoveOptions{})
	if err != nil {
		return err
	}

	return nil
}

// StartContainer starts a container
func StartContainer(containerID string) error {
	fmt.Println("Starting container", containerID)
	// Check if the container is not already in a running state
	containerInfo, err := GetContainerInfo(containerID)
	if err != nil {
		return fmt.Errorf("could not get container info: %v", err)
	}

	if containerInfo.State.Running {
		return fmt.Errorf("container is already running")
	}

	// Start container using the Docker client
	err = dockerCli.ContainerStart(context.Background(), containerID, types.ContainerStartOptions{})
	if err != nil {
		return fmt.Errorf("could not start container: %v", err)
	}

	return nil
}

// CreateContainer creates a container with the given parameters
func CreateContainer(cmdParams ContainerCommandParams) (string, error) {
	// Check if the Docker client has been initialized
	CheckIfInitialized()

	// Prepare port bindings
	portBindings := nat.PortMap{}
	for _, portMapping := range cmdParams.PortMappings {
		// Prepare exposed port
		exposedPort := nat.Port(portMapping.ContainerPort + "/" + portMapping.Protocol)
		portBindings[exposedPort] = []nat.PortBinding{
			{
				HostIP:   "0.0.0.0",
				HostPort: portMapping.HostPort,
			},
		}
	}

	// Prepare labels for Traefik
	labels := map[string]string{}
	for _, label := range cmdParams.Labels {
		keyValue := strings.Split(label, "=")
		if len(keyValue) == 2 {
			labels[keyValue[0]] = keyValue[1]
		}
	}

	// Prepare environment variables
	envVars := append([]string{}, cmdParams.Environment...)

	// Create container
	resp, err := dockerCli.ContainerCreate(
		context.Background(),
		&container.Config{
			Image:  cmdParams.ImageName,
			Labels: labels,
			Env:    envVars,
		},
		&container.HostConfig{
			PortBindings: portBindings,
			Binds:        cmdParams.Volumes,
			RestartPolicy: container.RestartPolicy{
				Name: cmdParams.Restart,
			},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				cmdParams.Network: {},
			},
		},
		nil,
		cmdParams.ContainerName,
	)
	if err != nil {
		return "", err
	}

	return resp.ID, nil
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

// UpdateContainerConfig updates the configuration of an existing container.
func UpdateContainerConfig(containerID string, newConfig *container.Config, newHostConfig *container.HostConfig, newNetworkingConfig *network.NetworkingConfig) error {
	ctx := context.Background()
	// 1. Gracefully stop the existing container
	_, err := StopContainerGracefully(containerID, 3*time.Second)
	if err != nil {
		return err
	}

	// 2. Remove the old container
	if err := dockerCli.ContainerRemove(ctx, containerID, types.ContainerRemoveOptions{}); err != nil {
		return err
	}

	// 3. Create a new container with the new configuration
	resp, err := dockerCli.ContainerCreate(
		ctx,
		newConfig,
		&container.HostConfig{},
		&network.NetworkingConfig{},
		nil,
		"",
	)
	if err != nil {
		return err
	}

	// 4. Start the new container
	if err := dockerCli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return err
	}

	return nil
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

// CheckContainerStatus checks if a container with the given name exists and is running
func CheckContainerStatus(containerName string) (bool, bool, error) {

	// List all containers
	containers, err := ListRunningContainers()
	if err != nil {
		return false, false, err
	}

	// Check if a container with the given name exists and is running
	for _, container := range containers {
		for _, name := range container.Names {
			if strings.TrimLeft(name, "/") == containerName {
				return true, container.State == "running", nil
			}
		}
	}

	return false, false, nil
}
