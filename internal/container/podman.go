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

// PodmanRuntime implements the Runtime interface using Podman API
// Podman API is compatible with Docker API, so we can reuse the Docker client
type PodmanRuntime struct {
	client *client.Client
}

// NewPodmanRuntime creates a new Podman runtime instance
func NewPodmanRuntime() (*PodmanRuntime, error) {
	// Try to create with auto-detection
	socketPath := getDefaultPodmanSocket(false) // Try root first
	rt, err := createPodmanRuntimeWithSocket(socketPath)
	if err != nil {
		// Try rootless
		socketPath = getDefaultPodmanSocket(true)
		rt, err = createPodmanRuntimeWithSocket(socketPath)
		if err != nil {
			return nil, fmt.Errorf("failed to create Podman runtime: %w", err)
		}
	}
	
	return rt.(*PodmanRuntime), nil
}

// CreateContainer creates a new container
func (p *PodmanRuntime) CreateContainer(ctx context.Context, config *runtime.ContainerConfig) (*runtime.Container, error) {
	// Convert ports to Podman/Docker format
	exposedPorts := make(nat.PortSet)
	portBindings := make(nat.PortMap)
	
	for _, port := range config.Ports {
		containerPort := nat.Port(fmt.Sprintf("%d/tcp", port))
		exposedPorts[containerPort] = struct{}{}
		
		// Bind to random available port on host
		portBindings[containerPort] = []nat.PortBinding{
			{
				HostIP:   "0.0.0.0",
				HostPort: "0", // Podman will assign a random available port
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
	resp, err := p.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, config.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to create container: %w", err)
	}

	log.Info().Str("id", resp.ID).Str("name", config.Name).Str("image", config.Image).Msg("Podman container created")

	// Inspect the created container to get full details
	return p.InspectContainer(ctx, resp.ID)
}

// StartContainer starts a container
func (p *PodmanRuntime) StartContainer(ctx context.Context, containerID string) error {
	err := p.client.ContainerStart(ctx, containerID, container.StartOptions{})
	if err != nil {
		return fmt.Errorf("failed to start container %s: %w", containerID, err)
	}

	log.Info().Str("id", containerID).Msg("Podman container started")
	return nil
}

// StopContainer stops a container
func (p *PodmanRuntime) StopContainer(ctx context.Context, containerID string) error {
	timeout := 30 // 30 seconds timeout
	err := p.client.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
	if err != nil {
		return fmt.Errorf("failed to stop container %s: %w", containerID, err)
	}

	log.Info().Str("id", containerID).Msg("Podman container stopped")
	return nil
}

// RestartContainer restarts a container
func (p *PodmanRuntime) RestartContainer(ctx context.Context, containerID string) error {
	timeout := 30 // 30 seconds timeout
	err := p.client.ContainerRestart(ctx, containerID, container.StopOptions{Timeout: &timeout})
	if err != nil {
		return fmt.Errorf("failed to restart container %s: %w", containerID, err)
	}

	log.Info().Str("id", containerID).Msg("Podman container restarted")
	return nil
}

// RemoveContainer removes a container
func (p *PodmanRuntime) RemoveContainer(ctx context.Context, containerID string, force bool) error {
	err := p.client.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: force})
	if err != nil {
		return fmt.Errorf("failed to remove container %s: %w", containerID, err)
	}

	log.Info().Str("id", containerID).Bool("force", force).Msg("Podman container removed")
	return nil
}

