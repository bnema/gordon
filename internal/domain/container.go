// Package domain contains pure business types without external dependencies.
// These types are used throughout the application and have no tags or framework dependencies.
package domain

// Container represents a running container in the system.
type Container struct {
	ID     string
	Image  string
	Name   string
	Status string
	Ports  []int
	Labels map[string]string
}

// NetworkInfo represents network configuration and state.
type NetworkInfo struct {
	ID         string
	Name       string
	Driver     string
	Containers []string
	Labels     map[string]string
}

// ContainerConfig holds configuration for creating a container.
type ContainerConfig struct {
	Image       string
	Name        string
	Env         []string
	Ports       []int
	Labels      map[string]string
	WorkingDir  string
	Cmd         []string
	AutoRemove  bool
	Volumes     map[string]string // map[containerPath]volumeName
	NetworkMode string            // Network to join
	Hostname    string            // Container hostname for DNS
	Aliases     []string          // Additional network aliases
}

// ContainerStatus represents the current state of a container.
type ContainerStatus string

const (
	ContainerStatusRunning ContainerStatus = "running"
	ContainerStatusStopped ContainerStatus = "stopped"
	ContainerStatusCreated ContainerStatus = "created"
	ContainerStatusExited  ContainerStatus = "exited"
	ContainerStatusPaused  ContainerStatus = "paused"
	ContainerStatusUnknown ContainerStatus = "unknown"
)
