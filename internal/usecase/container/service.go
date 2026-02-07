// Package container implements the container management use case.
package container

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// Config holds configuration needed by the container service.
type Config struct {
	RegistryAuthEnabled      bool
	RegistryDomain           string
	RegistryPort             int
	ServiceTokenUsername     string
	ServiceToken             string
	InternalRegistryUsername string
	InternalRegistryPassword string
	PullPolicy               string
	VolumeAutoCreate         bool
	VolumePrefix             string
	VolumePreserve           bool
	NetworkIsolation         bool
	NetworkPrefix            string
	DNSSuffix                string
	NetworkGroups            map[string][]string
	Attachments              map[string][]string
	ReadinessDelay           time.Duration // Delay after container starts before considering it ready
	DrainDelay               time.Duration // Grace period after cache invalidation before stopping old container
}

const (
	PullPolicyAlways       = "always"
	PullPolicyIfNotPresent = "if-not-present"
	PullPolicyIfTagChanged = "if-tag-changed"

	// readinessRecoveryWindow is an additional grace window used when a container
	// is briefly not running at the end of readiness delay. This avoids false
	// negatives during short startup flaps.
	readinessRecoveryWindow = 30 * time.Second
	internalPullMaxAttempts = 3
)

// Service implements the ContainerService interface.
type Service struct {
	runtime          out.ContainerRuntime
	envLoader        out.EnvLoader
	eventBus         out.EventPublisher
	logWriter        out.ContainerLogWriter
	cacheInvalidator out.ProxyCacheInvalidator
	config           Config
	containers       map[string]*domain.Container
	attachments      map[string][]string // ownerDomain → []containerIDs
	mu               sync.RWMutex
	deployMu         sync.Map // per-domain deploy locks (domain → *domainDeployLock)
}

// domainDeployLock is a context-aware mutex using a buffered channel.
// It allows Lock() to be interrupted by context cancellation.
type domainDeployLock struct {
	ch chan struct{}
}

// newDomainDeployLock creates a new lock that is initially unlocked.
func newDomainDeployLock() *domainDeployLock {
	l := &domainDeployLock{ch: make(chan struct{}, 1)}
	l.ch <- struct{}{}
	return l
}

// Lock acquires the lock or returns an error if the context is cancelled.
func (l *domainDeployLock) Lock(ctx context.Context) error {
	// Fast path: honor already-canceled contexts before blocking.
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.ch:
		// Re-check context after acquisition: if canceled while we were
		// in the select, release the token so the lock isn't consumed.
		if err := ctx.Err(); err != nil {
			l.ch <- struct{}{}
			return err
		}
		return nil
	}
}

