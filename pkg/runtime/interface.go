package runtime

import (
	"context"
	"io"
)

// Container represents a running container
type Container struct {
	ID       string
	Image    string
	Name     string
	Status   string
	Ports    []int
	Labels   map[string]string
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
}