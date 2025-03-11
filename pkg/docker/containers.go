package docker

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bnema/gordon/pkg/logger"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
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
	ProxyPort     string // Container port to be used by the reverse proxy
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
	err := CheckIfInitialized()
	if err != nil {
		return nil, err
	}
	// List containers using the Docker client:
	containers, err := dockerCli.ContainerList(context.Background(), container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	// Check if the list is empty
	if len(containers) == 0 {
		return nil, fmt.Errorf("no containers found")
	}
	return containers, nil
}

// StopContainer try to stop a container gracefully, if it fails, it will stop it forcefully
func StopContainer(containerID string) error {
	// Try graceful shutdown first
	stopped, err := StopContainerGracefully(containerID, 3*time.Second)
	if err != nil {
		logger.Warn("Failed to stop container gracefully", "error", err)
	}

	// If container wasn't stopped gracefully or there was an error, try forceful shutdown
	if !stopped {
		if err := StopContainerRagefully(containerID); err != nil {
			logger.Error("Failed to stop container forcefully", "error", err)
			return fmt.Errorf("failed to stop container forcefully: %w", err)
		}
		logger.Info("Container stopped forcefully", "containerID", containerID)
	} else {
		logger.Info("Container stopped gracefully", "containerID", containerID)
	}

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
		<-ticker.C
		// Check if the container is still running
		container, err := dockerCli.ContainerInspect(context.Background(), containerID)
		if err != nil {
			return false, err
		}

		// If the container is not running, return true
		if !container.State.Running {
			return true, nil
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
	err := dockerCli.ContainerRemove(context.Background(), containerID, container.RemoveOptions{Force: true})
	if err != nil {
		return err
	}

	return nil
}

// StartContainer starts a container
func StartContainer(containerID string) error {
	logger.Info("Starting container", "containerID", containerID)

	// Add timeout context
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get initial container state
	containerInfo, err := GetContainerInfo(containerID)
	if err != nil {
		logger.Error("Failed to get container info", "containerID", containerID, "error", err)
		return fmt.Errorf("could not get container info: %v", err)
	}

	if containerInfo.State.Running {
		logger.Warn("Container is already running", "containerID", containerID)
		return fmt.Errorf("container is already running")
	}

	// Start container
	logger.Debug("Executing container start", "containerID", containerID)
	err = dockerCli.ContainerStart(ctx, containerID, container.StartOptions{})
	if err != nil {
		// Get container logs if start failed
		logs, logErr := GetContainerLogs(containerID)
		if logErr == nil {
			logger.Error("Container logs after failed start", "containerID", containerID, "logs", logs)
		}
		return fmt.Errorf("could not start container: %v", err)
	}

	// Verify container is running
	for i := 0; i < 5; i++ {
		state, err := GetContainerState(containerID)
		if err != nil {
			logger.Warn("Error checking container state", "containerID", containerID, "error", err)
			continue
		}

		if state == "running" {
			logger.Info("Container started successfully", "containerID", containerID)
			return nil
		}

		logger.Debug("Waiting for container to start", "containerID", containerID, "state", state, "attempt", i+1)
		time.Sleep(time.Second)
	}

	// If we get here, container didn't start properly
	logs, _ := GetContainerLogs(containerID)
	logger.Error("Container failed to enter running state", "containerID", containerID, "logs", logs)
	return fmt.Errorf("container failed to enter running state. Logs: %s", logs)
}

// CreateContainer creates a container with the given parameters
func CreateContainer(cmdParams ContainerCommandParams) (string, error) {

	if err := CheckIfInitialized(); err != nil {
		logger.Error("Failed to check if docker client is initialized", "error", err)
		return "", fmt.Errorf("failed to check if docker client is initialized: %w", err)
	}

	logger.Info("Creating container with params",
		"name", cmdParams.ContainerName,
		"image", cmdParams.ImageName,
		"proxy_port", cmdParams.ProxyPort)

	// Check if a container with this name already exists - handle recreation case
	existingContainers, err := ListRunningContainers()
	if err != nil {
		logger.Warn("Failed to check for existing containers", "error", err)
	} else {
		for _, container := range existingContainers {
			for _, name := range container.Names {
				// Container names in Docker API have a leading slash
				cleanName := strings.TrimPrefix(name, "/")
				if cleanName == cmdParams.ContainerName {
					logger.Warn("Container with this name already exists, it will be replaced",
						"name", cmdParams.ContainerName,
						"existing_id", container.ID)

					// Stop and remove the existing container
					if err := StopContainer(container.ID); err != nil {
						logger.Warn("Failed to stop existing container",
							"container_id", container.ID,
							"error", err)
					}

					if err := RemoveContainer(container.ID); err != nil {
						logger.Warn("Failed to remove existing container",
							"container_id", container.ID,
							"error", err)
					}

					logger.Info("Removed existing container to create a new one",
						"name", cmdParams.ContainerName)
					break
				}
			}
		}
	}

	// Check network
	isNetworkCreated, err := CheckIfNetworkExists(cmdParams.Network)
	if err != nil {
		logger.Error("Network check failed", "network", cmdParams.Network, "error", err)
		return "", fmt.Errorf("network check failed: %v", err)
	}

	if !isNetworkCreated {
		logger.Info("Creating network", "network", cmdParams.Network)
		_, err := dockerCli.NetworkCreate(context.Background(), cmdParams.Network, network.CreateOptions{})
		if err != nil {
			logger.Error("Network creation failed", "network", cmdParams.Network, "error", err)
			return "", fmt.Errorf("network creation failed: %v", err)
		}
	}

	// Prepare port bindings with logging
	portBindings := nat.PortMap{}
	exposedPorts := nat.PortSet{}
	for _, portMapping := range cmdParams.PortMappings {
		exposedPort := nat.Port(portMapping.ContainerPort + "/" + portMapping.Protocol)
		portBindings[exposedPort] = []nat.PortBinding{
			{
				HostIP:   "0.0.0.0",
				HostPort: portMapping.HostPort,
			},
		}
		exposedPorts[exposedPort] = struct{}{}
		logger.Debug("Adding port mapping",
			"host_port", portMapping.HostPort,
			"container_port", portMapping.ContainerPort,
			"protocol", portMapping.Protocol)
	}

	// Prepare labels
	labels := map[string]string{}
	for _, label := range cmdParams.Labels {
		keyValue := strings.Split(label, "=")
		if len(keyValue) == 2 {
			labels[keyValue[0]] = keyValue[1]
		}
	}

	// Create container with platform specification
	resp, err := dockerCli.ContainerCreate(
		context.Background(),
		&container.Config{
			Image:        cmdParams.ImageName,
			Labels:       labels,
			Env:          cmdParams.Environment,
			ExposedPorts: exposedPorts,
			Hostname:     cmdParams.ServiceName,
		},
		&container.HostConfig{
			PortBindings: portBindings,
			Binds:        cmdParams.Volumes,
			RestartPolicy: container.RestartPolicy{
				Name: "always",
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
		return "", fmt.Errorf("container creation failed: %v", err)
	}

	logger.Info("Container created successfully", "id", resp.ID)

	return resp.ID, nil
}

func WaitForContainerToBeRunning(containerID string, timeout time.Duration) error {
	logger.Debug("Waiting for container to be running", "containerID", containerID, "timeout", timeout)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			logger.Error("Timeout waiting for container to start", "containerID", containerID)
			return fmt.Errorf("timeout waiting for container to start")
		case <-time.After(time.Second):
			state, err := GetContainerState(containerID)
			if err != nil {
				logger.Error("Error checking container state", "containerID", containerID, "error", err)
				return fmt.Errorf("error checking container state: %v", err)
			}

			logger.Debug("Container state check", "containerID", containerID, "state", state)
			if state == "running" {
				logger.Info("Container is now running", "containerID", containerID)
				return nil
			}

			if state == "exited" {
				logs, _ := GetContainerLogs(containerID)
				logger.Error("Container exited unexpectedly", "containerID", containerID, "logs", logs)
				return fmt.Errorf("container exited unexpectedly. Logs: %s", logs)
			}
		}
	}
}

func CheckIfNetworkExists(networkName string) (bool, error) {
	networks, err := dockerCli.NetworkList(context.Background(), network.ListOptions{})
	if err != nil {
		return false, err
	}

	for _, network := range networks {
		if network.Name == networkName {
			return true, nil
		}
	}

	return false, nil
}

// GetNetworkInfo returns information about a network
func GetNetworkInfo(networkName string) (*network.Inspect, error) {
	networkInfo, err := dockerCli.NetworkInspect(context.Background(), networkName, network.InspectOptions{})
	if err != nil {
		return nil, err
	}

	return &networkInfo, nil
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

// GetContainerName returns the name of a container
func GetContainerName(containerID string) (string, error) {
	containerInfo, err := GetContainerInfo(containerID)
	if err != nil {
		return "", err
	}

	return containerInfo.Name, nil
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
	if err := dockerCli.ContainerRemove(ctx, containerID, container.RemoveOptions{}); err != nil {
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
	if err := dockerCli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return err
	}

	return nil
}

// GetContainerLogs returns the logs of a container
func GetContainerLogs(containerID string) (string, error) {
	containerLogs, err := dockerCli.ContainerLogs(context.Background(), containerID, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return "", err
	}
	defer containerLogs.Close()

	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(containerLogs); err != nil {
		return "", fmt.Errorf("failed to read container logs: %w", err)
	}
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

func GetContainerIDByName(containerName string) string {
	containers, err := ListRunningContainers()
	if err != nil {
		return ""
	}

	for _, container := range containers {
		for _, name := range container.Names {
			if strings.TrimLeft(name, "/") == containerName {
				return container.ID
			}
		}
	}

	return ""
}

func ContainerExists(containerID string) bool {
	_, err := dockerCli.ContainerInspect(context.Background(), containerID)
	return err == nil
}

func GetContainerState(containerID string) (string, error) {
	info, err := dockerCli.ContainerInspect(context.Background(), containerID)
	if err != nil {
		return "", err
	}
	return info.State.Status, nil
}

// StartContainerWithContext starts a container with context
func StartContainerWithContext(ctx context.Context, containerID string) error {
	fmt.Println("Starting container", containerID)

	// Check if the container is not already in a running state
	containerInfo, err := GetContainerInfo(containerID)
	if err != nil {
		return fmt.Errorf("could not get container info: %v", err)
	}

	if containerInfo.State.Running {
		return fmt.Errorf("container is already running")
	}

	// Start container using the Docker client with context
	err = dockerCli.ContainerStart(ctx, containerID, container.StartOptions{})
	if err != nil {
		return fmt.Errorf("could not start container: %v", err)
	}

	return nil
}

func GetContainerPorts(containerID string) (string, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return "", err
	}
	defer cli.Close()

	container, err := cli.ContainerInspect(context.Background(), containerID)
	if err != nil {
		return "", err
	}

	var ports []string
	for port, bindings := range container.NetworkSettings.Ports {
		for _, binding := range bindings {
			ports = append(ports, fmt.Sprintf("%s:%s->%s", binding.HostIP, binding.HostPort, port))
		}
	}

	return strings.Join(ports, ", "), nil
}

func GetContainerUptime(containerID string) (string, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return "", err
	}
	defer cli.Close()

	container, err := cli.ContainerInspect(context.Background(), containerID)
	if err != nil {
		return "", err
	}

	if !container.State.Running {
		return "not running", nil
	}

	startTime, err := time.Parse(time.RFC3339, container.State.StartedAt)
	if err != nil {
		return "", err
	}

	duration := time.Since(startTime)
	return formatDuration(duration), nil
}

// ContainerPortInfo holds information about container ports
type ContainerPortInfo struct {
	Name           string
	ExposedPorts   map[nat.Port]struct{}
	PortBindings   map[nat.Port][]nat.PortBinding
	PublishedPorts []types.Port
}

// GetContainersUsingPort returns a list of container names that are using the specified port
func GetContainersUsingPort(port string) ([]string, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	return findContainersWithPort(ctx, cli, containers, port)
}

// findContainersWithPort processes the container list and finds those using the specified port
func findContainersWithPort(ctx context.Context, cli *client.Client, containers []types.Container, port string) ([]string, error) {
	var containersUsingPort []string
	seen := make(map[string]bool)

	for _, container := range containers {
		containerInfo, err := getContainerPortInfo(ctx, cli, container)
		if err != nil {
			logger.Warn("Failed to inspect container", "container", container.ID, "error", err)
			continue
		}

		if isContainerUsingPort(containerInfo, port) && !seen[containerInfo.Name] {
			seen[containerInfo.Name] = true
			containersUsingPort = append(containersUsingPort, containerInfo.Name)
		}
	}

	return containersUsingPort, nil
}

// getContainerPortInfo retrieves port-related information for a container
func getContainerPortInfo(ctx context.Context, cli *client.Client, container types.Container) (ContainerPortInfo, error) {
	inspect, err := cli.ContainerInspect(ctx, container.ID)
	if err != nil {
		return ContainerPortInfo{}, err
	}

	return ContainerPortInfo{
		Name:           strings.TrimPrefix(container.Names[0], "/"),
		ExposedPorts:   inspect.Config.ExposedPorts,
		PortBindings:   inspect.HostConfig.PortBindings,
		PublishedPorts: container.Ports,
	}, nil
}

// isContainerUsingPort checks if a container is using the specified port
func isContainerUsingPort(info ContainerPortInfo, port string) bool {
	// Check exposed ports
	if hasExposedPort(info.ExposedPorts, port) {
		return true
	}

	// Check port bindings
	if hasPortBinding(info.PortBindings, port) {
		return true
	}

	// Check published ports
	if hasPublishedPort(info.PublishedPorts, port) {
		return true
	}

	return false
}

// hasExposedPort checks if the port is exposed in the container configuration
func hasExposedPort(exposedPorts nat.PortSet, port string) bool {
	for exposedPort := range exposedPorts {
		if strings.HasPrefix(string(exposedPort), port+"/") {
			return true
		}
	}
	return false
}

// hasPortBinding checks if the port is bound in the host configuration
func hasPortBinding(portBindings nat.PortMap, port string) bool {
	for _, bindings := range portBindings {
		for _, binding := range bindings {
			if binding.HostPort == port {
				return true
			}
		}
	}
	return false
}

// hasPublishedPort checks if the port is currently published
func hasPublishedPort(ports []types.Port, port string) bool {
	for _, portBinding := range ports {
		if fmt.Sprintf("%d", portBinding.PublicPort) == port {
			return true
		}
	}
	return false
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

// ListenForContainerEvents starts polling for container state changes and calls the provided callback
// when relevant container events are detected. This is used for service discovery and IP updates.
// This implementation uses a polling approach instead of Docker event streams, which is more
// stable for both Docker and Podman environments.
func ListenForContainerEvents(networkFilter string, callback func(string, string, string)) error {
	// Check if the Docker client has been initialized
	err := CheckIfInitialized()
	if err != nil {
		return err
	}

	// Create a context with cancel to allow for clean shutdown
	ctx, cancel := context.WithCancel(context.Background())

	// Store cancel func to allow shutdown
	containerEventCancel = cancel

	// Start the polling loop in a goroutine
	go func() {
		// Configure polling interval - can be adjusted based on needs
		pollInterval := 10 * time.Second
		logger.Info("Starting container polling",
			"network_filter", networkFilter,
			"poll_interval", pollInterval)

		// Initialize container tracking map
		containerStates := make(map[string]containerState)
		// Track previous IPs to detect if they actually changed
		containerIPs := make(map[string]string)

		// Create ticker for polling
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()

		// Track failures for backoff
		consecutiveFailures := 0
		maxConsecutiveFailures := 5
		initialBackoff := 5 * time.Second
		maxBackoff := 1 * time.Minute
		currentBackoff := initialBackoff

		for {
			select {
			case <-ticker.C:
				// Get current list of running containers
				logger.Debug("Polling for container state changes", "network_filter", networkFilter)
				currentContainers, err := ListContainers(ctx)
				if err != nil {
					consecutiveFailures++
					logger.Error("Failed to list containers during polling",
						"error", err,
						"consecutive_failures", consecutiveFailures)

					// Implement backoff for repeated failures
					if consecutiveFailures >= maxConsecutiveFailures {
						// Increase backoff time
						currentBackoff = time.Duration(float64(currentBackoff) * 1.5)
						if currentBackoff > maxBackoff {
							currentBackoff = maxBackoff
						}

						logger.Warn("Multiple consecutive container polling failures",
							"count", consecutiveFailures,
							"next_poll_after", currentBackoff)

						// Temporarily slow down polling
						ticker.Reset(currentBackoff)
					}
					continue
				}

				// Reset on successful poll
				if consecutiveFailures > 0 {
					logger.Debug("Container polling recovered after failures",
						"previous_failure_count", consecutiveFailures)
					consecutiveFailures = 0
					// Reset to normal polling interval
					ticker.Reset(pollInterval)
					logger.Debug("Reset polling interval to normal", "interval", pollInterval)
				}

				// Process the current containers
				newStates, events := detectContainerEvents(containerStates, currentContainers, networkFilter)

				// Update our tracking map
				containerStates = newStates

				// Process detected events
				for _, event := range events {
					logger.Debug("Container event detected by polling",
						"container_id", event.id,
						"container_name", event.name,
						"event_type", event.eventType)

					// Handle container start events
					if event.eventType == "start" {
						// Get container IP from the network
						containerIP, err := GetContainerIPFromNetwork(event.id, networkFilter)
						if err != nil {
							logger.Error("Failed to get container IP from network",
								"container_id", event.id,
								"container_name", event.name,
								"network", networkFilter,
								"error", err)

							// Additional fallback to try to get IP through container inspection
							// This is especially important for Podman compatibility
							containerInfo, inspectErr := GetContainerInfo(event.id)
							if inspectErr == nil {
								// Try to get IP address from container inspection
								if containerInfo.NetworkSettings != nil {
									// First check if NetworkSettings.Networks contains our network
									if network, exists := containerInfo.NetworkSettings.Networks[networkFilter]; exists && network.IPAddress != "" {
										containerIP = network.IPAddress
										logger.Debug("Found container IP through inspection - network specific",
											"container_id", event.id,
											"container_name", event.name,
											"network", networkFilter,
											"ip", containerIP)
									} else if containerInfo.NetworkSettings.IPAddress != "" {
										// Fallback to default IPAddress
										containerIP = containerInfo.NetworkSettings.IPAddress
										logger.Debug("Found container IP through inspection - default network",
											"container_id", event.id,
											"container_name", event.name,
											"ip", containerIP)
									}
								}
							}

							// If we still don't have an IP, skip this container
							if containerIP == "" {
								logger.Error("Failed to get container IP through any method",
									"container_id", event.id,
									"container_name", event.name)
								continue
							}
						}

						// Check if this is a container IP change or if it's a new discovery
						prevIP, ipExists := containerIPs[event.id]
						if ipExists && prevIP == containerIP {
							// IP hasn't actually changed, no need to trigger callback
							logger.Debug("Container IP verified but unchanged - skipping callback",
								"container_id", event.id,
								"container_name", event.name,
								"container_ip", containerIP)

							// Update the container IP map to refresh timestamp
							containerIPs[event.id] = containerIP
							continue
						}

						// Update the container IP map
						containerIPs[event.id] = containerIP

						// For Gordon container, add extra verification to prevent routing issues
						if event.name == "gordon" {
							logger.Info("Gordon container event detected - verifying IP before callback",
								"container_id", event.id,
								"container_name", event.name,
								"container_ip", containerIP)

							// Double-check the IP with a direct inspection for reliability
							containerInfo, err := GetContainerInfo(event.id)
							if err == nil && containerInfo.NetworkSettings != nil {
								if network, exists := containerInfo.NetworkSettings.Networks[networkFilter]; exists {
									if network.IPAddress != "" && network.IPAddress != containerIP {
										logger.Warn("IP mismatch detected for Gordon container - using network inspection IP",
											"container_id", event.id,
											"polling_ip", containerIP,
											"inspection_ip", network.IPAddress)
										containerIP = network.IPAddress
										containerIPs[event.id] = containerIP
									}
								}
							}
						}

						// Add debug output before calling callback
						logger.Debug("Calling container event callback",
							"container_id", event.id,
							"container_name", event.name,
							"container_ip", containerIP)

						// Call the callback with the container ID, name, and IP
						callback(event.id, event.name, containerIP)

						logger.Debug("Processed container start",
							"container_id", event.id,
							"container_name", event.name,
							"container_ip", containerIP)
					} else if event.eventType == "stop" {
						// Handle container stop events if needed
						logger.Debug("Container stopped",
							"container_id", event.id,
							"container_name", event.name)
						// If any stop event handling is needed, add it here

						// Remove from IP tracking map when container stops
						delete(containerIPs, event.id)
					}
				}

			case <-ctx.Done():
				logger.Info("Container polling stopped due to context cancellation")
				return
			}
		}
	}()

	return nil
}

// Container state tracking
type containerState struct {
	id      string
	name    string
	status  string
	running bool
}

// Container event structure
type containerEvent struct {
	id        string
	name      string
	eventType string // "start" or "stop"
}

// detectContainerEvents compares previous and current container states to detect events
func detectContainerEvents(previousStates map[string]containerState, currentContainers []types.Container, networkFilter string) (map[string]containerState, []containerEvent) {
	newStates := make(map[string]containerState)
	var events []containerEvent

	// Build map of current containers by ID
	for _, container := range currentContainers {
		// Skip containers not in the specified network if networkFilter is provided
		if networkFilter != "" {
			// Check if container is in the specified network
			inNetwork := false

			// First, check if the container has any network with the network name matching our filter
			for networkName := range container.NetworkSettings.Networks {
				if networkName == networkFilter ||
					strings.Contains(strings.ToLower(networkName), strings.ToLower(networkFilter)) {
					inNetwork = true
					break
				}
			}

			// If not found by name, check network IDs as fallback (for compatibility with previous code)
			if !inNetwork {
				for _, network := range container.NetworkSettings.Networks {
					if network.NetworkID == networkFilter ||
						strings.Contains(strings.ToLower(network.NetworkID), strings.ToLower(networkFilter)) {
						inNetwork = true
						break
					}
				}
			}

			if !inNetwork {
				logger.Debug("Container not in target network - skipping",
					"container_id", container.ID,
					"container_name", strings.TrimPrefix(container.Names[0], "/"),
					"network_filter", networkFilter)
				continue
			}
		}

		containerName := strings.TrimPrefix(container.Names[0], "/")

		// Create current state entry
		newState := containerState{
			id:      container.ID,
			name:    containerName,
			status:  container.Status,
			running: strings.Contains(strings.ToLower(container.Status), "up"),
		}

		// Add to new state map
		newStates[container.ID] = newState

		// Check if this is a new container or status changed from not running to running
		prevState, existed := previousStates[container.ID]
		if !existed {
			// New container detected
			if newState.running {
				// New running container - fire "start" event
				events = append(events, containerEvent{
					id:        container.ID,
					name:      containerName,
					eventType: "start",
				})
				logger.Debug("New container detected",
					"container_id", container.ID,
					"container_name", containerName)
			}
		} else if !prevState.running && newState.running {
			// Container changed from not running to running - fire "start" event
			events = append(events, containerEvent{
				id:        container.ID,
				name:      containerName,
				eventType: "start",
			})
			logger.Debug("Container started",
				"container_id", container.ID,
				"container_name", containerName)
		}
	}

	// Check for stopped containers
	for id, prevState := range previousStates {
		if _, stillExists := newStates[id]; !stillExists && prevState.running {
			// Container was running but is now gone - fire "stop" event
			events = append(events, containerEvent{
				id:        id,
				name:      prevState.name,
				eventType: "stop",
			})
			logger.Debug("Container stopped or removed",
				"container_id", id,
				"container_name", prevState.name)
		}
	}

	return newStates, events
}

// GetContainerIPFromNetwork gets a container's IP address from the specified network
func GetContainerIPFromNetwork(containerID, networkName string) (string, error) {
	// Get network info
	networkInfo, err := GetNetworkInfo(networkName)
	if err != nil {
		// Simply log an error without trying to list networks
		logger.Debug("Failed to get network info", "network", networkName, "error", err)
		return "", fmt.Errorf("failed to get network info for %s: %w", networkName, err)
	}

	// Look for container in the network
	containerEndpoint, exists := networkInfo.Containers[containerID]
	if !exists {
		// For Podman compatibility, try alternative approaches

		// First, try to get the container info directly
		containerInfo, inspectErr := GetContainerInfo(containerID)
		if inspectErr == nil && containerInfo.NetworkSettings != nil {
			// Try to find the container IP using the NetworkSettings
			if network, exists := containerInfo.NetworkSettings.Networks[networkName]; exists && network.IPAddress != "" {
				logger.Debug("Found container IP using direct inspection - specific network",
					"container_id", containerID,
					"network", networkName,
					"ip", network.IPAddress)
				return network.IPAddress, nil
			} else if containerInfo.NetworkSettings.IPAddress != "" {
				// Fallback to default network IP
				logger.Debug("Found container IP using direct inspection - default network",
					"container_id", containerID,
					"ip", containerInfo.NetworkSettings.IPAddress)
				return containerInfo.NetworkSettings.IPAddress, nil
			}
		}

		// If we got here, we couldn't find the container in the network
		// Log details about the containers in the network for debugging
		containerNames := []string{}
		for id := range networkInfo.Containers {
			name, _ := GetContainerName(id)
			containerNames = append(containerNames, fmt.Sprintf("%s (%s)", name, id[:12]))
		}

		logger.Debug("Container not found in network",
			"container_id", containerID,
			"network", networkName,
			"containers_in_network", strings.Join(containerNames, ", "))

		return "", fmt.Errorf("container %s not found in network %s", containerID, networkName)
	}

	// If IPv4 address is not available, try IPv6
	if containerEndpoint.IPv4Address == "" && containerEndpoint.IPv6Address != "" {
		logger.Debug("Using IPv6 address instead of IPv4",
			"container_id", containerID,
			"ip", containerEndpoint.IPv6Address)
		return containerEndpoint.IPv6Address, nil
	}

	// Return the container's IP address
	// Extract just the IP part without subnet
	ipAddress := containerEndpoint.IPv4Address
	if idx := strings.Index(ipAddress, "/"); idx > 0 {
		ipAddress = ipAddress[:idx]
	}

	return ipAddress, nil
}

// StopContainerEventListener stops the container event listener
func StopContainerEventListener() {
	if containerEventCancel != nil {
		containerEventCancel()
		logger.Info("Stopped container event listener")
	}
}

// Variable to store cancel function for event listener
var containerEventCancel context.CancelFunc