// Unlock releases the lock.
func (l *domainDeployLock) Unlock() {
	select {
	case l.ch <- struct{}{}:
	default:
		// This should never happen if Lock/Unlock are used correctly.
		// Panic to detect double-unlocks or other misuse early.
		panic("domainDeployLock: double unlock detected")
	}
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

// SetProxyCacheInvalidator sets the proxy cache invalidator for synchronous
// cache invalidation during zero-downtime deployments. This must be called
// after construction because the proxy service is created after the container
// service during application initialization.
func (s *Service) SetProxyCacheInvalidator(inv out.ProxyCacheInvalidator) {
	s.mu.Lock()
	s.cacheInvalidator = inv
	s.mu.Unlock()
}

// domainDeployMu returns a per-domain lock for serializing deploy operations.
func (s *Service) domainDeployMu(domain string) *domainDeployLock {
	v, _ := s.deployMu.LoadOrStore(domain, newDomainDeployLock())
	// Type assertion is safe: LoadOrStore always stores *domainDeployLock values,
	// and any concurrent LoadOrStore calls will also store *domainDeployLock.
	return v.(*domainDeployLock)
}

// acquireDomainDeployLock acquires the per-domain deploy lock with context cancellation support.
// Returns an unlock function that must be called to release the lock.
func (s *Service) acquireDomainDeployLock(ctx context.Context, domain string) (func(), error) {
	lock := s.domainDeployMu(domain)
	if err := lock.Lock(ctx); err != nil {
		return nil, err
	}
	return func() { lock.Unlock() }, nil
}

// buildContainerConfig constructs the container configuration for deployment.
func (s *Service) deploymentContainerName(containerDomain string, existing *domain.Container) string {
	canonicalName := fmt.Sprintf("gordon-%s", containerDomain)
	if existing == nil {
		return canonicalName
	}

	// Alternate temporary names if the tracked container was left with a temp suffix.
	// This prevents name collisions after interrupted zero-downtime deploys.
	newName := canonicalName + "-new"
	nextName := canonicalName + "-next"
	switch existing.Name {
	case newName:
		return nextName
	case nextName:
		return newName
	default:
		return newName
	}
}

func (s *Service) buildContainerConfig(containerDomain, image, actualImageRef string, exposedPorts []int, envVars []string, volumes map[string]string, networkName string, existing *domain.Container) *domain.ContainerConfig {
	containerName := s.deploymentContainerName(containerDomain, existing)

	return &domain.ContainerConfig{
		Image:       actualImageRef,
		Name:        containerName,
		Ports:       exposedPorts,
		Env:         envVars,
		Volumes:     volumes,
		NetworkMode: networkName,
		Hostname:    containerDomain,
		Labels: map[string]string{
			"gordon.domain":  containerDomain,
			"gordon.image":   image,
			"gordon.managed": "true",
			"gordon.route":   containerDomain,
		},
		AutoRemove: false,
	}
}

// Deploy creates and starts a container for the given route.
// Implements zero-downtime deployment: new container starts before old one stops.
func (s *Service) Deploy(ctx context.Context, route domain.Route) (*domain.Container, error) {
	// Serialize deploys for the same domain to prevent race conditions
	// (e.g. multiple image.pushed events + explicit deploy call from CLI).
	unlock, err := s.acquireDomainDeployLock(ctx, route.Domain)
	if err != nil {
		return nil, err
	}
	defer unlock()

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
	// Skip the currently tracked container to preserve zero-downtime deployment
	existingID := ""
	if hasExisting {
		existingID = existing.ID
	}
	if err := s.cleanupOrphanedContainers(ctx, route.Domain, existingID); err != nil {
		log.WrapErr(err, "failed to cleanup orphaned containers")
	}

	// Build image reference and ensure it's available.
	// ensureImage returns the canonical reference; internal pulls may use the local registry.
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
		return nil, log.WrapErr(err, "failed to deploy attachments")
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

	// Build container configuration
	containerConfig := s.buildContainerConfig(route.Domain, route.Image, actualImageRef, exposedPorts, envVars, volumes, networkName, existing)

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

	// Publish container deployed event (other subscribers)
	s.publishContainerDeployed(ctx, route.Domain, newContainer.ID)

	// Synchronous cache invalidation: ensure the proxy stops routing to the
	// old container immediately, before we stop it. This fixes the race where
	// the async event bus invalidation arrives after the old container is killed.
	s.mu.RLock()
	inv := s.cacheInvalidator
	s.mu.RUnlock()
	if inv != nil {
		inv.InvalidateTarget(ctx, route.Domain)
	}

	// Grace period: let in-flight requests to the old container finish draining.
	if hasExisting {
		drainDelay := s.config.DrainDelay
		if drainDelay == 0 {
			drainDelay = 2 * time.Second
		}
		select {
		case <-time.After(drainDelay):
		case <-ctx.Done():
		}
	}

	// NOW safe to stop and remove old container
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

// Restart restarts a running container for the given domain.
func (s *Service) Restart(ctx context.Context, domainName string, withAttachments bool) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "Restart",
		"domain":              domainName,
	})
	log := zerowrap.FromCtx(ctx)

	s.mu.RLock()
	container, exists := s.containers[domainName]
	attachments := s.attachments[domainName]
	var attachmentIDs []string
	if len(attachments) > 0 {
		attachmentIDs = make([]string, len(attachments))
		copy(attachmentIDs, attachments)
	}
	s.mu.RUnlock()

	if !exists || container == nil {
		return domain.ErrContainerNotFound
	}

	// Restart the main container
	if err := s.runtime.RestartContainer(ctx, container.ID); err != nil {
		if !isContainerNotFoundError(err) {
			return log.WrapErr(err, "failed to restart container")
		}

		// In-memory container ID can become stale after external runtime changes.
		// Re-sync and retry once with the latest tracked container.
		log.Warn().
			Err(err).
			Str(zerowrap.FieldEntityID, container.ID).
			Msg("tracked container missing during restart, attempting state reconciliation")

		if syncErr := s.SyncContainers(ctx); syncErr != nil {
			log.Warn().Err(syncErr).Msg("failed to sync container state during restart recovery")
		}

		s.mu.RLock()
		refreshed, refreshedExists := s.containers[domainName]
		attachmentIDs = append([]string{}, s.attachments[domainName]...)
		s.mu.RUnlock()

		if !refreshedExists || refreshed == nil {
			return domain.ErrContainerNotFound
		}
		if err := s.runtime.RestartContainer(ctx, refreshed.ID); err != nil {
			return log.WrapErr(err, "failed to restart container after state reconciliation")
		}
		container = refreshed
	}
	log.Info().Str(zerowrap.FieldEntityID, container.ID).Msg("container restarted")

	// Restart attachments if requested
	if withAttachments && len(attachmentIDs) > 0 {
		for _, attachID := range attachmentIDs {
			if err := s.runtime.RestartContainer(ctx, attachID); err != nil {
				log.Warn().Err(err).Str("attachment_id", attachID).Msg("failed to restart attachment")
				continue // Don't fail the whole operation for one attachment
			}
			log.Info().Str("attachment_id", attachID).Msg("attachment restarted")
		}
	}

	return nil
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

	// Find domain and attachment IDs for cleanup
	containerDomain, attachmentIDs, cfg := s.findContainerContext(containerID)

	if err := s.runtime.RemoveContainer(ctx, containerID, force); err != nil {
		return log.WrapErr(err, "failed to remove container")
	}

	// Cleanup volumes
	if containerDomain != "" && !cfg.VolumePreserve {
		if err := s.cleanupVolumesForDomain(ctx, containerDomain); err != nil {
			log.WrapErrWithFields(err, "failed to cleanup volumes", map[string]any{"domain": containerDomain})
		}
	}

	// Clean up attachment containers
	s.removeAttachments(ctx, attachmentIDs)

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
	if removedDomain != "" {
		delete(s.attachments, removedDomain)
	}
	s.mu.Unlock()

	// Note: We intentionally do NOT clean up deployMu entries to avoid a race condition:
	// If Remove deletes the mutex entry while a concurrent Deploy is acquiring the lock,
	// the Deploy would create a fresh mutex, breaking serialization and allowing concurrent
	// deploys to the same domain. The memory footprint is acceptable (one small struct per
	// domain ever deployed), and this is the safer choice for correctness.

	// Cleanup network
	if removedDomain != "" && cfg.NetworkIsolation {
		networkName := s.getNetworkForApp(removedDomain)
		if err := s.cleanupNetworkIfEmpty(ctx, networkName); err != nil {
			log.WrapErrWithFields(err, "failed to cleanup network", map[string]any{"network": networkName})
		}
	}

	return nil
}

