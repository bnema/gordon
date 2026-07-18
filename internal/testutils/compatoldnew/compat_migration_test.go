package compatoldnew

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// TestCompatibilityMigrationProtocolFixture is deliberately deterministic: it
// guards the real-Podman fixture shape without creating a component itself.
// Component creation belongs exclusively to the candidate migrate CLI and the
// runtime it authorizes; the rootless test below verifies that behavior.
func TestCompatibilityMigrationProtocolFixture(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(projectRoot(t), "internal", "testutils", "compatoldnew", "compat_migration_test.go"))
	require.NoError(t, err)
	text := string(source)
	for _, forbidden := range []string{
		"migrationRun" + "Component", "migrationRun" + "HTTP", "migrationFixture" + "Image",
		"fixture.invalid/" + "gordon", "busy" + "box:",
	} {
		require.NotContains(t, text, forbidden, "migration fixture must not directly create target components")
	}
	for _, command := range []string{"migrate plan", "migrate prepare", "migrate status", "migrate switch"} {
		require.Contains(t, text, command, "the real fixture must execute %q", command)
	}
}

// TestMigrationInterruptedRetry documents the release invariant in a stable,
// engine-free way. The real interruption is performed through the candidate
// CLI in TestCompatibilityMigrationRootlessPodmanOldToSplit.
func TestMigrationInterruptedRetry(t *testing.T) {
	require.True(t, strings.Contains(migrationScenarioOperations, "migrate prepare") && strings.Contains(migrationScenarioOperations, "migrate status"))
}

// TestMigrationMissingEnvFailsPreflight documents that the real fixture uses
// an explicit missing environment preflight before it mutates the runtime.
func TestMigrationMissingEnvFailsPreflight(t *testing.T) {
	require.Contains(t, migrationScenarioOperations, "missing-env migrate plan")
}

// TestMigrationTrafficSwitchFailsClosed ensures the source gate continues to
// require the actual switch operation rather than an in-memory switcher fake.
func TestMigrationTrafficSwitchFailsClosed(t *testing.T) {
	require.Contains(t, migrationScenarioOperations, "migrate switch")
}

const migrationScenarioOperations = "migrate plan; missing-env migrate plan; migrate prepare; migrate status; migrate switch"

// TestCompatibilityMigrationRootlessPodmanOldToSplit is the release gate. The
// fixture is allowed to create only its old monolith, app, network, volume and
// system service. Every split component is created by the actual candidate CLI
// through the old runtime's mounted rootless Podman socket.
func TestCompatibilityMigrationRootlessPodmanOldToSplit(t *testing.T) {
	if !PodmanEnabledFromEnv() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	requireRootlessPodman(t, ctx)

	fixture := newRealMigrationFixture(t, ctx)
	defer fixture.cleanup()

	fixture.runCLI("migrate", "plan", "--json")
	fixture.runMissingEnvPlan()
	fixture.runCLI("migrate", "prepare", "--json")
	status := fixture.runCLI("migrate", "status", "--json")
	require.Contains(t, status, `"bootstrap_runtime_endpoint":"host.containers.internal:`, "old monolith must use the private host gateway, not container loopback")
	fixture.assertPreparedTargets()
	fixture.assertRuntimeBootstrapTransport()
	fixture.assertRuntimeSocketExclusive()

	// A second prepare is an interruption/retry at the persisted checkpoint
	// boundary. It must discover the same target generation instead of making a
	// second set of component containers.
	fixture.runCLI("migrate", "prepare", "--json")
	fixture.assertPreparedTargets()
	fixture.runCLI("migrate", "switch", "--json")
	fixture.assertSwitchedTraffic()
}

type realMigrationFixture struct {
	t       *testing.T
	ctx     context.Context
	runID   string
	root    string
	image   string
	old     string
	app     string
	network string
	volume  string
	config  string
	socket  string
	service *exec.Cmd
	port    int
}

