package container

import (
	"context"
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"

	"gordon/internal/config"
	"gordon/pkg/runtime"
)

// Manager handles container lifecycle and management
type Manager struct {
	runtime   runtime.Runtime
	config    *config.Config
	containers map[string]*runtime.Container // map[domain] -> container
	mu        sync.RWMutex
}

// NewManager creates a new container manager
func NewManager(cfg *config.Config) (*Manager, error) {
	// Create runtime using the factory
	rt, err := CreateRuntime(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create container runtime: %w", err)
	}

	// Test runtime connectivity
	ctx := context.Background()
	if err := rt.Ping(ctx); err != nil {
		return nil, fmt.Errorf("runtime not available: %w", err)
	}

	version, err := rt.Version(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("Could not get runtime version")
	} else {
		log.Info().Str("runtime", cfg.Server.Runtime).Str("version", version).Msg("Container runtime connected")
	}

	return &Manager{
		runtime:    rt,
		config:     cfg,
		containers: make(map[string]*runtime.Container),
	}, nil
}

// DeployContainer deploys a container for a specific route
func (m *Manager) DeployContainer(ctx context.Context, route config.Route) (*runtime.Container, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if container already exists for this domain
	if existing, exists := m.containers[route.Domain]; exists {
		log.Info().Str("domain", route.Domain).Str("container", existing.ID).Msg("Container already exists, restarting")
		
		// Stop and remove existing container
		if err := m.runtime.StopContainer(ctx, existing.ID); err != nil {
			log.Warn().Err(err).Str("container", existing.ID).Msg("Failed to stop existing container")
		}
		
		if err := m.runtime.RemoveContainer(ctx, existing.ID, true); err != nil {
			log.Warn().Err(err).Str("container", existing.ID).Msg("Failed to remove existing container")
		}
		
		delete(m.containers, route.Domain)
	}

	// Pull the image first
	log.Info().Str("image", route.Image).Msg("Pulling image")
	if err := m.runtime.PullImage(ctx, route.Image); err != nil {
		return nil, fmt.Errorf("failed to pull image %s: %w", route.Image, err)
	}

	// Get exposed ports from the image
	exposedPorts, err := m.runtime.GetImageExposedPorts(ctx, route.Image)
	if err != nil {
		log.Warn().Err(err).Str("image", route.Image).Msg("Failed to get exposed ports from image, using defaults")
		exposedPorts = []int{80, 8080, 3000} // Fallback to common web server ports
	}

	// Create container configuration
	containerConfig := &runtime.ContainerConfig{
		Image:      route.Image,
		Name:       fmt.Sprintf("gordon-%s", route.Domain),
		Ports:      exposedPorts,
		Labels: map[string]string{
			"gordon.domain": route.Domain,
			"gordon.image":  route.Image,
			"gordon.managed": "true",
			"gordon.route": route.Domain,
		},
		AutoRemove: false, // Keep containers for inspection
	}

	// Create the container
	container, err := m.runtime.CreateContainer(ctx, containerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create container for %s: %w", route.Domain, err)
	}

	// Start the container
	if err := m.runtime.StartContainer(ctx, container.ID); err != nil {
		// Clean up the created container
		m.runtime.RemoveContainer(ctx, container.ID, true)
		return nil, fmt.Errorf("failed to start container for %s: %w", route.Domain, err)
	}

	// Re-inspect to get updated port information
	container, err = m.runtime.InspectContainer(ctx, container.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect started container: %w", err)
	}

	// Store the container mapping
	m.containers[route.Domain] = container

	log.Info().
		Str("domain", route.Domain).
		Str("image", route.Image).
		Str("container", container.ID).
		Ints("ports", container.Ports).
		Msg("Container deployed successfully")

	return container, nil
}

// GetContainer returns the container for a domain
func (m *Manager) GetContainer(domain string) (*runtime.Container, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	container, exists := m.containers[domain]
	return container, exists
}

