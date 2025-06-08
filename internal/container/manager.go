package container

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"

	"gordon/internal/config"
	"gordon/internal/env"
	"gordon/pkg/runtime"
)

// Manager handles container lifecycle and management
type Manager struct {
	runtime    runtime.Runtime
	config     *config.Config
	containers map[string]*runtime.Container // map[domain] -> container
	envLoader  *env.Loader
	mu         sync.RWMutex
}

// NewManager creates a new container manager
func NewManager(cfg *config.Config) (*Manager, error) {
	// Test runtime connectivity with background context
	ctx := context.Background()
	
	// Create runtime using the factory
	rt, err := CreateRuntime(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create container runtime: %w", err)
	}

	// Test runtime connectivity
	if err := rt.Ping(ctx); err != nil {
		return nil, fmt.Errorf("runtime not available: %w", err)
	}

	version, err := rt.Version(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("Could not get runtime version")
	} else {
		log.Info().Str("runtime", cfg.Server.Runtime).Str("version", version).Msg("Container runtime connected")
	}

	// Create environment loader
	envLoader := env.NewLoader(cfg)
	
	// Register secret providers based on config
	for _, providerName := range cfg.Env.Providers {
		switch providerName {
		case "pass":
			envLoader.RegisterSecretProvider("pass", env.NewPassProvider())
		case "sops":
			envLoader.RegisterSecretProvider("sops", env.NewSopsProvider())
		}
	}
	
	// Ensure env directory exists
	if err := envLoader.EnsureEnvDir(); err != nil {
		log.Warn().Err(err).Msg("Failed to ensure env directory exists")
	}

	// Create empty env files for all configured routes (if they don't exist)
	if err := envLoader.CreateEnvFilesForRoutes(); err != nil {
		log.Warn().Err(err).Msg("Failed to create env files for routes")
	}

	return &Manager{
		runtime:    rt,
		config:     cfg,
		containers: make(map[string]*runtime.Container),
		envLoader:  envLoader,
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

	// Also check for orphaned containers with the same name in the runtime
	expectedContainerName := fmt.Sprintf("gordon-%s", route.Domain)
	allContainers, err := m.runtime.ListContainers(ctx, true) // include stopped containers
	if err != nil {
		log.Warn().Err(err).Msg("Failed to list all containers, proceeding with deployment")
	} else {
		for _, container := range allContainers {
			if container.Name == expectedContainerName {
				log.Info().
					Str("domain", route.Domain).
					Str("container", container.ID).
					Str("status", container.Status).
					Msg("Found orphaned container with same name, removing")

				// Stop if running
				if err := m.runtime.StopContainer(ctx, container.ID); err != nil {
					log.Warn().Err(err).Str("container", container.ID).Msg("Failed to stop orphaned container")
				}

				// Force remove
				if err := m.runtime.RemoveContainer(ctx, container.ID, true); err != nil {
					log.Warn().Err(err).Str("container", container.ID).Msg("Failed to remove orphaned container")
				} else {
					log.Info().Str("container", container.ID).Msg("Successfully removed orphaned container")
				}
			}
		}
	}

	// Construct the full image reference
	imageRef := route.Image

	// If registry auth is enabled and image doesn't already contain a registry domain, prepend it
	if m.config.RegistryAuth.Enabled && m.config.Server.RegistryDomain != "" {
		// Check if image already contains a registry domain (has a '.' and doesn't start with official Docker Hub libraries)
		if !strings.Contains(strings.Split(imageRef, ":")[0], ".") {
			imageRef = fmt.Sprintf("%s/%s", m.config.Server.RegistryDomain, route.Image)
		}
	}

	// Check if image exists locally first
	imageAvailable := false
	localImages, err := m.runtime.ListImages(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to list local images, will attempt to pull from registry")
	} else {
		// Normalize image references for comparison
		normalizedImageRef := normalizeImageRef(imageRef)
		
		for _, localImage := range localImages {
			if normalizeImageRef(localImage) == normalizedImageRef {
				imageAvailable = true
				log.Info().
					Str("image", imageRef).
					Msg("Image found locally, skipping pull")
				break
			}
		}
	}

	// Pull the image only if not available locally
	if !imageAvailable {
		log.Info().Str("image", imageRef).Msg("Image not found locally, pulling from registry")

		// Use authenticated pull if registry auth is configured
		if m.config.RegistryAuth.Enabled {
			log.Info().
				Str("image", imageRef).
				Str("username", m.config.RegistryAuth.Username).
				Msg("Pulling image with authentication")
			
			err := m.runtime.PullImageWithAuth(ctx, imageRef, m.config.RegistryAuth.Username, m.config.RegistryAuth.Password)
			if err != nil {
				// Provide better error message with context
				availableImages := make([]string, 0, len(localImages))
				for _, img := range localImages {
					availableImages = append(availableImages, img)
				}
				
				return nil, fmt.Errorf("failed to pull image '%s' from registry '%s' with authentication. "+
					"Please check: 1) Image name spelling, 2) Registry credentials, 3) Image exists in registry. "+
					"Available local images: %v. Error: %w", 
					imageRef, m.config.Server.RegistryDomain, availableImages, err)
			}
			log.Info().Str("image", imageRef).Msg("Image pulled successfully with authentication")
		} else {
			log.Info().Str("image", imageRef).Msg("Pulling image without authentication")
			
			err := m.runtime.PullImage(ctx, imageRef)
			if err != nil {
				// Provide better error message with context
				availableImages := make([]string, 0, len(localImages))
				for _, img := range localImages {
					availableImages = append(availableImages, img)
				}
				
				return nil, fmt.Errorf("failed to pull image '%s' from public registry. "+
					"Please check: 1) Image name spelling, 2) Image exists in registry. "+
					"Available local images: %v. Error: %w", 
					imageRef, availableImages, err)
			}
			log.Info().Str("image", imageRef).Msg("Image pulled successfully")
		}
	}

	// Get exposed ports from the image
	exposedPorts, err := m.runtime.GetImageExposedPorts(ctx, imageRef)
	if err != nil {
		log.Warn().Err(err).Str("image", imageRef).Msg("Failed to get exposed ports from image, using defaults")
		exposedPorts = []int{80, 8080, 3000} // Fallback to common web server ports
	}

	// Load environment variables for this route
	envVars, err := m.envLoader.LoadEnvForRoute(route.Domain)
	if err != nil {
		log.Error().Err(err).Str("domain", route.Domain).Msg("Failed to load environment variables for route")
		return nil, fmt.Errorf("failed to load environment variables for %s: %w", route.Domain, err)
	}

	// Create container configuration
	containerConfig := &runtime.ContainerConfig{
		Image: imageRef,
		Name:  fmt.Sprintf("gordon-%s", route.Domain),
		Ports: exposedPorts,
		Env:   envVars,
		Labels: map[string]string{
			"gordon.domain":  route.Domain,
			"gordon.image":   route.Image,
			"gordon.managed": "true",
			"gordon.route":   route.Domain,
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

// AutoStartContainers automatically starts containers that match configured routes
func (m *Manager) AutoStartContainers(ctx context.Context) error {
	routes := m.config.GetRoutes()
	if len(routes) == 0 {
		log.Info().Msg("No routes configured, skipping auto-start")
		return nil
	}

	// Get all containers (including stopped ones) to check for matches
	allContainers, err := m.runtime.ListContainers(ctx, true)
	if err != nil {
		return fmt.Errorf("failed to list all containers: %w", err)
	}

	// Create maps for quick lookup
	gordonManagedContainers := make(map[string]*runtime.Container) // domain -> container
	containersByImage := make(map[string][]*runtime.Container)      // image -> containers

	for _, container := range allContainers {
		// Track Gordon-managed containers by domain
		if container.Labels != nil && container.Labels["gordon.managed"] == "true" {
			if domain, exists := container.Labels["gordon.domain"]; exists {
				gordonManagedContainers[domain] = container
			}
		}
		
		// Track all containers by image (normalize image name)
		normalizedImage := m.normalizeImageName(container.Image)
		containersByImage[normalizedImage] = append(containersByImage[normalizedImage], container)
	}

	startedCount := 0
	for _, route := range routes {
		// Build the expected image reference
		expectedImage := route.Image
		if m.config.RegistryAuth.Enabled && m.config.Server.RegistryDomain != "" {
			if !strings.Contains(strings.Split(expectedImage, ":")[0], ".") {
				expectedImage = fmt.Sprintf("%s/%s", m.config.Server.RegistryDomain, route.Image)
			}
		}

		// First, check if we already have a Gordon-managed container for this domain
		if existingContainer, exists := gordonManagedContainers[route.Domain]; exists {
			isRunning, err := m.runtime.IsContainerRunning(ctx, existingContainer.ID)
			if err != nil {
				log.Warn().Err(err).Str("domain", route.Domain).Str("container", existingContainer.ID).Msg("Failed to check container status")
				continue
			}

			if isRunning {
				log.Info().Str("domain", route.Domain).Str("container", existingContainer.ID).Msg("Gordon-managed container already running")
				m.mu.Lock()
				m.containers[route.Domain] = existingContainer
				m.mu.Unlock()
				continue
			}

			// Container exists but is stopped, start it
			log.Info().Str("domain", route.Domain).Str("container", existingContainer.ID).Msg("Starting existing Gordon-managed container")
			if err := m.runtime.StartContainer(ctx, existingContainer.ID); err != nil {
				log.Error().Err(err).Str("domain", route.Domain).Str("container", existingContainer.ID).Msg("Failed to start existing container")
				continue
			}

			// Re-inspect to get updated information
			container, err := m.runtime.InspectContainer(ctx, existingContainer.ID)
			if err != nil {
				log.Error().Err(err).Str("container", existingContainer.ID).Msg("Failed to inspect started container")
				continue
			}

			m.mu.Lock()
			m.containers[route.Domain] = container
			m.mu.Unlock()
			startedCount++
			
			log.Info().Str("domain", route.Domain).Str("container", existingContainer.ID).Msg("Existing Gordon-managed container started successfully")
			continue
		}

		// Second, check if there are existing containers with the same image and clean them up
		normalizedExpectedImage := m.normalizeImageName(expectedImage)
		if containers, exists := containersByImage[normalizedExpectedImage]; exists {
			// Stop and remove existing containers with the same image to avoid conflicts
			for _, container := range containers {
				// Skip Gordon-managed containers (already handled above)
				if container.Labels != nil && container.Labels["gordon.managed"] == "true" {
					continue
				}

				log.Info().Str("domain", route.Domain).Str("container", container.ID).Str("image", container.Image).Msg("Stopping existing container with same image to avoid conflicts")
				
				// Stop the container if it's running
				isRunning, err := m.runtime.IsContainerRunning(ctx, container.ID)
				if err != nil {
					log.Warn().Err(err).Str("container", container.ID).Msg("Failed to check container status")
					continue
				}

				if isRunning {
					if err := m.runtime.StopContainer(ctx, container.ID); err != nil {
						log.Warn().Err(err).Str("container", container.ID).Msg("Failed to stop existing container")
						continue
					}
				}

				// Remove the container to clean up
				if err := m.runtime.RemoveContainer(ctx, container.ID, true); err != nil {
					log.Warn().Err(err).Str("container", container.ID).Msg("Failed to remove existing container")
				} else {
					log.Info().Str("container", container.ID).Msg("Removed existing container with same image")
				}
			}
		}

		// No existing container found, deploy a new one
		log.Info().Str("domain", route.Domain).Str("image", route.Image).Msg("Deploying new container for route")
		
		_, err := m.DeployContainer(ctx, route)
		if err != nil {
			log.Error().Err(err).Str("domain", route.Domain).Str("image", route.Image).Msg("Failed to deploy container for route")
			continue
		}
		startedCount++
	}

	log.Info().Int("started", startedCount).Int("total_routes", len(routes)).Msg("Auto-start completed")
	return nil
}

// normalizeImageName normalizes image names for comparison
func (m *Manager) normalizeImageName(image string) string {
	// Remove tag if present, keep only repository name
	parts := strings.Split(image, ":")
	repo := parts[0]
	
	// If no registry domain and it's a simple name, it's from Docker Hub
	if !strings.Contains(repo, "/") {
		return "docker.io/library/" + repo
	}
	
	// If it has one slash and no domain, it's a user repo on Docker Hub
	if strings.Count(repo, "/") == 1 && !strings.Contains(strings.Split(repo, "/")[0], ".") {
		return "docker.io/" + repo
	}
	
	return repo
}

// StopAllManagedContainers stops all containers managed by Gordon
func (m *Manager) StopAllManagedContainers(ctx context.Context) error {
	m.mu.RLock()
	containers := make(map[string]*runtime.Container)
	for domain, container := range m.containers {
		containers[domain] = container
	}
	m.mu.RUnlock()

	if len(containers) == 0 {
		log.Info().Msg("No managed containers to stop")
		return nil
	}

	log.Info().Int("count", len(containers)).Msg("Stopping all managed containers...")

	var errors []error
	for domain, container := range containers {
		log.Info().Str("domain", domain).Str("container", container.ID).Msg("Stopping managed container")
		
		if err := m.runtime.StopContainer(ctx, container.ID); err != nil {
			log.Error().Err(err).Str("domain", domain).Str("container", container.ID).Msg("Failed to stop managed container")
			errors = append(errors, fmt.Errorf("failed to stop container %s for domain %s: %w", container.ID, domain, err))
			continue
		}

		// Remove from tracking
		m.mu.Lock()
		delete(m.containers, domain)
		m.mu.Unlock()

		log.Info().Str("domain", domain).Str("container", container.ID).Msg("Managed container stopped successfully")
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to stop %d containers: %v", len(errors), errors)
	}

	log.Info().Msg("All managed containers stopped successfully")
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

// UpdateConfig updates the manager's configuration and creates env files for new routes
func (m *Manager) UpdateConfig(cfg *config.Config) {
	m.config = cfg
	
	// Update env loader config
	m.envLoader.UpdateConfig(cfg)
	
	// Create env files for any new routes
	if err := m.envLoader.CreateEnvFilesForRoutes(); err != nil {
		log.Warn().Err(err).Msg("Failed to create env files for new routes during config update")
	}
}

// normalizeImageRef normalizes image references for comparison
func normalizeImageRef(imageRef string) string {
	// Split image and tag
	parts := strings.Split(imageRef, ":")
	image := parts[0]
	tag := "latest"
	if len(parts) > 1 {
		tag = parts[1]
	}

	// Normalize Docker Hub references
	if !strings.Contains(image, "/") {
		// Official library image (e.g., "nginx" -> "docker.io/library/nginx")
		image = "docker.io/library/" + image
	} else if strings.Count(image, "/") == 1 && !strings.Contains(strings.Split(image, "/")[0], ".") {
		// User repository (e.g., "user/repo" -> "docker.io/user/repo")
		image = "docker.io/" + image
	}

	return image + ":" + tag
}
