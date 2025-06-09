package container

import (
	"context"

	"gordon/internal/config"
	"gordon/pkg/runtime"
)

// ManagerInterface defines the interface for container management operations
// This interface is used to enable testing with mocks
type ManagerInterface interface {
	DeployContainer(ctx context.Context, route config.Route) (*runtime.Container, error)
	StopContainer(ctx context.Context, containerID string) error
	RemoveContainer(ctx context.Context, containerID string, force bool) error
	ListContainers() map[string]*runtime.Container
	GetContainer(domain string) (*runtime.Container, bool)
	StopContainerByDomain(ctx context.Context, domain string) error
	Runtime() runtime.Runtime
	HealthCheck(ctx context.Context) map[string]bool
	SyncContainers(ctx context.Context) error
	GetContainerPort(ctx context.Context, domain string, internalPort int) (int, error)
	GetNetworkForApp(domain string) string
	AutoStartContainers(ctx context.Context) error
	StopAllManagedContainers(ctx context.Context) error
}

// Ensure Manager implements the interface
var _ ManagerInterface = (*Manager)(nil)