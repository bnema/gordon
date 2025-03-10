package docker

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
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
		log.Warn("Failed to stop container gracefully", "error", err)
	}

	// If container wasn't stopped gracefully or there was an error, try forceful shutdown
	if !stopped {
		if err := StopContainerRagefully(containerID); err != nil {
			return fmt.Errorf("failed to stop container forcefully: %w", err)
		}
		log.Info("Container stopped forcefully", "containerID", containerID)
	} else {
		log.Info("Container stopped gracefully", "containerID", containerID)
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
	fmt.Println("Starting container", containerID)

	// Add timeout context
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get initial container state
	containerInfo, err := GetContainerInfo(containerID)
	if err != nil {
		return fmt.Errorf("could not get container info: %v", err)
	}

	if containerInfo.State.Running {
		return fmt.Errorf("container is already running")
	}

	// Start container
	err = dockerCli.ContainerStart(ctx, containerID, container.StartOptions{})
	if err != nil {
		// Get container logs if start failed
		logs, logErr := GetContainerLogs(containerID)
		if logErr == nil {
			fmt.Printf("Container logs after failed start: %s\n", logs)
		}
		return fmt.Errorf("could not start container: %v", err)
	}

	// Verify container is running
	for i := 0; i < 5; i++ {
		state, err := GetContainerState(containerID)
		if err != nil {
			fmt.Printf("Error checking container state: %v\n", err)
			continue
		}

		if state == "running" {
			return nil
		}

		time.Sleep(time.Second)
	}

	// If we get here, container didn't start properly
	logs, _ := GetContainerLogs(containerID)
	return fmt.Errorf("container failed to enter running state. Logs: %s", logs)
}

