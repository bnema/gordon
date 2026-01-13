// Package container implements the container management use case.
package container

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	"gordon/internal/boundaries/out"
	"gordon/internal/domain"
)

// Config holds configuration needed by the container service.
type Config struct {
	RegistryAuthEnabled bool
	RegistryDomain      string
	RegistryPort        int
	RegistryUsername    string
	RegistryPassword    string
	VolumeAutoCreate    bool
	VolumePrefix        string
	VolumePreserve      bool
	NetworkIsolation    bool
	NetworkPrefix       string
	DNSSuffix           string
	NetworkGroups       map[string][]string
	Attachments         map[string][]string
	ReadinessDelay      time.Duration // Delay after container starts before considering it ready
}

// Service implements the ContainerService interface.
type Service struct {
	runtime     out.ContainerRuntime
	envLoader   out.EnvLoader
	eventBus    out.EventPublisher
	logWriter   out.ContainerLogWriter
	config      Config
	containers  map[string]*domain.Container
	attachments map[string][]string // ownerDomain → []containerIDs
	mu          sync.RWMutex
}

// NewService creates a new container service.
func NewService(
	runtime out.ContainerRuntime,
	envLoader out.EnvLoader,
	eventBus out.EventPublisher,
	logWriter out.ContainerLogWriter,
	config Config,
) *Service {
	return &Service{
		runtime:     runtime,
		envLoader:   envLoader,
		eventBus:    eventBus,
		logWriter:   logWriter,
		config:      config,
		containers:  make(map[string]*domain.Container),
		attachments: make(map[string][]string),
	}
}

