// Package container implements the container management use case.
package container

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnema/zerowrap"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/bnema/gordon/internal/adapters/out/telemetry"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// Config holds configuration needed by the container service.
type Config struct {
	RegistryAuthEnabled        bool
	RegistryDomain             string
	RegistryPort               int
	ServiceTokenUsername       string
	ServiceToken               string
	InternalRegistryUsername   string
	InternalRegistryPassword   string
	PullPolicy                 string
	VolumeAutoCreate           bool
	VolumePrefix               string
	VolumePreserve             bool
	NetworkIsolation           bool
	NetworkPrefix              string
	NetworkGroups              map[string][]string
	Attachments                map[string][]string
	ReadinessDelay             time.Duration // Delay after container starts before considering it ready
	ReadinessMode              string        // Readiness strategy: auto, docker-health, delay
	HealthTimeout              time.Duration // Max wait for health-based readiness
	DrainDelay                 time.Duration // Grace period after cache invalidation before stopping old container
	DrainDelayConfigured       bool          // True when deploy.drain_delay was explicitly configured
	DrainMode                  string        // Drain strategy: auto, inflight, delay
	DrainTimeout               time.Duration // Max wait for in-flight requests to drain
	StabilizationDelay         time.Duration // Post-switch monitoring window (default 2s)
	TCPProbeTimeout            time.Duration // TCP probe timeout (default 30s)
	HTTPProbeTimeout           time.Duration // HTTP probe timeout (default 60s)
	AttachmentReadinessTimeout time.Duration // Max wait for attachment readiness (default 30s)
	DefaultMemoryLimit         int64         // Default memory limit in bytes for containers (0 = no limit)
	DefaultNanoCPUs            int64         // Default CPU quota in nanoseconds for containers (0 = no limit)
	DefaultPidsLimit           int64         // Default max PIDs for containers (0 = no limit)
}

var tracer = otel.Tracer("gordon.container")

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
	drainWaiter      out.ProxyDrainWaiter
	config           Config
	configProvider   AttachmentConfigProvider // live config reads for attachments/networks (may be nil)
	metrics          *telemetry.Metrics
	containers       map[string]*domain.Container
	attachments      map[string][]string // ownerDomain → []containerIDs
	managedCount     int64               // tracks UpDownCounter value for delta computation
	mu               sync.RWMutex
	deployMu         sync.Map       // per-domain deploy locks (domain → *domainDeployLock)
	cleanupWg        sync.WaitGroup // tracks background old-container cleanup goroutines
	monitor          *Monitor
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
	configProvider AttachmentConfigProvider,
) *Service {
	return &Service{
		runtime:        runtime,
		envLoader:      envLoader,
		eventBus:       eventBus,
		logWriter:      logWriter,
		config:         config,
		configProvider: configProvider,
		containers:     make(map[string]*domain.Container),
		attachments:    make(map[string][]string),
	}
}

