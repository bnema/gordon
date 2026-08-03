package edgesnapshot

import (
	"context"
	"fmt"
	"sync"

	"github.com/bnema/gordon/internal/domain"
	trafficusecase "github.com/bnema/gordon/internal/usecase/traffic"
)

// TrafficGraphProducerOptions contains the small, explicitly permitted control
// inputs for an edge traffic graph. It deliberately does not contain Config or
// any runtime identity.
type TrafficGraphProducerOptions struct {
	EntryPoints          map[string]trafficusecase.EntryPointConfig
	Traffic              trafficusecase.Config
	ExternalRouteTargets []domain.RouteTargetEntry
	NetworkServices      []trafficusecase.NetworkServiceConfig
	Services             []domain.StandaloneService
}

// TrafficGraphProducer turns route snapshots into independently versioned edge
// traffic graphs. Route callbacks are invoked by SnapshotHub without its state
// mutex, so graph construction and validation never block readers of the route
// hub.
type TrafficGraphProducer struct {
	routes *SnapshotHub
	graphs *TrafficGraphHub

	mu         sync.Mutex
	options    TrafficGraphProducerOptions
	generation domain.TrafficGraphGeneration
	started    bool
	stop       func()
}

func NewTrafficGraphProducer(routes *SnapshotHub, graphs *TrafficGraphHub, options TrafficGraphProducerOptions) (*TrafficGraphProducer, error) {
	if routes == nil {
		return nil, fmt.Errorf("route snapshot hub is required")
	}
	if graphs == nil {
		return nil, fmt.Errorf("traffic graph hub is required")
	}
	return &TrafficGraphProducer{routes: routes, graphs: graphs, options: cloneTrafficGraphProducerOptions(options)}, nil
}

// Start publishes a graph for the current route snapshot before returning and
// observes later transitions. It is safe to call only once.
func (p *TrafficGraphProducer) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return fmt.Errorf("traffic graph producer already started")
	}
	p.started = true
	p.mu.Unlock()

	p.stop = p.routes.ObserveTransitions(ctx, func(observerCtx context.Context, _ *domain.RouteTargetSnapshot, current domain.RouteTargetSnapshot) {
		// A bad update must never replace the last known-good graph.
		_ = p.publish(observerCtx, current)
	})
	current, err := p.routes.Current(ctx)
	if err != nil {
		p.stop()
		return fmt.Errorf("read initial route snapshot: %w", err)
	}
	if err := p.publish(ctx, current); err != nil {
		p.stop()
		return err
	}
	return nil
}

// Reload replaces only the canonical control-owned graph inputs and publishes
// a new graph for the current route snapshot. This gives config reloads a new
// graph generation even when routes have not changed.
func (p *TrafficGraphProducer) Reload(ctx context.Context, options TrafficGraphProducerOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	if !p.started {
		p.mu.Unlock()
		return fmt.Errorf("traffic graph producer has not started")
	}
	p.options = cloneTrafficGraphProducerOptions(options)
	p.mu.Unlock()
	current, err := p.routes.Current(ctx)
	if err != nil {
		return fmt.Errorf("read route snapshot for traffic graph reload: %w", err)
	}
	return p.publish(ctx, current)
}

func (p *TrafficGraphProducer) publish(ctx context.Context, routes domain.RouteTargetSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Serialize builds as well as publication: a route transition and a config
	// reload may otherwise choose the same generation concurrently. This mutex
	// is producer-local; SnapshotHub invokes us without holding its state lock.
	p.mu.Lock()
	defer p.mu.Unlock()
	options := cloneTrafficGraphProducerOptions(p.options)
	next := p.generation + 1
	if routeGeneration := domain.TrafficGraphGeneration(routes.Generation); routeGeneration > next {
		next = routeGeneration
	}

	graph, err := trafficusecase.BuildEdgeGraph(trafficusecase.EdgeInput{
		EntryPoints:          options.EntryPoints,
		Traffic:              options.Traffic,
		RouteSnapshot:        routes,
		ExternalRouteTargets: options.ExternalRouteTargets,
		NetworkServices:      options.NetworkServices,
		Services:             options.Services,
	})
	if err != nil {
		return fmt.Errorf("build edge traffic graph: %w", err)
	}
	snapshot := domain.TrafficGraphSnapshot{Generation: next, Graph: graph}
	if err := p.graphs.Publish(snapshot); err != nil {
		return fmt.Errorf("publish traffic graph: %w", err)
	}
	p.generation = next
	return nil
}

func cloneTrafficGraphProducerOptions(options TrafficGraphProducerOptions) TrafficGraphProducerOptions {
	clone := options
	clone.EntryPoints = make(map[string]trafficusecase.EntryPointConfig, len(options.EntryPoints))
	for name, entry := range options.EntryPoints {
		entry.TrustedCIDRs = append([]string(nil), entry.TrustedCIDRs...)
		entry.RawFallbackTrustedCIDRs = append([]string(nil), entry.RawFallbackTrustedCIDRs...)
		clone.EntryPoints[name] = entry
	}
	clone.ExternalRouteTargets = append([]domain.RouteTargetEntry(nil), options.ExternalRouteTargets...)
	clone.NetworkServices = append([]trafficusecase.NetworkServiceConfig(nil), options.NetworkServices...)
	clone.Services = append([]domain.StandaloneService(nil), options.Services...)
	return clone
}
