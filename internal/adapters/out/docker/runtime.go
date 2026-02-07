// Package docker implements the container runtime adapter using Docker API.
package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
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

		// Bind to random available port on localhost only.
		// SECURITY: Using 127.0.0.1 prevents direct access from the network,
		// forcing all traffic through Gordon's reverse proxy where auth,
		// rate limiting, and security headers are applied.
		portBindings[containerPort] = []nat.PortBinding{
			{
				HostIP:   "127.0.0.1",
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
		Hostname:     config.Hostname,
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
			Status: c.State, // Use State (e.g., "running") not Status (e.g., "Up 2 days")
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
		Bool("has_auth", username != "" || password != "").
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

// TagImage tags a locally available image with a new reference.
func (r *Runtime) TagImage(ctx context.Context, sourceRef, targetRef string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "TagImage",
		"source":              sourceRef,
		"target":              targetRef,
	})
	log := zerowrap.FromCtx(ctx)

	if err := r.client.ImageTag(ctx, sourceRef, targetRef); err != nil {
		return log.WrapErr(err, "failed to tag image")
	}

	log.Info().Msg("image tagged successfully")
	return nil
}

// UntagImage removes a tag from an image without deleting the underlying image layers.
func (r *Runtime) UntagImage(ctx context.Context, imageRef string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "UntagImage",
		"image":               imageRef,
	})
	log := zerowrap.FromCtx(ctx)

	// ImageRemove with PruneChildren=false only removes the tag, not the image layers
	_, err := r.client.ImageRemove(ctx, imageRef, image.RemoveOptions{
		Force:         false,
		PruneChildren: false,
	})
	if err != nil {
		return log.WrapErr(err, "failed to untag image")
	}

	log.Debug().Msg("image untagged successfully")
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

	// Sort ports with HTTP-friendly ports first (avoid SSH port 22, etc.)
	// Priority: 80, 8080, 3000, 8000, 5000, then others sorted ascending
	sortPortsHTTPFirst(ports)

	log.Info().Ints("exposed_ports", ports).Msg("found exposed ports in image")
	return ports, nil
}

// sortPortsHTTPFirst sorts ports with common HTTP ports first, avoiding SSH (22).
func sortPortsHTTPFirst(ports []int) {
	priority := map[int]int{
		80: 0, 443: 1, 8080: 2, 3000: 3, 8000: 4, 5000: 5, 9000: 6,
	}
	sort.Slice(ports, func(i, j int) bool {
		pi, oki := priority[ports[i]]
		pj, okj := priority[ports[j]]
		if oki && okj {
			return pi < pj
		}
		if oki {
			return true
		}
		if okj {
			return false
		}
		// Neither is a priority port - sort ascending but push 22 to the end
		if ports[i] == 22 {
			return false
		}
		if ports[j] == 22 {
			return true
		}
		return ports[i] < ports[j]
	})
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

// GetContainerNetwork returns the network name a container is connected to.
func (r *Runtime) GetContainerNetwork(ctx context.Context, containerID string) (string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:    "adapter",
		zerowrap.FieldAdapter:  "docker",
		zerowrap.FieldAction:   "GetContainerNetwork",
		zerowrap.FieldEntityID: containerID,
	})
	log := zerowrap.FromCtx(ctx)

	resp, err := r.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", log.WrapErr(err, "failed to inspect container")
	}

	networks := resp.NetworkSettings
	if networks == nil || networks.Networks == nil || len(networks.Networks) == 0 {
		return "bridge", nil
	}

	// Priority: gordon-* networks first, then bridge, then any other
	selected := ""
	hasBridge := false
	for name := range networks.Networks {
		if strings.HasPrefix(name, "gordon-") {
			selected = name
			break
		}
		if name == "bridge" {
			hasBridge = true
		} else if selected == "" {
			selected = name
		}
	}

	// If no gordon-* network found, prefer bridge over random network
	if selected == "" || (hasBridge && !strings.HasPrefix(selected, "gordon-")) {
		if hasBridge {
			selected = "bridge"
		}
	}

	if selected == "" {
		return "bridge", nil
	}

	log.Debug().Str("network", selected).Msg("container network detected")
	return selected, nil
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

// GetImageLabels returns the labels defined on an image.
func (r *Runtime) GetImageLabels(ctx context.Context, imageRef string) (map[string]string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "GetImageLabels",
		"image":               imageRef,
	})
	log := zerowrap.FromCtx(ctx)

	imageInspect, err := r.client.ImageInspect(ctx, imageRef)
	if err != nil {
		return nil, log.WrapErr(err, "failed to inspect image")
	}

	labels := make(map[string]string)
	if imageInspect.Config != nil && imageInspect.Config.Labels != nil {
		labels = imageInspect.Config.Labels
	}

	log.Debug().Int("label_count", len(labels)).Msg("retrieved image labels")
	return labels, nil
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