// Deploy creates and starts a container for the given route.
// Implements zero-downtime deployment: new container starts before old one stops.
func (s *Service) Deploy(ctx context.Context, route domain.Route) (*domain.Container, error) {
	// Enrich context with use case fields for all downstream logs
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "Deploy",
		"domain":              route.Domain,
	})
	log := zerowrap.FromCtx(ctx)

	// Get existing container (if any) without holding lock
	s.mu.RLock()
	existing, hasExisting := s.containers[route.Domain]
	s.mu.RUnlock()

	// Clean up orphaned containers (containers with same name but not tracked)
	if err := s.cleanupOrphanedContainers(ctx, route.Domain); err != nil {
		log.WrapErr(err, "failed to cleanup orphaned containers")
	}

	// Build image reference and ensure it's available
	// ensureImage returns the actual image reference used (may be rewritten for internal pulls)
	imageRef := s.buildImageRef(route.Image)
	actualImageRef, err := s.ensureImage(ctx, imageRef)
	if err != nil {
		return nil, err
	}

	// Setup network FIRST (attachments need it)
	networkName := s.getNetworkForApp(route.Domain)
	if err := s.createNetworkIfNeeded(ctx, networkName); err != nil {
		return nil, log.WrapErr(err, "failed to create network")
	}

	// Deploy attachments BEFORE main container (dependencies first)
	if err := s.deployAttachments(ctx, route.Domain, networkName); err != nil {
		log.WrapErr(err, "failed to deploy some attachments")
	}

	// Get exposed ports (use actual image ref from pull)
	exposedPorts, err := s.runtime.GetImageExposedPorts(ctx, actualImageRef)
	if err != nil {
		log.WrapErr(err, "failed to get exposed ports, using defaults")
		exposedPorts = []int{80, 8080, 3000}
	}

	// Load environment
	envVars, err := s.loadEnvironment(ctx, route.Domain, actualImageRef)
	if err != nil {
		return nil, err
	}

	// Setup volumes (use actual image ref from pull)
	volumes, err := s.setupVolumes(ctx, route.Domain, actualImageRef)
	if err != nil {
		return nil, err
	}

	// Determine container name (use temp suffix for zero-downtime if existing)
	containerName := fmt.Sprintf("gordon-%s", route.Domain)
	if hasExisting {
		containerName = fmt.Sprintf("gordon-%s-new", route.Domain)
	}

	// Create container (use actual image ref from pull, but track original in labels)
	containerConfig := &domain.ContainerConfig{
		Image:       actualImageRef,
		Name:        containerName,
		Ports:       exposedPorts,
		Env:         envVars,
		Volumes:     volumes,
		NetworkMode: networkName,
		Hostname:    route.Domain,
		Labels: map[string]string{
			"gordon.domain":  route.Domain,
			"gordon.image":   route.Image,
			"gordon.managed": "true",
			"gordon.route":   route.Domain,
		},
		AutoRemove: false,
	}

	newContainer, err := s.runtime.CreateContainer(ctx, containerConfig)
	if err != nil {
		return nil, log.WrapErr(err, "failed to create container")
	}

	// Start new container
	if err := s.runtime.StartContainer(ctx, newContainer.ID); err != nil {
		s.runtime.RemoveContainer(ctx, newContainer.ID, true)
		return nil, log.WrapErr(err, "failed to start container")
	}

	// Wait for new container to be ready
	if err := s.waitForReady(ctx, newContainer.ID); err != nil {
		s.cleanupFailedContainer(ctx, newContainer.ID)
		return nil, log.WrapErr(err, "container failed readiness check")
	}

	// Re-inspect for updated info (ports, etc.)
	newContainer, err = s.runtime.InspectContainer(ctx, newContainer.ID)
	if err != nil {
		return nil, log.WrapErr(err, "failed to inspect started container")
	}

	// ATOMIC SWITCH: Update tracking first (proxy will now route to new container)
	s.mu.Lock()
	s.containers[route.Domain] = newContainer
	s.mu.Unlock()

	// Publish container deployed event (proxy will invalidate cache)
	s.publishContainerDeployed(ctx, route.Domain, newContainer.ID)

	// NOW stop and remove old container (traffic already going to new one)
	if hasExisting {
		s.cleanupOldContainer(ctx, existing, newContainer.ID, route.Domain)
	}

	// Start container log collection (non-blocking, errors don't fail deployment)
	s.startLogCollection(ctx, newContainer.ID, route.Domain)

	log.Info().
		Str("image", route.Image).
		Str(zerowrap.FieldEntityID, newContainer.ID).
		Ints("ports", newContainer.Ports).
		Str("network", networkName).
		Bool("zero_downtime", hasExisting).
		Msg("container deployed successfully")

	return newContainer, nil
}

// Stop stops a running container.
func (s *Service) Stop(ctx context.Context, containerID string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:    "usecase",
		zerowrap.FieldUseCase:  "Stop",
		zerowrap.FieldEntityID: containerID,
	})
	log := zerowrap.FromCtx(ctx)

	// Stop log collection before stopping container
	if s.logWriter != nil {
		if err := s.logWriter.StopLogging(containerID); err != nil {
			log.Warn().Err(err).Msg("failed to stop container log collection")
		}
	}

	if err := s.runtime.StopContainer(ctx, containerID); err != nil {
		return log.WrapErr(err, "failed to stop container")
	}

	log.Info().Msg("container stopped")
	return nil
}

