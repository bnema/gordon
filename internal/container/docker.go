package container

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/rs/zerolog/log"

	"gordon/pkg/runtime"
)

// DockerRuntime implements the Runtime interface using Docker API
type DockerRuntime struct {
	client *client.Client
}

// NewDockerRuntime creates a new Docker runtime instance
func NewDockerRuntime() (*DockerRuntime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	return &DockerRuntime{
		client: cli,
	}, nil
}

// CreateContainer creates a new container
func (d *DockerRuntime) CreateContainer(ctx context.Context, config *runtime.ContainerConfig) (*runtime.Container, error) {
	// Convert ports to Docker format
	exposedPorts := make(nat.PortSet)
	portBindings := make(nat.PortMap)
	
	for _, port := range config.Ports {
		containerPort := nat.Port(fmt.Sprintf("%d/tcp", port))
		exposedPorts[containerPort] = struct{}{}
		
		// Bind to random available port on host
		portBindings[containerPort] = []nat.PortBinding{
			{
				HostIP:   "0.0.0.0",
				HostPort: "0", // Docker will assign a random available port
			},
		}
	}

	// Create container configuration
	containerConfig := &container.Config{
		Image:        config.Image,
		Env:          config.Env,
		ExposedPorts: exposedPorts,
		WorkingDir:   config.WorkingDir,
		Cmd:          config.Cmd,
		Labels:       config.Labels,
	}

	hostConfig := &container.HostConfig{
		PortBindings: portBindings,
		AutoRemove:   config.AutoRemove,
	}

	// Create the container
	resp, err := d.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, config.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to create container: %w", err)
	}

	log.Info().Str("id", resp.ID).Str("name", config.Name).Str("image", config.Image).Msg("Container created")

	// Inspect the created container to get full details
	return d.InspectContainer(ctx, resp.ID)
}

// StartContainer starts a container
func (d *DockerRuntime) StartContainer(ctx context.Context, containerID string) error {
	err := d.client.ContainerStart(ctx, containerID, container.StartOptions{})
	if err != nil {
		return fmt.Errorf("failed to start container %s: %w", containerID, err)
	}

	log.Info().Str("id", containerID).Msg("Container started")
	return nil
}

// StopContainer stops a container
func (d *DockerRuntime) StopContainer(ctx context.Context, containerID string) error {
	timeout := 30 // 30 seconds timeout
	err := d.client.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
	if err != nil {
		return fmt.Errorf("failed to stop container %s: %w", containerID, err)
	}

	log.Info().Str("id", containerID).Msg("Container stopped")
	return nil
}

// RestartContainer restarts a container
func (d *DockerRuntime) RestartContainer(ctx context.Context, containerID string) error {
	timeout := 30 // 30 seconds timeout
	err := d.client.ContainerRestart(ctx, containerID, container.StopOptions{Timeout: &timeout})
	if err != nil {
		return fmt.Errorf("failed to restart container %s: %w", containerID, err)
	}

	log.Info().Str("id", containerID).Msg("Container restarted")
	return nil
}

// RemoveContainer removes a container
func (d *DockerRuntime) RemoveContainer(ctx context.Context, containerID string, force bool) error {
	err := d.client.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: force})
	if err != nil {
		return fmt.Errorf("failed to remove container %s: %w", containerID, err)
	}

	log.Info().Str("id", containerID).Bool("force", force).Msg("Container removed")
	return nil
}

// ListContainers lists containers
func (d *DockerRuntime) ListContainers(ctx context.Context, all bool) ([]*runtime.Container, error) {
	containers, err := d.client.ContainerList(ctx, container.ListOptions{All: all})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	var result []*runtime.Container
	for _, c := range containers {
		// Extract ports
		var ports []int
		for _, port := range c.Ports {
			if port.PublicPort > 0 {
				ports = append(ports, int(port.PublicPort))
			}
		}

		// Get the primary name (remove leading slash)
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}

		result = append(result, &runtime.Container{
			ID:     c.ID,
			Image:  c.Image,
			Name:   name,
			Status: c.Status,
			Ports:  ports,
			Labels: c.Labels,
		})
	}

	return result, nil
}