// ExecInContainer executes a command in a running container.
func (r *Runtime) ExecInContainer(ctx context.Context, containerID string, cmd []string) (*out.ExecResult, error) {
	if len(cmd) == 0 {
		return nil, fmt.Errorf("command cannot be empty")
	}

	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:    "adapter",
		zerowrap.FieldAdapter:  "docker",
		zerowrap.FieldAction:   "ExecInContainer",
		zerowrap.FieldEntityID: containerID,
		"command":              cmd[0],
		"arg_count":            len(cmd) - 1,
	})
	log := zerowrap.FromCtx(ctx)

	execResp, err := r.client.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, log.WrapErr(err, "failed to create exec")
	}

	attachCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		attachCtx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	}

	attachResp, err := r.client.ContainerExecAttach(attachCtx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, log.WrapErr(err, "failed to attach to exec")
	}
	defer attachResp.Close()
	if deadline, ok := attachCtx.Deadline(); ok && attachResp.Conn != nil {
		_ = attachResp.Conn.SetReadDeadline(deadline)
	}

	stdout, stderr, err := parseExecOutput(attachResp.Reader)
	if err != nil {
		return nil, log.WrapErr(err, "failed to read exec output")
	}

	inspectResp, err := r.client.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return nil, log.WrapErr(err, "failed to inspect exec result")
	}

	return &out.ExecResult{
		ExitCode: inspectResp.ExitCode,
		Stdout:   stdout,
		Stderr:   stderr,
	}, nil
}

func parseExecOutput(reader io.Reader) ([]byte, []byte, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if _, err := stdcopy.StdCopy(&stdout, &stderr, reader); err != nil {
		return nil, nil, err
	}

	return stdout.Bytes(), stderr.Bytes(), nil
}

// CopyFromContainer copies a file from a container and returns a content stream.
func (r *Runtime) CopyFromContainer(ctx context.Context, containerID, srcPath string) (io.ReadCloser, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:    "adapter",
		zerowrap.FieldAdapter:  "docker",
		zerowrap.FieldAction:   "CopyFromContainer",
		zerowrap.FieldEntityID: containerID,
		"src_path":             srcPath,
	})
	log := zerowrap.FromCtx(ctx)

	tarReader, _, err := r.client.CopyFromContainer(ctx, containerID, srcPath)
	if err != nil {
		return nil, log.WrapErr(err, "failed to copy from container")
	}

	// The response is a tar archive - we need to extract the requested file stream.
	reader, err := extractFileFromTar(tarReader, srcPath)
	if err != nil {
		_ = tarReader.Close()
		return nil, log.WrapErr(err, "failed to extract file from tar")
	}

	return reader, nil
}

// extractFileFromTar extracts a single file from a tar archive.
func extractFileFromTar(reader io.ReadCloser, targetPath string) (io.ReadCloser, error) {
	tr := tar.NewReader(reader)

	// The path in the tar may be relative or absolute
	// We need to match based on the filename
	targetName := filepath.Base(targetPath)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		// Check if this is our target file
		if header.Typeflag == tar.TypeReg {
			headerName := filepath.Base(header.Name)
			if headerName == targetName {
				size := header.Size
				pr, pw := io.Pipe()
				go func() {
					defer reader.Close()
					_, copyErr := io.CopyN(pw, tr, size)
					if copyErr != nil {
						_ = pw.CloseWithError(copyErr)
						return
					}
					_ = pw.Close()
				}()
				return pr, nil
			}
		}
	}

	_ = reader.Close()
	return nil, fmt.Errorf("file not found in container: %s", targetPath)
}

// ExtractEnvFileFromImage extracts an env file from an image.
// This creates a temporary container, copies the file, and removes the container.
func (r *Runtime) ExtractEnvFileFromImage(ctx context.Context, imageRef, envFilePath string) ([]byte, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "ExtractEnvFileFromImage",
		"image":               imageRef,
		"env_file":            envFilePath,
	})
	log := zerowrap.FromCtx(ctx)

	log.Info().Msg("extracting env file from image")

	// Create a temporary container without starting it
	containerConfig := &container.Config{
		Image: imageRef,
		Cmd:   []string{"true"}, // Dummy command
	}

	resp, err := r.client.ContainerCreate(ctx, containerConfig, nil, nil, nil, "")
	if err != nil {
		return nil, log.WrapErr(err, "failed to create temporary container")
	}

	tempContainerID := resp.ID
	log.Debug().Str("temp_container_id", tempContainerID).Msg("created temporary container")

	// Ensure cleanup
	defer func() {
		if err := r.client.ContainerRemove(ctx, tempContainerID, container.RemoveOptions{Force: true}); err != nil {
			log.Warn().Err(err).Str("temp_container_id", tempContainerID).Msg("failed to remove temporary container")
		}
	}()

	// Copy the env file from the container
	reader, err := r.CopyFromContainer(ctx, tempContainerID, envFilePath)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, log.WrapErr(err, "failed to read extracted env file")
	}

	log.Info().Int("size", len(data)).Msg("env file extracted from image")
	return data, nil
}
