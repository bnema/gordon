// Package health implements the health check use case for routes.
package health

import (
	"context"
	"fmt"
	"sync"

	"github.com/bnema/zerowrap"

	"gordon/internal/boundaries/in"
	"gordon/internal/domain"
)

// maxConcurrentProbes limits the number of concurrent health probes to prevent resource exhaustion.
const maxConcurrentProbes = 10

// Service implements the HealthService interface.
type Service struct {
	configSvc    in.ConfigService
	containerSvc in.ContainerService
	prober       in.HTTPProber
	log          zerowrap.Logger
}

// NewService creates a new health service.
func NewService(
	configSvc in.ConfigService,
	containerSvc in.ContainerService,
	prober in.HTTPProber,
	log zerowrap.Logger,
) *Service {
	return &Service{
		configSvc:    configSvc,
		containerSvc: containerSvc,
		prober:       prober,
		log:          log,
	}
}

// CheckRoute performs a health check on a single route.
func (s *Service) CheckRoute(ctx context.Context, route domain.Route) *domain.RouteHealth {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "CheckRoute",
		"domain":              route.Domain,
	})
	log := zerowrap.FromCtx(ctx)

	health := &domain.RouteHealth{
		Domain:          route.Domain,
		ContainerStatus: "unknown",
	}

	// Check container status
	container, exists := s.containerSvc.Get(ctx, route.Domain)
	if !exists || container == nil {
		health.ContainerStatus = "not found"
		health.Error = "container not found"
		log.Debug().Msg("container not found")
		return health
	}

	health.ContainerStatus = container.Status

	// Only probe HTTP if container is running
	if container.Status != string(domain.ContainerStatusRunning) {
		health.Error = fmt.Sprintf("container is %s", container.Status)
		log.Debug().Str("status", container.Status).Msg("container not running, skipping HTTP probe")
		return health
	}

	// Probe HTTP endpoint
	url := fmt.Sprintf("https://%s/", route.Domain)
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

// CheckAllRoutes performs health checks on all configured routes.
func (s *Service) CheckAllRoutes(ctx context.Context) map[string]*domain.RouteHealth {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "CheckAllRoutes",
	})
	log := zerowrap.FromCtx(ctx)

	routes := s.configSvc.GetRoutes(ctx)
	results := make(map[string]*domain.RouteHealth, len(routes))

	if len(routes) == 0 {
		log.Debug().Msg("no routes configured")
		return results
	}

	// Check routes concurrently with semaphore to limit resource usage
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentProbes)

	for _, route := range routes {
		wg.Add(1)
		go func(r domain.Route) {
			defer wg.Done()
			sem <- struct{}{}        // Acquire semaphore
			defer func() { <-sem }() // Release semaphore

			health := s.CheckRoute(ctx, r)
			mu.Lock()
			results[r.Domain] = health
			mu.Unlock()
		}(route)
	}

	wg.Wait()

	log.Debug().Int("routes_checked", len(results)).Msg("all health checks complete")
	return results
}
