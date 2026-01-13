// Package docker implements the container runtime adapter using Docker API.
package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/bnema/zerowrap"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"

	"gordon/internal/domain"
)

// Runtime implements the ContainerRuntime interface using Docker API.
type Runtime struct {
	client *client.Client
}

// NewRuntime creates a new Docker runtime instance.
func NewRuntime() (*Runtime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	return &Runtime{
		client: cli,
	}, nil
}

// NewRuntimeWithClient creates a new Docker runtime instance with a custom client (for testing).
func NewRuntimeWithClient(cli *client.Client) *Runtime {
	return &Runtime{
		client: cli,
	}
}

// CreateContainer creates a new container.
func (r *Runtime) CreateContainer(ctx context.Context, config *domain.ContainerConfig) (*domain.Container, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "CreateContainer",
		"container_name":      config.Name,
		"image":               config.Image,
	})
	log := zerowrap.FromCtx(ctx)

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
			log.Debug().Str("volume", volumeName).Str("mount_path", containerPath).Msg("adding volume mount")
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
	resp, err := r.client.ContainerCreate(ctx, containerConfig, hostConfig, networkConfig, nil, config.Name)
	if err != nil {
		return nil, log.WrapErr(err, "failed to create container")
	}

	log.Info().Str(zerowrap.FieldEntityID, resp.ID).Msg("container created")

	// Inspect the created container to get full details
	return r.InspectContainer(ctx, resp.ID)
}

// StartContainer starts a container.
func (r *Runtime) StartContainer(ctx context.Context, containerID string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:    "adapter",
		zerowrap.FieldAdapter:  "docker",
		zerowrap.FieldAction:   "StartContainer",
		zerowrap.FieldEntityID: containerID,
	})
	log := zerowrap.FromCtx(ctx)

	err := r.client.ContainerStart(ctx, containerID, container.StartOptions{})
	if err != nil {
		return log.WrapErr(err, "failed to start container")
	}

	log.Info().Msg("container started")
	return nil
}

// StopContainer stops a container.
func (r *Runtime) StopContainer(ctx context.Context, containerID string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:    "adapter",
		zerowrap.FieldAdapter:  "docker",
		zerowrap.FieldAction:   "StopContainer",
		zerowrap.FieldEntityID: containerID,
	})
	log := zerowrap.FromCtx(ctx)

	timeout := 30 // 30 seconds timeout
	err := r.client.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
	if err != nil {
		return log.WrapErr(err, "failed to stop container")
	}

	log.Info().Msg("container stopped")
	return nil
}

// RestartContainer restarts a container.
func (r *Runtime) RestartContainer(ctx context.Context, containerID string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:    "adapter",
		zerowrap.FieldAdapter:  "docker",
		zerowrap.FieldAction:   "RestartContainer",
		zerowrap.FieldEntityID: containerID,
	})
	log := zerowrap.FromCtx(ctx)

	timeout := 30 // 30 seconds timeout
	err := r.client.ContainerRestart(ctx, containerID, container.StopOptions{Timeout: &timeout})
	if err != nil {
		return log.WrapErr(err, "failed to restart container")
	}

	log.Info().Msg("container restarted")
	return nil
}

// RemoveContainer removes a container.
func (r *Runtime) RemoveContainer(ctx context.Context, containerID string, force bool) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:    "adapter",
		zerowrap.FieldAdapter:  "docker",
		zerowrap.FieldAction:   "RemoveContainer",
		zerowrap.FieldEntityID: containerID,
		"force":                force,
	})
	log := zerowrap.FromCtx(ctx)

	err := r.client.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: force})
	if err != nil {
		return log.WrapErr(err, "failed to remove container")
	}

	log.Info().Msg("container removed")
	return nil
}