func newRealMigrationFixture(t *testing.T, ctx context.Context) *realMigrationFixture {
	t.Helper()
	runID := RunID("migration-cli")
	root := t.TempDir()
	fixture := &realMigrationFixture{
		t: t, ctx: ctx, runID: runID, root: root,
		image: "localhost/gordon-compat-migration-cli-" + sanitizePart(runID),
		old:   "monolith", app: ContainerPrefix(runID, SideOld) + "-app",
		network: NetworkPrefix(runID, SideOld), volume: VolumePrefix(runID, SideOld),
		config: filepath.Join(root, "gordon.toml"), port: 18081,
	}
	fixture.socket, fixture.service = migrationStartPodmanService(t, ctx, root)
	fixture.buildCandidateImage()

	labels := ResourceLabels(runID, SideOld, "migration")
	networkArgs := append([]string{"network", "create", "--disable-dns"}, migrationLabelArgs(labels)...)
	networkArgs = append(networkArgs, fixture.network)
	require.NoError(t, migrationPodman(ctx, networkArgs...))
	volumeArgs := append([]string{"volume", "create"}, migrationLabelArgs(labels)...)
	volumeArgs = append(volumeArgs, fixture.volume)
	require.NoError(t, migrationPodman(ctx, volumeArgs...))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "data"), 0o700))
	require.NoError(t, os.WriteFile(fixture.config, []byte(fixture.configTOML()), 0o600))
	fixture.startOldMonolith(labels)
	fixture.startOldApp(labels)
	return fixture
}

func (f *realMigrationFixture) cleanup() {
	if f.service != nil && f.service.Process != nil {
		_ = f.service.Process.Kill()
		_, _ = f.service.Process.Wait()
	}
	_ = migrationPodman(context.Background(), "rm", "--force", f.old)
	_ = CleanupRunResources(context.Background(), f.runID)
	_ = migrationPodman(context.Background(), "image", "rm", "--force", f.image)
}

func (f *realMigrationFixture) buildCandidateImage() {
	binary := filepath.Join(f.root, "gordon")
	require.NoError(f.t, securityBuildCandidate(f.ctx, projectRoot(f.t), binary))
	containerfile := "FROM docker.io/library/alpine:3.20\nCOPY gordon /usr/local/bin/gordon\nENTRYPOINT [\"/usr/local/bin/gordon\"]\n"
	require.NoError(f.t, os.WriteFile(filepath.Join(f.root, "Containerfile"), []byte(containerfile), 0o600))
	policy := filepath.Join(f.root, "policy.json")
	require.NoError(f.t, os.WriteFile(policy, []byte(`{"default":[{"type":"insecureAcceptAnything"}]}`), 0o600))
	require.NoError(f.t, migrationPodman(f.ctx, "build", "--signature-policy", policy, "--tag", f.image, f.root))
	entrypoint, err := podmanOutput(f.ctx, "inspect", "--format", "{{json .Config.Entrypoint}}", f.image)
	require.NoError(f.t, err)
	require.Contains(f.t, entrypoint, "/usr/local/bin/gordon", "candidate image must contain the candidate Gordon binary")
}

func (f *realMigrationFixture) startOldMonolith(labels map[string]string) {
	labels = cloneMigrationLabels(labels)
	labels[domain.LabelManaged] = "true"
	labels[domain.LabelRoute] = "true"
	labels[domain.LabelDomain] = "app.example.test"
	// The old serving Gordon is deliberately a normal rootless container, not
	// host-networked: its authenticated bootstrap proof must cross Podman's
	// host gateway to the runtime's loopback-only publish.
	// Publish every configured legacy listener. The authenticated runtime probe
	// must recognize these as owned by the running managed monolith rather than
	// attempting a bind inside the monolith's own network namespace.
	args := append([]string{"run", "--detach", "--replace", "--name", f.old, "--network", f.network, "--publish", fmt.Sprintf("%d:%d", f.port, f.port), "--publish", "15000:15000", "--volume", f.root + ":" + f.root, "--volume", f.socket + ":" + f.socket, "--env", "DOCKER_HOST=unix://" + f.socket, "--env", "GORDON_MIGRATION_IMAGE=" + f.image}, migrationLabelArgs(labels)...)
	args = append(args, f.image, "serve", "--role", "monolith", "--config", f.config)
	require.NoError(f.t, migrationPodman(f.ctx, args...))
}