// findContainerContext looks up the domain, attachment IDs, and config snapshot for a container.
func (s *Service) findContainerContext(containerID string) (string, []string, Config) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg := s.config
	var containerDomain string
	for d, c := range s.containers {
		if c.ID == containerID {
			containerDomain = d
			break
		}
	}
	var attachmentIDs []string
	if containerDomain != "" {
		attachmentIDs = append(attachmentIDs, s.attachments[containerDomain]...)
	}
	return containerDomain, attachmentIDs, cfg
}

// removeAttachments stops and removes attachment containers best-effort.
func (s *Service) removeAttachments(ctx context.Context, attachmentIDs []string) {
	log := zerowrap.FromCtx(ctx)
	for _, attachID := range attachmentIDs {
		if s.logWriter != nil {
			if err := s.logWriter.StopLogging(attachID); err != nil {
				log.Warn().Err(err).Str("attachment_id", attachID).Msg("failed to stop attachment log collection")
			}
		}
		if err := s.runtime.StopContainer(ctx, attachID); err != nil {
			log.Warn().Err(err).Str("attachment_id", attachID).Msg("failed to stop attachment container")
		}
		if err := s.runtime.RemoveContainer(ctx, attachID, true); err != nil {
			log.Warn().Err(err).Str("attachment_id", attachID).Msg("failed to remove attachment container")
		}
	}
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

// ListRoutesWithDetails returns routes with network and attachment info.
// Note: Container map is copied under lock, then external calls are made without lock.
// If containers are removed between copy and runtime calls, errors are handled gracefully
// (network becomes empty string). This trade-off avoids holding locks during I/O.
func (s *Service) ListRoutesWithDetails(ctx context.Context) []domain.RouteInfo {
	s.mu.RLock()
	containers := make(map[string]*domain.Container, len(s.containers))
	maps.Copy(containers, s.containers)
	s.mu.RUnlock()

	// Fetch all attachments once to avoid N+1 queries
	attachmentsByDomain := s.getAllAttachments(ctx)

	results := make([]domain.RouteInfo, 0, len(containers))
	for domainName, container := range containers {
		network := ""
		image := ""
		containerID := ""
		status := ""
		if container != nil {
			containerID = container.ID
			status = container.Status
			image = container.Image
			if container.Labels != nil {
				if labelImage, ok := container.Labels[domain.LabelImage]; ok && labelImage != "" {
					image = labelImage
				}
			}
			// Strip registry domain prefix from image for cleaner display
			image = s.stripRegistryPrefix(image)
			if networkName, err := s.runtime.GetContainerNetwork(ctx, container.ID); err == nil {
				network = networkName
			}
		}

		results = append(results, domain.RouteInfo{
			Domain:          domainName,
			Image:           image,
			ContainerID:     containerID,
			ContainerStatus: status,
			Network:         network,
			Attachments:     attachmentsByDomain[domainName],
		})
	}

	return results
}

// stripRegistryPrefix removes the configured registry domain prefix from an image reference.
// For example, "reg.example.com/myapp:latest" becomes "myapp:latest" if registry domain is "reg.example.com".
func (s *Service) stripRegistryPrefix(image string) string {
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()

	if cfg.RegistryDomain == "" {
		return image
	}
	prefix := strings.TrimSuffix(cfg.RegistryDomain, "/") + "/"
	if strings.HasPrefix(image, prefix) {
		return strings.TrimPrefix(image, prefix)
	}
	return image
}

// ListAttachments returns attachments for a domain.
func (s *Service) ListAttachments(ctx context.Context, domainName string) []domain.Attachment {
	return s.getAttachmentsForDomain(ctx, domainName)
}

// ListNetworks returns Gordon-managed networks.
func (s *Service) ListNetworks(ctx context.Context) ([]*domain.NetworkInfo, error) {
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()

	networks, err := s.runtime.ListNetworks(ctx)
	if err != nil {
		return nil, err
	}

	var filtered []*domain.NetworkInfo
	for _, network := range networks {
		if strings.HasPrefix(network.Name, cfg.NetworkPrefix+"-") {
			filtered = append(filtered, network)
		}
	}

	return filtered, nil
}

// HealthCheck performs health checks on all containers.
func (s *Service) HealthCheck(ctx context.Context) map[string]bool {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "HealthCheck",
	})
	log := zerowrap.FromCtx(ctx)

	// Copy container IDs under lock, then release before making Docker API calls
	s.mu.RLock()
	snapshot := make(map[string]string, len(s.containers))
	for d, c := range s.containers {
		snapshot[d] = c.ID
	}
	s.mu.RUnlock()

	health := make(map[string]bool, len(snapshot))
	for d, id := range snapshot {
		running, err := s.runtime.IsContainerRunning(ctx, id)
		if err != nil {
			log.WrapErrWithFields(err, "health check failed", map[string]any{"domain": d, zerowrap.FieldEntityID: id})
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

	// List containers without holding lock to avoid blocking during Docker API call
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

	s.mu.Lock()
	s.containers = managed
	s.mu.Unlock()

	log.Info().Int(zerowrap.FieldCount, len(managed)).Msg("container state synchronized")
	return nil
}

// AutoStart starts containers for the provided routes that aren't running.
func (s *Service) AutoStart(ctx context.Context, routes []domain.Route) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "AutoStart",
	})
	log := zerowrap.FromCtx(ctx)

	log.Info().Int("route_count", len(routes)).Msg("auto-starting containers for configured routes")

	var started, skipped, errors int
	for _, route := range routes {
		// Check if container already exists and is running
		if _, exists := s.Get(ctx, route.Domain); exists {
			log.Debug().Str("domain", route.Domain).Msg("container already running, skipping")
			skipped++
			continue
		}

		log.Info().
			Str("domain", route.Domain).
			Str("image", route.Image).
			Msg("auto-starting container for route")

		if _, err := s.Deploy(ctx, route); err != nil {
			log.Warn().Err(err).Str("domain", route.Domain).Msg("failed to auto-start container")
			errors++
			continue
		}

		started++
	}

	log.Info().
		Int("started", started).
		Int("skipped", skipped).
		Int("errors", errors).
		Msg("auto-start completed")

	if errors > 0 {
		return fmt.Errorf("auto-start completed with %d errors", errors)
	}
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
	s.mu.Lock()
	s.config = config
	s.mu.Unlock()
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
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()

	if !cfg.RegistryAuthEnabled || cfg.RegistryDomain == "" {
		return image
	}

	// Normalize registry domain by trimming any trailing slash
	reg := strings.TrimSuffix(cfg.RegistryDomain, "/")

	// Check if image already has the registry domain prefix
	prefix := reg + "/"
	if strings.HasPrefix(image, prefix) {
		return image
	}

	// Check if image already has an explicit registry (host:port/ or host.domain/).
	// We need to detect patterns like:
	// - "docker.io/library/nginx:latest" (has dot and slash)
	// - "localhost:5001/myapp:latest" (has host:port/ pattern)
	// - "myregistry:5000/app:v1" (has host:port/ pattern)
	// - "[fd00::1]:5000/app:v1" (IPv6 with port)
	if hasExplicitRegistry(image) {
		return image
	}

	return fmt.Sprintf("%s/%s", reg, image)
}