// RenameContainer renames a container.
func (r *Runtime) RenameContainer(ctx context.Context, containerID, newName string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:    "adapter",
		zerowrap.FieldAdapter:  "docker",
		zerowrap.FieldAction:   "RenameContainer",
		zerowrap.FieldEntityID: containerID,
		"new_name":             newName,
	})
	log := zerowrap.FromCtx(ctx)

	err := r.client.ContainerRename(ctx, containerID, newName)
	if err != nil {
		return log.WrapErr(err, "failed to rename container")
	}

	log.Info().Msg("container renamed")
	return nil
}

// ListContainers lists containers.
func (r *Runtime) ListContainers(ctx context.Context, all bool) ([]*domain.Container, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "ListContainers",
		"all":                 all,
	})
	log := zerowrap.FromCtx(ctx)

	containers, err := r.client.ContainerList(ctx, container.ListOptions{All: all})
	if err != nil {
		return nil, log.WrapErr(err, "failed to list containers")
	}

	var result []*domain.Container
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

		result = append(result, &domain.Container{
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

// InspectContainer inspects a container.
func (r *Runtime) InspectContainer(ctx context.Context, containerID string) (*domain.Container, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:    "adapter",
		zerowrap.FieldAdapter:  "docker",
		zerowrap.FieldAction:   "InspectContainer",
		zerowrap.FieldEntityID: containerID,
	})
	log := zerowrap.FromCtx(ctx)

	resp, err := r.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, log.WrapErr(err, "failed to inspect container")
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

	return &domain.Container{
		ID:     resp.ID,
		Image:  resp.Config.Image,
		Name:   name,
		Status: resp.State.Status,
		Ports:  ports,
		Labels: resp.Config.Labels,
	}, nil
}

// GetContainerLogs gets container logs.
func (r *Runtime) GetContainerLogs(ctx context.Context, containerID string, follow bool) (io.ReadCloser, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:    "adapter",
		zerowrap.FieldAdapter:  "docker",
		zerowrap.FieldAction:   "GetContainerLogs",
		zerowrap.FieldEntityID: containerID,
	})
	log := zerowrap.FromCtx(ctx)

	logs, err := r.client.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Timestamps: true,
	})
	if err != nil {
		return nil, log.WrapErr(err, "failed to get container logs")
	}

	return logs, nil
}

// PullImage pulls an image.
func (r *Runtime) PullImage(ctx context.Context, imageRef string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "PullImage",
		"image":               imageRef,
	})
	log := zerowrap.FromCtx(ctx)

	log.Info().Msg("pulling image")

	reader, err := r.client.ImagePull(ctx, imageRef, image.PullOptions{})
	if err != nil {
		return log.WrapErr(err, "failed to pull image")
	}
	defer reader.Close()

	// Read the response to completion (this is required for the pull to complete)
	_, err = io.Copy(io.Discard, reader)
	if err != nil {
		return log.WrapErr(err, "failed to read pull response")
	}

	log.Info().Msg("image pulled successfully")
	return nil
}

// PullImageWithAuth pulls an image with authentication.
func (r *Runtime) PullImageWithAuth(ctx context.Context, imageRef, username, password string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "PullImageWithAuth",
		"image":               imageRef,
		"username":            username,
	})
	log := zerowrap.FromCtx(ctx)

	log.Info().Msg("pulling image with authentication")

	// Extract registry server address from image reference
	// e.g., "registry.bnema.dev/image:tag" -> "registry.bnema.dev"
	serverAddress := imageRef
	if idx := strings.Index(imageRef, "/"); idx > 0 {
		serverAddress = imageRef[:idx]
	}

	// Create authentication configuration
	authConfig := registry.AuthConfig{
		Username:      username,
		Password:      password,
		ServerAddress: serverAddress,
	}

	// Encode authentication to base64 JSON
	// Note: Docker API accepts both StdEncoding and URLEncoding, but Podman
	// may be more strict. Using StdEncoding for maximum compatibility.
	authConfigBytes, err := json.Marshal(authConfig)
	if err != nil {
		return log.WrapErr(err, "failed to marshal auth config")
	}
	authStr := base64.StdEncoding.EncodeToString(authConfigBytes)

	log.Debug().
		Str("server_address", serverAddress).
		Str("auth_json", string(authConfigBytes)).
		Msg("auth config for pull")

	// Pull with authentication
	reader, err := r.client.ImagePull(ctx, imageRef, image.PullOptions{
		RegistryAuth: authStr,
	})
	if err != nil {
		return log.WrapErr(err, "failed to pull image with auth")
	}
	defer reader.Close()

	// Read the response to completion (this is required for the pull to complete)
	_, err = io.Copy(io.Discard, reader)
	if err != nil {
		return log.WrapErr(err, "failed to read pull response")
	}

	log.Info().Msg("image pulled successfully with authentication")
	return nil
}

