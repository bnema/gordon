// Package container implements the container management use case.
package container

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/bnema/zerowrap"

	"gordon/internal/boundaries/out"
	"gordon/internal/domain"
)

// Config holds configuration needed by the container service.
type Config struct {
	RegistryAuthEnabled bool
	RegistryDomain      string
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
}

// Service implements the ContainerService interface.
type Service struct {
	runtime    out.ContainerRuntime
	envLoader  out.EnvLoader
	eventBus   out.EventPublisher
	logWriter  out.ContainerLogWriter
	config     Config
	containers map[string]*domain.Container
	mu         sync.RWMutex
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
		runtime:    runtime,
		envLoader:  envLoader,
		eventBus:   eventBus,
		logWriter:  logWriter,
		config:     config,
		containers: make(map[string]*domain.Container),
	}
}

// Deploy creates and starts a container for the given route.
func (s *Service) Deploy(ctx context.Context, route domain.Route) (*domain.Container, error) {
	// Enrich context with use case fields for all downstream logs
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "Deploy",
		"domain":              route.Domain,
	})
	log := zerowrap.FromCtx(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if container already exists for this domain
	if existing, exists := s.containers[route.Domain]; exists {
		log.Info().Str(zerowrap.FieldEntityID, existing.ID).Msg("container already exists, restarting")

		if err := s.runtime.StopContainer(ctx, existing.ID); err != nil {
			log.WrapErrWithFields(err, "failed to stop existing container", map[string]any{zerowrap.FieldEntityID: existing.ID})
		}

		if err := s.runtime.RemoveContainer(ctx, existing.ID, true); err != nil {
			log.WrapErrWithFields(err, "failed to remove existing container", map[string]any{zerowrap.FieldEntityID: existing.ID})
		}

		delete(s.containers, route.Domain)
	}

	// Clean up orphaned containers
	if err := s.cleanupOrphanedContainers(ctx, route.Domain); err != nil {
		log.WrapErr(err, "failed to cleanup orphaned containers")
	}

	// Build image reference and ensure it's available
	imageRef := s.buildImageRef(route.Image)
	if err := s.ensureImage(ctx, imageRef); err != nil {
		return nil, err
	}

	// Get exposed ports
	exposedPorts, err := s.runtime.GetImageExposedPorts(ctx, imageRef)
	if err != nil {
		log.WrapErr(err, "failed to get exposed ports, using defaults")
		exposedPorts = []int{80, 8080, 3000}
	}

	// Load environment
	envVars, err := s.loadEnvironment(ctx, route.Domain, imageRef)
	if err != nil {
		return nil, err
	}

	// Setup volumes
	volumes, err := s.setupVolumes(ctx, route.Domain, imageRef)
	if err != nil {
		return nil, err
	}

	// Setup network
	networkName := s.getNetworkForApp(route.Domain)
	if err := s.createNetworkIfNeeded(ctx, networkName); err != nil {
		return nil, log.WrapErr(err, "failed to create network")
	}

	// Create container
	containerConfig := &domain.ContainerConfig{
		Image:       imageRef,
		Name:        fmt.Sprintf("gordon-%s", route.Domain),
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

	container, err := s.runtime.CreateContainer(ctx, containerConfig)
	if err != nil {
		return nil, log.WrapErr(err, "failed to create container")
	}

	// Start container
	if err := s.runtime.StartContainer(ctx, container.ID); err != nil {
		s.runtime.RemoveContainer(ctx, container.ID, true)
		return nil, log.WrapErr(err, "failed to start container")
	}

	// Re-inspect for updated info
	container, err = s.runtime.InspectContainer(ctx, container.ID)
	if err != nil {
		return nil, log.WrapErr(err, "failed to inspect started container")
	}

	// Start container log collection (non-blocking, errors don't fail deployment)
	s.startLogCollection(ctx, container.ID, route.Domain)

	s.containers[route.Domain] = container

	log.Info().
		Str("image", route.Image).
		Str(zerowrap.FieldEntityID, container.ID).
		Ints("ports", container.Ports).
		Str("network", networkName).
		Msg("container deployed successfully")

	// Deploy attachments
	if err := s.deployAttachments(ctx, route.Domain); err != nil {
		log.WrapErr(err, "failed to deploy some attachments")
	}

	return container, nil
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

func (s *Service) ensureImage(ctx context.Context, imageRef string) error {
	ctx = zerowrap.CtxWithField(ctx, "image", imageRef)
	log := zerowrap.FromCtx(ctx)

	// Check local images
	localImages, err := s.runtime.ListImages(ctx)
	if err != nil {
		log.WrapErr(err, "failed to list local images, will attempt pull")
	} else {
		normalizedRef := normalizeImageRef(imageRef)
		for _, img := range localImages {
			if normalizeImageRef(img) == normalizedRef {
				log.Info().Msg("image found locally, skipping pull")
				return nil
			}
		}
	}

	// Pull image
	log.Info().Msg("pulling image from registry")

	if s.config.RegistryAuthEnabled {
		if err := s.runtime.PullImageWithAuth(ctx, imageRef, s.config.RegistryUsername, s.config.RegistryPassword); err != nil {
			return log.WrapErr(err, "failed to pull image with auth")
		}
	} else {
		if err := s.runtime.PullImage(ctx, imageRef); err != nil {
			return log.WrapErr(err, "failed to pull image")
		}
	}

	log.Info().Msg("image pulled successfully")
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

func (s *Service) deployAttachments(ctx context.Context, domainName string) error {
	attachments, ok := s.config.Attachments[domainName]
	if !ok {
		return nil
	}

	log := zerowrap.FromCtx(ctx)
	for _, svc := range attachments {
		if err := s.deployAttachedService(ctx, domainName, svc); err != nil {
			log.WrapErrWithFields(err, "failed to deploy attachment", map[string]any{zerowrap.FieldService: svc, "domain": domainName})
		}
	}

	return nil
}

func (s *Service) deployAttachedService(ctx context.Context, identifier, serviceImage string) error {
	log := zerowrap.FromCtx(ctx)
	log.Info().Str("identifier", identifier).Str(zerowrap.FieldService, serviceImage).Msg("deploying attached service")
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