// hasExplicitRegistry checks if an image reference already includes an explicit registry.
// Returns true for patterns like "docker.io/image", "localhost:5000/image", "localhost/image",
// "registry:8080/image", "[fd00::1]/image", "[fd00::1]:5000/image".
func hasExplicitRegistry(image string) bool {
	// Find the first slash which separates registry from image name
	slashIdx := strings.Index(image, "/")
	if slashIdx == -1 {
		return false // No slash means no registry prefix (e.g., "myapp:latest")
	}

	// Extract the part before the first slash (potential registry)
	registryPart := image[:slashIdx]

	// Check for bracketed IPv6 address (e.g., "[fd00::1]" or "[fd00::1]:5000")
	if strings.HasPrefix(registryPart, "[") {
		// Look for closing bracket
		if closeBracket := strings.Index(registryPart, "]"); closeBracket != -1 {
			// Valid bracketed IPv6: either ends at ] or has ]:port
			return true
		}
	}

	// Check for localhost (with or without port)
	if registryPart == "localhost" {
		return true
	}

	// Check for host:port pattern (e.g., "localhost:5000", "registry:8080")
	if colonIdx := strings.LastIndex(registryPart, ":"); colonIdx != -1 {
		port := registryPart[colonIdx+1:]
		// If everything after colon is digits, it's a port
		if len(port) > 0 && isNumeric(port) {
			return true
		}
	}

	// Check for domain pattern (has a dot, e.g., "docker.io", "gcr.io")
	if strings.Contains(registryPart, ".") {
		return true
	}

	return false
}

// isNumeric checks if a string contains only digits.
func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func normalizePullPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case PullPolicyAlways:
		return PullPolicyAlways
	case PullPolicyIfTagChanged:
		return PullPolicyIfTagChanged
	case PullPolicyIfNotPresent:
		return PullPolicyIfNotPresent
	default:
		return PullPolicyIfNotPresent
	}
}

func isDigestRef(imageRef string) bool {
	return strings.Contains(imageRef, "@sha256:")
}

func (s *Service) pullRefForDeploy(ctx context.Context, imageRef string) (string, bool) {
	if !domain.IsInternalDeploy(ctx) {
		return imageRef, false
	}
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()
	return rewriteToLocalRegistry(imageRef, cfg.RegistryDomain, cfg.RegistryPort), true
}

