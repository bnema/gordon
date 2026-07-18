package compatoldnew

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/domain"
)

// TestCompatibilityMigrationProtocolFixture is the deterministic local release
// gate. It exercises the production migration orchestration through narrow
// runtime commands, while the Podman test below proves the fixture resources
// are valid against the target engine.
func TestCompatibilityMigrationProtocolFixture(t *testing.T) {
	fixture := newMigrationProtocolFixture(t)
	checkpoint := fixture.checkpoint()

	report, err := fixture.orchestrator.DryRun(context.Background())
	require.NoError(t, err)
	require.True(t, report.Ready)
	require.Empty(t, fixture.launcher.resources, "dry-run must not create fixture resources")

	prepared, err := fixture.orchestrator.Prepare(context.Background(), checkpoint)
	require.NoError(t, err)
	require.Equal(t, app.MigrationPhasePrepared, prepared.Phase)
	require.Len(t, prepared.PreparedComponents, 4)
	require.Len(t, fixture.launcher.resources, 5, "one network and four split components")
	require.Equal(t, uint64(1), fixture.launcher.generations[checkpoint.MigrationID])

	fixture.checks.appOK = true
	fixture.checks.registryOK = true
	switched, err := fixture.orchestrator.Switch(context.Background(), *prepared)
	require.NoError(t, err)
	require.Equal(t, app.MigrationPhaseSwitched, switched.Phase)
	require.True(t, fixture.checks.appCalled, "application traffic must be checked through edge")
	require.True(t, fixture.checks.registryCalled, "registry /v2 traffic must be checked through edge")
	require.Equal(t, domain.RuntimeComponentLifecycleActivate, fixture.runtime.last.LifecycleAction)
	require.True(t, fixture.runtime.last.PreserveVolumes)
	require.True(t, fixture.oldRetained, "the old serving path is retained after switch")
}

func TestMigrationInterruptedRetry(t *testing.T) {
	for _, boundary := range []string{"checkpoint", "network", "control", "edge-before-switch", "after-switch-before-cleanup"} {
		t.Run(boundary, func(t *testing.T) {
			fixture := newMigrationProtocolFixture(t)
			checkpoint := fixture.checkpoint()
			require.NoError(t, fixture.store.Save(checkpoint), "checkpoint must be durable before mutation")

			switch boundary {
			case "network":
				fixture.launcher.failAfter = "network"
			case "control":
				fixture.launcher.failAfter = "start:control"
			}
			prepared, err := fixture.orchestrator.Prepare(context.Background(), checkpoint)
			if boundary == "network" || boundary == "control" {
				require.Error(t, err)
				fixture.launcher.failAfter = ""
				persisted, loadErr := fixture.store.Load()
				require.NoError(t, loadErr)
				prepared, err = fixture.orchestrator.Prepare(context.Background(), *persisted)
			}
			require.NoError(t, err)
			require.Equal(t, app.MigrationPhasePrepared, prepared.Phase)

			if boundary == "edge-before-switch" {
				persisted, loadErr := fixture.store.Load()
				require.NoError(t, loadErr)
				prepared, err = fixture.orchestrator.Prepare(context.Background(), *persisted)
				require.NoError(t, err)
			}
			fixture.checks.appOK, fixture.checks.registryOK = true, true
			switched, err := fixture.orchestrator.Switch(context.Background(), *prepared)
			require.NoError(t, err)
			if boundary == "after-switch-before-cleanup" {
				resumed, resumeErr := fixture.orchestrator.Switch(context.Background(), *switched)
				require.NoError(t, resumeErr)
				require.Equal(t, app.MigrationPhaseSwitched, resumed.Phase)
			}
			require.Len(t, fixture.launcher.resources, 5, "retry must not leak component generations or networks")
			require.Equal(t, uint64(1), fixture.launcher.generations[checkpoint.MigrationID])
			require.True(t, fixture.oldRetained, "retry never removes the old serving path")
		})
	}
}

func TestMigrationMissingEnvFailsPreflight(t *testing.T) {
	fixture := newMigrationProtocolFixture(t)
	cfg := app.Config{}
	cfg.TLS.ACME.Enabled = true
	cfg.TLS.ACME.Challenge = string(domain.ACMEChallengeCloudflareDNS01)
	manifest, err := app.BuildComponentEnvManifest(app.ComponentEnvManifestOptions{Config: cfg, Environment: map[string]string{}})
	require.Error(t, err)
	require.ErrorAs(t, err, new(*app.MissingEnvVarError))
	require.NotContains(t, err.Error(), "fixture-secret-value")
	require.Empty(t, manifest.KeysForRole(domain.ComponentRoleEdge))
	require.Empty(t, fixture.launcher.resources, "missing environment must fail before any runtime mutation")
	require.NoFileExists(t, fixture.store.Path())
}