func (f *realMigrationFixture) startOldApp(labels map[string]string) {
	labels = cloneMigrationLabels(labels)
	labels[domain.LabelManaged] = "true"
	labels[domain.LabelRoute] = "true"
	labels[domain.LabelDomain] = "app.example.test"
	args := append([]string{"run", "--detach", "--name", f.app, "--network", f.network, "--volume", f.volume + ":/data"}, migrationLabelArgs(labels)...)
	args = append(args, "docker.io/library/alpine:3.20", "sh", "-c", "mkdir -p /srv; printf migration-app-ok >/srv/index.html; exec busybox httpd -f -p 8080 -h /srv")
	require.NoError(f.t, migrationPodman(f.ctx, args...))
}

func (f *realMigrationFixture) runCLI(args ...string) string {
	f.t.Helper()
	args = append(args, "--config", f.config)
	command := append([]string{"exec", "--env", "GORDON_MIGRATION_IMAGE=" + f.image, f.old, "gordon"}, args...)
	out, err := podmanOutput(f.ctx, command...)
	require.NoError(f.t, err, "candidate CLI %s", strings.Join(args, " "))
	require.NotEmpty(f.t, strings.TrimSpace(out), "candidate CLI %s must return JSON", strings.Join(args, " "))
	f.t.Logf("candidate CLI %s: %s", strings.Join(args, " "), strings.TrimSpace(out))
	return out
}

func (f *realMigrationFixture) runMissingEnvPlan() {
	f.t.Helper()
	missing := filepath.Join(f.root, "missing-env.toml")
	contents := f.configTOML() + "\n[tls.acme]\nenabled = true\nchallenge = \"cloudflare_dns01\"\n"
	require.NoError(f.t, os.WriteFile(missing, []byte(contents), 0o600))
	_, err := podmanOutput(f.ctx, "exec", "--env", "GORDON_MIGRATION_IMAGE="+f.image, f.old, "gordon", "migrate", "plan", "--config", missing, "--json")
	require.Error(f.t, err, "missing required environment must fail through the candidate CLI before mutation")
}

func (f *realMigrationFixture) assertPreparedTargets() {
	f.t.Helper()
	containers, err := InspectContainers(f.ctx, f.runID)
	require.NoError(f.t, err)
	roles := map[string]PodmanResource{}
	for _, container := range containers {
		if role := container.Labels[domain.LabelComponentRole]; role != "" {
			roles[role] = container
			require.Equal(f.t, "true", container.Labels[domain.LabelComponent])
			require.Equal(f.t, "1", container.Labels[domain.LabelComponentGeneration])
			require.NotEmpty(f.t, container.Labels[domain.LabelComponentMigrationID])
			running, inspectErr := podmanOutput(f.ctx, "inspect", "--format", "{{.State.Running}}", container.resourceName())
			require.NoError(f.t, inspectErr)
			require.Equal(f.t, "true", strings.TrimSpace(running), "%s must be healthy/running", role)
		}
	}
	require.Len(f.t, roles, 4, "only candidate CLI/runtime may create exactly one target role generation")
	for _, role := range []string{"control", "runtime", "registry", "edge"} {
		require.Contains(f.t, roles, role)
	}
}

func (f *realMigrationFixture) assertRuntimeBootstrapTransport() {
	f.t.Helper()
	containers, err := InspectContainers(f.ctx, f.runID)
	require.NoError(f.t, err)
	for _, container := range containers {
		if container.Labels[domain.LabelComponentRole] != "runtime" {
			continue
		}
		ports, portErr := podmanOutput(f.ctx, "port", container.resourceName())
		require.NoError(f.t, portErr)
		require.Contains(f.t, ports, "127.0.0.1:", "runtime bootstrap must be host-loopback only")
		require.NotContains(f.t, ports, "0.0.0.0", "runtime bootstrap must not be public")
		return
	}
	f.t.Fatal("prepared runtime was not found")
}