// ListContainers lists containers
func (p *PodmanRuntime) ListContainers(ctx context.Context, all bool) ([]*runtime.Container, error) {
	containers, err := p.client.ContainerList(ctx, container.ListOptions{All: all})
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
func (p *PodmanRuntime) InspectContainer(ctx context.Context, containerID string) (*runtime.Container, error) {
	resp, err := p.client.ContainerInspect(ctx, containerID)
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
func (p *PodmanRuntime) GetContainerLogs(ctx context.Context, containerID string, follow bool) (io.ReadCloser, error) {
	logs, err := p.client.ContainerLogs(ctx, containerID, container.LogsOptions{
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
func (p *PodmanRuntime) PullImage(ctx context.Context, imageRef string) error {
	log.Info().Str("image", imageRef).Msg("Pulling image with Podman")
	
	reader, err := p.client.ImagePull(ctx, imageRef, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageRef, err)
	}
	defer reader.Close()

	// Read the response to completion (this is required for the pull to complete)
	_, err = io.Copy(io.Discard, reader)
	if err != nil {
		return fmt.Errorf("failed to read pull response for image %s: %w", imageRef, err)
	}

	log.Info().Str("image", imageRef).Msg("Image pulled successfully with Podman")
	return nil
}

// RemoveImage removes an image
func (p *PodmanRuntime) RemoveImage(ctx context.Context, imageRef string, force bool) error {
	_, err := p.client.ImageRemove(ctx, imageRef, image.RemoveOptions{Force: force})
	if err != nil {
		return fmt.Errorf("failed to remove image %s: %w", imageRef, err)
	}

	log.Info().Str("image", imageRef).Bool("force", force).Msg("Podman image removed")
	return nil
}

// ListImages lists images
func (p *PodmanRuntime) ListImages(ctx context.Context) ([]string, error) {
	images, err := p.client.ImageList(ctx, image.ListOptions{})
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

// Ping checks if Podman is responsive
func (p *PodmanRuntime) Ping(ctx context.Context) error {
	_, err := p.client.Ping(ctx)
	if err != nil {
		return fmt.Errorf("Podman ping failed: %w", err)
	}
	return nil
}

// Version returns Podman version
func (p *PodmanRuntime) Version(ctx context.Context) (string, error) {
	version, err := p.client.ServerVersion(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get Podman version: %w", err)
	}
	return version.Version, nil
}

// IsContainerRunning checks if a container is running
func (p *PodmanRuntime) IsContainerRunning(ctx context.Context, containerID string) (bool, error) {
	container, err := p.InspectContainer(ctx, containerID)
	if err != nil {
		return false, err
	}
	return container.Status == "running", nil
}

// GetContainerPort gets the host port for a container's internal port
func (p *PodmanRuntime) GetContainerPort(ctx context.Context, containerID string, internalPort int) (int, error) {
	resp, err := p.client.ContainerInspect(ctx, containerID)
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

// GetImageExposedPorts gets the exposed ports from an image
func (p *PodmanRuntime) GetImageExposedPorts(ctx context.Context, imageRef string) ([]int, error) {
	imageInspect, err := p.client.ImageInspect(ctx, imageRef)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect image %s: %w", imageRef, err)
	}

	var ports []int
	if imageInspect.Config != nil && imageInspect.Config.ExposedPorts != nil {
		for portSpec := range imageInspect.Config.ExposedPorts {
			// Parse port from format like "80/tcp"
			portStr := strings.Split(string(portSpec), "/")[0]
			if port, err := strconv.Atoi(portStr); err == nil {
				ports = append(ports, port)
			}
		}
	}

	// If no exposed ports found, return common web ports as fallback
	if len(ports) == 0 {
		log.Warn().Str("image", imageRef).Msg("No EXPOSE directives found in image, using common web ports")
		return []int{80, 8080, 3000}, nil
	}

	log.Info().Str("image", imageRef).Ints("exposed_ports", ports).Msg("Found exposed ports in image")
	return ports, nil
}

// GetContainerExposedPorts gets all exposed ports from a running container
func (p *PodmanRuntime) GetContainerExposedPorts(ctx context.Context, containerID string) ([]int, error) {
	resp, err := p.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container %s: %w", containerID, err)
	}

	var ports []int
	if resp.NetworkSettings != nil && resp.NetworkSettings.Ports != nil {
		for portSpec := range resp.NetworkSettings.Ports {
			// Parse port from format like "80/tcp"
			portStr := strings.Split(string(portSpec), "/")[0]
			if port, err := strconv.Atoi(portStr); err == nil {
				ports = append(ports, port)
			}
		}
	}

	return ports, nil
}

// GetContainerNetworkInfo gets container's internal IP and automatically detects the best port to use
func (p *PodmanRuntime) GetContainerNetworkInfo(ctx context.Context, containerID string) (string, int, error) {
	resp, err := p.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", 0, fmt.Errorf("failed to inspect container %s: %w", containerID, err)
	}

	// Get container's internal IP address
	var containerIP string
	if resp.NetworkSettings != nil && resp.NetworkSettings.Networks != nil {
		// Use the first available network (usually bridge or custom network)
		for _, network := range resp.NetworkSettings.Networks {
			if network.IPAddress != "" {
				containerIP = network.IPAddress
				break
			}
		}
	}

	if containerIP == "" {
		return "", 0, fmt.Errorf("no IP address found for container %s", containerID)
	}

	// Get exposed ports and select the best one (Traefik-style logic)
	var exposedPorts []int
	if resp.NetworkSettings != nil && resp.NetworkSettings.Ports != nil {
		for portSpec := range resp.NetworkSettings.Ports {
			// Parse port from format like "80/tcp"
			portStr := strings.Split(string(portSpec), "/")[0]
			if port, err := strconv.Atoi(portStr); err == nil {
				exposedPorts = append(exposedPorts, port)
			}
		}
	}

	if len(exposedPorts) == 0 {
		return "", 0, fmt.Errorf("no exposed ports found for container %s", containerID)
	}

	// Sort ports and select the lowest one (Traefik strategy)
	var selectedPort int
	if len(exposedPorts) == 1 {
		selectedPort = exposedPorts[0]
	} else {
		// Find the lowest port number
		selectedPort = exposedPorts[0]
		for _, port := range exposedPorts {
			if port < selectedPort {
				selectedPort = port
			}
		}
	}

	log.Info().
		Str("container_id", containerID).
		Str("ip", containerIP).
		Int("selected_port", selectedPort).
		Ints("available_ports", exposedPorts).
		Msg("Podman container network info detected")

	return containerIP, selectedPort, nil
}