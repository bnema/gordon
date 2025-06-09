package container

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"gordon/pkg/runtime"
	"io"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/go-connections/nat"
	"github.com/rs/zerolog/log"
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

// NewDockerRuntimeWithClient creates a new Docker runtime instance with a custom client (for testing)
func NewDockerRuntimeWithClient(cli *client.Client) *DockerRuntime {
	return &DockerRuntime{
		client: cli,
	}
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

	// Convert volumes to Docker format
	var binds []string
	if config.Volumes != nil {
		for containerPath, volumeName := range config.Volumes {
			bind := fmt.Sprintf("%s:%s", volumeName, containerPath)
			binds = append(binds, bind)
			log.Debug().Str("volume", volumeName).Str("mount_path", containerPath).Msg("Adding volume mount")
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
		Binds:        binds,
		NetworkMode:  container.NetworkMode(config.NetworkMode),
	}

	// Create network configuration for container
	var networkConfig *network.NetworkingConfig
	if config.NetworkMode != "" && config.NetworkMode != "default" && config.NetworkMode != "bridge" {
		networkConfig = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				config.NetworkMode: {
					Aliases: config.Aliases,
				},
			},
		}
		if config.Hostname != "" {
			networkConfig.EndpointsConfig[config.NetworkMode].NetworkID = config.NetworkMode
		}
	}

	// Create the container
	resp, err := d.client.ContainerCreate(ctx, containerConfig, hostConfig, networkConfig, nil, config.Name)
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

// PullImageWithAuth pulls an image with authentication
func (d *DockerRuntime) PullImageWithAuth(ctx context.Context, imageRef, username, password string) error {
	log.Info().Str("image", imageRef).Str("username", username).Msg("Pulling image with authentication")

	// Create authentication configuration
	authConfig := registry.AuthConfig{
		Username: username,
		Password: password,
	}

	// Encode authentication to base64 JSON
	authConfigBytes, err := json.Marshal(authConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal auth config: %w", err)
	}
	authStr := base64.URLEncoding.EncodeToString(authConfigBytes)

	// Pull with authentication
	reader, err := d.client.ImagePull(ctx, imageRef, image.PullOptions{
		RegistryAuth: authStr,
	})
	if err != nil {
		return fmt.Errorf("failed to pull image %s with auth: %w", imageRef, err)
	}
	defer reader.Close()

	// Read the response to completion (this is required for the pull to complete)
	_, err = io.Copy(io.Discard, reader)
	if err != nil {
		return fmt.Errorf("failed to read pull response for image %s: %w", imageRef, err)
	}

	log.Info().Str("image", imageRef).Msg("Image pulled successfully with authentication")
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

// GetImageExposedPorts gets the exposed ports from an image
func (d *DockerRuntime) GetImageExposedPorts(ctx context.Context, imageRef string) ([]int, error) {
	imageInspect, _, err := d.client.ImageInspectWithRaw(ctx, imageRef)
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

	if len(ports) == 0 {
		return nil, fmt.Errorf("no EXPOSE directives found in image %s - please add EXPOSE directive to Dockerfile", imageRef)
	}

	log.Info().Str("image", imageRef).Ints("exposed_ports", ports).Msg("Found exposed ports in image")
	return ports, nil
}

// GetContainerExposedPorts gets all exposed ports from a running container
func (d *DockerRuntime) GetContainerExposedPorts(ctx context.Context, containerID string) ([]int, error) {
	resp, err := d.client.ContainerInspect(ctx, containerID)
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
func (d *DockerRuntime) GetContainerNetworkInfo(ctx context.Context, containerID string) (string, int, error) {
	resp, err := d.client.ContainerInspect(ctx, containerID)
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

	// Get exposed ports from container
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
		return "", 0, fmt.Errorf("no EXPOSE directives found for container %s - please add EXPOSE directive to Dockerfile", containerID)
	}

	// Use the first exposed port
	selectedPort := exposedPorts[0]

	log.Info().
		Str("container_id", containerID).
		Str("ip", containerIP).
		Int("selected_port", selectedPort).
		Ints("exposed_ports", exposedPorts).
		Msg("Container network info detected")

	return containerIP, selectedPort, nil
}

// InspectImageVolumes gets the volume mount points declared in the image
func (d *DockerRuntime) InspectImageVolumes(ctx context.Context, imageRef string) ([]string, error) {
	imageInspect, _, err := d.client.ImageInspectWithRaw(ctx, imageRef)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect image %s: %w", imageRef, err)
	}

	var volumes []string
	if imageInspect.Config != nil && imageInspect.Config.Volumes != nil {
		for volumePath := range imageInspect.Config.Volumes {
			volumes = append(volumes, volumePath)
		}
	}

	if len(volumes) > 0 {
		log.Info().Str("image", imageRef).Strs("volumes", volumes).Msg("Found VOLUME directives in image")
	} else {
		log.Debug().Str("image", imageRef).Msg("No VOLUME directives found in image")
	}

	return volumes, nil
}

// VolumeExists checks if a Docker volume exists
func (d *DockerRuntime) VolumeExists(ctx context.Context, volumeName string) (bool, error) {
	_, err := d.client.VolumeInspect(ctx, volumeName)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to inspect volume %s: %w", volumeName, err)
	}
	return true, nil
}

