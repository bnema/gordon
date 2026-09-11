// Package container implements the retained container lifecycle use case.
//
// The v2.50 declarative-apps cutover removed the route-container engine
// (deploy/restart/remove/reconcile/attachments/sync/autostart, image-label
// inference, readiness cascade, monitor). Workload effects belong to the
// deployment engine through out.ContainerRuntime directly. What remains:
//
//   - ListNetworks: read-only inventory of Gordon-managed networks.
//   - Shutdown: graceful teardown (log writer close; containers are left
//     running across Gordon restarts by design).
//   - UpdateConfig/SetMetrics/SetProxyCacheInvalidator/SetProxyDrainWaiter:
//     wiring hooks kept for reload/metrics registration.
package container

import (
	"context"
	"strings"
	"sync"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/out/telemetry"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// Config holds configuration needed by the container service.
type Config struct {
	NetworkPrefix string
}

// Service implements the ContainerService interface.
type Service struct {
	runtime          out.ContainerRuntime
	envLoader        out.EnvLoader
	eventBus         out.EventPublisher
	logWriter        out.ContainerLogWriter
	cacheInvalidator out.ProxyCacheInvalidator
	drainWaiter      out.ProxyDrainWaiter
	config           Config
	metrics          *telemetry.Metrics
	mu               sync.RWMutex
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
		runtime:   runtime,
		envLoader: envLoader,
		eventBus:  eventBus,
		logWriter: logWriter,
		config:    config,
	}
}

// SetMetrics sets telemetry metrics.
func (s *Service) SetMetrics(m *telemetry.Metrics) {
	s.metrics = m
}

// SetProxyCacheInvalidator sets the proxy cache invalidator.
func (s *Service) SetProxyCacheInvalidator(inv out.ProxyCacheInvalidator) {
	s.mu.Lock()
	s.cacheInvalidator = inv
	s.mu.Unlock()
}

// SetProxyDrainWaiter sets the proxy in-flight drain waiter.
func (s *Service) SetProxyDrainWaiter(waiter out.ProxyDrainWaiter) {
	s.mu.Lock()
	s.drainWaiter = waiter
	s.mu.Unlock()
}

// ListNetworks returns Gordon-managed networks (prefix-filtered,
// manager-labeled).
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

// Shutdown gracefully shuts down the container manager.
// Containers are left running across Gordon restarts by design;
// boot reconciliation picks them back up from ACTIVE state.
func (s *Service) Shutdown(ctx context.Context) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "Shutdown",
	})
	log := zerowrap.FromCtx(ctx)
	log.Info().Msg("shutting down container manager...")

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