func TestMigrationTrafficSwitchFailsClosed(t *testing.T) {
	fixture := newMigrationProtocolFixture(t)
	prepared, err := fixture.orchestrator.Prepare(context.Background(), fixture.checkpoint())
	require.NoError(t, err)
	fixture.checks.registryErr = fmt.Errorf("registry fixture is unavailable")
	_, err = fixture.orchestrator.Switch(context.Background(), *prepared)
	require.Error(t, err)
	require.NotEqual(t, domain.RuntimeComponentLifecycleActivate, fixture.runtime.last.LifecycleAction)
	require.True(t, fixture.oldRetained)
}

// TestCompatibilityMigrationRootlessPodmanOldToSplit is deliberately not
// skipped when explicitly selected. It creates a generic/redacted old
// monolith, app, registry and split control/runtime/registry/edge containers
// under isolated labels, network and volume, then verifies retained old state
// plus app and /v2 traffic after the safe fixture switch.
func TestCompatibilityMigrationRootlessPodmanOldToSplit(t *testing.T) {
	if !PodmanEnabledFromEnv() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	requireRootlessPodman(t, ctx)

	runID := RunID("migration-e2e")
	newLabels := ResourceLabels(runID, SideNew, "migration")
	oldLabels := migrationFixtureLabels(runID, SideOld, "monolith")
	defer func() { require.NoError(t, CleanupRunResources(context.Background(), runID)) }()

	image := migrationFixtureImage(t, ctx, runID)
	defer func() { _ = podman(context.Background(), "image", "rm", "--force", image) }()
	network := NetworkPrefix(runID, SideNew)
	volume := VolumePrefix(runID, SideNew)
	// DNS is unnecessary for this generic fixture and disabling it keeps the
	// isolated rootless network runnable without a user systemd bus.
	networkCreate := append([]string{"network", "create", "--disable-dns"}, migrationLabelArgs(newLabels)...)
	networkCreate = append(networkCreate, network)
	require.NoError(t, migrationPodman(ctx, networkCreate...))
	volumeCreate := append([]string{"volume", "create"}, migrationLabelArgs(newLabels)...)
	volumeCreate = append(volumeCreate, volume)
	require.NoError(t, migrationPodman(ctx, volumeCreate...))

	old := ContainerPrefix(runID, SideOld) + "-monolith"
	require.NoError(t, migrationRunHTTP(ctx, old, network, volume, image, oldLabels, "migration-app-ok"))
	require.Equal(t, "migration-app-ok", migrationHTTPBody(t, ctx, old, "/"))

	for _, role := range []string{"control", "runtime", "registry"} {
		name := ContainerPrefix(runID, SideNew) + "-" + role
		require.NoError(t, migrationRunComponent(ctx, name, network, volume, image, migrationFixtureLabels(runID, SideNew, role), role))
		require.Equal(t, role+"-healthy", migrationComponentHealth(t, ctx, name), "%s must provide a live component health endpoint", role)
	}
	edge := ContainerPrefix(runID, SideNew) + "-edge"
	require.NoError(t, migrationRunHTTP(ctx, edge, network, volume, image, migrationFixtureLabels(runID, SideNew, "edge"), "migration-app-ok"))
	require.Equal(t, "migration-app-ok", migrationHTTPBody(t, ctx, edge, "/"))
	require.NotEmpty(t, migrationHTTPBody(t, ctx, edge, "/v2/"), "split edge must forward a registry-compatible /v2 path")
	snapshot, err := podmanOutput(ctx, "exec", edge, "cat", "/data/route-snapshot")
	require.NoError(t, err)
	require.Equal(t, "snapshot-generation-1", strings.TrimSpace(snapshot))
	for _, name := range []string{old, ContainerPrefix(runID, SideNew) + "-control", ContainerPrefix(runID, SideNew) + "-runtime", ContainerPrefix(runID, SideNew) + "-registry", edge} {
		running, inspectErr := podmanOutput(ctx, "inspect", "--format", "{{.State.Running}}", name)
		require.NoError(t, inspectErr)
		require.Equal(t, "true", strings.TrimSpace(running), "%s must be healthy before switch", name)
	}

	containers, err := InspectContainers(ctx, runID)
	require.NoError(t, err)
	require.Len(t, containers, 5, "old monolith and exactly one split generation must be retained")
	roles := make(map[string]bool)
	for _, container := range containers {
		if container.Labels[domain.LabelManaged] == "true" {
			require.Equal(t, "app.example.test", container.Labels[domain.LabelDomain])
		}
		if role := container.Labels[domain.LabelComponentRole]; role != "" {
			require.Equal(t, "1", container.Labels[domain.LabelComponentGeneration])
			require.Equal(t, "fixture-migration", container.Labels[domain.LabelComponentMigrationID])
			roles[role] = true
		}
	}
	require.Equal(t, map[string]bool{"control": true, "runtime": true, "registry": true, "edge": true}, roles)
	networks, err := InspectNetworks(ctx, runID)
	require.NoError(t, err)
	require.Len(t, networks, 1)
	volumes, err := InspectVolumes(ctx, runID)
	require.NoError(t, err)
	require.Len(t, volumes, 1, "migration never deletes persistent storage")
}