// SetMetrics sets the telemetry metrics for the container service.
func (s *Service) SetMetrics(m *telemetry.Metrics) {
	s.metrics = m
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

// SetProxyDrainWaiter sets the proxy in-flight drain waiter used during
// zero-downtime replacement before stopping the old container.
func (s *Service) SetProxyDrainWaiter(waiter out.ProxyDrainWaiter) {
	s.mu.Lock()
	s.drainWaiter = waiter
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

// containerConfigInput groups the parameters for building a container configuration.
type containerConfigInput struct {
	Domain       string
	Image        string
	ImageRef     string
	ExposedPorts []int
	EnvVars      []string
	EnvHash      string
	Volumes      map[string]string
	NetworkName  string
	ImageLabels  map[string]string
	Existing     *domain.Container
}

func (s *Service) buildContainerConfig(in containerConfigInput) *domain.ContainerConfig {
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()

	containerName := s.deploymentContainerName(in.Domain, in.Existing)
	labels := map[string]string{
		domain.LabelDomain:  in.Domain,
		domain.LabelEnvHash: in.EnvHash,
		domain.LabelImage:   in.Image,
		domain.LabelManaged: "true",
		domain.LabelRoute:   in.Domain,
	}

	// Propagate proxy/port labels from image so readiness probes can find them
	for _, key := range []string{domain.LabelProxyPort, domain.LabelPort, domain.LabelHealth} {
		if v, ok := in.ImageLabels[key]; ok && v != "" {
			labels[key] = v
		}
	}

	// Ensure the label-specified proxy port is included in exposed ports
	// so Docker creates a host port binding for it.
	ports := in.ExposedPorts
	if portStr, ok := labels[domain.LabelProxyPort]; ok {
		if port, err := strconv.Atoi(portStr); err == nil && port > 0 && port <= 65535 {
			if !slices.Contains(ports, port) {
				ports = append(ports, port)
			}
		}
	}

	return &domain.ContainerConfig{
		Image:       in.ImageRef,
		Name:        containerName,
		Ports:       ports,
		Env:         in.EnvVars,
		Volumes:     in.Volumes,
		NetworkMode: in.NetworkName,
		Hostname:    in.Domain,
		Labels:      labels,
		AutoRemove:  false,
		MemoryLimit: cfg.DefaultMemoryLimit,
		NanoCPUs:    cfg.DefaultNanoCPUs,
		PidsLimit:   cfg.DefaultPidsLimit,
	}
}

// Deploy creates and starts a container for the given route.
// Implements zero-downtime deployment: new container starts before old one stops.
func (s *Service) Deploy(ctx context.Context, route domain.Route) (*domain.Container, error) {
	ctx, span := tracer.Start(ctx, "container.deploy",
		trace.WithAttributes(
			attribute.String("domain", route.Domain),
			attribute.String("image", route.Image),
		))
	defer span.End()

	deployStart := time.Now()

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

	// Record deploy metrics and trace status
	defer s.recordDeployMetrics(ctx, span, route, deployStart, &err)

	existing, hasExisting := s.resolveExistingContainer(ctx, route.Domain)

	resources, err := s.prepareDeployResources(ctx, route, existing)
	if err != nil {
		return nil, err
	}

	// Skip redundant deploy: if the existing container is already running
	// the exact same image (by Docker image ID), return it immediately.
	// This prevents the double-deploy caused by the event-based deploy
	// (triggered by image.pushed) racing with the explicit CLI deploy call.
	if hasExisting {
		existingForSkip := existing
		if existingForSkip.ImageID == "" && existingForSkip.Image != "" && normalizeImageRef(existingForSkip.Image) == normalizeImageRef(route.Image) {
			existingForSkip = s.containerForRedundantCheck(ctx, existingForSkip)
		}
		if existingForSkip.ImageID != "" {
			if skip, container := s.skipRedundantDeploy(ctx, existingForSkip, resources.actualImageRef, resources.envHash); skip {
				return container, nil
			}
		}
	}

	newContainer, err := s.createStartedContainer(ctx, route, existing, resources)
	if err != nil {
		return nil, err
	}

	invalidated := s.activateDeployedContainer(ctx, route.Domain, newContainer)

	// Post-switch stabilization: verify new container stays running
	if hasExisting {
		stable, stabilizeErr := s.stabilizeNewContainer(ctx, route.Domain, newContainer, existing)
		if stabilizeErr != nil {
			// Both old and new containers are dead; assign to named return so
			// the deferred recordDeployMetrics and span see the failure.
			err = stabilizeErr
			return nil, err
		}
		if !stable {
			// Rollback performed — old container is restored
			return existing, nil
		}
	}

	// Finalize old container in the background — the new container is already
	// serving traffic, so there's no reason to block the deploy response while
	// waiting for the old container to stop (which can take 20s if the app
	// doesn't handle SIGTERM).
	s.cleanupWg.Add(1)
	go func() {
		defer s.cleanupWg.Done()
		s.finalizePreviousContainer(context.WithoutCancel(ctx), route.Domain, existing, hasExisting, invalidated, newContainer.ID)
	}()

	// Start container log collection (non-blocking, errors don't fail deployment)
	s.startLogCollection(ctx, newContainer.ID, route.Domain)

	log.Info().
		Str("image", route.Image).
		Str(zerowrap.FieldEntityID, newContainer.ID).
		Ints("ports", newContainer.Ports).
		Str("network", resources.networkName).
		Bool("zero_downtime", hasExisting).
		Msg("container deployed successfully")

	return newContainer, nil
}

// recordDeployMetrics records span error status and deploy metrics at the end of a Deploy call.
// It is called via defer and receives a pointer to the named return error so it can observe
// the final error value after all deferred functions have run.
func (s *Service) recordDeployMetrics(ctx context.Context, span trace.Span, route domain.Route, start time.Time, errPtr *error) {
	err := *errPtr
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	if s.metrics == nil {
		return
	}
	attrs := metric.WithAttributes(
		attribute.String("domain", route.Domain),
		attribute.String("image", route.Image),
	)
	s.metrics.DeployTotal.Add(ctx, 1, attrs)
	s.metrics.DeployDuration.Record(ctx, time.Since(start).Seconds(), attrs)
	if err != nil {
		s.metrics.DeployErrors.Add(ctx, 1, attrs)
	}
}

// skipRedundantDeploy checks whether the existing container is already running
// the same image (by Docker image ID) as the one we are about to deploy.
// When a push triggers both an event-based deploy and an explicit CLI deploy,
// the second one arrives after the first has already completed; this avoids
// a full redundant create-start-readiness cycle.
func (s *Service) skipRedundantDeploy(ctx context.Context, existing *domain.Container, actualImageRef, envHash string) (bool, *domain.Container) {
	log := zerowrap.FromCtx(ctx)

	newImageID, err := s.runtime.GetImageID(ctx, actualImageRef)
	if err != nil {
		log.Debug().Err(err).Msg("cannot resolve image ID for redundancy check, proceeding with deploy")
		return false, nil
	}

	if existing.ImageID != newImageID {
		return false, nil
	}

	// Verify the container is actually still running before skipping.
	running, err := s.runtime.IsContainerRunning(ctx, existing.ID)
	if err != nil || !running {
		return false, nil
	}

	if existing.Labels != nil {
		existingEnvHash, hasEnvHash := existing.Labels[domain.LabelEnvHash]
		if !hasEnvHash || existingEnvHash != envHash {
			log.Info().
				Str("container_id", existing.ID).
				Bool("has_env_hash", hasEnvHash).
				Msg("env changed, proceeding with deploy despite same image")
			return false, nil
		}
	} else {
		return false, nil
	}

	log.Info().
		Str("container_id", existing.ID).
		Str("image_id", newImageID).
		Msg("skipping redundant deploy: container already running this image")

	return true, existing
}

func (s *Service) containerForRedundantCheck(ctx context.Context, existing *domain.Container) *domain.Container {
	if existing == nil || existing.ImageID != "" || existing.ID == "" {
		return existing
	}

	log := zerowrap.FromCtx(ctx)
	inspected, err := s.runtime.InspectContainer(ctx, existing.ID)
	if err != nil {
		log.Debug().Err(err).Str("container_id", existing.ID).Msg("cannot inspect existing container for redundancy check")
		return existing
	}
	if inspected == nil || inspected.ImageID == "" {
		return existing
	}

	existingCopy := *existing
	existingCopy.ImageID = inspected.ImageID
	return &existingCopy
}

type deployResources struct {
	networkName    string
	actualImageRef string
	exposedPorts   []int
	imageLabels    map[string]string
	envVars        []string
	envHash        string
	volumes        map[string]string
}

// resolveExistingContainer returns the currently running container for a domain.
// It first checks the in-memory map (fast path). If not found, it queries the
// runtime for a running container with matching name and managed label. This
// handles cases where Gordon restarted and in-memory state is stale.
//
// The slow path checks canonical, -new, and -next names because a previous
// deploy may have been interrupted (e.g. eventbus timeout, Gordon restart)
// leaving the active container under a temp name. Canonical is preferred when
// multiple matches exist.
func (s *Service) resolveExistingContainer(ctx context.Context, domainName string) (*domain.Container, bool) {
	// Fast path: check in-memory state
	s.mu.RLock()
	container, ok := s.containers[domainName]
	s.mu.RUnlock()
	if ok {
		return container, true
	}

	// Slow path: query runtime for running containers
	log := zerowrap.FromCtx(ctx)
	canonicalName := fmt.Sprintf("gordon-%s", domainName)
	candidateNames := map[string]bool{
		canonicalName:           true,
		canonicalName + "-new":  true,
		canonicalName + "-next": true,
	}

	running, err := s.runtime.ListContainers(ctx, false)
	if err != nil {
		log.Warn().Err(err).Msg("failed to list running containers for existing container resolution")
		return nil, false
	}

	// Prefer canonical name; fall back to any running managed temp container.
	var best *domain.Container
	for _, c := range running {
		if !candidateNames[c.Name] || c.Labels[domain.LabelManaged] != "true" {
			continue
		}
		if best == nil || c.Name == canonicalName {
			best = c
		}
	}

	if best != nil {
		log.Info().
			Str("container_id", best.ID).
			Str("container_name", best.Name).
			Msg("resolved existing container from runtime (in-memory state was stale)")

		// Update in-memory state so subsequent lookups are fast
		s.mu.Lock()
		if _, alreadyTracked := s.containers[domainName]; !alreadyTracked {
			s.managedCount++
		}
		s.containers[domainName] = best
		s.mu.Unlock()

		return best, true
	}

	return nil, false
}

func (s *Service) prepareDeployResources(ctx context.Context, route domain.Route, existing *domain.Container) (*deployResources, error) {
	log := zerowrap.FromCtx(ctx)

	existingID := ""
	if existing != nil {
		existingID = existing.ID
	}
	if err := s.cleanupOrphanedContainers(ctx, route.Domain, existingID); err != nil {
		log.WrapErr(err, "failed to cleanup orphaned containers")
	}

	imageRef := s.buildImageRef(route.Image)
	actualImageRef, err := s.ensureImage(ctx, imageRef)
	if err != nil {
		return nil, err
	}

	networkName := s.getNetworkForApp(route.Domain)
	if err := s.createNetworkIfNeeded(ctx, networkName); err != nil {
		return nil, log.WrapErr(err, "failed to create network")
	}
	if err := s.deployAttachments(ctx, route.Domain, networkName); err != nil {
		return nil, log.WrapErr(err, "failed to deploy attachments")
	}

	exposedPorts, err := s.runtime.GetImageExposedPorts(ctx, actualImageRef)
	if err != nil {
		log.WrapErr(err, "failed to get exposed ports, using defaults")
		exposedPorts = []int{80, 8080, 3000}
	}

	imageLabels, err := s.runtime.GetImageLabels(ctx, actualImageRef)
	if err != nil {
		log.Warn().Err(err).Msg("failed to get image labels, skipping label propagation")
		imageLabels = nil
	}

	envVars, err := s.loadEnvironment(ctx, route.Env, route.Domain, actualImageRef)
	if err != nil {
		return nil, err
	}
	envHash := hashEnvironment(envVars)

	volumes, err := s.setupVolumes(ctx, route.Domain, actualImageRef)
	if err != nil {
		return nil, err
	}

	return &deployResources{
		networkName:    networkName,
		actualImageRef: actualImageRef,
		exposedPorts:   exposedPorts,
		imageLabels:    imageLabels,
		envVars:        envVars,
		envHash:        envHash,
		volumes:        volumes,
	}, nil
}

func (s *Service) createStartedContainer(ctx context.Context, route domain.Route, existing *domain.Container, resources *deployResources) (*domain.Container, error) {
	ctx, span := tracer.Start(ctx, "container.create_and_start")
	defer span.End()

	log := zerowrap.FromCtx(ctx)

	containerConfig := s.buildContainerConfig(containerConfigInput{
		Domain:       route.Domain,
		Image:        route.Image,
		ImageRef:     resources.actualImageRef,
		ExposedPorts: resources.exposedPorts,
		EnvVars:      resources.envVars,
		EnvHash:      resources.envHash,
		Volumes:      resources.volumes,
		NetworkName:  resources.networkName,
		ImageLabels:  resources.imageLabels,
		Existing:     existing,
	})

	newContainer, err := s.runtime.CreateContainer(ctx, containerConfig)
	if err != nil {
		return nil, log.WrapErr(err, "failed to create container")
	}
	if err := s.runtime.StartContainer(ctx, newContainer.ID); err != nil {
		s.runtime.RemoveContainer(ctx, newContainer.ID, true)
		return nil, log.WrapErr(err, "failed to start container")
	}
	if !domain.IsSkipReadiness(ctx) {
		if err := s.waitForReady(ctx, newContainer.ID, containerConfig); err != nil {
			s.cleanupFailedContainer(ctx, newContainer.ID)
			return nil, log.WrapErr(err, "container failed readiness check")
		}
	}

	inspected, err := s.runtime.InspectContainer(ctx, newContainer.ID)
	if err != nil {
		s.cleanupFailedContainer(ctx, newContainer.ID)
		return nil, log.WrapErr(err, "failed to inspect started container")
	}

	return inspected, nil
}

func (s *Service) activateDeployedContainer(ctx context.Context, domainName string, container *domain.Container) bool {
	s.mu.Lock()
	_, wasTracked := s.containers[domainName]
	s.containers[domainName] = container
	if !wasTracked {
		s.managedCount++
	}
	s.mu.Unlock()

	// Track managed container count (only increment for new domains, not replacements)
	if !wasTracked && s.metrics != nil {
		s.metrics.ManagedContainers.Add(ctx, 1)
	}

	s.publishContainerDeployed(ctx, domainName, container.ID)

	s.mu.RLock()
	inv := s.cacheInvalidator
	s.mu.RUnlock()
	if inv != nil {
		inv.InvalidateTarget(ctx, domainName)
		return true
	}

	return false
}

// stabilizeNewContainer monitors the new container briefly after traffic switch.
// If it crashes during this window, rolls back to old container.
// Returns true if stabilization succeeded, false if rollback was performed.
// Returns an error only when both new and old containers are dead.
func (s *Service) stabilizeNewContainer(ctx context.Context, domainName string, newContainer, oldContainer *domain.Container) (bool, error) {
	log := zerowrap.FromCtx(ctx)

	if oldContainer == nil {
		return true, nil
	}

	s.mu.RLock()
	delay := s.config.StabilizationDelay
	s.mu.RUnlock()
	if delay == 0 {
		delay = 2 * time.Second
	}

	// Brief stabilization: verify new container is still running after delay
	select {
	case <-time.After(delay):
	case <-ctx.Done():
		return true, nil
	}

	running, err := s.runtime.IsContainerRunning(ctx, newContainer.ID)
	if err != nil || !running {
		log.Error().
			Str("new_container", newContainer.ID).
			Str("old_container", oldContainer.ID).
			Msg("new container crashed during stabilization, rolling back to old")

		// Verify old container is still running before restoring it.
		// If both old and new are dead (e.g., OOM), returning a dead
		// container as "existing" would leave the domain in a broken state.
		oldRunning, oldErr := s.runtime.IsContainerRunning(ctx, oldContainer.ID)
		if oldErr != nil || !oldRunning {
			log.Error().
				Str("old_container", oldContainer.ID).
				Msg("old container is also not running, cannot rollback")

			// Cleanup failed new container
			if stopErr := s.runtime.StopContainer(ctx, newContainer.ID); stopErr != nil {
				log.WrapErrWithFields(stopErr, "failed to stop failed new container during rollback", map[string]any{zerowrap.FieldEntityID: newContainer.ID})
			}
			if removeErr := s.runtime.RemoveContainer(ctx, newContainer.ID, true); removeErr != nil {
				log.WrapErrWithFields(removeErr, "failed to remove failed new container during rollback", map[string]any{zerowrap.FieldEntityID: newContainer.ID})
			}

			return false, fmt.Errorf("stabilization failed: new container crashed and old container %s is also not running", oldContainer.ID)
		}

		// Rollback: restore old container as tracked
		s.mu.Lock()
		s.containers[domainName] = oldContainer
		s.mu.Unlock()

		// Re-invalidate proxy cache to point back to old
		s.mu.RLock()
		inv := s.cacheInvalidator
		s.mu.RUnlock()
		if inv != nil {
			inv.InvalidateTarget(ctx, domainName)
		}

		// Cleanup failed new container
		if stopErr := s.runtime.StopContainer(ctx, newContainer.ID); stopErr != nil {
			log.WrapErrWithFields(stopErr, "failed to stop failed new container during rollback", map[string]any{zerowrap.FieldEntityID: newContainer.ID})
		}
		if removeErr := s.runtime.RemoveContainer(ctx, newContainer.ID, true); removeErr != nil {
			log.WrapErrWithFields(removeErr, "failed to remove failed new container during rollback", map[string]any{zerowrap.FieldEntityID: newContainer.ID})
		}

		return false, nil
	}

	return true, nil
}

func (s *Service) finalizePreviousContainer(ctx context.Context, domainName string, existing *domain.Container, hasExisting, invalidated bool, newContainerID string) {
	if !hasExisting {
		return
	}

	if invalidated {
		s.waitForDrain(ctx, existing.ID)
	}

	s.cleanupOldContainer(ctx, existing, newContainerID, domainName)
}

func (s *Service) waitForDrain(ctx context.Context, oldContainerID string) {
	log := zerowrap.FromCtx(ctx)

	s.mu.RLock()
	cfg := s.config
	waiter := s.drainWaiter
	s.mu.RUnlock()

	drainMode := cfg.DrainMode
	if drainMode == "" {
		drainMode = "delay"
	}

	shouldUseInFlight := (drainMode == "inflight" || drainMode == "auto") && waiter != nil
	if shouldUseInFlight {
		timeout := cfg.DrainTimeout
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		drained := waiter.WaitForNoInFlight(ctx, oldContainerID, timeout)
		if !drained {
			log.Warn().
				Str("old_container_id", oldContainerID).
				Dur("drain_timeout", timeout).
				Msg("drain wait timed out; old container may still have in-flight traffic")
		}
		return
	}

	drainDelay := 2 * time.Second
	if cfg.DrainDelayConfigured {
		drainDelay = cfg.DrainDelay
	}
	if drainDelay <= 0 {
		return
	}
	select {
	case <-time.After(drainDelay):
	case <-ctx.Done():
	}
}

// restartContainerWithRecovery restarts a container, reconciling stale state if needed.
// It returns the (possibly refreshed) container and updated attachment IDs.
func (s *Service) restartContainerWithRecovery(ctx context.Context, domainName string, container *domain.Container, attachmentIDs []string) (*domain.Container, []string, error) {
	log := zerowrap.FromCtx(ctx)

	if err := s.runtime.RestartContainer(ctx, container.ID); err != nil {
		if !isContainerNotFoundError(err) {
			return nil, nil, log.WrapErr(err, "failed to restart container")
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
			return nil, nil, domain.ErrContainerNotFound
		}
		if err := s.runtime.RestartContainer(ctx, refreshed.ID); err != nil {
			return nil, nil, log.WrapErr(err, "failed to restart container after state reconciliation")
		}
		return refreshed, attachmentIDs, nil
	}

	return container, attachmentIDs, nil
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

	// When attachments are requested, sync runtime state first to get accurate
	// attachment tracking, then check if configured attachments are deployed.
	// If fewer attachment containers are tracked than configured, fail early with
	// guidance rather than restarting the main container into a broken state
	// (e.g., missing database hostname).
	if withAttachments {
		if syncErr := s.SyncContainers(ctx); syncErr != nil {
			log.Warn().Err(syncErr).Msg("failed to sync container state before attachment check")
		}

		// Re-read attachment IDs after sync
		s.mu.RLock()
		if freshAttachments := s.attachments[domainName]; len(freshAttachments) > 0 {
			attachmentIDs = make([]string, len(freshAttachments))
			copy(attachmentIDs, freshAttachments)
		} else {
			attachmentIDs = nil
		}
		s.mu.RUnlock()

		configuredAttachments := s.resolveAttachmentsForDomain(domainName)
		if len(configuredAttachments) > 0 && len(attachmentIDs) < len(configuredAttachments) {
			missing := len(configuredAttachments) - len(attachmentIDs)
			return fmt.Errorf("%w: domain %q has %d configured attachment(s) but only %d deployed (%d missing)",
				domain.ErrAttachmentNotDeployed, domainName, len(configuredAttachments), len(attachmentIDs), missing)
		}
	}

	// Restart the main container (with stale-state recovery)
	container, attachmentIDs, err := s.restartContainerWithRecovery(ctx, domainName, container, attachmentIDs)
	if err != nil {
		return err
	}
	log.Info().Str(zerowrap.FieldEntityID, container.ID).Msg("container restarted")

	// Record restart metric
	if s.metrics != nil {
		s.metrics.ContainerRestarts.Add(ctx, 1, metric.WithAttributes(
			attribute.String("domain", domainName),
			attribute.String("source", "api"),
		))
	}

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
			s.managedCount--
			log.Info().Str("domain", d).Msg("container removed")
			break
		}
	}
	if removedDomain != "" {
		delete(s.attachments, removedDomain)
	}
	s.mu.Unlock()

	// Decrement managed container count
	if removedDomain != "" && s.metrics != nil {
		s.metrics.ManagedContainers.Add(ctx, -1)
	}

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
		if strings.HasPrefix(network.Name, cfg.NetworkPrefix+"-") && network.Labels[domain.LabelManaged] == "true" {
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
	attachments := make(map[string][]string)
	for _, c := range allContainers {
		if c.Labels == nil || c.Labels[domain.LabelManaged] != "true" {
			continue
		}
		if c.Labels[domain.LabelAttachment] == "true" {
			owner := c.Labels[domain.LabelAttachedTo]
			if owner != "" {
				attachments[owner] = append(attachments[owner], c.ID)
			}
			continue
		}
		if d, ok := c.Labels[domain.LabelDomain]; ok {
			managed[d] = c
		}
	}

	s.mu.Lock()
	s.containers = managed
	s.attachments = attachments
	newCount := int64(len(managed))
	delta := newCount - s.managedCount
	s.managedCount = newCount
	s.mu.Unlock()

	// Report initial/delta managed container count to OTel UpDownCounter.
	if delta != 0 && s.metrics != nil {
		s.metrics.ManagedContainers.Add(ctx, delta)
	}

	log.Info().Int(zerowrap.FieldCount, len(managed)).Msg("container state synchronized")
	return nil
}

// AutoStart starts containers for the provided routes that aren't running.
// It skips readiness checks to avoid blocking boot; the background monitor
// handles crash recovery.
func (s *Service) AutoStart(ctx context.Context, routes []domain.Route) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "AutoStart",
	})
	log := zerowrap.FromCtx(ctx)

	log.Info().Int("route_count", len(routes)).Msg("auto-starting containers for configured routes")

	// Filter out routes that already have a running container.
	var pending []domain.Route
	skipped := 0
	for _, route := range routes {
		if _, exists := s.Get(ctx, route.Domain); exists {
			log.Debug().Str("domain", route.Domain).Msg("container already running, skipping")
			skipped++
		} else {
			pending = append(pending, route)
		}
	}

	if len(pending) == 0 {
		log.Info().Int("skipped", skipped).Msg("auto-start completed, all containers already running")
		return nil
	}

	// Deploy all pending routes concurrently with readiness checks skipped.
	deployCtx := domain.WithSkipReadiness(ctx)

	type result struct {
		route domain.Route
		err   error
	}
	results := make(chan result, len(pending))
	sem := make(chan struct{}, 4) // limit concurrency

	for _, route := range pending {
		sem <- struct{}{}
		go func(r domain.Route) {
			defer func() { <-sem }()

			log.Info().Str("domain", r.Domain).Str("image", r.Image).Msg("auto-starting container for route")

			if _, err := s.Deploy(deployCtx, r); err != nil {
				results <- result{route: r, err: err}
				return
			}
			results <- result{route: r}
		}(route)
	}

	var started, errCount int
	for i := 0; i < len(pending); i++ {
		res := <-results
		if res.err != nil {
			log.Warn().Err(res.err).Str("domain", res.route.Domain).Msg("failed to auto-start container")
			errCount++
		} else {
			started++
		}
	}

	log.Info().
		Int("started", started).
		Int("skipped", skipped).
		Int("errors", errCount).
		Msg("auto-start completed")

	if errCount > 0 {
		return fmt.Errorf("auto-start completed with %d errors", errCount)
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

	// Wait for any in-flight background cleanup goroutines to finish
	s.cleanupWg.Wait()

	// Containers are left running across Gordon restarts.
	// SyncContainers + AutoStart will pick them back up on next boot.

	// Close log writer to stop all log collection
	if s.logWriter != nil {
		if err := s.logWriter.Close(); err != nil {
			log.Warn().Err(err).Msg("failed to close container log writer")
		}
	}

	log.Info().Msg("container manager shutdown complete")
	return nil
}