// RemoveImage removes an image.
func (r *Runtime) RemoveImage(ctx context.Context, imageRef string, force bool) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "RemoveImage",
		"image":               imageRef,
		"force":               force,
	})
	log := zerowrap.FromCtx(ctx)

	_, err := r.client.ImageRemove(ctx, imageRef, image.RemoveOptions{Force: force})
	if err != nil {
		return log.WrapErr(err, "failed to remove image")
	}

	log.Info().Msg("image removed")
	return nil
}

// ListImages lists images.
func (r *Runtime) ListImages(ctx context.Context) ([]string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "ListImages",
	})
	log := zerowrap.FromCtx(ctx)

	images, err := r.client.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return nil, log.WrapErr(err, "failed to list images")
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

// Ping checks if Docker is responsive.
func (r *Runtime) Ping(ctx context.Context) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "Ping",
	})
	log := zerowrap.FromCtx(ctx)

	_, err := r.client.Ping(ctx)
	if err != nil {
		return log.WrapErr(err, "Docker ping failed")
	}
	return nil
}

// Version returns Docker version.
func (r *Runtime) Version(ctx context.Context) (string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "Version",
	})
	log := zerowrap.FromCtx(ctx)

	version, err := r.client.ServerVersion(ctx)
	if err != nil {
		return "", log.WrapErr(err, "failed to get Docker version")
	}
	return version.Version, nil
}

// IsContainerRunning checks if a container is running.
func (r *Runtime) IsContainerRunning(ctx context.Context, containerID string) (bool, error) {
	ctr, err := r.InspectContainer(ctx, containerID)
	if err != nil {
		return false, err
	}
	return ctr.Status == string(domain.ContainerStatusRunning), nil
}

// GetContainerPort gets the host port for a container's internal port.
func (r *Runtime) GetContainerPort(ctx context.Context, containerID string, internalPort int) (int, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:    "adapter",
		zerowrap.FieldAdapter:  "docker",
		zerowrap.FieldAction:   "GetContainerPort",
		zerowrap.FieldEntityID: containerID,
		"internal_port":        internalPort,
	})
	log := zerowrap.FromCtx(ctx)

	resp, err := r.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return 0, log.WrapErr(err, "failed to inspect container")
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

// GetImageExposedPorts gets the exposed ports from an image.
func (r *Runtime) GetImageExposedPorts(ctx context.Context, imageRef string) ([]int, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "GetImageExposedPorts",
		"image":               imageRef,
	})
	log := zerowrap.FromCtx(ctx)

	imageInspect, err := r.client.ImageInspect(ctx, imageRef)
	if err != nil {
		return nil, log.WrapErr(err, "failed to inspect image")
	}

	var ports []int
	if imageInspect.Config != nil && imageInspect.Config.ExposedPorts != nil {
		for portSpec := range imageInspect.Config.ExposedPorts {
			// Parse port from format like "80/tcp"
			portStr := strings.Split(portSpec, "/")[0]
			if port, err := strconv.Atoi(portStr); err == nil {
				ports = append(ports, port)
			}
		}
	}

	if len(ports) == 0 {
		return nil, fmt.Errorf("no EXPOSE directives found in image %s - please add EXPOSE directive to Dockerfile", imageRef)
	}

	log.Info().Ints("exposed_ports", ports).Msg("found exposed ports in image")
	return ports, nil
}