// Remove removes a container.
func (s *Service) Remove(ctx context.Context, containerID string, force bool) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:    "usecase",
		zerowrap.FieldUseCase:  "Remove",
		zerowrap.FieldEntityID: containerID,
	})
	log := zerowrap.FromCtx(ctx)

	// Stop log collection before removing container
	if s.logWriter != nil {
		if err := s.logWriter.StopLogging(containerID); err != nil {
			log.Warn().Err(err).Msg("failed to stop container log collection")
		}
	}

	// Find domain for cleanup
	var containerDomain string
	s.mu.RLock()
	for d, c := range s.containers {
		if c.ID == containerID {
			containerDomain = d
			break
		}
	}
	s.mu.RUnlock()

	if err := s.runtime.RemoveContainer(ctx, containerID, force); err != nil {
		return log.WrapErr(err, "failed to remove container")
	}

	// Cleanup volumes
	if containerDomain != "" && !s.config.VolumePreserve {
		if err := s.cleanupVolumesForDomain(ctx, containerDomain); err != nil {
			log.WrapErrWithFields(err, "failed to cleanup volumes", map[string]any{"domain": containerDomain})
		}
	}

	// Remove from tracking
	s.mu.Lock()
	var removedDomain string
	for d, c := range s.containers {
		if c.ID == containerID {
			delete(s.containers, d)
			removedDomain = d
			log.Info().Str("domain", d).Msg("container removed")
			break
		}
	}
	s.mu.Unlock()

	// Cleanup network
	if removedDomain != "" && s.config.NetworkIsolation {
		networkName := s.getNetworkForApp(removedDomain)
		if err := s.cleanupNetworkIfEmpty(ctx, networkName); err != nil {
			log.WrapErrWithFields(err, "failed to cleanup network", map[string]any{"network": networkName})
		}
	}

	return nil
}

// Get retrieves a container by domain.
func (s *Service) Get(_ context.Context, domainName string) (*domain.Container, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	container, exists := s.containers[domainName]
	return container, exists
}

// List returns all managed containers.
func (s *Service) List(_ context.Context) map[string]*domain.Container {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]*domain.Container, len(s.containers))
	maps.Copy(result, s.containers)
	return result
}

// HealthCheck performs health checks on all containers.
func (s *Service) HealthCheck(ctx context.Context) map[string]bool {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "HealthCheck",
	})
	log := zerowrap.FromCtx(ctx)

	s.mu.RLock()
	defer s.mu.RUnlock()

	health := make(map[string]bool, len(s.containers))
	for d, c := range s.containers {
		running, err := s.runtime.IsContainerRunning(ctx, c.ID)
		if err != nil {
			log.WrapErrWithFields(err, "health check failed", map[string]any{"domain": d, zerowrap.FieldEntityID: c.ID})
			health[d] = false
		} else {
			health[d] = running
		}
	}
	return health
}

// SyncContainers synchronizes containers with runtime state.
func (s *Service) SyncContainers(ctx context.Context) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "SyncContainers",
	})
	log := zerowrap.FromCtx(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()

	allContainers, err := s.runtime.ListContainers(ctx, false)
	if err != nil {
		return log.WrapErr(err, "failed to list containers")
	}

	managed := make(map[string]*domain.Container)
	for _, c := range allContainers {
		if c.Labels != nil {
			if d, ok := c.Labels["gordon.domain"]; ok && c.Labels["gordon.managed"] == "true" {
				managed[d] = c
			}
		}
	}

	s.containers = managed
	log.Info().Int(zerowrap.FieldCount, len(managed)).Msg("container state synchronized")
	return nil
}

// AutoStart starts containers for configured routes.
func (s *Service) AutoStart(_ context.Context) error {
	return nil
}

// Shutdown gracefully shuts down all managed containers.
func (s *Service) Shutdown(ctx context.Context) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "Shutdown",
	})
	log := zerowrap.FromCtx(ctx)
	log.Info().Msg("shutting down container manager...")

	if err := s.stopAllManagedContainers(ctx); err != nil {
		log.WrapErr(err, "failed to stop all containers during shutdown")
	}

	// Close log writer to stop all log collection
	if s.logWriter != nil {
		if err := s.logWriter.Close(); err != nil {
			log.Warn().Err(err).Msg("failed to close container log writer")
		}
	}

	log.Info().Msg("container manager shutdown complete")
	return nil
}

// UpdateConfig updates the service configuration.
func (s *Service) UpdateConfig(config Config) {
	s.config = config
}

// Helper methods