// GetContainerPort returns the host port for a domain's container
func (m *Manager) GetContainerPort(ctx context.Context, domain string, internalPort int) (int, error) {
	m.mu.RLock()
	container, exists := m.containers[domain]
	m.mu.RUnlock()
	
	if !exists {
		return 0, fmt.Errorf("no container found for domain %s", domain)
	}

	return m.runtime.GetContainerPort(ctx, container.ID, internalPort)
}

// ListContainers returns all managed containers
func (m *Manager) ListContainers() map[string]*runtime.Container {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	// Return a copy of the map to avoid race conditions
	result := make(map[string]*runtime.Container)
	for domain, container := range m.containers {
		result[domain] = container
	}
	return result
}

// FindContainerByDomain returns the container ID for a domain
func (m *Manager) FindContainerByDomain(domain string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	if container, exists := m.containers[domain]; exists {
		return container.ID, true
	}
	return "", false
}

// FindDomainByContainerID returns the domain for a container ID
func (m *Manager) FindDomainByContainerID(containerID string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	for domain, container := range m.containers {
		if container.ID == containerID {
			return domain, true
		}
	}
	return "", false
}

// StopContainer stops a container by ID
func (m *Manager) StopContainer(ctx context.Context, containerID string) error {
	if err := m.runtime.StopContainer(ctx, containerID); err != nil {
		return fmt.Errorf("failed to stop container %s: %w", containerID, err)
	}

	log.Info().Str("container", containerID).Msg("Container stopped")
	return nil
}

// StopContainerByDomain stops a container for a domain
func (m *Manager) StopContainerByDomain(ctx context.Context, domain string) error {
	m.mu.RLock()
	container, exists := m.containers[domain]
	m.mu.RUnlock()
	
	if !exists {
		return fmt.Errorf("no container found for domain %s", domain)
	}

	return m.StopContainer(ctx, container.ID)
}

// RemoveContainer removes a container by ID
func (m *Manager) RemoveContainer(ctx context.Context, containerID string, force bool) error {
	if err := m.runtime.RemoveContainer(ctx, containerID, force); err != nil {
		return fmt.Errorf("failed to remove container %s: %w", containerID, err)
	}

	// Remove from our tracking map
	m.mu.Lock()
	defer m.mu.Unlock()
	for domain, container := range m.containers {
		if container.ID == containerID {
			delete(m.containers, domain)
			log.Info().Str("domain", domain).Str("container", containerID).Msg("Container removed")
			break
		}
	}
	
	return nil
}

// RemoveContainerByDomain removes a container for a domain
func (m *Manager) RemoveContainerByDomain(ctx context.Context, domain string, force bool) error {
	m.mu.RLock()
	container, exists := m.containers[domain]
	m.mu.RUnlock()
	
	if !exists {
		return fmt.Errorf("no container found for domain %s", domain)
	}

	return m.RemoveContainer(ctx, container.ID, force)
}

// SyncContainers synchronizes the internal state with the actual running containers
func (m *Manager) SyncContainers(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Get all containers from the runtime
	allContainers, err := m.runtime.ListContainers(ctx, false) // only running containers
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	// Find containers managed by Gordon
	managedContainers := make(map[string]*runtime.Container)
	for _, container := range allContainers {
		if container.Labels != nil {
			if domain, exists := container.Labels["gordon.domain"]; exists && container.Labels["gordon.managed"] == "true" {
				managedContainers[domain] = container
			}
		}
	}

	// Update our internal state
	m.containers = managedContainers

	log.Info().Int("count", len(managedContainers)).Msg("Container state synchronized")
	return nil
}

// HealthCheck checks the health of all managed containers
func (m *Manager) HealthCheck(ctx context.Context) map[string]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	health := make(map[string]bool)
	for domain, container := range m.containers {
		running, err := m.runtime.IsContainerRunning(ctx, container.ID)
		if err != nil {
			log.Warn().Err(err).Str("domain", domain).Str("container", container.ID).Msg("Failed to check container health")
			health[domain] = false
		} else {
			health[domain] = running
		}
	}

	return health
}

// Runtime returns the underlying runtime interface
func (m *Manager) Runtime() runtime.Runtime {
	return m.runtime
}