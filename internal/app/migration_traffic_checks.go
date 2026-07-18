package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	edgesnapshotusecase "github.com/bnema/gordon/internal/usecase/edgesnapshot"
)

// migrationTrafficChecks is the production cutover reader. It consumes only
// authenticated runtime lifecycle commands, sanitized runtime state, and edge
// reports; it never creates a Docker-compatible client or opens a socket.
type migrationTrafficChecks struct {
	runtime out.RuntimeSelfUpdater
	state   out.RuntimeStateSubscriber
	store   *MigrationCheckpointStore
	applied *edgesnapshotusecase.AppliedStateTracker
	cfg     Config
	http    *http.Client
}

func newMigrationTrafficChecks(runtime out.RuntimeSelfUpdater, state out.RuntimeStateSubscriber, store *MigrationCheckpointStore, applied *edgesnapshotusecase.AppliedStateTracker, cfg Config) (TrafficSwitchChecks, error) {
	if runtime == nil || state == nil || store == nil || applied == nil {
		return nil, fmt.Errorf("runtime lifecycle, runtime state, checkpoint store, and edge applied-state tracker are required")
	}
	return &migrationTrafficChecks{
		runtime: runtime, state: state, store: store, applied: applied, cfg: cfg, http: newMigrationProbeHTTPClient(),
	}, nil
}

// ComponentHealthy uses the runtime-owned lifecycle health command instead of
// guessing from a process/listener. The runtime command channel is authenticated
// and is the only holder of the container socket.
func (c *migrationTrafficChecks) ComponentHealthy(ctx context.Context, role domain.ComponentRole) error {
	component, err := c.component(role)
	if err != nil {
		return err
	}
	executor, err := NewRuntimeComponentRedeployExecutor(c.runtime)
	if err != nil {
		return err
	}
	if err := executor.CheckReplacementHealth(ctx, component); err != nil {
		return fmt.Errorf("runtime lifecycle health for %s: %w", role, err)
	}
	return nil
}

// ComponentAuthenticationHealthy proves control's authenticated runtime path
// for every role. Edge has an additional authenticated acknowledgement from the
// edge itself; no TCP success is treated as component authentication.
func (c *migrationTrafficChecks) ComponentAuthenticationHealthy(ctx context.Context, role domain.ComponentRole) error {
	if err := c.ComponentHealthy(ctx, role); err != nil {
		return fmt.Errorf("authenticated %s readiness: %w", role, err)
	}
	if role != domain.ComponentRoleEdge {
		return nil
	}
	checkpoint, err := c.store.Load()
	if err != nil {
		return fmt.Errorf("load migration checkpoint: %w", err)
	}
	edge, err := componentFromCheckpoint(*checkpoint, domain.ComponentRoleEdge)
	if err != nil {
		return err
	}
	if err := c.applied.AppliedFor(ctx, edge.ComponentID, checkpoint.RouteSnapshotGeneration, checkpoint.RouteSnapshotGeneration); err != nil {
		return fmt.Errorf("authenticated edge applied readiness: %w", err)
	}
	return nil
}

func (c *migrationTrafficChecks) AppliedRouteGeneration(ctx context.Context) (uint64, error) {
	return c.appliedGeneration(ctx)
}
func (c *migrationTrafficChecks) AppliedTrafficGeneration(ctx context.Context) (uint64, error) {
	return c.appliedGeneration(ctx)
}

func (c *migrationTrafficChecks) appliedGeneration(ctx context.Context) (uint64, error) {
	checkpoint, err := c.store.Load()
	if err != nil {
		return 0, err
	}
	edge, err := componentFromCheckpoint(*checkpoint, domain.ComponentRoleEdge)
	if err != nil {
		return 0, err
	}
	if err := c.applied.AppliedFor(ctx, edge.ComponentID, checkpoint.RouteSnapshotGeneration, checkpoint.RouteSnapshotGeneration); err != nil {
		return 0, err
	}
	return checkpoint.RouteSnapshotGeneration, nil
}