// cleanupFailedContainer stops and removes a container that failed to start properly.
func (s *Service) cleanupFailedContainer(ctx context.Context, containerID string) {
	log := zerowrap.FromCtx(ctx)
	if err := s.runtime.StopContainer(ctx, containerID); err != nil {
		log.Warn().Err(err).Str(zerowrap.FieldEntityID, containerID).Msg("failed to stop container after failure")
	}
	if err := s.runtime.RemoveContainer(ctx, containerID, true); err != nil {
		log.Warn().Err(err).Str(zerowrap.FieldEntityID, containerID).Msg("failed to remove container after failure")
	}
}

// cleanupOldContainer stops and removes an old container after zero-downtime switch.
// It also renames the new container to the canonical name.
func (s *Service) cleanupOldContainer(ctx context.Context, old *domain.Container, newContainerID, domainName string) {
	log := zerowrap.FromCtx(ctx)
	log.Info().Str(zerowrap.FieldEntityID, old.ID).Msg("stopping old container after zero-downtime switch")

	if s.logWriter != nil {
		if err := s.logWriter.StopLogging(old.ID); err != nil {
			log.Warn().Err(err).Str(zerowrap.FieldEntityID, old.ID).Msg("failed to stop logging for old container")
		}
	}

	if err := s.runtime.StopContainer(ctx, old.ID); err != nil {
		log.Warn().Err(err).Str(zerowrap.FieldEntityID, old.ID).Msg("failed to stop old container")
	}

	if err := s.runtime.RemoveContainer(ctx, old.ID, true); err != nil {
		log.Warn().Err(err).Str(zerowrap.FieldEntityID, old.ID).Msg("failed to remove old container")
	}

	// Rename new container to canonical name
	canonicalName := fmt.Sprintf("gordon-%s", domainName)
	if err := s.runtime.RenameContainer(ctx, newContainerID, canonicalName); err != nil {
		log.Warn().Err(err).Str("canonical_name", canonicalName).Msg("failed to rename container to canonical name")
	}
}

// startLogCollection starts log collection for a container in the background.
// Errors are logged but don't fail the calling operation.
func (s *Service) startLogCollection(ctx context.Context, containerID, domainName string) {
	if s.logWriter == nil {
		return
	}

	log := zerowrap.FromCtx(ctx)

	logStream, err := s.runtime.GetContainerLogs(ctx, containerID, true)
	if err != nil {
		log.Warn().Err(err).Msg("failed to get container logs for collection")
		return
	}

	if err := s.logWriter.StartLogging(ctx, containerID, domainName, logStream); err != nil {
		log.Warn().Err(err).Msg("failed to start container log collection")
		logStream.Close()
	}
}

func (s *Service) buildImageRef(image string) string {
	if !s.config.RegistryAuthEnabled || s.config.RegistryDomain == "" {
		return image
	}

	repoPart := strings.Split(image, ":")[0]
	if strings.HasPrefix(repoPart, s.config.RegistryDomain+"/") {
		return image
	}
	if strings.Contains(repoPart, ".") && strings.Contains(repoPart, "/") {
		return image
	}
	return fmt.Sprintf("%s/%s", s.config.RegistryDomain, image)
}

// ensureImage ensures the image is available locally, pulling if needed.
// Returns the actual image reference to use for container operations (may differ
// from input if localhost rewrite was applied for internal pulls).
func (s *Service) ensureImage(ctx context.Context, imageRef string) (string, error) {
	ctx = zerowrap.CtxWithField(ctx, "image", imageRef)
	log := zerowrap.FromCtx(ctx)

	// Determine if this is an internal deploy and what reference to use
	pullRef := imageRef
	isInternal := domain.IsInternalDeploy(ctx)
	if isInternal && s.config.RegistryPort > 0 {
		pullRef = rewriteToLocalhost(imageRef, s.config.RegistryDomain, s.config.RegistryPort)
		log.Info().
			Str("original_ref", imageRef).
			Str("local_ref", pullRef).
			Msg("internal deploy: using localhost for pull")
	}

	// Check local images using the reference we'll actually use
	localImages, err := s.runtime.ListImages(ctx)
	if err != nil {
		log.WrapErr(err, "failed to list local images, will attempt pull")
	} else {
		normalizedRef := normalizeImageRef(pullRef)
		for _, img := range localImages {
			if normalizeImageRef(img) == normalizedRef {
				log.Info().Msg("image found locally, skipping pull")
				return pullRef, nil
			}
		}
	}

	// Pull image
	log.Info().Msg("pulling image from registry")

	if s.config.RegistryAuthEnabled && !isInternal {
		if err := s.runtime.PullImageWithAuth(ctx, pullRef, s.config.RegistryUsername, s.config.RegistryPassword); err != nil {
			return "", log.WrapErr(err, "failed to pull image with auth")
		}
	} else {
		if err := s.runtime.PullImage(ctx, pullRef); err != nil {
			return "", log.WrapErr(err, "failed to pull image")
		}
	}

	log.Info().Msg("image pulled successfully")
	return pullRef, nil
}