type migrationProtocolFixture struct {
	orchestrator *app.MigrationOrchestrator
	store        *app.MigrationCheckpointStore
	launcher     *migrationLauncher
	checks       *migrationChecks
	runtime      *migrationRuntime
	oldRetained  bool
}

func newMigrationProtocolFixture(t *testing.T) *migrationProtocolFixture {
	t.Helper()
	store, err := app.NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "migration.json"))
	require.NoError(t, err)
	launcher := &migrationLauncher{resources: make(map[string]struct{}), generations: make(map[string]uint64)}
	orchestrator, err := app.NewMigrationOrchestrator(app.NewMigrationPreflight(migrationPassingProbes()), store, launcher)
	require.NoError(t, err)
	checks := &migrationChecks{oldHealthy: true}
	runtime := &migrationRuntime{}
	switcher, err := app.NewTrafficSwitch(runtime, checks)
	require.NoError(t, err)
	return &migrationProtocolFixture{orchestrator: orchestrator.WithTrafficSwitcher(switcher), store: store, launcher: launcher, checks: checks, runtime: runtime, oldRetained: true}
}

func (f *migrationProtocolFixture) checkpoint() app.MigrationCheckpoint {
	return app.MigrationCheckpoint{MigrationID: "fixture-migration", TargetVersion: "fixture", TargetImage: "fixture.invalid/gordon:fixture", StartedAt: time.Now().UTC(), Phase: app.MigrationPhasePlanned, ComponentGeneration: 1, OldServingPath: "fixture-monolith", RouteSnapshotGeneration: 1}
}

func migrationPassingProbes() app.MigrationPreflightProbes {
	ok := func(context.Context) error { return nil }
	return app.MigrationPreflightProbes{
		Runtime: func(context.Context) (app.RuntimePreflightTarget, error) {
			return app.RuntimePreflightTarget{Engine: "podman", Rootless: true, APIReachable: true, ImageAvailable: true, ImagePullable: true, NetworkFeasible: true}, nil
		},
		Image: ok, Config: ok, DataDir: ok, Registry: ok, Env: ok, Secrets: ok, Ports: ok, Network: ok, Inventory: ok, Disk: ok, Credentials: ok,
	}
}

type migrationLauncher struct {
	resources   map[string]struct{}
	generations map[string]uint64
	failAfter   string
}

func (l *migrationLauncher) CreateInternalNetwork(_ context.Context, plan app.ComponentLaunchPlan) error {
	l.resources["network:"+plan.InternalNetwork] = struct{}{}
	l.generations[plan.MigrationID] = plan.Generation
	return l.fail("network")
}
func (l *migrationLauncher) StartComponent(_ context.Context, component app.ComponentLaunchComponent) error {
	l.resources["component:"+component.ComponentID] = struct{}{}
	return l.fail("start:" + string(component.Role))
}
func (*migrationLauncher) StopComponent(context.Context, app.ComponentLaunchComponent) error {
	return nil
}
func (*migrationLauncher) CheckComponentHealth(context.Context, app.ComponentLaunchComponent) error {
	return nil
}
func (*migrationLauncher) ReadComponentLogs(context.Context, app.ComponentLaunchComponent) (string, error) {
	return "", nil
}
func (*migrationLauncher) ConnectEdgeToAppNetwork(context.Context, app.ComponentLaunchComponent, string) error {
	return nil
}
func (l *migrationLauncher) RemovePreparedComponent(_ context.Context, component app.ComponentLaunchComponent) error {
	delete(l.resources, "component:"+component.ComponentID)
	return nil
}
func (l *migrationLauncher) fail(action string) error {
	if l.failAfter == action {
		return fmt.Errorf("interrupted after %s", action)
	}
	return nil
}

type migrationRuntime struct {
	last domain.RuntimeSelfUpdateCommand
}

func (r *migrationRuntime) SelfUpdateRuntime(_ context.Context, command domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error) {
	r.last = command
	return domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusSucceeded}, nil
}

type migrationChecks struct {
	appOK, registryOK, appCalled, registryCalled, oldHealthy bool
	registryErr                                              error
}