// CreateVolume creates a new Docker volume
func (d *DockerRuntime) CreateVolume(ctx context.Context, volumeName string) error {
	_, err := d.client.VolumeCreate(ctx, volume.CreateOptions{
		Name: volumeName,
		Labels: map[string]string{
			"gordon.managed": "true",
			"gordon.created": "auto",
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create volume %s: %w", volumeName, err)
	}

	log.Info().Str("volume", volumeName).Msg("Volume created")
	return nil
}

// RemoveVolume removes a Docker volume
func (d *DockerRuntime) RemoveVolume(ctx context.Context, volumeName string, force bool) error {
	err := d.client.VolumeRemove(ctx, volumeName, force)
	if err != nil {
		if errdefs.IsNotFound(err) {
			log.Debug().Str("volume", volumeName).Msg("Volume not found, already removed")
			return nil
		}
		return fmt.Errorf("failed to remove volume %s: %w", volumeName, err)
	}

	log.Info().Str("volume", volumeName).Msg("Volume removed")
	return nil
}

// InspectImageEnv gets the environment variables declared in the image
func (d *DockerRuntime) InspectImageEnv(ctx context.Context, imageRef string) ([]string, error) {
	imageInspect, _, err := d.client.ImageInspectWithRaw(ctx, imageRef)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect image %s: %w", imageRef, err)
	}

	var envVars []string
	if imageInspect.Config != nil && imageInspect.Config.Env != nil {
		envVars = imageInspect.Config.Env
	}

	if len(envVars) > 0 {
		log.Debug().Str("image", imageRef).Strs("env_vars", envVars).Msg("Found ENV directives in image")
	} else {
		log.Debug().Str("image", imageRef).Msg("No ENV directives found in image")
	}

	return envVars, nil
}

// CreateNetwork creates a new Docker network
func (d *DockerRuntime) CreateNetwork(ctx context.Context, name string, options map[string]string) error {
	// Set default driver to bridge if not specified
	driver := "bridge"
	if driverOption, exists := options["driver"]; exists {
		driver = driverOption
	}

	createOptions := network.CreateOptions{
		Driver: driver,
		Labels: map[string]string{
			"gordon.managed": "true",
		},
	}

	// Add any additional options to labels
	for key, value := range options {
		if key != "driver" {
			createOptions.Labels["gordon."+key] = value
		}
	}

	_, err := d.client.NetworkCreate(ctx, name, createOptions)
	if err != nil {
		return fmt.Errorf("failed to create network %s: %w", name, err)
	}

	log.Info().Str("network", name).Str("driver", driver).Msg("Network created")
	return nil
}

// RemoveNetwork removes a Docker network
func (d *DockerRuntime) RemoveNetwork(ctx context.Context, name string) error {
	err := d.client.NetworkRemove(ctx, name)
	if err != nil {
		if errdefs.IsNotFound(err) {
			log.Debug().Str("network", name).Msg("Network not found, already removed")
			return nil
		}
		return fmt.Errorf("failed to remove network %s: %w", name, err)
	}

	log.Info().Str("network", name).Msg("Network removed")
	return nil
}

// ListNetworks lists all Docker networks
func (d *DockerRuntime) ListNetworks(ctx context.Context) ([]*runtime.NetworkInfo, error) {
	networks, err := d.client.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list networks: %w", err)
	}

	var result []*runtime.NetworkInfo
	for _, net := range networks {
		var containers []string
		for containerID := range net.Containers {
			containers = append(containers, containerID)
		}

		result = append(result, &runtime.NetworkInfo{
			ID:         net.ID,
			Name:       net.Name,
			Driver:     net.Driver,
			Containers: containers,
			Labels:     net.Labels,
		})
	}

	return result, nil
}

// NetworkExists checks if a Docker network exists
func (d *DockerRuntime) NetworkExists(ctx context.Context, name string) (bool, error) {
	_, err := d.client.NetworkInspect(ctx, name, network.InspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to inspect network %s: %w", name, err)
	}
	return true, nil
}

// ConnectContainerToNetwork connects a container to a network
func (d *DockerRuntime) ConnectContainerToNetwork(ctx context.Context, containerName, networkName string) error {
	err := d.client.NetworkConnect(ctx, networkName, containerName, &network.EndpointSettings{})
	if err != nil {
		return fmt.Errorf("failed to connect container %s to network %s: %w", containerName, networkName, err)
	}

	log.Info().Str("container", containerName).Str("network", networkName).Msg("Container connected to network")
	return nil
}

// DisconnectContainerFromNetwork disconnects a container from a network
func (d *DockerRuntime) DisconnectContainerFromNetwork(ctx context.Context, containerName, networkName string) error {
	err := d.client.NetworkDisconnect(ctx, networkName, containerName, false)
	if err != nil {
		return fmt.Errorf("failed to disconnect container %s from network %s: %w", containerName, networkName, err)
	}

	log.Info().Str("container", containerName).Str("network", networkName).Msg("Container disconnected from network")
	return nil
}
