// Package out defines output ports (interfaces) for infrastructure.
// These interfaces define the contract between use cases and driven adapters
// (Docker, filesystem, etc.).
package out

import (
	"context"
	"io"

	"github.com/bnema/gordon/internal/domain"
)

// ContainerRuntime defines the contract for container runtime operations.
// This interface abstracts the underlying container runtime (Docker, Podman, etc.).
type ContainerRuntime interface {
	// Container lifecycle
	CreateContainer(ctx context.Context, config *domain.ContainerConfig) (*domain.Container, error)
	StartContainer(ctx context.Context, containerID string) error
	StopContainer(ctx context.Context, containerID string) error
	RestartContainer(ctx context.Context, containerID string) error
	RemoveContainer(ctx context.Context, containerID string, force bool) error
	RenameContainer(ctx context.Context, containerID, newName string) error

	// Container inspection
	ListContainers(ctx context.Context, all bool) ([]*domain.Container, error)
	InspectContainer(ctx context.Context, containerID string) (*domain.Container, error)
	GetContainerLogs(ctx context.Context, containerID string, follow bool) (io.ReadCloser, error)

	// Image operations
	PullImage(ctx context.Context, image string) error
	PullImageWithAuth(ctx context.Context, image, username, password string) error
	TagImage(ctx context.Context, sourceRef, targetRef string) error
	UntagImage(ctx context.Context, imageRef string) error
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
	GetContainerNetwork(ctx context.Context, containerID string) (string, error)

	// Volume management
	InspectImageVolumes(ctx context.Context, imageRef string) ([]string, error)
	VolumeExists(ctx context.Context, volumeName string) (bool, error)
	CreateVolume(ctx context.Context, volumeName string) error
	RemoveVolume(ctx context.Context, volumeName string, force bool) error

	// Environment inspection
	InspectImageEnv(ctx context.Context, imageRef string) ([]string, error)

	// Label inspection
	GetImageLabels(ctx context.Context, imageRef string) (map[string]string, error)

	// In-container operations
	ExecInContainer(ctx context.Context, containerID string, cmd []string) (*ExecResult, error)
	CopyFromContainer(ctx context.Context, containerID, srcPath string) (io.ReadCloser, error)

	// Network management
	CreateNetwork(ctx context.Context, name string, options map[string]string) error
	RemoveNetwork(ctx context.Context, name string) error
	ListNetworks(ctx context.Context) ([]*domain.NetworkInfo, error)
	NetworkExists(ctx context.Context, name string) (bool, error)
	ConnectContainerToNetwork(ctx context.Context, containerName, networkName string) error
	DisconnectContainerFromNetwork(ctx context.Context, containerName, networkName string) error
}

// ExecResult holds the result of executing a command in a container.
type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}