// ensureImage ensures the image is available locally, pulling if needed.
// Returns the image reference to use for container operations:
//   - For digest references (@sha256:...), returns the pullRef since Docker can't tag digests
//   - For tagged images, returns the original imageRef after tagging the pulled image
func (s *Service) ensureImage(ctx context.Context, imageRef string) (string, error) {
	ctx = zerowrap.CtxWithField(ctx, "image", imageRef)
	log := zerowrap.FromCtx(ctx)

	// Determine if this is an internal deploy and what reference to use for pulls.
	pullRef, isInternal := s.pullRefForDeploy(ctx, imageRef)
	if pullRef != imageRef {
		log.Info().
			Str("original_ref", imageRef).
			Str("pull_ref", pullRef).
			Msg("internal deploy: using localhost registry for pull")
	}

	found, err := s.ensureLocalImage(ctx, imageRef, pullRef)
	if err != nil {
		return "", err
	}
	if found {
		return imageRef, nil
	}

	// Pull image
	log.Info().Msg("pulling image from registry")

	if err := s.pullImage(ctx, pullRef, isInternal); err != nil {
		return "", err
	}

	// For digest references, we can't create a tag (Docker doesn't allow it).
	// In this case, use the pullRef directly since the image is available under that reference.
	if strings.Contains(imageRef, "@sha256:") {
		log.Info().
			Str("pull_ref", pullRef).
			Msg("digest reference: using pull reference for container operations")
		return pullRef, nil
	}

	if err := s.tagImageIfNeeded(ctx, pullRef, imageRef); err != nil {
		return "", err
	}

	// Clean up the temporary pull reference tag to avoid duplicate entries
	if pullRef != imageRef {
		if err := s.runtime.UntagImage(ctx, pullRef); err != nil {
			// Log but don't fail - the canonical tag is already applied
			log.Debug().Err(err).Str("pull_ref", pullRef).Msg("failed to remove temporary pull tag")
		}
	}

	log.Info().Msg("image pulled successfully")
	return imageRef, nil
}

func (s *Service) ensureLocalImage(ctx context.Context, imageRef, pullRef string) (bool, error) {
	log := zerowrap.FromCtx(ctx)

	// For internal deploys (image push events), always pull fresh image
	// because the same tag may reference new content (e.g., latest tag updated)
	if domain.IsInternalDeploy(ctx) {
		log.Info().Msg("internal deploy detected, forcing image pull to ensure latest content")
		return false, nil
	}

	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()

	pullPolicy := normalizePullPolicy(cfg.PullPolicy)
	switch pullPolicy {
	case PullPolicyAlways:
		log.Info().Str("pull_policy", pullPolicy).Msg("pull policy forces image pull")
		return false, nil
	case PullPolicyIfTagChanged:
		if !isDigestRef(imageRef) {
			log.Info().Str("pull_policy", pullPolicy).Msg("tag reference detected, pulling to check for updates")
			return false, nil
		}
	}

	localImages, err := s.runtime.ListImages(ctx)
	if err != nil {
		log.WrapErr(err, "failed to list local images, will attempt pull")
		return false, nil
	}

	normalizedRef := normalizeImageRef(imageRef)
	normalizedPullRef := normalizeImageRef(pullRef)
	for _, img := range localImages {
		normalizedImage := normalizeImageRef(img)
		if normalizedImage == normalizedRef {
			log.Info().Msg("image found locally, skipping pull")
			return true, nil
		}
		if normalizedImage == normalizedPullRef {
			if err := s.tagImageIfNeeded(ctx, pullRef, imageRef); err != nil {
				return false, err
			}
			// Clean up the temporary pull reference tag
			if pullRef != imageRef {
				if err := s.runtime.UntagImage(ctx, pullRef); err != nil {
					log.Debug().Err(err).Str("pull_ref", pullRef).Msg("failed to remove temporary pull tag")
				}
			}
			log.Info().Msg("image found locally, skipping pull")
			return true, nil
		}
	}

	return false, nil
}

func (s *Service) pullImage(ctx context.Context, pullRef string, isInternal bool) error {
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()

	log := zerowrap.FromCtx(ctx)

	switch {
	case isInternal && cfg.RegistryAuthEnabled:
		if cfg.InternalRegistryUsername == "" || cfg.InternalRegistryPassword == "" {
			return log.WrapErr(fmt.Errorf("internal registry auth not configured"), "failed to pull image for internal deploy")
		}
		if err := s.pullImageWithRetry(ctx, func(ctx context.Context) error {
			return s.runtime.PullImageWithAuth(ctx, pullRef, cfg.InternalRegistryUsername, cfg.InternalRegistryPassword)
		}, isInternal); err != nil {
			return log.WrapErr(err, "failed to pull image with internal auth")
		}
	case isInternal:
		if err := s.pullImageWithRetry(ctx, func(ctx context.Context) error {
			return s.runtime.PullImage(ctx, pullRef)
		}, isInternal); err != nil {
			return log.WrapErr(err, "failed to pull image")
		}
	case cfg.RegistryAuthEnabled:
		if cfg.ServiceTokenUsername == "" || cfg.ServiceToken == "" {
			return log.WrapErr(fmt.Errorf("registry service token not configured"), "failed to pull image for registry auth")
		}
		if err := s.runtime.PullImageWithAuth(ctx, pullRef, cfg.ServiceTokenUsername, cfg.ServiceToken); err != nil {
			return log.WrapErr(err, "failed to pull image with auth")
		}
	default:
		if err := s.runtime.PullImage(ctx, pullRef); err != nil {
			return log.WrapErr(err, "failed to pull image")
		}
	}

	return nil
}