// CreateContainer creates a container with the given parameters
func CreateContainer(cmdParams ContainerCommandParams) (string, error) {

	if err := CheckIfInitialized(); err != nil {
		return "", fmt.Errorf("failed to check if docker client is initialized: %w", err)
	}

	log.Info("Creating container with params",
		"name", cmdParams.ContainerName,
		"image", cmdParams.ImageName,
		"proxy_port", cmdParams.ProxyPort)

	// Check if a container with this name already exists - handle recreation case
	existingContainers, err := ListRunningContainers()
	if err != nil {
		log.Warn("Failed to check for existing containers", "error", err)
	} else {
		for _, container := range existingContainers {
			for _, name := range container.Names {
				// Container names in Docker API have a leading slash
				cleanName := strings.TrimPrefix(name, "/")
				if cleanName == cmdParams.ContainerName {
					log.Warn("Container with this name already exists, it will be replaced",
						"name", cmdParams.ContainerName,
						"existing_id", container.ID)

					// Stop and remove the existing container
					if err := StopContainer(container.ID); err != nil {
						log.Warn("Failed to stop existing container",
							"container_id", container.ID,
							"error", err)
					}

					if err := RemoveContainer(container.ID); err != nil {
						log.Warn("Failed to remove existing container",
							"container_id", container.ID,
							"error", err)
					}

					log.Info("Removed existing container to create a new one",
						"name", cmdParams.ContainerName)
					break
				}
			}
		}
	}

	// Check network
	isNetworkCreated, err := CheckIfNetworkExists(cmdParams.Network)
	if err != nil {
		return "", fmt.Errorf("network check failed: %v", err)
	}

	if !isNetworkCreated {
		fmt.Printf("Creating network: %s\n", cmdParams.Network)
		_, err := dockerCli.NetworkCreate(context.Background(), cmdParams.Network, network.CreateOptions{})
		if err != nil {
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
		fmt.Printf("Adding port mapping: %s:%s/%s\n",
			portMapping.HostPort,
			portMapping.ContainerPort,
			portMapping.Protocol)
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

	log.Info("Container created successfully with ID: %s\n", resp.ID)

	return resp.ID, nil
}

func WaitForContainerToBeRunning(containerID string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for container to start")
		case <-time.After(time.Second):
			state, err := GetContainerState(containerID)
			if err != nil {
				return fmt.Errorf("error checking container state: %v", err)
			}

			if state == "running" {
				return nil
			}

			if state == "exited" {
				logs, _ := GetContainerLogs(containerID)
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
			log.Warn("Failed to inspect container", "container", container.ID, "error", err)
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

// ListenForContainerEvents starts listening for container events and calls the provided callback
// when relevant container events occur. This is used for real-time service discovery and IP updates.
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

	// Start the main listener loop in a goroutine
	go func() {
		// Implement exponential backoff for reconnection
		initialDelay := 1 * time.Second
		maxDelay := 1 * time.Minute
		currentDelay := initialDelay
		consecutiveFailures := 0

		for {
			// Check if context was canceled (application is shutting down)
			if ctx.Err() != nil {
				return
			}

			// Set up filters for the events we're interested in
			filters := makeEventFilters(networkFilter)

			// Start listening for events
			messages, errs := dockerCli.Events(ctx, types.EventsOptions{
				Filters: filters,
			})

			log.Info("Started container event listener", "network_filter", networkFilter)

			// Process events in an inner loop
			listenSuccess := false
		eventStreamLoop:
			for {
				select {
				case err := <-errs:
					if err != nil && ctx.Err() == nil {
						// Only log if this isn't due to context cancellation
						log.Error("Error in container event stream", "error", err)
						// Increment failure counter for backoff calculation
						consecutiveFailures++

						// Calculate backoff delay with exponential increase
						if listenSuccess {
							// If we had successful events before failure, reset the delay
							currentDelay = initialDelay
						} else {
							// Exponential backoff: double the delay each time, up to max
							currentDelay = time.Duration(float64(currentDelay) * 1.5)
							if currentDelay > maxDelay {
								currentDelay = maxDelay
							}
						}

						log.Debug("Will attempt to reconnect to container event stream",
							"delay", currentDelay,
							"consecutive_failures", consecutiveFailures)

						// Break out of the inner event loop to reconnect
						break eventStreamLoop
					}
					// Context was canceled, exit completely
					if ctx.Err() != nil {
						return
					}
				case event := <-messages:
					// Mark that we've successfully received events
					if !listenSuccess {
						listenSuccess = true
						// Reset failure counter on successful event
						consecutiveFailures = 0
						log.Info("Successfully receiving container events")
					}

					// Process container events
					if event.Type == "container" {
						containerID := event.Actor.ID

						// Get container name from event attributes
						containerName := event.Actor.Attributes["name"]
						if containerName == "" {
							// If not in attributes, try to get it from the API
							name, err := GetContainerName(containerID)
							if err == nil {
								containerName = strings.TrimPrefix(name, "/")
							}
						}

						// Handle container events based on action
						switch event.Action {
						case "start":
							// Container started - need to get its IP and update any proxy routes
							log.Debug("Container started event",
								"container_id", containerID,
								"container_name", containerName)

							// Get container IP from the network
							containerIP, err := getContainerIPFromNetwork(containerID, networkFilter)
							if err != nil {
								log.Error("Failed to get container IP",
									"container_id", containerID,
									"error", err)
								continue
							}

							// Call the callback with the container ID, name, and IP
							callback(containerID, containerName, containerIP)

						case "die", "kill", "destroy":
							// Container stopped - might need to mark routes as inactive
							log.Debug("Container stopped event",
								"container_id", containerID,
								"container_name", containerName,
								"action", event.Action)

						case "rename":
							// Container renamed - update related proxy routes
							log.Debug("Container renamed event",
								"container_id", containerID,
								"container_name", containerName,
								"new_name", event.Actor.Attributes["name"])

							// Add other event types as needed
						}
					}
				case <-ctx.Done():
					return
				}
			}

			// If we get here, the event stream failed and we need to reconnect
			// Wait for the calculated delay before reconnecting
			select {
			case <-time.After(currentDelay):
				log.Info("Attempting to reconnect to container event stream")
			case <-ctx.Done():
				return
			}
		}
	}()

	return nil
}

// StopContainerEventListener stops the container event listener
func StopContainerEventListener() {
	if containerEventCancel != nil {
		containerEventCancel()
		log.Info("Stopped container event listener")
	}
}

// getContainerIPFromNetwork gets a container's IP address from the specified network
func getContainerIPFromNetwork(containerID, networkName string) (string, error) {
	// Get network info
	networkInfo, err := GetNetworkInfo(networkName)
	if err != nil {
		return "", fmt.Errorf("failed to get network info: %w", err)
	}

	// Look for container in the network
	containerEndpoint, exists := networkInfo.Containers[containerID]
	if !exists {
		return "", fmt.Errorf("container not found in network %s", networkName)
	}

	// Return the container's IP address
	return containerEndpoint.IPv4Address, nil
}

// makeEventFilters creates filters for the Events API
func makeEventFilters(networkName string) filters.Args {
	f := filters.NewArgs()

	// Filter for container events only
	f.Add("type", "container")

	// Filter for events we're interested in
	f.Add("event", "start")
	f.Add("event", "die")
	f.Add("event", "destroy")
	f.Add("event", "rename")

	// If network name provided, filter for containers in that network
	if networkName != "" {
		f.Add("network", networkName)
	}

	return f
}

// Variable to store cancel function for event listener
var containerEventCancel context.CancelFunc