func (s *Service) loadEnvironment(ctx context.Context, domainName, imageRef string) ([]string, error) {
	log := zerowrap.FromCtx(ctx)

	userEnvVars, err := s.envLoader.LoadEnv(ctx, domainName)
	if err != nil {
		return nil, log.WrapErr(err, "failed to load environment variables")
	}

	dockerfileEnvVars, err := s.runtime.InspectImageEnv(ctx, imageRef)
	if err != nil {
		log.WrapErr(err, "failed to inspect image environment")
		dockerfileEnvVars = []string{}
	}

	return mergeEnvironmentVariables(dockerfileEnvVars, userEnvVars), nil
}

func (s *Service) setupVolumes(ctx context.Context, domainName, imageRef string) (map[string]string, error) {
	log := zerowrap.FromCtx(ctx)
	volumes := make(map[string]string)

	if !s.config.VolumeAutoCreate {
		return volumes, nil
	}

	volumePaths, err := s.runtime.InspectImageVolumes(ctx, imageRef)
	if err != nil {
		log.WrapErr(err, "failed to inspect image volumes")
		return volumes, nil
	}

	for _, path := range volumePaths {
		name := generateVolumeName(s.config.VolumePrefix, domainName, path)

		exists, err := s.runtime.VolumeExists(ctx, name)
		if err != nil {
			log.WrapErrWithFields(err, "failed to check volume", map[string]any{"volume": name})
			continue
		}

		if !exists {
			if err := s.runtime.CreateVolume(ctx, name); err != nil {
				log.WrapErrWithFields(err, "failed to create volume", map[string]any{"volume": name})
				continue
			}
			log.Info().Str("volume", name).Str(zerowrap.FieldPath, path).Msg("created volume")
		}

		volumes[path] = name
	}

	return volumes, nil
}

func (s *Service) getNetworkForApp(domainName string) string {
	if !s.config.NetworkIsolation {
		return "bridge"
	}

	for groupName, domains := range s.config.NetworkGroups {
		if slices.Contains(domains, domainName) {
			return s.generateNetworkName(groupName)
		}
	}

	return s.generateNetworkName(domainName)
}

func (s *Service) generateNetworkName(identifier string) string {
	return fmt.Sprintf("%s-%s", s.config.NetworkPrefix, strings.ReplaceAll(identifier, ".", "-"))
}

func (s *Service) createNetworkIfNeeded(ctx context.Context, networkName string) error {
	if networkName == "bridge" || networkName == "default" {
		return nil
	}

	ctx = zerowrap.CtxWithField(ctx, "network", networkName)
	log := zerowrap.FromCtx(ctx)

	exists, err := s.runtime.NetworkExists(ctx, networkName)
	if err != nil {
		return log.WrapErr(err, "failed to check network existence")
	}

	if !exists {
		if err := s.runtime.CreateNetwork(ctx, networkName, map[string]string{"driver": "bridge"}); err != nil {
			return log.WrapErr(err, "failed to create network")
		}
		log.Info().Msg("created network for app isolation")
	}

	return nil
}

