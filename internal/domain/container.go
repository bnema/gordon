// Package domain contains pure business types without external dependencies.
// These types are used throughout the application and have no tags or framework dependencies.
package domain

import (
	"strconv"
	"time"
)

// ComponentProcessIdentity is the fixed in-image UNIX identity for a split Gordon role.
type ComponentProcessIdentity struct {
	UID  int
	GID  int
	User string
}

// FixedComponentProcessIdentity returns the immutable non-root identity for a split role.
// These identities are used only by rootless split deployments; monolith and ordinary
// workload containers retain their image/default user and user namespace.
func FixedComponentProcessIdentity(role ComponentRole) (ComponentProcessIdentity, bool) {
	var id int
	switch role {
	case ComponentRoleRuntime:
		id = 21001
	case ComponentRoleControl:
		id = 21002
	case ComponentRoleEdge:
		id = 21003
	case ComponentRoleRegistry:
		id = 21004
	default:
		return ComponentProcessIdentity{}, false
	}
	idText := strconv.Itoa(id)
	return ComponentProcessIdentity{UID: id, GID: id, User: idText + ":" + idText}, true
}

// Container represents a running container in the system.
type Container struct {
	ID              string
	Image           string
	ImageID         string // Docker image ID (sha256 digest) used to detect redundant deploys
	Name            string
	Status          string
	ExitCode        int
	Ports           []int
	Labels          map[string]string
	VolumeMounts    []ContainerVolumeMount
	User            string
	UsernsMode      string
	GroupAdd        []string
	CapDrop         []string
	CapAdd          []string
	NoNewPrivileges bool
	Created         time.Time
}

// ContainerVolumeMount describes a mounted volume-like resource on a container.
type ContainerVolumeMount struct {
	Name        string
	Type        string
	Source      string
	Destination string
	Driver      string
	Mode        string
	Propagation string
	ReadOnly    bool
}

// NetworkInfo represents network configuration and state.
type NetworkInfo struct {
	ID         string
	Name       string
	Driver     string
	Internal   bool
	Containers []string
	Labels     map[string]string
}

// NetworkConfig holds options for creating a container network.
type NetworkConfig struct {
	Driver   string
	Internal bool
	Labels   map[string]string
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

// ContainerPortPublish describes an explicit host port binding for a container port.
type ContainerPortPublish struct {
	HostIP        string
	HostPort      int
	ContainerPort int
	Protocol      NetworkProtocol
}

// ContainerConfig holds configuration for creating a container.
type ContainerConfig struct {
	Image           string
	Name            string
	Env             []string
	Ports           []int
	PortPublishes   []ContainerPortPublish
	Labels          map[string]string
	WorkingDir      string
	Cmd             []string
	AutoRemove      bool
	RestartPolicy   string
	Volumes         map[string]string // map[containerPath]volumeName
	ReadOnlyVolumes map[string]string // containerPath -> volumeName (mounted read-only)
	NetworkMode     string            // Network to join
	Hostname        string            // Container hostname for DNS
	Aliases         []string          // Additional network aliases
	MemoryLimit     int64             // Memory limit in bytes (0 = no limit)
	NanoCPUs        int64             // CPU quota in nanoseconds (1e9 = 1 core, 0 = no limit)
	PidsLimit       int64             // Max number of PIDs (0 = no limit)
	ReadOnlyRootFS  bool              // Mount container root filesystem read-only
	Privileged      bool              // Run container with elevated host privileges
	User            string            // User to run as
	UsernsMode      string            // User namespace mode; keep-id is only for rootless split roles
	GroupAdd        []string          // Supplementary groups for explicitly shared mounted data
	CapDrop         []string          // Linux capabilities to drop; nil uses runtime compat defaults
	CapAdd          []string          // Linux capabilities to add; nil uses runtime compat defaults
	NoNewPrivileges *bool             // nil preserves the runtime hardening default (enabled)
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

const (
	RestartPolicyAlways = "always"
)