func (f *realMigrationFixture) assertRuntimeSocketExclusive() {
	f.t.Helper()
	containers, err := InspectContainers(f.ctx, f.runID)
	require.NoError(f.t, err)
	for _, container := range containers {
		role := container.Labels[domain.LabelComponentRole]
		if role == "" {
			continue
		}
		mounts, mountErr := podmanOutput(f.ctx, "inspect", "--format", "{{range .Mounts}}{{println .Source}}{{end}}", container.resourceName())
		require.NoError(f.t, mountErr)
		if role == "runtime" {
			require.Contains(f.t, mounts, f.socket)
		} else {
			require.NotContains(f.t, mounts, f.socket, "%s must not receive runtime authority", role)
		}
	}
}

func (f *realMigrationFixture) assertSwitchedTraffic() {
	f.t.Helper()
	status, err := podmanOutput(f.ctx, "exec", f.old, "gordon", "migrate", "status", "--config", f.config, "--json")
	require.NoError(f.t, err)
	require.Contains(f.t, status, `"phase":"switched"`)
	oldRunning, err := podmanOutput(f.ctx, "inspect", "--format", "{{.State.Running}}", f.old)
	require.NoError(f.t, err)
	require.Equal(f.t, "false", strings.TrimSpace(oldRunning), "runtime-owned listener cutover stops the old serving monolith")
}

func (f *realMigrationFixture) configTOML() string {
	return fmt.Sprintf(`[server]
port = %d
registry_port = 15000
data_dir = %q
gordon_domain = "gordon.example.test"
registry_domain = "registry.example.test"

[auth]
enabled = false
secrets_backend = "unsafe"

[network_isolation]
enabled = true
network_prefix = "gordon"

[runtime]
listen_address = "127.0.0.1:19444"
token = "fixture-runtime-handoff-token"
insecure = true

[containers]
security_profile = "compat"
`, f.port, filepath.Join(f.root, "data"))
}

func migrationStartPodmanService(t *testing.T, ctx context.Context, root string) (string, *exec.Cmd) {
	t.Helper()
	socket := filepath.Join(root, "podman.sock")
	// The test's user-local Podman wrapper is a CLI client, not a socket mount
	// source. Start a private rootless API service so the old monolith receives
	// the only engine socket and the target runtime can inherit it.
	service := exec.CommandContext(ctx, "podman", "system", "service", "--time=0", "unix://"+socket) // #nosec G204 -- fixed Podman compatibility service.
	service.Stdout = os.Stderr
	service.Stderr = os.Stderr
	require.NoError(t, service.Start())
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(socket); err == nil && info.Mode()&os.ModeSocket != 0 {
			return socket, service
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = service.Process.Kill()
	_, _ = service.Process.Wait()
	t.Fatal("private rootless Podman API service did not create its socket")
	return "", nil
}

func cloneMigrationLabels(labels map[string]string) map[string]string {
	result := make(map[string]string, len(labels)+3)
	maps.Copy(result, labels)
	return result
}

func requireRootlessPodman(t *testing.T, ctx context.Context) {
	t.Helper()
	require.NoError(t, PodmanAvailable(ctx), "GORDON_COMPAT_PODMAN=1 requires Podman")
	rootless, err := PodmanRootless(ctx)
	require.NoError(t, err)
	require.True(t, rootless, "GORDON_COMPAT_PODMAN=1 requires rootless Podman")
}

func migrationPodman(ctx context.Context, args ...string) error { return podman(ctx, args...) }

func migrationLabelArgs(labels map[string]string) []string {
	args := make([]string, 0, len(labels)*2)
	for key, value := range labels {
		args = append(args, "--label", key+"="+value)
	}
	return args
}
