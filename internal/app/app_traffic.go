package app

import (
	"context"
	"fmt"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/usecase/apptraffic"
)

// appTrafficPublisher is the single serialized HTTP/L4 publication
// boundary for apps. Activation rebuilds, fail-closed recovery
// withdrawal, and config reload all take the same mutex: the host index
// replacement and the runtime graph application always run as one
// operation, so no caller can observe an index that disagrees with the
// applied graph, and a stale config can never win a race.
type appTrafficPublisher struct {
	// gate is a one-slot semaphore instead of a mutex so a waiting
	// caller can abandon the wait when its context ends: a monitor pass
	// must never hold an app lock for a full graph application that a
	// sibling publication started, and shutdown must not block behind
	// it.
	gate chan struct{}
	svc  *services
	cfg  Config
	// applyGraph applies one prepared configuration. The publication
	// projects and validates a candidate against this call and swaps the
	// live host index only after it returns nil, so a rejected graph can
	// never leave the index ahead of the dataplane. It is a field so
	// publication ordering is testable without a live traffic manager.
	applyGraph func(ctx context.Context, cfg Config, hosts appHostRoutes) error
}

var _ out.AppTrafficRefresher = (*appTrafficPublisher)(nil)

// newAppTrafficPublisher wires the boundary. The installation config is
// stored so activation rebuilds use the same validated snapshot that
// reload applied.
func newAppTrafficPublisher(svc *services, cfg Config) *appTrafficPublisher {
	publisher := &appTrafficPublisher{gate: make(chan struct{}, 1), svc: svc, cfg: cfg}
	publisher.applyGraph = func(ctx context.Context, cfg Config, hosts appHostRoutes) error {
		return applyTrafficRuntimeConfig(ctx, svc.trafficManager, cfg, svc.configSvc, hosts)
	}
	publisher.gate <- struct{}{}
	return publisher
}

// acquire waits for the publication gate or the caller's deadline.
func (p *appTrafficPublisher) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.gate:
		return nil
	}
}

// release returns the publication gate to the next caller.
func (p *appTrafficPublisher) release() {
	p.gate <- struct{}{}
}

// RebuildTraffic re-projects verified ACTIVE state and applies the full
// HTTP/L4 graph.
func (p *appTrafficPublisher) RebuildTraffic(ctx context.Context) error {
	if err := p.acquire(ctx); err != nil {
		return fmt.Errorf("traffic publisher: %w", err)
	}
	defer p.release()
	return p.rebuild(ctx, p.cfg)
}

// RebuildWithConfig applies the current validated config through the
// same serialization point. Startup and reload call it so they cannot
// interleave with a recovery withdrawal.
func (p *appTrafficPublisher) RebuildWithConfig(ctx context.Context, cfg Config) error {
	if err := p.acquire(ctx); err != nil {
		return fmt.Errorf("traffic publisher: %w", err)
	}
	defer p.release()
	p.cfg = cfg
	return p.rebuild(ctx, cfg)
}

// WithdrawService makes one service non-forwardable and republishes.
// The durable ACTIVE record loses the service's binds first: every
// later projection is then fail-closed even if the graph application
// itself fails. A non-nil result means stale forwarding may remain and
// the caller must retain its publication inhibition.
// WithdrawServiceState is the canonical state-only ACTIVE bind
// withdrawal: it clears the recorded binds through the same
// serialization point as WithdrawService but applies no graph.
func (p *appTrafficPublisher) WithdrawServiceState(ctx context.Context, app, service string) error {
	if err := p.acquire(ctx); err != nil {
		return fmt.Errorf("traffic publisher: %w", err)
	}
	defer p.release()
	return p.withdrawFromState(ctx, app, service)
}

func (p *appTrafficPublisher) WithdrawService(ctx context.Context, app, service string) error {
	if err := p.acquire(ctx); err != nil {
		return fmt.Errorf("traffic publisher: %w", err)
	}
	defer p.release()
	if err := p.withdrawFromState(ctx, app, service); err != nil {
		return err
	}
	err := p.rebuild(ctx, p.cfg)
	if err == nil {
		return nil
	}
	// The graph apply failed while the durable ACTIVE record already
	// dropped the service's binds. The read model still has to follow: a
	// stale index entry could dial a loopback port that has been
	// recycled by another workload, so the proxy must fail closed. The
	// error is returned unchanged so the caller keeps its publication
	// inhibition and retries the graph.
	if candidate, projErr := p.projectCandidate(ctx, p.cfg); projErr == nil && candidate != nil {
		p.svc.appHostIndex.Replace(candidate.Entries())
	}
	return err
}

// withdrawFromState drops the recorded binds of one service in ACTIVE.
// The container itself is untouched: withdrawal is a traffic decision,
// never a workload mutation.
func (p *appTrafficPublisher) withdrawFromState(ctx context.Context, app, service string) error {
	if p.svc.appState == nil {
		return nil
	}
	active, ok, err := p.svc.appState.LoadActive(ctx, app)
	if err != nil {
		return fmt.Errorf("withdraw %s/%s: load active: %w", app, service, err)
	}
	if !ok {
		return nil
	}
	current, ok := active.Services[service]
	if !ok || (len(current.BackendBinds) == 0 && len(current.UDPBackendBinds) == 0) {
		return nil
	}
	current.BackendBinds = nil
	current.UDPBackendBinds = nil
	active.Services[service] = current
	if err := p.svc.appState.SaveActive(ctx, active); err != nil {
		return fmt.Errorf("withdraw %s/%s: persist fail-closed state: %w", app, service, err)
	}
	return nil
}

// rebuild projects ACTIVE into a candidate host view, applies the full
// graph against that candidate, and publishes the candidate only after
// the dataplane accepted it. The caller holds the publication gate. On
// any failure the live index and the applied graph both stay at their
// previous generation: the proxy never serves routing the dataplane
// rejected, and no retirement ever follows an unpublished switch.
func (p *appTrafficPublisher) rebuild(ctx context.Context, cfg Config) error {
	candidate, err := p.projectCandidate(ctx, cfg)
	if err != nil {
		return err
	}
	var hosts appHostRoutes
	if candidate != nil {
		hosts = candidate
	}
	if err := p.applyGraph(ctx, cfg, hosts); err != nil {
		return fmt.Errorf("apply traffic graph: %w", err)
	}
	if candidate != nil {
		p.svc.appHostIndex.Replace(candidate.Entries())
	}
	return nil
}

// projectCandidate builds the detached host view for one publication.
// The candidate never touches the live index: a failing projection leaves
// what the proxy serves untouched. It returns nil when app state is not
// wired, in which case the graph carries configuration routes only.
func (p *appTrafficPublisher) projectCandidate(ctx context.Context, cfg Config) (*apptraffic.HostIndex, error) {
	if p.svc.appActivator == nil || p.svc.appState == nil || p.svc.appHostIndex == nil {
		return nil, nil
	}
	candidate := apptraffic.NewHostIndex()
	if err := p.svc.appActivator.RebuildHostIndex(ctx, candidate, p.svc.appState, appEntrypointPolicies(cfg)); err != nil {
		return nil, fmt.Errorf("project app host index: %w", err)
	}
	return candidate, nil
}