// GetContainerExposedPorts gets all exposed ports from a running container.
func (r *Runtime) GetContainerExposedPorts(ctx context.Context, containerID string) ([]int, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:    "adapter",
		zerowrap.FieldAdapter:  "docker",
		zerowrap.FieldAction:   "GetContainerExposedPorts",
		zerowrap.FieldEntityID: containerID,
	})
	log := zerowrap.FromCtx(ctx)

	resp, err := r.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, log.WrapErr(err, "failed to inspect container")
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

// GetContainerNetworkInfo gets container's internal IP and automatically detects the best port to use.
func (r *Runtime) GetContainerNetworkInfo(ctx context.Context, containerID string) (string, int, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:    "adapter",
		zerowrap.FieldAdapter:  "docker",
		zerowrap.FieldAction:   "GetContainerNetworkInfo",
		zerowrap.FieldEntityID: containerID,
	})
	log := zerowrap.FromCtx(ctx)

	resp, err := r.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", 0, log.WrapErr(err, "failed to inspect container")
	}

	// Get container's internal IP address
	var containerIP string
	if resp.NetworkSettings != nil && resp.NetworkSettings.Networks != nil {
		// Use the first available network (usually bridge or custom network)
		for _, net := range resp.NetworkSettings.Networks {
			if net.IPAddress != "" {
				containerIP = net.IPAddress
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
		Str("ip", containerIP).
		Int("selected_port", selectedPort).
		Ints("exposed_ports", exposedPorts).
		Msg("container network info detected")

	return containerIP, selectedPort, nil
}

// InspectImageVolumes gets the volume mount points declared in the image.
func (r *Runtime) InspectImageVolumes(ctx context.Context, imageRef string) ([]string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "InspectImageVolumes",
		"image":               imageRef,
	})
	log := zerowrap.FromCtx(ctx)

	imageInspect, err := r.client.ImageInspect(ctx, imageRef)
	if err != nil {
		return nil, log.WrapErr(err, "failed to inspect image")
	}

	var volumes []string
	if imageInspect.Config != nil && imageInspect.Config.Volumes != nil {
		for volumePath := range imageInspect.Config.Volumes {
			volumes = append(volumes, volumePath)
		}
	}

	if len(volumes) > 0 {
		log.Info().Strs("volumes", volumes).Msg("found VOLUME directives in image")
	} else {
		log.Debug().Msg("no VOLUME directives found in image")
	}

	return volumes, nil
}

// VolumeExists checks if a Docker volume exists.
func (r *Runtime) VolumeExists(ctx context.Context, volumeName string) (bool, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "VolumeExists",
		"volume":              volumeName,
	})
	log := zerowrap.FromCtx(ctx)

	_, err := r.client.VolumeInspect(ctx, volumeName)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return false, nil
		}
		return false, log.WrapErr(err, "failed to inspect volume")
	}
	return true, nil
}

// CreateVolume creates a new Docker volume.
func (r *Runtime) CreateVolume(ctx context.Context, volumeName string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "CreateVolume",
		"volume":              volumeName,
	})
	log := zerowrap.FromCtx(ctx)

	_, err := r.client.VolumeCreate(ctx, volume.CreateOptions{
		Name: volumeName,
		Labels: map[string]string{
			"gordon.managed": "true",
			"gordon.created": "auto",
		},
	})
	if err != nil {
		return log.WrapErr(err, "failed to create volume")
	}

	log.Info().Msg("volume created")
	return nil
}

// RemoveVolume removes a Docker volume.
func (r *Runtime) RemoveVolume(ctx context.Context, volumeName string, force bool) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "RemoveVolume",
		"volume":              volumeName,
		"force":               force,
	})
	log := zerowrap.FromCtx(ctx)

	err := r.client.VolumeRemove(ctx, volumeName, force)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			log.Debug().Msg("volume not found, already removed")
			return nil
		}
		return log.WrapErr(err, "failed to remove volume")
	}

	log.Info().Msg("volume removed")
	return nil
}