func (s *Service) cleanupOrphanedContainers(ctx context.Context, domainName string) error {
	log := zerowrap.FromCtx(ctx)
	expectedName := fmt.Sprintf("gordon-%s", domainName)

	allContainers, err := s.runtime.ListContainers(ctx, true)
	if err != nil {
		return err
	}

	for _, c := range allContainers {
		if c.Name == expectedName {
			log.Info().Str(zerowrap.FieldEntityID, c.ID).Str(zerowrap.FieldStatus, c.Status).Msg("found orphaned container, removing")

			if err := s.runtime.StopContainer(ctx, c.ID); err != nil {
				log.WrapErrWithFields(err, "failed to stop orphaned container", map[string]any{zerowrap.FieldEntityID: c.ID})
			}

			if err := s.runtime.RemoveContainer(ctx, c.ID, true); err != nil {
				log.WrapErrWithFields(err, "failed to remove orphaned container", map[string]any{zerowrap.FieldEntityID: c.ID})
			}
		}
	}

	return nil
}

func (s *Service) cleanupVolumesForDomain(_ context.Context, _ string) error {
	return nil
}

func (s *Service) cleanupNetworkIfEmpty(ctx context.Context, networkName string) error {
	if networkName == "bridge" || networkName == "default" {
		return nil
	}

	ctx = zerowrap.CtxWithField(ctx, "network", networkName)
	log := zerowrap.FromCtx(ctx)

	networks, err := s.runtime.ListNetworks(ctx)
	if err != nil {
		return log.WrapErr(err, "failed to list networks")
	}

	for _, n := range networks {
		if n.Name == networkName && len(n.Containers) == 0 {
			if err := s.runtime.RemoveNetwork(ctx, networkName); err != nil {
				return err
			}
			log.Info().Msg("cleaned up empty network")
			break
		}
	}

	return nil
}

func (s *Service) deployAttachments(ctx context.Context, domainName, networkName string) error {
	attachments, ok := s.config.Attachments[domainName]
	if !ok {
		return nil
	}

	log := zerowrap.FromCtx(ctx)
	for _, svc := range attachments {
		if err := s.deployAttachedService(ctx, domainName, svc, networkName); err != nil {
			log.WrapErrWithFields(err, "failed to deploy attachment", map[string]any{zerowrap.FieldService: svc, "domain": domainName})
		}
	}

	return nil
}