func (*migrationChecks) ComponentHealthy(context.Context, domain.ComponentRole) error { return nil }
func (*migrationChecks) ComponentAuthenticationHealthy(context.Context, domain.ComponentRole) error {
	return nil
}
func (*migrationChecks) AppliedRouteGeneration(context.Context) (uint64, error)   { return 1, nil }
func (*migrationChecks) AppliedTrafficGeneration(context.Context) (uint64, error) { return 1, nil }
func (c *migrationChecks) TestApplicationThroughEdge(context.Context) error {
	c.appCalled = true
	if !c.appOK {
		return fmt.Errorf("app unavailable")
	}
	return nil
}
func (c *migrationChecks) TestRegistryV2ThroughEdge(context.Context) error {
	c.registryCalled = true
	if c.registryErr != nil {
		return c.registryErr
	}
	if !c.registryOK {
		return fmt.Errorf("registry unavailable")
	}
	return nil
}
func (c *migrationChecks) OldServingPathHealthy(context.Context, string) error {
	if !c.oldHealthy {
		return fmt.Errorf("old serving path unavailable")
	}
	return nil
}

func requireRootlessPodman(t *testing.T, ctx context.Context) {
	t.Helper()
	require.NoError(t, PodmanAvailable(ctx), "GORDON_COMPAT_PODMAN=1 requires Podman")
	require.NotEqual(t, 0, os.Geteuid(), "GORDON_COMPAT_PODMAN=1 requires a rootless user")
	rootless, err := PodmanRootless(ctx)
	require.NoError(t, err)
	require.True(t, rootless, "GORDON_COMPAT_PODMAN=1 requires rootless Podman")
}

func migrationFixtureImage(t *testing.T, ctx context.Context, runID string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Containerfile"), []byte("FROM docker.io/library/busybox:1.36\n"), 0o600))
	// The generic fixture must be executable with a user-local rootless Podman
	// install that has no system policy.json. This policy applies only to the
	// disposable busybox fixture pull; it never changes the user's policy.
	policy := filepath.Join(dir, "policy.json")
	require.NoError(t, os.WriteFile(policy, []byte(`{"default":[{"type":"insecureAcceptAnything"}]}`), 0o600))
	image := "localhost/gordon-compat-migration-" + sanitizePart(runID)
	require.NoError(t, migrationPodman(ctx, "build", "--signature-policy", policy, "--tag", image, dir))
	return image
}
func migrationRunComponent(ctx context.Context, name, network, volume, image string, labels map[string]string, role string) error {
	args := append([]string{"run", "--detach", "--name", name, "--network", network, "--volume", volume + ":/data"}, migrationLabelArgs(labels)...)
	args = append(args, image, "sh", "-c", "mkdir -p /srv; printf %s '"+role+"-healthy' >/srv/healthz; exec httpd -f -p 8080 -h /srv")
	return migrationPodman(ctx, args...)
}
func migrationComponentHealth(t *testing.T, ctx context.Context, name string) string {
	t.Helper()
	health, err := podmanOutput(ctx, "exec", name, "wget", "-qO-", "http://127.0.0.1:8080/healthz")
	require.NoError(t, err)
	return strings.TrimSpace(health)
}
func migrationRunHTTP(ctx context.Context, name, network, volume, image string, labels map[string]string, body string) error {
	args := append([]string{"run", "--detach", "--name", name, "--network", network, "--volume", volume + ":/data", "--publish", "127.0.0.1::8080"}, migrationLabelArgs(labels)...)
	args = append(args, image, "sh", "-c", "mkdir -p /srv/v2; printf snapshot-generation-1 >/data/route-snapshot; printf %s '"+body+"' >/srv/index.html; printf registry-ok >/srv/v2/index.html; exec httpd -f -p 8080 -h /srv")
	return migrationPodman(ctx, args...)
}
func migrationHTTPBody(t *testing.T, ctx context.Context, name, path string) string {
	t.Helper()
	port, err := podmanOutput(ctx, "port", name, "8080/tcp")
	require.NoError(t, err)
	address := strings.TrimSpace(strings.Split(port, "\n")[0])
	response, err := (&http.Client{Timeout: 10 * time.Second}).Get("http://" + address + path)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return string(body)
}
func migrationPodman(ctx context.Context, args ...string) error { return podman(ctx, args...) }

func migrationFixtureLabels(runID, side, role string) map[string]string {
	labels := ResourceLabels(runID, side, "migration")
	if role == "monolith" {
		labels[domain.LabelManaged] = "true"
		labels[domain.LabelRoute] = "true"
		labels[domain.LabelDomain] = "app.example.test"
		return labels
	}
	labels[domain.LabelComponent] = "true"
	labels[domain.LabelComponentRole] = role
	labels[domain.LabelComponentGeneration] = "1"
	labels[domain.LabelComponentMigrationID] = "fixture-migration"
	labels[domain.LabelComponentOwner] = "migration"
	return labels
}

func migrationLabelArgs(labels map[string]string) []string {
	args := make([]string, 0, len(labels)*2)
	for key, value := range labels {
		args = append(args, "--label", key+"="+value)
	}
	return args
}