// InspectContainer inspects a container
func (d *DockerRuntime) InspectContainer(ctx context.Context, containerID string) (*runtime.Container, error) {
	resp, err := d.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container %s: %w", containerID, err)
	}

	// Extract published ports
	var ports []int
	if resp.NetworkSettings != nil && resp.NetworkSettings.Ports != nil {
		for _, bindings := range resp.NetworkSettings.Ports {
			for _, binding := range bindings {
				if binding.HostPort != "" {
					if port, err := strconv.Atoi(binding.HostPort); err == nil {
						ports = append(ports, port)
					}
				}
			}
		}
	}

	// Get container name (remove leading slash)
	name := strings.TrimPrefix(resp.Name, "/")

	return &runtime.Container{
		ID:     resp.ID,
		Image:  resp.Config.Image,
		Name:   name,
		Status: resp.State.Status,
		Ports:  ports,
		Labels: resp.Config.Labels,
	}, nil
}

// GetContainerLogs gets container logs
func (d *DockerRuntime) GetContainerLogs(ctx context.Context, containerID string, follow bool) (io.ReadCloser, error) {
	logs, err := d.client.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Timestamps: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get logs for container %s: %w", containerID, err)
	}

	return logs, nil
}

// PullImage pulls an image
func (d *DockerRuntime) PullImage(ctx context.Context, imageRef string) error {
	log.Info().Str("image", imageRef).Msg("Pulling image")
	
	reader, err := d.client.ImagePull(ctx, imageRef, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageRef, err)
	}
	defer reader.Close()

	// Read the response to completion (this is required for the pull to complete)
	_, err = io.Copy(io.Discard, reader)
	if err != nil {
		return fmt.Errorf("failed to read pull response for image %s: %w", imageRef, err)
	}

	log.Info().Str("image", imageRef).Msg("Image pulled successfully")
	return nil
}

// RemoveImage removes an image
func (d *DockerRuntime) RemoveImage(ctx context.Context, imageRef string, force bool) error {
	_, err := d.client.ImageRemove(ctx, imageRef, image.RemoveOptions{Force: force})
	if err != nil {
		return fmt.Errorf("failed to remove image %s: %w", imageRef, err)
	}

	log.Info().Str("image", imageRef).Bool("force", force).Msg("Image removed")
	return nil
}

// ListImages lists images
func (d *DockerRuntime) ListImages(ctx context.Context) ([]string, error) {
	images, err := d.client.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	var result []string
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if tag != "<none>:<none>" {
				result = append(result, tag)
			}
		}
	}

	return result, nil
}

// Ping checks if Docker is responsive
func (d *DockerRuntime) Ping(ctx context.Context) error {
	_, err := d.client.Ping(ctx)
	if err != nil {
		return fmt.Errorf("Docker ping failed: %w", err)
	}
	return nil
}

// Version returns Docker version
func (d *DockerRuntime) Version(ctx context.Context) (string, error) {
	version, err := d.client.ServerVersion(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get Docker version: %w", err)
	}
	return version.Version, nil
}

// IsContainerRunning checks if a container is running
func (d *DockerRuntime) IsContainerRunning(ctx context.Context, containerID string) (bool, error) {
	container, err := d.InspectContainer(ctx, containerID)
	if err != nil {
		return false, err
	}
	return container.Status == "running", nil
}

// GetContainerPort gets the host port for a container's internal port
func (d *DockerRuntime) GetContainerPort(ctx context.Context, containerID string, internalPort int) (int, error) {
	resp, err := d.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return 0, fmt.Errorf("failed to inspect container %s: %w", containerID, err)
	}

	if resp.NetworkSettings == nil || resp.NetworkSettings.Ports == nil {
		return 0, fmt.Errorf("no port mappings found for container %s", containerID)
	}

	containerPort := nat.Port(fmt.Sprintf("%d/tcp", internalPort))
	bindings, exists := resp.NetworkSettings.Ports[containerPort]
	if !exists || len(bindings) == 0 {
		return 0, fmt.Errorf("port %d not mapped for container %s", internalPort, containerID)
	}

	hostPort, err := strconv.Atoi(bindings[0].HostPort)
	if err != nil {
		return 0, fmt.Errorf("invalid host port for container %s: %w", containerID, err)
	}

	return hostPort, nil
}