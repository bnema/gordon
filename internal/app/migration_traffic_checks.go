package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
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
	runtime              out.RuntimeSelfUpdater
	state                out.RuntimeStateSubscriber
	store                *MigrationCheckpointStore
	applied              *edgesnapshotusecase.AppliedStateTracker
	cfg                  Config
	http                 *http.Client
	probeToken           string
	privateProbeResolver func(context.Context, string) (net.IP, error)
	privateProbeClient   func(string, net.IP) *http.Client
}

func newMigrationTrafficChecks(runtime out.RuntimeSelfUpdater, state out.RuntimeStateSubscriber, store *MigrationCheckpointStore, applied *edgesnapshotusecase.AppliedStateTracker, cfg Config) (TrafficSwitchChecks, error) {
	if runtime == nil || state == nil || store == nil || applied == nil {
		return nil, fmt.Errorf("runtime lifecycle, runtime state, checkpoint store, and edge applied-state tracker are required")
	}
	return &migrationTrafficChecks{
		runtime: runtime, state: state, store: store, applied: applied, cfg: cfg, http: newMigrationProbeHTTPClient(), probeToken: migrationProbeToken(migrationRuntimeSeed(cfg)), privateProbeResolver: resolveMigrationPrivateProbeAddress, privateProbeClient: newMigrationPrivateProbeHTTPClient,
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
	if _, err := c.appliedGenerationFor(*checkpoint, edge.ComponentID, ctx); err != nil {
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
	return c.appliedGenerationFor(*checkpoint, edge.ComponentID, ctx)
}

// appliedGenerationFor accepts an in-memory report when available, then falls
// back to the checkpointed attestation written by the authenticated control
// receiver. The latter is required when the monolith CLI retries a switch
// after the replacement control process has restarted.
func (c *migrationTrafficChecks) appliedGenerationFor(checkpoint MigrationCheckpoint, edgeID string, ctx context.Context) (uint64, error) {
	if checkpoint.RouteSnapshotGeneration == 0 || checkpoint.AppliedEdgeComponentID != "" && checkpoint.AppliedEdgeComponentID != edgeID {
		return 0, edgesnapshotusecase.ErrAppliedStateStale
	}
	if err := c.applied.AppliedFor(ctx, edgeID, checkpoint.RouteSnapshotGeneration, checkpoint.RouteSnapshotGeneration); err == nil {
		return checkpoint.RouteSnapshotGeneration, nil
	}
	// New checkpoints always carry the authenticated reporter identity. An
	// empty field is retained only for old checkpoint compatibility and still
	// requires a live tracker report, never a synthetic generation.
	if checkpoint.AppliedEdgeComponentID == "" {
		return 0, edgesnapshotusecase.ErrAppliedStateStale
	}
	return checkpoint.RouteSnapshotGeneration, nil
}

func (c *migrationTrafficChecks) TestApplicationThroughEdge(ctx context.Context) error {
	checkpoint, err := c.store.Load()
	if err != nil {
		return err
	}
	return c.probePreparedEdge(ctx, *checkpoint, c.cfg.Server.GordonDomain, "/", false)
}

func (c *migrationTrafficChecks) TestRegistryV2ThroughEdge(ctx context.Context) error {
	checkpoint, err := c.store.Load()
	if err != nil {
		return err
	}
	return c.probePreparedEdge(ctx, *checkpoint, c.cfg.Server.RegistryDomain, "/v2/", true)
}

// OldServingPathHealthy deliberately checks both the old managed container and
// its in-namespace registry /v2 listener before activating edge. The candidate
// CLI shares the old monolith namespace, while rootless host port forwarding
// does not; probing the monolith listener avoids treating NAT as serving health.
// A live listener on an unrelated process can therefore never satisfy the
// prerequisite.
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
				// This is intentionally a public, credential-free registry request.
				// The private migration credential remains scoped to the prepared
				// edge probe and cannot turn the old-path check into an authority.
				return c.probe(ctx, checkpoint.OldServingProbeEndpoint, c.cfg.Server.RegistryDomain, "/v2/", true, false)
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
func (c *migrationTrafficChecks) probe(ctx context.Context, endpoint, host, path string, registry, migrationProbe bool) error {
	if err := validLoopbackProbeEndpoint(endpoint); err != nil {
		return err
	}
	return c.probeWithClient(ctx, c.http, endpoint, host, path, registry, migrationProbe)
}

// probePreparedEdge reaches the prepared edge on its deterministic container
// identity over an already checkpointed managed app network. The old monolith
// (which runs the candidate CLI) is on that network; its loopback cannot reach
// a rootless host publish. The host-loopback listener remains only a bootstrap
// diagnostic surface and is never used to authorize switch.
func (c *migrationTrafficChecks) probePreparedEdge(ctx context.Context, checkpoint MigrationCheckpoint, host, path string, registry bool) error {
	endpoint, err := migrationPrivateEdgeProbeEndpoint(checkpoint, c.cfg.Server.Port)
	if err != nil {
		return err
	}
	name, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return fmt.Errorf("invalid prepared edge endpoint")
	}
	if c.privateProbeResolver == nil || c.privateProbeClient == nil {
		return fmt.Errorf("prepared edge probe is unavailable")
	}
	address, err := c.privateProbeResolver(ctx, name)
	if err != nil {
		return fmt.Errorf("resolve prepared edge endpoint: %w", err)
	}
	return c.probeWithClient(ctx, c.privateProbeClient(endpoint, address), endpoint, host, path, registry, true)
}

func (c *migrationTrafficChecks) probeWithClient(ctx context.Context, client *http.Client, endpoint, host, path string, registry, migrationProbe bool) error {
	if client == nil {
		return fmt.Errorf("migration probe client is unavailable")
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
	if migrationProbe {
		if c.probeToken == "" {
			return fmt.Errorf("migration probe credential is unavailable")
		}
		// Only the checkpointed bootstrap endpoint receives this header. The old
		// serving-path check remains a normal public request, so this credential
		// cannot become a general outbound HTTP capability.
		request.Header.Set(migrationProbeHeader, c.probeToken)
	}
	response, err := client.Do(request)
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

func migrationRuntimeSeed(cfg Config) string {
	if token := strings.TrimSpace(cfg.Runtime.Token); token != "" {
		return token
	}
	if envKey := strings.TrimSpace(cfg.Runtime.TokenEnv); envKey != "" {
		return strings.TrimSpace(os.Getenv(envKey))
	}
	return ""
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

// migrationPrivateEdgeProbeEndpoint derives the only DNS name the switch may
// contact. It intentionally does not trust an address from checkpoint data:
// the component identity is stable across retries and restarts, while the port
// comes from the active local configuration.
func migrationPrivateEdgeProbeEndpoint(checkpoint MigrationCheckpoint, port int) (string, error) {
	if !componentLabelValue.MatchString(checkpoint.MigrationID) || checkpoint.ComponentGeneration == 0 || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid prepared edge identity")
	}
	return net.JoinHostPort("gordon-edge-"+checkpoint.MigrationID+"-g"+strconv.FormatUint(checkpoint.ComponentGeneration, 10), strconv.Itoa(port)), nil
}

// resolveMigrationPrivateProbeAddress resolves once and accepts exactly one
// RFC1918/ULA address. Dialing that literal address prevents a DNS rebinding
// response between validation and connection from redirecting the authenticated
// probe outside the old monolith's managed network.
func resolveMigrationPrivateProbeAddress(ctx context.Context, name string) (net.IP, error) {
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", name)
	if err != nil || len(addresses) != 1 {
		return nil, fmt.Errorf("prepared edge name did not resolve uniquely")
	}
	address := net.IP(addresses[0].AsSlice())
	if err := validMigrationPrivateProbeAddress(address); err != nil {
		return nil, err
	}
	return address, nil
}

func validMigrationPrivateProbeAddress(address net.IP) error {
	if address == nil || address.IsLoopback() || !address.IsPrivate() {
		return fmt.Errorf("prepared edge address is not private")
	}
	return nil
}

func newMigrationPrivateProbeHTTPClient(endpoint string, address net.IP) *http.Client {
	_, port, err := net.SplitHostPort(endpoint)
	if err != nil || validMigrationPrivateProbeAddress(address) != nil {
		return nil
	}
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	return &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, requested string) (net.Conn, error) {
			if requested != endpoint {
				return nil, fmt.Errorf("unexpected prepared edge dial target")
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(address.String(), port))
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
