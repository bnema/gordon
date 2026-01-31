// Package app provides the application initialization and wiring.
package app

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// SubContainerSpec defines the specification for a sub-container.
type SubContainerSpec struct {
	Name         string            // Container name (e.g., "gordon-proxy")
	Image        string            // Docker image to use
	Component    string            // Component type (proxy, registry, secrets)
	Env          map[string]string // Environment variables
	Ports        map[string]string // Port mappings (host:container)
	Volumes      map[string]string // Volume mounts (host:container)
	NetworkMode  string            // Network mode (typically "gordon-internal")
	Privileged   bool              // Whether container needs privileged mode
	ReadOnlyRoot bool              // Whether root filesystem should be read-only
}

// LifecycleManager manages the lifecycle of Gordon sub-containers.
// It handles deployment, health monitoring, and auto-restart of:
//   - gordon-proxy (HTTP reverse proxy)
//   - gordon-registry (Docker registry)
//   - gordon-secrets (secrets management)
type LifecycleManager struct {
	runtime out.ContainerRuntime
	image   string // Self-image to use for sub-containers
	specs   []SubContainerSpec
	log     zerowrap.Logger
	stopCh  chan struct{}
}

// NewLifecycleManager creates a new lifecycle manager.
func NewLifecycleManager(runtime out.ContainerRuntime, selfImage string, log zerowrap.Logger) *LifecycleManager {
	return &LifecycleManager{
		runtime: runtime,
		image:   selfImage,
		log:     log,
		stopCh:  make(chan struct{}),
	}
}

// InitializeSpecs creates the specifications for all sub-containers.
// This should be called after configuration is loaded.
func (lm *LifecycleManager) InitializeSpecs(cfg Config) {
	dataDir := cfg.Server.DataDir
	if dataDir == "" {
		dataDir = "/var/lib/gordon"
	}

	// Common environment variables
	logLevel := cfg.Logging.Level
	if logLevel == "" {
		logLevel = "info"
	}

	commonEnv := map[string]string{
		"GORDON_LOG_LEVEL": logLevel,
	}

	// gordon-secrets: Isolated secrets management
	secretsSpec := SubContainerSpec{
		Name:         "gordon-secrets",
		Image:        lm.image,
		Component:    "secrets",
		Env:          mergeEnv(commonEnv, map[string]string{"GORDON_COMPONENT": "secrets"}),
		Ports:        map[string]string{"9091": "9091"}, // gRPC
		NetworkMode:  "gordon-internal",
		ReadOnlyRoot: true,
		Volumes:      map[string]string{
			// Secrets are mounted read-only from host
			// Actual paths depend on secrets backend (pass, sops, etc.)
		},
	}

	// gordon-registry: Docker registry with gRPC inspection
	registrySpec := SubContainerSpec{
		Name:         "gordon-registry",
		Image:        lm.image,
		Component:    "registry",
		Env:          mergeEnv(commonEnv, map[string]string{"GORDON_COMPONENT": "registry"}),
		Ports:        map[string]string{"5000": "5000", "9092": "9092"}, // HTTP + gRPC
		NetworkMode:  "gordon-internal",
		ReadOnlyRoot: true,
		Volumes: map[string]string{
			dataDir + "/registry": "/var/lib/registry",
		},
	}

	// gordon-proxy: Internet-facing HTTP reverse proxy
	proxySpec := SubContainerSpec{
		Name:         "gordon-proxy",
		Image:        lm.image,
		Component:    "proxy",
		Env:          mergeEnv(commonEnv, map[string]string{"GORDON_COMPONENT": "proxy"}),
		Ports:        map[string]string{"80": "80"}, // HTTP
		NetworkMode:  "gordon-internal",
		ReadOnlyRoot: true,
		// No volumes - proxy has no persistent state
		// No secrets - proxy cannot access GPG/password-store
	}

	lm.specs = []SubContainerSpec{secretsSpec, registrySpec, proxySpec}
}

