package runtime

import (
	"context"
	"io"
	"time"
)

// Container represents a running container
type Container struct {
	ID     string
	Image  string
	Name   string
	Status string
	Ports  []int
	Labels map[string]string
}

// NetworkInfo represents a network
type NetworkInfo struct {
	ID         string
	Name       string
	Driver     string
	Containers []string
	Labels     map[string]string
}

// ContainerConfig holds configuration for creating a container
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

// PruneReport represents the result of an image prune operation.
type PruneReport struct {
	// DeletedIDs contains runtime-provided image identifiers removed by prune.
	DeletedIDs []string
	// SpaceReclaimed is the number of bytes reclaimed by prune.
	SpaceReclaimed int64
}

// ImageDetail represents detailed metadata for an image.
type ImageDetail struct {
	// ID is the runtime image identifier (for example, image ID/digest).
	ID string
	// RepoTags contains repository:tag references known for the image.
	RepoTags []string
	// Size is the image size in bytes.
	Size int64
	// Created is the image creation timestamp as reported by the runtime.
	Created time.Time
}

// Runtime interface defines the contract for container runtime implementations
type Runtime interface {
	// Container lifecycle
	CreateContainer(ctx context.Context, config *ContainerConfig) (*Container, error)
	StartContainer(ctx context.Context, containerID string) error
	StopContainer(ctx context.Context, containerID string) error
	RestartContainer(ctx context.Context, containerID string) error
	RemoveContainer(ctx context.Context, containerID string, force bool) error

	// Container inspection
	ListContainers(ctx context.Context, all bool) ([]*Container, error)
	InspectContainer(ctx context.Context, containerID string) (*Container, error)
	GetContainerLogs(ctx context.Context, containerID string, follow bool) (io.ReadCloser, error)

	// Image operations
	PullImage(ctx context.Context, image string) error
	PullImageWithAuth(ctx context.Context, image, username, password string) error
	RemoveImage(ctx context.Context, image string, force bool) error
	ListImages(ctx context.Context) ([]string, error)
	// ListImagesDetailed returns metadata for all images visible to the runtime.
	ListImagesDetailed(ctx context.Context) ([]ImageDetail, error)
	// PruneImages removes images eligible for prune; when danglingOnly is true,
	// only dangling images are pruned.
	PruneImages(ctx context.Context, danglingOnly bool) (PruneReport, error)

	// Runtime information
	Ping(ctx context.Context) error
	Version(ctx context.Context) (string, error)

	// Health and status
	IsContainerRunning(ctx context.Context, containerID string) (bool, error)
	GetContainerPort(ctx context.Context, containerID string, internalPort int) (int, error)

	// Image and port inspection
	GetImageExposedPorts(ctx context.Context, imageRef string) ([]int, error)
	GetContainerExposedPorts(ctx context.Context, containerID string) ([]int, error)
	GetContainerNetworkInfo(ctx context.Context, containerID string) (string, int, error)

	// Volume management
	InspectImageVolumes(ctx context.Context, imageRef string) ([]string, error)
	VolumeExists(ctx context.Context, volumeName string) (bool, error)
	CreateVolume(ctx context.Context, volumeName string) error
	RemoveVolume(ctx context.Context, volumeName string, force bool) error

	// Environment inspection
	InspectImageEnv(ctx context.Context, imageRef string) ([]string, error)

	// Network management
	CreateNetwork(ctx context.Context, name string, options map[string]string) error
	RemoveNetwork(ctx context.Context, name string) error
	ListNetworks(ctx context.Context) ([]*NetworkInfo, error)
	NetworkExists(ctx context.Context, name string) (bool, error)
	ConnectContainerToNetwork(ctx context.Context, containerName, networkName string) error
	DisconnectContainerFromNetwork(ctx context.Context, containerName, networkName string) error
}