// InspectImageEnv gets the environment variables declared in the image.
func (r *Runtime) InspectImageEnv(ctx context.Context, imageRef string) ([]string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "InspectImageEnv",
		"image":               imageRef,
	})
	log := zerowrap.FromCtx(ctx)

	imageInspect, err := r.client.ImageInspect(ctx, imageRef)
	if err != nil {
		return nil, log.WrapErr(err, "failed to inspect image")
	}

	var envVars []string
	if imageInspect.Config != nil && imageInspect.Config.Env != nil {
		envVars = imageInspect.Config.Env
	}

	if len(envVars) > 0 {
		log.Debug().Strs("env_vars", envVars).Msg("found ENV directives in image")
	} else {
		log.Debug().Msg("no ENV directives found in image")
	}

	return envVars, nil
}

// CreateNetwork creates a new Docker network.
func (r *Runtime) CreateNetwork(ctx context.Context, name string, options map[string]string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "CreateNetwork",
		"network":             name,
	})
	log := zerowrap.FromCtx(ctx)

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

	_, err := r.client.NetworkCreate(ctx, name, createOptions)
	if err != nil {
		return log.WrapErr(err, "failed to create network")
	}

	log.Info().Str("driver", driver).Msg("network created")
	return nil
}

// RemoveNetwork removes a Docker network.
func (r *Runtime) RemoveNetwork(ctx context.Context, name string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "RemoveNetwork",
		"network":             name,
	})
	log := zerowrap.FromCtx(ctx)

	err := r.client.NetworkRemove(ctx, name)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			log.Debug().Msg("network not found, already removed")
			return nil
		}
		return log.WrapErr(err, "failed to remove network")
	}

	log.Info().Msg("network removed")
	return nil
}

// ListNetworks lists all Docker networks.
func (r *Runtime) ListNetworks(ctx context.Context) ([]*domain.NetworkInfo, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "ListNetworks",
	})
	log := zerowrap.FromCtx(ctx)

	networks, err := r.client.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil, log.WrapErr(err, "failed to list networks")
	}

	var result []*domain.NetworkInfo
	for _, net := range networks {
		var containers []string
		for containerID := range net.Containers {
			containers = append(containers, containerID)
		}

		result = append(result, &domain.NetworkInfo{
			ID:         net.ID,
			Name:       net.Name,
			Driver:     net.Driver,
			Containers: containers,
			Labels:     net.Labels,
		})
	}

	return result, nil
}

// NetworkExists checks if a Docker network exists.
func (r *Runtime) NetworkExists(ctx context.Context, name string) (bool, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "NetworkExists",
		"network":             name,
	})
	log := zerowrap.FromCtx(ctx)

	_, err := r.client.NetworkInspect(ctx, name, network.InspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return false, nil
		}
		return false, log.WrapErr(err, "failed to inspect network")
	}
	return true, nil
}

// ConnectContainerToNetwork connects a container to a network.
func (r *Runtime) ConnectContainerToNetwork(ctx context.Context, containerName, networkName string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "ConnectContainerToNetwork",
		"container":           containerName,
		"network":             networkName,
	})
	log := zerowrap.FromCtx(ctx)

	err := r.client.NetworkConnect(ctx, networkName, containerName, &network.EndpointSettings{})
	if err != nil {
		return log.WrapErr(err, "failed to connect container to network")
	}

	log.Info().Msg("container connected to network")
	return nil
}

// DisconnectContainerFromNetwork disconnects a container from a network.
func (r *Runtime) DisconnectContainerFromNetwork(ctx context.Context, containerName, networkName string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "DisconnectContainerFromNetwork",
		"container":           containerName,
		"network":             networkName,
	})
	log := zerowrap.FromCtx(ctx)

	err := r.client.NetworkDisconnect(ctx, networkName, containerName, false)
	if err != nil {
		return log.WrapErr(err, "failed to disconnect container from network")
	}

	log.Info().Msg("container disconnected from network")
	return nil
}