// DeployAll deploys all sub-containers and ensures the network exists.
func (lm *LifecycleManager) DeployAll(ctx context.Context) error {
	log := lm.log.With().
		Str("usecase", "DeployAll").
		Logger()

	// Ensure internal network exists
	if err := lm.ensureNetwork(ctx, "gordon-internal"); err != nil {
		return fmt.Errorf("failed to ensure internal network: %w", err)
	}

	// Deploy each sub-container in order
	deployOrder := []string{"gordon-secrets", "gordon-registry", "gordon-proxy"}
	for _, name := range deployOrder {
		spec := lm.findSpec(name)
		if spec == nil {
			continue
		}

		log.Info().Str("container", name).Msg("deploying sub-container")

		if err := lm.EnsureRunning(ctx, *spec); err != nil {
			return fmt.Errorf("failed to deploy %s: %w", name, err)
		}

		// Wait for health check
		if err := lm.waitForHealth(ctx, name, spec.Component); err != nil {
			return fmt.Errorf("health check failed for %s: %w", name, err)
		}

		log.Info().Str("container", name).Msg("sub-container ready")
	}

	return nil
}

// EnsureRunning ensures a sub-container is running with the correct configuration.
func (lm *LifecycleManager) EnsureRunning(ctx context.Context, spec SubContainerSpec) error {
	log := lm.log.With().
		Str("container", spec.Name).
		Str("component", spec.Component).
		Logger()

	// Check if container exists and is running
	containers, err := lm.runtime.ListContainers(ctx, true)
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	var existing *domain.Container
	for _, c := range containers {
		if c.Name == spec.Name {
			existing = c
			break
		}
	}

	if existing != nil {
		// Check if it's using the correct image
		isRunning, _ := lm.runtime.IsContainerRunning(ctx, existing.ID)

		if isRunning {
			// Verify it's the right image
			inspect, err := lm.runtime.InspectContainer(ctx, existing.ID)
			if err == nil && strings.HasPrefix(inspect.Image, spec.Image) {
				log.Debug().Msg("container already running with correct image")
				return nil
			}
		}

		// Stop and remove if wrong image or not running
		log.Info().Msg("stopping existing container for restart")
		if err := lm.runtime.StopContainer(ctx, existing.ID); err != nil {
			log.Warn().Err(err).Msg("failed to stop container, forcing removal")
		}
		if err := lm.runtime.RemoveContainer(ctx, existing.ID, true); err != nil {
			return fmt.Errorf("failed to remove existing container: %w", err)
		}
	}

	// Create new container
	log.Info().Msg("creating new container")

	// Build environment variables
	envVars := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		envVars = append(envVars, fmt.Sprintf("%s=%s", k, v))
	}

	// Convert ports to domain format
	ports := make([]int, 0, len(spec.Ports))
	for _, containerPort := range spec.Ports {
		p, _ := strconv.Atoi(containerPort)
		if p > 0 {
			ports = append(ports, p)
		}
	}

	config := &domain.ContainerConfig{
		Name:        spec.Name,
		Image:       spec.Image,
		Env:         envVars,
		Ports:       ports,
		NetworkMode: spec.NetworkMode,
		Cmd:         []string{"--component=" + spec.Component},
		Labels: map[string]string{
			"gordon.managed":   "true",
			"gordon.component": spec.Component,
		},
	}

	container, err := lm.runtime.CreateContainer(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}

	// Start the container
	if err := lm.runtime.StartContainer(ctx, container.ID); err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	log.Info().Str("container_id", container.ID).Msg("container started")
	return nil
}

// MonitorLoop runs a continuous monitoring loop for all sub-containers.
// It checks health every 15 seconds and restarts failed containers.
func (lm *LifecycleManager) MonitorLoop(ctx context.Context) {
	log := lm.log.With().
		Str("usecase", "MonitorLoop").
		Logger()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	log.Info().Msg("starting sub-container monitoring loop")

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("stopping monitoring loop")
			return
		case <-lm.stopCh:
			log.Info().Msg("stopping monitoring loop (stop signal)")
			return
		case <-ticker.C:
			lm.checkAndRestart(ctx)
		}
	}
}