func (s *Service) deployAttachedService(ctx context.Context, ownerDomain, serviceImage, networkName string) error {
	log := zerowrap.FromCtx(ctx)

	// Parse service name from image (e.g., "my-postgres:latest" → "postgres")
	serviceName := extractServiceName(serviceImage)
	containerName := fmt.Sprintf("gordon-%s-%s", sanitizeName(ownerDomain), serviceName)

	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		"attachment":     serviceName,
		"container_name": containerName,
		"owner_domain":   ownerDomain,
	})

	// Check if already running (idempotent)
	existingContainer := s.findContainerByName(ctx, containerName)
	if existingContainer != nil && existingContainer.Status == string(domain.ContainerStatusRunning) {
		log.Debug().Str("container_name", containerName).Msg("attachment already running, skipping")
		return nil
	}

	// Remove existing stopped container if present
	if existingContainer != nil {
		log.Info().Str("container_name", containerName).Msg("removing stopped attachment container")
		if err := s.runtime.RemoveContainer(ctx, existingContainer.ID, true); err != nil {
			log.WrapErr(err, "failed to remove existing attachment container")
		}
	}

	log.Info().Str(zerowrap.FieldService, serviceImage).Msg("deploying attached service")

	// Ensure image (get actual image ref, may be rewritten for internal pulls)
	imageRef := s.buildImageRef(serviceImage)
	actualImageRef, err := s.ensureImage(ctx, imageRef)
	if err != nil {
		return err
	}

	// Get image metadata
	exposedPorts, err := s.runtime.GetImageExposedPorts(ctx, actualImageRef)
	if err != nil {
		log.WrapErr(err, "failed to get exposed ports for attachment, using defaults")
		exposedPorts = []int{}
	}

	// Setup volumes (attachments need persistent data)
	volumes, err := s.setupVolumes(ctx, containerName, actualImageRef)
	if err != nil {
		log.WrapErr(err, "failed to setup volumes for attachment")
		volumes = make(map[string]string)
	}

	// Load environment (attachment-specific env file)
	envVars, err := s.loadEnvironment(ctx, containerName, actualImageRef)
	if err != nil {
		log.WrapErr(err, "failed to load environment for attachment")
		envVars = []string{}
	}

	// Create container with attachment labels (use actual ref, track original in labels)
	config := &domain.ContainerConfig{
		Image:       actualImageRef,
		Name:        containerName,
		Hostname:    serviceName, // Internal DNS: postgres, redis, etc.
		Ports:       exposedPorts,
		Env:         envVars,
		Volumes:     volumes,
		NetworkMode: networkName, // Same network as main app
		Labels: map[string]string{
			"gordon.managed":     "true",
			"gordon.attachment":  "true",
			"gordon.attached-to": ownerDomain,
			"gordon.image":       serviceImage,
		},
	}

	container, err := s.runtime.CreateContainer(ctx, config)
	if err != nil {
		return log.WrapErr(err, "failed to create attachment container")
	}

	// Start container
	if err := s.runtime.StartContainer(ctx, container.ID); err != nil {
		s.runtime.RemoveContainer(ctx, container.ID, true)
		return log.WrapErr(err, "failed to start attachment container")
	}

	// Track attachment
	s.mu.Lock()
	s.attachments[ownerDomain] = append(s.attachments[ownerDomain], container.ID)
	s.mu.Unlock()

	// Start log collection for attachment
	s.startLogCollection(ctx, container.ID, containerName)

	log.Info().Str(zerowrap.FieldEntityID, container.ID).Msg("attachment deployed successfully")
	return nil
}

func (s *Service) stopAllManagedContainers(ctx context.Context) error {
	log := zerowrap.FromCtx(ctx)

	s.mu.RLock()
	containers := make(map[string]*domain.Container, len(s.containers))
	maps.Copy(containers, s.containers)
	s.mu.RUnlock()

	if len(containers) == 0 {
		log.Info().Msg("no managed containers to stop")
		return nil
	}

	log.Info().Int(zerowrap.FieldCount, len(containers)).Msg("stopping all managed containers...")

	errorCount := 0
	for d, c := range containers {
		log.Info().Str("domain", d).Str(zerowrap.FieldEntityID, c.ID).Msg("stopping managed container")

		if err := s.runtime.StopContainer(ctx, c.ID); err != nil {
			log.WrapErrWithFields(err, "failed to stop container", map[string]any{"domain": d, zerowrap.FieldEntityID: c.ID})
			errorCount++
			continue
		}

		s.mu.Lock()
		delete(s.containers, d)
		s.mu.Unlock()

		log.Info().Str("domain", d).Str(zerowrap.FieldEntityID, c.ID).Msg("container stopped successfully")
	}

	if errorCount > 0 {
		return fmt.Errorf("failed to stop %d containers", errorCount)
	}

	log.Info().Msg("all managed containers stopped successfully")
	return nil
}

// Utility functions

func normalizeImageRef(image string) string {
	parts := strings.Split(image, ":")
	repo := parts[0]

	if !strings.Contains(repo, "/") {
		return "docker.io/library/" + repo
	}

	if strings.Count(repo, "/") == 1 && !strings.Contains(strings.Split(repo, "/")[0], ".") {
		return "docker.io/" + repo
	}

	return repo
}

func generateVolumeName(prefix, domainName, volumePath string) string {
	return fmt.Sprintf("%s-%s-%s",
		prefix,
		strings.ReplaceAll(domainName, ".", "-"),
		strings.ReplaceAll(strings.Trim(volumePath, "/"), "/", "-"))
}

