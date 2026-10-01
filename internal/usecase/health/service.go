// Package health implements health checking over ACTIVE app services.
package health

import (
	"context"
	"fmt"
	"sync"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// maxConcurrentProbes limits the number of concurrent health probes to prevent resource exhaustion.
const maxConcurrentProbes = 10

// Service implements the HealthService interface over ACTIVE app state.
// Probes dial recorded loopback backends rootless-first, never
// container IPs.
type Service struct {
	state   out.AppStateReader
	runtime out.ContainerRuntime
	prober  in.HTTPProber
	log     zerowrap.Logger
}

// NewService creates a new health service.
func NewService(
	state out.AppStateReader,
	runtime out.ContainerRuntime,
	prober in.HTTPProber,
	log zerowrap.Logger,
) *Service {
	return &Service{
		state:   state,
		runtime: runtime,
		prober:  prober,
		log:     log,
	}
}

// CheckAllRoutes performs health checks on all app-served HTTP hosts.
// The result maps canonical host to health; stopped-intent apps are
// skipped (not routable, not healthy).
func (s *Service) CheckAllRoutes(ctx context.Context) map[string]*domain.RouteHealth {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "CheckAllRoutes",
	})
	log := zerowrap.FromCtx(ctx)

	targets := s.hostTargets(ctx)
	results := make(map[string]*domain.RouteHealth, len(targets))

	if len(targets) == 0 {
		log.Debug().Msg("no app HTTP hosts to check")
		return results
	}

	// Check hosts concurrently with semaphore to limit resource usage
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentProbes)

	for _, target := range targets {
		wg.Add(1)
		go func(t hostTarget) {
			defer wg.Done()
			sem <- struct{}{}        // Acquire semaphore
			defer func() { <-sem }() // Release semaphore

			health := s.checkHost(ctx, t)
			mu.Lock()
			results[t.host] = health
			mu.Unlock()
		}(target)
	}

	wg.Wait()

	log.Debug().Int("routes_checked", len(results)).Msg("all health checks complete")
	return results
}

// hostTarget is one probed HTTP host.
type hostTarget struct {
	host    string
	backend domain.AppBackend
}

// hostTargets enumerates probed hosts from ACTIVE state, skipping
// stopped-intent apps.
func (s *Service) hostTargets(ctx context.Context) []hostTarget {
	apps, err := s.state.ListApps(ctx)
	if err != nil {
		return nil
	}
	var targets []hostTarget
	for _, app := range apps {
		intent, err := s.state.LoadIntent(ctx, app)
		if err != nil || intent.Stopped {
			continue
		}
		active, ok, err := s.state.LoadActive(ctx, app)
		if err != nil || !ok {
			continue
		}
		for _, eff := range active.Services {
			for _, h := range eff.Spec.HTTP {
				if h.Host == "" {
					continue
				}
				targets = append(targets, hostTarget{host: h.Host, backend: eff.BackendFor(h.Port)})
			}
		}
	}
	return targets
}

// checkHost probes one host's recorded loopback backend.
func (s *Service) checkHost(ctx context.Context, target hostTarget) *domain.RouteHealth {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "CheckHost",
		"domain":              target.host,
	})
	log := zerowrap.FromCtx(ctx)

	health := &domain.RouteHealth{
		Domain:          target.host,
		ContainerStatus: "unknown",
	}

	if !target.backend.Resolved() {
		health.ContainerStatus = "not found"
		health.Error = "no recorded backend bind for host"
		log.Debug().Msg("host has no resolved backend")
		return health
	}

	running, err := s.runtime.IsContainerRunning(ctx, target.backend.ContainerID)
	if err != nil || !running {
		health.ContainerStatus = "not running"
		health.Error = "container not running"
		log.Debug().Msg("container not running, skipping HTTP probe")
		return health
	}
	health.ContainerStatus = string(domain.ContainerStatusRunning)

	url := fmt.Sprintf("http://127.0.0.1:%d/", target.backend.Port)
	statusCode, responseTime, err := s.prober.Probe(ctx, url)
	if err != nil {
		health.Error = err.Error()
		log.Debug().Err(err).Str("url", url).Msg("HTTP probe failed")
		return health
	}

	health.HTTPStatus = statusCode
	health.ResponseTimeMs = responseTime
	health.Healthy = statusCode >= 200 && statusCode < 400

	log.Debug().
		Int("http_status", statusCode).
		Int64("response_time_ms", responseTime).
		Bool("healthy", health.Healthy).
		Msg("health check complete")

	return health
}