// WaitForCleanup blocks until all background cleanup goroutines complete.
// Intended for use in tests to avoid mock assertion races.
func (s *Service) WaitForCleanup() {
	s.cleanupWg.Wait()
}

// StartMonitor begins background monitoring of tracked containers.
// The monitor restarts crashed containers and detects crash loops.
// Safe to call multiple times; subsequent calls are no-ops.
func (s *Service) StartMonitor(ctx context.Context) {
	s.mu.Lock()
	if s.monitor != nil {
		s.mu.Unlock()
		return
	}
	s.monitor = newMonitor(s)
	m := s.monitor
	m.Start(ctx)
	s.mu.Unlock()
}

// StopMonitor stops the background container monitor.
// Safe to call multiple times; subsequent calls are no-ops.
func (s *Service) StopMonitor() {
	s.mu.Lock()
	m := s.monitor
	s.monitor = nil
	s.mu.Unlock()
	if m != nil {
		m.Stop()
	}
}

// UpdateConfig updates the service configuration.
func (s *Service) UpdateConfig(config Config) {
	s.mu.Lock()
	s.config = config
	s.mu.Unlock()
}

// UpdateAttachments updates only the attachment configuration in the service.
// This is called after a config reload to propagate attachment changes without restart.
// The incoming map is deep-copied so external callers cannot mutate service state.
func (s *Service) UpdateAttachments(attachments map[string][]string) {
	var copied map[string][]string
	if attachments != nil {
		copied = make(map[string][]string, len(attachments))
		for k, v := range attachments {
			sl := make([]string, len(v))
			copy(sl, v)
			copied[k] = sl
		}
	}
	s.mu.Lock()
	s.config.Attachments = copied
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

	// Use context.WithoutCancel so the log stream outlives the HTTP request.
	// Without this, the Docker log stream closes when the deploy response completes.
	bgCtx := context.WithoutCancel(ctx)

	logStream, err := s.runtime.GetContainerLogs(bgCtx, containerID, true)
	if err != nil {
		log.Warn().Err(err).Msg("failed to get container logs for collection")
		return
	}

	if err := s.logWriter.StartLogging(bgCtx, containerID, domainName, logStream); err != nil {
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
	ctx, span := tracer.Start(ctx, "container.ensure_image",
		trace.WithAttributes(attribute.String("image", imageRef)))
	defer span.End()

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

func (s *Service) loadEnvironment(ctx context.Context, preResolved []string, domainName, imageRef string) ([]string, error) {
	log := zerowrap.FromCtx(ctx)

	var userEnvVars []string
	if len(preResolved) > 0 {
		userEnvVars = preResolved
	} else {
		var err error
		userEnvVars, err = s.envLoader.LoadEnv(ctx, domainName)
		if err != nil {
			return nil, log.WrapErr(err, "failed to load environment variables")
		}
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
	var networkIsolation bool
	var networkGroups map[string][]string

	if s.configProvider != nil {
		snap := s.configProvider.GetAttachmentConfig()
		networkGroups = snap.NetworkGroups
		s.mu.RLock()
		networkIsolation = s.config.NetworkIsolation
		s.mu.RUnlock()
	} else {
		s.mu.RLock()
		networkIsolation = s.config.NetworkIsolation
		networkGroups = s.config.NetworkGroups
		s.mu.RUnlock()
	}

	if !networkIsolation {
		return "bridge"
	}

	for groupName, domains := range networkGroups {
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
			// Skip any running container regardless of name — a running
			// temp container may be actively serving traffic while the
			// new container stabilizes, or may be from a concurrent deploy.
			if c.Status == "running" {
				log.Debug().
					Str(zerowrap.FieldEntityID, c.ID).
					Str("container_name", c.Name).
					Str(zerowrap.FieldStatus, c.Status).
					Msg("skipping running container during orphan cleanup")
				continue
			}

			log.Info().Str(zerowrap.FieldEntityID, c.ID).Str("container_name", c.Name).Str(zerowrap.FieldStatus, c.Status).Msg("found orphaned container, removing")

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
	var attachments map[string][]string
	var networkGroups map[string][]string

	if s.configProvider != nil {
		snap := s.configProvider.GetAttachmentConfig()
		attachments = snap.Attachments
		networkGroups = snap.NetworkGroups
	} else {
		s.mu.RLock()
		attachments = s.config.Attachments
		networkGroups = s.config.NetworkGroups
		s.mu.RUnlock()
	}

	seen := make(map[string]bool)
	var result []string

	// First, add domain-specific attachments
	if domainAttachments, ok := attachments[domainName]; ok {
		for _, img := range domainAttachments {
			if !seen[img] {
				seen[img] = true
				result = append(result, img)
			}
		}
	}

	// Then, find which network group this domain belongs to and add group attachments
	for groupName, domains := range networkGroups {
		if slices.Contains(domains, domainName) {
			if groupAttachments, ok := attachments[groupName]; ok {
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
			Ports:       append([]int(nil), container.Ports...),
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
			Ports:       append([]int(nil), container.Ports...),
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
	existingContainer, err := s.resolveExistingAttachment(ctx, ownerDomain, serviceName)
	if err != nil {
		return err
	}

	if existingContainer != nil {
		shouldSkip, err := s.handleRunningAttachment(ctx, existingContainer, containerName, serviceImage)
		if err != nil {
			return err
		}
		if shouldSkip {
			return nil
		}
		if existingContainer.Status == string(domain.ContainerStatusRunning) {
			existingContainer = nil
		}
	}

	if err := s.removeStoppedAttachment(ctx, existingContainer, containerName); err != nil {
		return err
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
	envVars, err := s.loadEnvironment(ctx, nil, containerName, actualImageRef)
	if err != nil {
		log.WrapErr(err, "failed to load environment for attachment")
		envVars = []string{}
	}
	envHash := hashEnvironment(envVars)

	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()

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
			domain.LabelManaged:    "true",
			domain.LabelAttachment: "true",
			domain.LabelAttachedTo: ownerDomain,
			domain.LabelEnvHash:    envHash,
			domain.LabelImage:      serviceImage,
		},
		MemoryLimit: cfg.DefaultMemoryLimit,
		NanoCPUs:    cfg.DefaultNanoCPUs,
		PidsLimit:   cfg.DefaultPidsLimit,
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

func (s *Service) attachmentEnvDrifted(ctx context.Context, existing *domain.Container, containerName, serviceImage string) (bool, error) {
	currentEnv, err := s.loadEnvironment(ctx, nil, containerName, s.buildImageRef(serviceImage))
	if err != nil {
		return false, err
	}

	currentHash := hashEnvironment(currentEnv)
	existingHash, ok := existing.Labels[domain.LabelEnvHash]
	if ok && existingHash == currentHash {
		return false, nil
	}

	return true, nil
}

func (s *Service) handleRunningAttachment(ctx context.Context, existing *domain.Container, containerName, serviceImage string) (bool, error) {
	if existing.Status != string(domain.ContainerStatusRunning) {
		return false, nil
	}

	log := zerowrap.FromCtx(ctx)

	// Check image drift
	existingImage := existing.Labels[domain.LabelImage]
	imageDrifted := existingImage != "" && existingImage != serviceImage

	// Check env drift
	envDrifted, err := s.attachmentEnvDrifted(ctx, existing, containerName, serviceImage)
	if err != nil {
		return false, log.WrapErr(err, "failed to check attachment env drift")
	}

	if !envDrifted && !imageDrifted {
		log.Debug().Str("container_name", containerName).Msg("attachment already running with current env and image, skipping")
		return true, nil
	}

	if imageDrifted {
		log.Info().Str("container_name", containerName).Str("existing_image", existingImage).Str("desired_image", serviceImage).Msg("attachment image changed, recreating")
	}
	if envDrifted {
		log.Info().Str("container_name", containerName).Msg("attachment env changed, recreating")
	}

	if err := s.runtime.StopContainer(ctx, existing.ID); err != nil {
		return false, log.WrapErr(err, "failed to stop attachment for drift update")
	}
	if err := s.runtime.RemoveContainer(ctx, existing.ID, true); err != nil {
		return false, log.WrapErr(err, "failed to remove attachment for drift update")
	}

	return false, nil
}

func (s *Service) removeStoppedAttachment(ctx context.Context, existing *domain.Container, containerName string) error {
	if existing == nil {
		return nil
	}

	log := zerowrap.FromCtx(ctx)
	log.Info().Str("container_name", containerName).Msg("removing stopped attachment container")
	if err := s.runtime.RemoveContainer(ctx, existing.ID, true); err != nil {
		return log.WrapErr(err, "failed to remove existing attachment container")
	}

	return nil
}

func (s *Service) resolveExistingAttachment(ctx context.Context, ownerDomain, serviceName string) (*domain.Container, error) {
	log := zerowrap.FromCtx(ctx)
	containerName := fmt.Sprintf("gordon-%s-%s", sanitizeName(ownerDomain), serviceName)
	existingContainer := s.findContainerByName(ctx, containerName)
	if existingContainer != nil {
		return existingContainer, nil
	}

	containerNameLegacy := fmt.Sprintf("gordon-%s-%s", sanitizeNameLegacy(ownerDomain), serviceName)
	existingContainer = s.findContainerByName(ctx, containerNameLegacy)
	if existingContainer == nil {
		return nil, nil
	}

	log.Info().Str("container_name", containerNameLegacy).Msg("found attachment with legacy naming, will be replaced")
	if err := s.runtime.StopContainer(ctx, existingContainer.ID); err != nil {
		return nil, log.WrapErr(err, "failed to stop legacy attachment container")
	}
	if err := s.runtime.RemoveContainer(ctx, existingContainer.ID, true); err != nil {
		return nil, log.WrapErr(err, "failed to remove legacy attachment container")
	}

	return nil, nil
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

	keys := make([]string, 0, len(envMap))
	for k := range envMap {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	result := make([]string, 0, len(envMap))
	for _, k := range keys {
		result = append(result, k+"="+envMap[k])
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

func (s *Service) waitForReady(ctx context.Context, containerID string, containerConfig *domain.ContainerConfig) error {
	if err := s.pollContainerRunning(ctx, containerID); err != nil {
		return err
	}

	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()

	readinessMode := cfg.ReadinessMode
	if readinessMode == "" {
		readinessMode = "delay"
	}

	switch readinessMode {
	case "delay":
		return s.waitForReadyByDelay(ctx, containerID)
	case "docker-health":
		_, hasHealthcheck, err := s.runtime.GetContainerHealthStatus(ctx, containerID)
		if err != nil {
			return err
		}
		if !hasHealthcheck {
			return errors.New("no healthcheck detected")
		}
		return s.waitForHealthy(ctx, containerID, cfg.HealthTimeout)
	default: // auto
		return s.readinessCascade(ctx, containerID, containerConfig, cfg)
	}
}

// readinessCascade auto-detects the strongest available readiness signal:
//  1. Docker healthcheck (if present) → wait for healthy status
//  2. HTTP probe (if gordon.health label set) → GET until 2xx/3xx
//  3. Default HTTP probe (GET / on exposed port) → wait for 2xx/3xx
//  4. TCP probe (if port info available) → connect until accepted
//  5. Delay fallback (last resort) → waitForReadyByDelay
func (s *Service) readinessCascade(ctx context.Context, containerID string, containerConfig *domain.ContainerConfig, cfg Config) error {
	log := zerowrap.FromCtx(ctx)

	// 1. Docker healthcheck
	_, hasHealthcheck, err := s.runtime.GetContainerHealthStatus(ctx, containerID)
	if err != nil {
		return err
	}
	if hasHealthcheck {
		log.Info().Msg("readiness cascade: using Docker healthcheck")
		return s.waitForHealthy(ctx, containerID, cfg.HealthTimeout)
	}

	// 2. HTTP probe via gordon.health label
	if probed, probeErr := s.tryHTTPProbe(ctx, containerID, containerConfig, cfg); probed {
		return probeErr
	}

	// 3. Default HTTP probe on root path (if port info available)
	if probed, probeErr := s.tryDefaultHTTPProbe(ctx, containerID, containerConfig, cfg); probed {
		return probeErr
	}

	// 4. TCP probe (if port info available)
	if probed, probeErr := s.tryTCPProbe(ctx, containerID, containerConfig, cfg); probed {
		return probeErr
	}

	// 5. Delay fallback
	log.Info().Msg("readiness cascade: using delay fallback")
	return s.waitForReadyByDelay(ctx, containerID)
}

// tryHTTPProbe attempts an HTTP probe if the gordon.health label is set.
// Returns (true, err) if the probe was attempted, (false, nil) if skipped.
func (s *Service) tryHTTPProbe(ctx context.Context, containerID string, containerConfig *domain.ContainerConfig, cfg Config) (bool, error) {
	log := zerowrap.FromCtx(ctx)
	if containerConfig == nil {
		return false, nil
	}
	healthPath, ok := containerConfig.Labels[domain.LabelHealth]
	if !ok || healthPath == "" {
		return false, nil
	}
	ip, port, probeErr := s.resolveProbeEndpoint(ctx, containerID, containerConfig)
	if probeErr != nil || ip == "" || port <= 0 {
		log.Debug().Err(probeErr).Msg("readiness cascade: HTTP probe skipped, could not resolve container endpoint")
		return false, nil
	}
	url := fmt.Sprintf("http://%s:%d%s", ip, port, healthPath)
	timeout := cfg.HTTPProbeTimeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	log.Info().Str("url", url).Dur("timeout", timeout).Msg("readiness cascade: using HTTP probe")
	return true, httpProbe(ctx, url, timeout)
}

// tryDefaultHTTPProbe attempts an HTTP GET on "/" using the container's
// exposed port. This is the default readiness check for web containers —
// it verifies the app is actually serving HTTP, not just accepting TCP.
// Returns (true, err) if the probe was attempted, (false, nil) if skipped.
func (s *Service) tryDefaultHTTPProbe(ctx context.Context, containerID string, containerConfig *domain.ContainerConfig, cfg Config) (bool, error) {
	log := zerowrap.FromCtx(ctx)
	if containerConfig == nil || len(containerConfig.Ports) == 0 {
		return false, nil
	}
	ip, port, probeErr := s.resolveProbeEndpoint(ctx, containerID, containerConfig)
	if probeErr != nil || ip == "" || port <= 0 {
		log.Debug().Err(probeErr).Msg("readiness cascade: default HTTP probe skipped, could not resolve container endpoint")
		return false, nil
	}
	url := fmt.Sprintf("http://%s:%d/", ip, port)
	timeout := cfg.HTTPProbeTimeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	log.Info().Str("url", url).Dur("timeout", timeout).Msg("readiness cascade: using default HTTP alive probe")
	return true, httpAliveProbe(ctx, url, timeout)
}

// tryTCPProbe attempts a TCP probe if port info is available.
// Returns (true, err) if the probe was attempted, (false, nil) if skipped.
func (s *Service) tryTCPProbe(ctx context.Context, containerID string, containerConfig *domain.ContainerConfig, cfg Config) (bool, error) {
	log := zerowrap.FromCtx(ctx)
	if containerConfig == nil || len(containerConfig.Ports) == 0 {
		return false, nil
	}
	ip, port, probeErr := s.resolveProbeEndpoint(ctx, containerID, containerConfig)
	if probeErr != nil || ip == "" || port <= 0 {
		log.Debug().Err(probeErr).Msg("readiness cascade: TCP probe skipped, could not resolve container endpoint")
		return false, nil
	}
	addr := fmt.Sprintf("%s:%d", ip, port)
	timeout := cfg.TCPProbeTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	log.Info().Str("addr", addr).Dur("timeout", timeout).Msg("readiness cascade: using TCP probe")
	return true, tcpProbe(ctx, addr, timeout)
}

// resolveContainerEndpoint returns a host-reachable address for probing.
// In rootless podman/Docker setups, container internal IPs are not routable
// from the host. We use the host port binding (127.0.0.1:<mapped_port>)
// which is always reachable. Falls back to the container's internal IP
// only if no host port mapping exists (e.g. host-network mode).
func (s *Service) resolveProbeEndpoint(ctx context.Context, containerID string, containerConfig *domain.ContainerConfig) (string, int, error) {
	log := zerowrap.FromCtx(ctx)

	internalPort := 0
	if containerConfig != nil {
		if labels := containerConfig.Labels; labels != nil {
			for _, key := range []string{domain.LabelProxyPort, domain.LabelPort} {
				if portStr, ok := labels[key]; ok && portStr != "" {
					port, err := strconv.Atoi(portStr)
					if err == nil && port > 0 && port <= 65535 {
						internalPort = port
						break
					}
					log.Warn().Str("label", key).Str("port_value", portStr).Msg("invalid probe port label value")
				}
			}
		}
	}

	if internalPort == 0 {
		_, resolvedPort, err := s.runtime.GetContainerNetworkInfo(ctx, containerID)
		if err != nil {
			return "", 0, err
		}
		internalPort = resolvedPort
	}

	hostPort, hostErr := s.runtime.GetContainerPort(ctx, containerID, internalPort)
	if hostErr == nil && hostPort > 0 {
		log.Debug().
			Int("internal_port", internalPort).
			Int("host_port", hostPort).
			Msg("resolved container endpoint via host port binding")
		return "127.0.0.1", hostPort, nil
	}

	ip, _, fallbackErr := s.runtime.GetContainerNetworkInfo(ctx, containerID)
	if fallbackErr != nil {
		return "", 0, fallbackErr
	}
	log.Debug().
		Str("ip", ip).
		Int("port", internalPort).
		Msg("resolved container endpoint via internal IP (no host port binding)")
	return ip, internalPort, nil
}

// waitForReadyByDelay waits using the legacy running+delay strategy.
func (s *Service) waitForReadyByDelay(ctx context.Context, containerID string) error {
	log := zerowrap.FromCtx(ctx)

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

func (s *Service) waitForHealthy(ctx context.Context, containerID string, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastStatus string
	for {
		status, hasHealthcheck, err := s.runtime.GetContainerHealthStatus(waitCtx, containerID)
		if err != nil {
			return err
		}
		if !hasHealthcheck {
			return errors.New("no healthcheck detected")
		}
		// Treat empty health status as transitional startup. Some runtimes can
		// temporarily report an empty status before first probe results.
		if status == "" {
			status = "starting"
		}
		if status == "healthy" {
			return nil
		}
		lastStatus = status

		select {
		case <-time.After(time.Second):
		case <-waitCtx.Done():
			return fmt.Errorf("container healthcheck timeout after %s (last status: %s)", timeout, lastStatus)
		}
	}
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