func mergeEnvironmentVariables(dockerfileEnv, userEnv []string) []string {
	envMap := make(map[string]string)

	for _, env := range dockerfileEnv {
		if k, v, ok := strings.Cut(env, "="); ok {
			envMap[k] = v
		}
	}

	for _, env := range userEnv {
		if k, v, ok := strings.Cut(env, "="); ok {
			envMap[k] = v
		}
	}

	result := make([]string, 0, len(envMap))
	for k, v := range envMap {
		result = append(result, k+"="+v)
	}

	return result
}

// extractServiceName gets service name from image reference.
// "my-postgres:latest" → "postgres", "redis:7" → "redis"
func extractServiceName(image string) string {
	// Remove tag
	parts := strings.Split(image, ":")
	name := parts[0]

	// Remove registry prefix if present
	if strings.Contains(name, "/") {
		nameParts := strings.Split(name, "/")
		name = nameParts[len(nameParts)-1]
	}

	// Remove common prefixes like "my-"
	name = strings.TrimPrefix(name, "my-")

	return name
}

// sanitizeName makes a domain safe for container naming.
func sanitizeName(domain string) string {
	// Replace dots and other special chars with dashes
	result := strings.ReplaceAll(domain, ".", "-")
	result = strings.ReplaceAll(result, ":", "-")
	result = strings.ReplaceAll(result, "/", "-")
	return result
}

// findContainerByName finds a container by its name.
func (s *Service) findContainerByName(ctx context.Context, name string) *domain.Container {
	containers, err := s.runtime.ListContainers(ctx, true)
	if err != nil {
		return nil
	}

	for _, c := range containers {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// waitForReady waits for a container to be ready.
// Uses simple "running + delay" approach for universal compatibility.
func (s *Service) waitForReady(ctx context.Context, containerID string) error {
	log := zerowrap.FromCtx(ctx)

	// Poll for container to be running (max 30 seconds)
	for i := 0; i < 30; i++ {
		running, err := s.runtime.IsContainerRunning(ctx, containerID)
		if err != nil {
			return err
		}
		if running {
			break
		}
		if i == 29 {
			return fmt.Errorf("container did not start within 30 seconds")
		}
		time.Sleep(time.Second)
	}

	// Additional readiness delay (configurable, default 5 seconds)
	delay := s.config.ReadinessDelay
	if delay == 0 {
		delay = 5 * time.Second
	}

	log.Debug().Dur("delay", delay).Msg("waiting for container readiness")
	time.Sleep(delay)

	// Verify still running after delay
	running, err := s.runtime.IsContainerRunning(ctx, containerID)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("container stopped during readiness delay")
	}

	return nil
}

// publishContainerDeployed publishes a container.deployed event.
func (s *Service) publishContainerDeployed(ctx context.Context, domainName, containerID string) {
	payload := &domain.ContainerEventPayload{
		ContainerID: containerID,
		Domain:      domainName,
		Action:      "deployed",
	}

	if err := s.eventBus.Publish(domain.EventContainerDeployed, payload); err != nil {
		log := zerowrap.FromCtx(ctx)
		log.Warn().Err(err).Msg("failed to publish container deployed event")
	}
}

// rewriteToLocalhost rewrites an image reference to use localhost for internal pulls.
// e.g., "registry.example.com/myapp:latest" -> "localhost:5000/myapp:latest"
func rewriteToLocalhost(imageRef, registryDomain string, registryPort int) string {
	if registryDomain == "" {
		return imageRef
	}

	// Strip the registry domain prefix if present
	prefix := registryDomain + "/"
	imagePath := imageRef
	if strings.HasPrefix(imageRef, prefix) {
		imagePath = strings.TrimPrefix(imageRef, prefix)
	}

	return fmt.Sprintf("localhost:%d/%s", registryPort, imagePath)
}