func (c *migrationTrafficChecks) TestApplicationThroughEdge(ctx context.Context) error {
	checkpoint, err := c.store.Load()
	if err != nil {
		return err
	}
	return c.probe(ctx, checkpoint.BootstrapEdgeProbeEndpoint, c.cfg.Server.GordonDomain, "/", false)
}

func (c *migrationTrafficChecks) TestRegistryV2ThroughEdge(ctx context.Context) error {
	checkpoint, err := c.store.Load()
	if err != nil {
		return err
	}
	return c.probe(ctx, checkpoint.BootstrapEdgeProbeEndpoint, c.cfg.Server.RegistryDomain, "/v2/", true)
}

// OldServingPathHealthy deliberately checks both the old managed container and
// the old loopback HTTP listener before activating edge. A live TCP listener on
// an unrelated process can therefore never satisfy the prerequisite.
func (c *migrationTrafficChecks) OldServingPathHealthy(ctx context.Context, old string) error {
	checkpoint, err := c.store.Load()
	if err != nil {
		return err
	}
	if old == "" || old != checkpoint.OldServingPath {
		return fmt.Errorf("old serving path does not match checkpoint")
	}
	updates, err := c.state.SubscribeRuntimeState(ctx)
	if err != nil {
		return fmt.Errorf("subscribe runtime state: %w", err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case snapshot, ok := <-updates:
		if !ok {
			return fmt.Errorf("runtime state closed before old serving health")
		}
		for _, container := range snapshot.Containers {
			if container.Name == old && container.Status == domain.ContainerStatusRunning && container.Labels[domain.LabelManaged] == "true" {
				return c.probe(ctx, checkpoint.OldServingProbeEndpoint, c.cfg.Server.GordonDomain, "/", false)
			}
		}
		return fmt.Errorf("old managed serving path is not running")
	}
}

func (c *migrationTrafficChecks) component(role domain.ComponentRole) (ComponentLaunchComponent, error) {
	checkpoint, err := c.store.Load()
	if err != nil {
		return ComponentLaunchComponent{}, fmt.Errorf("load migration checkpoint: %w", err)
	}
	return componentFromCheckpoint(*checkpoint, role)
}

func componentFromCheckpoint(checkpoint MigrationCheckpoint, role domain.ComponentRole) (ComponentLaunchComponent, error) {
	plan, err := NewComponentLaunchPlan(checkpoint)
	if err != nil {
		return ComponentLaunchComponent{}, err
	}
	component, found := componentForRole(plan, role)
	if !found {
		return ComponentLaunchComponent{}, fmt.Errorf("component %s is absent", role)
	}
	return component, nil
}

// probe permits only a checkpointed numeric loopback endpoint. It constructs
// the Host header from validated configuration, never from checkpoint data,
// and follows no redirects, preventing a migration checkpoint from becoming an
// SSRF primitive.
func (c *migrationTrafficChecks) probe(ctx context.Context, endpoint, host, path string, registry bool) error {
	if err := validLoopbackProbeEndpoint(endpoint); err != nil {
		return err
	}
	if !domain.IsValidRouteDomain(host) {
		return fmt.Errorf("invalid probe host")
	}
	requestURL := (&url.URL{Scheme: "http", Host: endpoint, Path: path}).String()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return fmt.Errorf("create probe request: %w", err)
	}
	request.Host = host
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("edge probe request failed: %w", err)
	}
	defer response.Body.Close()
	if registry {
		if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusUnauthorized {
			return nil
		}
	} else if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusBadRequest {
		return nil
	}
	return fmt.Errorf("edge probe returned status %d", response.StatusCode)
}

func newMigrationProbeHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	return &http.Client{
		Timeout: 3 * time.Second,
		// Never honor HTTP_PROXY for cutover checks: a checkpointed loopback
		// destination must not be diverted to a network proxy.
		Transport: &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if err := validLoopbackProbeEndpoint(address); err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, address)
		}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func validLoopbackProbeEndpoint(endpoint string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(endpoint))
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("invalid loopback probe endpoint")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("probe endpoint must be a literal loopback address")
	}
	return nil
}
