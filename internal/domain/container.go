// Package domain contains pure business types without external dependencies.
// These types are used throughout the application and have no tags or framework dependencies.
package domain

import "time"

// Container represents a running container in the system.
type Container struct {
	ID       string
	Image    string
	ImageID  string // Docker image ID (sha256 digest) used to detect redundant deploys
	Name     string
	Status   string
	ExitCode int
	Ports    []int
	Labels   map[string]string
	Created  time.Time
}

// NetworkInfo represents network configuration and state.
type NetworkInfo struct {
	ID         string
	Name       string
	Driver     string
	Containers []string
	Labels     map[string]string
}

// Attachment represents an attached service container.
type Attachment struct {
	Name        string
	Image       string
	ContainerID string
	Status      string
	Network     string
	Ports       []int
}

// RouteInfo combines route configuration with runtime state.
type RouteInfo struct {
	Domain          string
	Image           string
	ContainerID     string
	ContainerStatus string
	Network         string
	Attachments     []Attachment
}

// ContainerConfig holds configuration for creating a container.
type ContainerConfig struct {
	Image           string
	Name            string
	Env             []string
	Ports           []int
	Labels          map[string]string
	WorkingDir      string
	Cmd             []string
	AutoRemove      bool
	Volumes         map[string]string // map[containerPath]volumeName
	ReadOnlyVolumes map[string]string // containerPath -> volumeName (mounted read-only)
	NetworkMode     string            // Network to join
	Hostname        string            // Container hostname for DNS
	Aliases         []string          // Additional network aliases
	MemoryLimit     int64             // Memory limit in bytes (0 = no limit)
	NanoCPUs        int64             // CPU quota in nanoseconds (1e9 = 1 core, 0 = no limit)
	PidsLimit       int64             // Max number of PIDs (0 = no limit)
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