// Stop stops the monitoring loop.
func (lm *LifecycleManager) Stop() {
	close(lm.stopCh)
}

// checkAndRestart checks all sub-containers and restarts any that are down.
func (lm *LifecycleManager) checkAndRestart(ctx context.Context) {
	for _, spec := range lm.specs {
		isHealthy, err := lm.checkHealth(ctx, spec.Name, spec.Component)
		if err != nil || !isHealthy {
			lm.log.Warn().
				Str("container", spec.Name).
				Err(err).
				Msg("sub-container unhealthy, restarting")

			if err := lm.EnsureRunning(ctx, spec); err != nil {
				lm.log.Error().
					Str("container", spec.Name).
					Err(err).
					Msg("failed to restart sub-container")
			}
		}
	}
}

// checkHealth checks if a sub-container is healthy.
func (lm *LifecycleManager) checkHealth(ctx context.Context, name, component string) (bool, error) {
	// Check if container is running
	containers, err := lm.runtime.ListContainers(ctx, false)
	if err != nil {
		return false, err
	}

	for _, c := range containers {
		if c.Name == name {
			return lm.runtime.IsContainerRunning(ctx, c.ID)
		}
	}

	return false, fmt.Errorf("container not found: %s", name)
}

// waitForHealth waits for a sub-container to become healthy.
func (lm *LifecycleManager) waitForHealth(ctx context.Context, name, component string) error {
	// Wait up to 30 seconds for container to be healthy
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		isHealthy, err := lm.checkHealth(ctx, name, component)
		if err == nil && isHealthy {
			return nil
		}
		time.Sleep(1 * time.Second)
	}

	return fmt.Errorf("timeout waiting for %s to become healthy", name)
}

// ensureNetwork ensures the specified Docker network exists.
func (lm *LifecycleManager) ensureNetwork(ctx context.Context, name string) error {
	exists, err := lm.runtime.NetworkExists(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to check network existence: %w", err)
	}

	if exists {
		return nil
	}

	lm.log.Info().Str("network", name).Msg("creating Docker network")

	return lm.runtime.CreateNetwork(ctx, name, map[string]string{
		"gordon.managed": "true",
	})
}

// findSpec finds a sub-container spec by name.
func (lm *LifecycleManager) findSpec(name string) *SubContainerSpec {
	for i := range lm.specs {
		if lm.specs[i].Name == name {
			return &lm.specs[i]
		}
	}
	return nil
}

// mergeEnv merges multiple environment maps.
func mergeEnv(maps ...map[string]string) map[string]string {
	result := make(map[string]string)
	for _, m := range maps {
		for k, v := range m {
			result[k] = v
		}
	}
	return result
}

// GetSelfImage detects the Docker image of the current (core) container.
// It tries multiple methods: GORDON_IMAGE env var, /proc/self/cgroup, hostname.
func GetSelfImage(runtime out.ContainerRuntime) string {
	// First, check environment variable
	if image := os.Getenv("GORDON_IMAGE"); image != "" {
		return image
	}

	// Try to detect from running container
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get hostname (usually container ID)
	hostname, err := os.Hostname()
	if err == nil && len(hostname) == 12 {
		// Short container ID format
		container, err := runtime.InspectContainer(ctx, hostname)
		if err == nil && container != nil {
			return container.Image
		}
	}

	// Fallback: try to find a container named gordon-core or similar
	containers, err := runtime.ListContainers(ctx, false)
	if err == nil {
		for _, c := range containers {
			if c.Name == "gordon-core" || strings.Contains(c.Name, "gordon") {
				return c.Image
			}
		}
	}

	// Ultimate fallback: default image name
	return "bnema/gordon:latest"
}