func (s *Service) pullImageWithRetry(ctx context.Context, pullFn func(context.Context) error, isInternal bool) error {
	log := zerowrap.FromCtx(ctx)

	for attempt := 1; attempt <= internalPullMaxAttempts; attempt++ {
		err := pullFn(ctx)
		if err == nil {
			return nil
		}
		if !isInternal || !isConnectionRefusedError(err) || attempt == internalPullMaxAttempts {
			return fmt.Errorf("internal image pull failed after %d attempts: %w", attempt, err)
		}

		backoff := time.Duration(attempt) * time.Second
		log.Warn().
			Err(err).
			Int("attempt", attempt).
			Dur("backoff", backoff).
			Msg("internal image pull failed with connection refused, retrying")

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *Service) tagImageIfNeeded(ctx context.Context, sourceRef, targetRef string) error {
	if sourceRef == targetRef {
		return nil
	}

	// Cannot create a tag with a digest reference - Docker/Podman doesn't allow it.
	// When using digest references (image@sha256:...), skip tagging as the image
	// is already available by its digest.
	if strings.Contains(targetRef, "@sha256:") {
		return nil
	}

	log := zerowrap.FromCtx(ctx)
	if err := s.runtime.TagImage(ctx, sourceRef, targetRef); err != nil {
		return log.WrapErr(err, "failed to tag image from pull reference")
	}
	return nil
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
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()

	log := zerowrap.FromCtx(ctx)
	volumes := make(map[string]string)

	if !cfg.VolumeAutoCreate {
		return volumes, nil
	}

	volumePaths, err := s.runtime.InspectImageVolumes(ctx, imageRef)
	if err != nil {
		log.WrapErr(err, "failed to inspect image volumes")
		return volumes, nil
	}

	for _, path := range volumePaths {
		name := generateVolumeName(cfg.VolumePrefix, domainName, path)

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
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()

	if !cfg.NetworkIsolation {
		return "bridge"
	}

	for groupName, domains := range cfg.NetworkGroups {
		if slices.Contains(domains, domainName) {
			return s.generateNetworkName(groupName)
		}
	}

	return s.generateNetworkName(domainName)
}

func (s *Service) generateNetworkName(identifier string) string {
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()

	return fmt.Sprintf("%s-%s", cfg.NetworkPrefix, strings.ReplaceAll(identifier, ".", "-"))
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

func (s *Service) cleanupOrphanedContainers(ctx context.Context, domainName string, skipContainerID string) error {
	log := zerowrap.FromCtx(ctx)
	expectedName := fmt.Sprintf("gordon-%s", domainName)
	expectedNewName := expectedName + "-new"
	expectedNextName := expectedName + "-next"

	allContainers, err := s.runtime.ListContainers(ctx, true)
	if err != nil {
		return err
	}

	for _, c := range allContainers {
		if (c.Name == expectedName || c.Name == expectedNewName || c.Name == expectedNextName) && c.ID != skipContainerID {
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
	// Collect attachments from both domain-specific and network group configs
	attachments := s.resolveAttachmentsForDomain(domainName)
	if len(attachments) == 0 {
		return nil
	}

	log := zerowrap.FromCtx(ctx)
	var deployed []string // track successfully deployed attachment IDs for rollback
	for _, svc := range attachments {
		if err := s.deployAttachedService(ctx, domainName, svc, networkName); err != nil {
			log.WrapErrWithFields(err, "failed to deploy attachment", map[string]any{zerowrap.FieldService: svc, "domain": domainName})

			// Rollback: clean up already-deployed attachments
			for _, id := range deployed {
				if stopErr := s.runtime.StopContainer(ctx, id); stopErr != nil {
					log.Warn().Err(stopErr).Str("attachment_id", id).Msg("rollback: failed to stop attachment")
				}
				if rmErr := s.runtime.RemoveContainer(ctx, id, true); rmErr != nil {
					log.Warn().Err(rmErr).Str("attachment_id", id).Msg("rollback: failed to remove attachment")
				}
			}
			// Deregister rolled-back attachments
			if len(deployed) > 0 {
				s.mu.Lock()
				remaining := s.attachments[domainName]
				filtered := remaining[:0]
				rollbackSet := make(map[string]bool, len(deployed))
				for _, id := range deployed {
					rollbackSet[id] = true
				}
				for _, id := range remaining {
					if !rollbackSet[id] {
						filtered = append(filtered, id)
					}
				}
				s.attachments[domainName] = filtered
				s.mu.Unlock()
			}

			return fmt.Errorf("failed to deploy attachment %q (rolled back %d already-deployed)", svc, len(deployed))
		}
		// Collect IDs of attachments tracked under this domain after successful deploy
		s.mu.RLock()
		ids := s.attachments[domainName]
		if len(ids) > 0 {
			latest := ids[len(ids)-1]
			deployed = append(deployed, latest)
		}
		s.mu.RUnlock()
	}
	return nil
}

// resolveAttachmentsForDomain returns attachments for a domain by checking:
// 1. Direct domain attachments (attachments[domain])
// 2. Network group attachments (attachments[group] where domain is in network_groups[group])
func (s *Service) resolveAttachmentsForDomain(domainName string) []string {
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()

	seen := make(map[string]bool)
	var result []string

	// First, add domain-specific attachments
	if domainAttachments, ok := cfg.Attachments[domainName]; ok {
		for _, img := range domainAttachments {
			if !seen[img] {
				seen[img] = true
				result = append(result, img)
			}
		}
	}

	// Then, find which network group this domain belongs to and add group attachments
	for groupName, domains := range cfg.NetworkGroups {
		if slices.Contains(domains, domainName) {
			if groupAttachments, ok := cfg.Attachments[groupName]; ok {
				for _, img := range groupAttachments {
					if !seen[img] {
						seen[img] = true
						result = append(result, img)
					}
				}
			}
			break // Domain can only be in one network group
		}
	}

	return result
}

func (s *Service) getAttachmentsForDomain(ctx context.Context, domainName string) []domain.Attachment {
	containers, err := s.runtime.ListContainers(ctx, true)
	if err != nil {
		return nil
	}

	return s.filterAttachments(ctx, containers, domainName)
}

// getAllAttachments fetches all containers once and returns attachments grouped by domain.
// This avoids N+1 queries when listing multiple routes.
func (s *Service) getAllAttachments(ctx context.Context) map[string][]domain.Attachment {
	containers, err := s.runtime.ListContainers(ctx, true)
	if err != nil {
		return nil
	}

	result := make(map[string][]domain.Attachment)
	for _, container := range containers {
		if container.Labels == nil {
			continue
		}
		if container.Labels[domain.LabelAttachment] != "true" {
			continue
		}
		ownerDomain := container.Labels[domain.LabelAttachedTo]
		if ownerDomain == "" {
			continue
		}
		image := container.Image
		serviceName := container.Name
		if labelImage, ok := container.Labels[domain.LabelImage]; ok && labelImage != "" {
			image = labelImage
			serviceName = extractServiceName(labelImage)
		}
		// Get the container's network for display in the routes table
		network := ""
		if networkName, err := s.runtime.GetContainerNetwork(ctx, container.ID); err == nil {
			network = networkName
		}
		attachment := domain.Attachment{
			Name:        serviceName,
			Image:       s.stripRegistryPrefix(image),
			ContainerID: container.ID,
			Status:      container.Status,
			Network:     network,
		}
		result[ownerDomain] = append(result[ownerDomain], attachment)
	}

	return result
}

// filterAttachments extracts attachments for a specific domain from a container list.
func (s *Service) filterAttachments(ctx context.Context, containers []*domain.Container, domainName string) []domain.Attachment {
	attachments := make([]domain.Attachment, 0)
	for _, container := range containers {
		if container.Labels == nil {
			continue
		}
		if container.Labels[domain.LabelAttachment] != "true" {
			continue
		}
		if container.Labels[domain.LabelAttachedTo] != domainName {
			continue
		}
		image := container.Image
		serviceName := container.Name
		if labelImage, ok := container.Labels[domain.LabelImage]; ok && labelImage != "" {
			image = labelImage
			serviceName = extractServiceName(labelImage)
		}
		// Get the container's network for display in the routes table
		network := ""
		if networkName, err := s.runtime.GetContainerNetwork(ctx, container.ID); err == nil {
			network = networkName
		}
		attachment := domain.Attachment{
			Name:        serviceName,
			Image:       s.stripRegistryPrefix(image),
			ContainerID: container.ID,
			Status:      container.Status,
			Network:     network,
		}
		attachments = append(attachments, attachment)
	}

	return attachments
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
	// Try new name first, then fall back to legacy name for backwards compatibility
	existingContainer := s.findContainerByName(ctx, containerName)
	if existingContainer == nil {
		// Try legacy naming scheme for backwards compatibility during upgrades
		containerNameLegacy := fmt.Sprintf("gordon-%s-%s", sanitizeNameLegacy(ownerDomain), serviceName)
		existingContainer = s.findContainerByName(ctx, containerNameLegacy)
		if existingContainer != nil {
			log.Info().Str("container_name", containerNameLegacy).Msg("found attachment with legacy naming, will be replaced")
			// Remove the old container and recreate with new name
			if err := s.runtime.StopContainer(ctx, existingContainer.ID); err != nil {
				return log.WrapErr(err, "failed to stop legacy attachment container")
			}
			if err := s.runtime.RemoveContainer(ctx, existingContainer.ID, true); err != nil {
				return log.WrapErr(err, "failed to remove legacy attachment container")
			}
			existingContainer = nil // Clear so we create new one below
		}
	}

	if existingContainer != nil && existingContainer.Status == string(domain.ContainerStatusRunning) {
		log.Debug().Str("container_name", containerName).Msg("attachment already running, skipping")
		return nil
	}

	// Remove existing stopped container if present
	if existingContainer != nil {
		log.Info().Str("container_name", containerName).Msg("removing stopped attachment container")
		if err := s.runtime.RemoveContainer(ctx, existingContainer.ID, true); err != nil {
			return log.WrapErr(err, "failed to remove existing attachment container")
		}
	}

	log.Info().Str(zerowrap.FieldService, serviceImage).Msg("deploying attached service")

	// Ensure image (canonical ref is returned; internal pulls may use the local registry).
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
	// Extract repository from image reference, handling port numbers correctly.
	// Examples:
	//   nginx:latest -> docker.io/library/nginx
	//   user/repo:tag -> docker.io/user/repo
	//   localhost:5000/image:tag -> localhost:5000/image
	//   registry.example.com/image:tag -> registry.example.com/image
	//
	// Note: This function assumes valid Docker image references. Edge cases like
	// "registry.com:5000" (registry with port but no image name) are not valid
	// image references per Docker's naming conventions, which require at least
	// one path component after the registry (e.g., "registry.com:5000/image").

	// Find the tag separator: the last colon that isn't part of a port number.
	// A colon is part of a port if there's no slash after it until the next colon.
	repo := image
	lastColon := strings.LastIndex(image, ":")
	if lastColon != -1 {
		afterColon := image[lastColon+1:]
		// If there's no slash after the colon, it's the tag separator
		if !strings.Contains(afterColon, "/") {
			repo = image[:lastColon]
		}
	}

	if !strings.Contains(repo, "/") {
		return "docker.io/library/" + repo
	}

	if strings.Count(repo, "/") == 1 && !strings.Contains(strings.Split(repo, "/")[0], ".") && !strings.Contains(strings.Split(repo, "/")[0], ":") {
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

// sanitizeName is a convenience wrapper around domain.SanitizeDomainForContainer.
func sanitizeName(d string) string {
	return domain.SanitizeDomainForContainer(d)
}

// sanitizeNameLegacy is a convenience wrapper around domain.SanitizeDomainForContainerLegacy.
func sanitizeNameLegacy(d string) string {
	return domain.SanitizeDomainForContainerLegacy(d)
}

func isContainerNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, domain.ErrContainerNotFound) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such container") ||
		strings.Contains(msg, "no container with name or id") ||
		strings.Contains(msg, "container not found")
}

func isConnectionRefusedError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "connection refused")
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

	if err := s.pollContainerRunning(ctx, containerID); err != nil {
		return err
	}

	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()

	delay := cfg.ReadinessDelay
	if delay == 0 {
		delay = 5 * time.Second
	}

	log.Debug().Dur("delay", delay).Msg("waiting for container readiness")
	select {
	case <-time.After(delay):
	case <-ctx.Done():
		return ctx.Err()
	}

	// Verify still running after delay
	running, err := s.runtime.IsContainerRunning(ctx, containerID)
	if err != nil {
		return err
	}
	if running {
		return nil
	}

	return s.waitForRecovery(ctx, containerID)
}

// pollContainerRunning polls until the container is running (max 30 seconds).
func (s *Service) pollContainerRunning(ctx context.Context, containerID string) error {
	for i := 0; i < 30; i++ {
		running, err := s.runtime.IsContainerRunning(ctx, containerID)
		if err != nil {
			return err
		}
		if running {
			return nil
		}
		if i == 29 {
			return fmt.Errorf("container did not start within 30 seconds")
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("container did not start within 30 seconds")
}

// waitForRecovery waits for a container to recover within the recovery window.
func (s *Service) waitForRecovery(ctx context.Context, containerID string) error {
	log := zerowrap.FromCtx(ctx)
	log.Warn().
		Dur("recovery_window", readinessRecoveryWindow).
		Msg("container not running after readiness delay, waiting for recovery")

	deadline := time.Now().Add(readinessRecoveryWindow)
	for time.Now().Before(deadline) {
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}

		running, err := s.runtime.IsContainerRunning(ctx, containerID)
		if err != nil {
			return err
		}
		if running {
			log.Info().Msg("container recovered during readiness recovery window")
			return nil
		}
	}

	return fmt.Errorf("container not running after readiness delay and recovery window")
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

// rewriteToRegistryDomain rewrites an image reference to use the configured registry domain.
// e.g., "myapp:latest" -> "registry.example.com/myapp:latest"
func rewriteToRegistryDomain(imageRef, registryDomain string) string {
	if registryDomain == "" {
		return imageRef
	}

	prefix := registryDomain + "/"
	if strings.HasPrefix(imageRef, prefix) {
		return imageRef
	}

	return prefix + imageRef
}

// rewriteToLocalRegistry rewrites an image reference to use the local registry address.
// e.g., "registry.example.com/myapp:latest" -> "localhost:5000/myapp:latest"
func rewriteToLocalRegistry(imageRef, registryDomain string, registryPort int) string {
	if imageRef == "" {
		return imageRef
	}

	localRegistry := fmt.Sprintf("localhost:%d", registryPort)
	localPrefix := localRegistry + "/"
	imageRef = strings.TrimPrefix(imageRef, localPrefix)

	registryDomain = strings.TrimSuffix(registryDomain, "/")
	if registryDomain != "" {
		prefix := registryDomain + "/"
		imageRef = strings.TrimPrefix(imageRef, prefix)
	}

	return localPrefix + imageRef
}
