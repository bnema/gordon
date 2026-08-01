package compatoldnew

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

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
	cleanupMigrationCandidates(ctx)
	runID := RunID("migration-cli")
	// Keep a failed fixture only on explicit request so rootless Podman state can
	// be inspected without t.TempDir removing the mounted checkpoint first.
	root, err := os.MkdirTemp("", "gordon-compat-migration-")
	require.NoError(t, err)
	fixture := &realMigrationFixture{
		t: t, ctx: ctx, runID: runID, root: root,
		image:   "localhost/gordon-compat-migration-cli-" + sanitizePart(runID),
		old:     ContainerPrefix(runID, SideOld) + "-monolith",
		app:     ContainerPrefix(runID, SideOld) + "-app",
		network: NetworkPrefix(runID, SideOld), volume: VolumePrefix(runID, SideOld),
		config: filepath.Join(root, "gordon.toml"), port: 18081,
	}
	fixture.socket, fixture.service = migrationStartPodmanService(t, ctx, root)
	fixture.buildCandidateImage()

	labels := ResourceLabels(runID, SideOld, "migration")
	networkLabels := cloneMigrationLabels(labels)
	networkLabels[domain.LabelManaged] = "true"
	networkArgs := append([]string{"network", "create"}, migrationLabelArgs(networkLabels)...)
	networkArgs = append(networkArgs, fixture.network)
	require.NoError(t, migrationPodman(ctx, networkArgs...))
	volumeArgs := append([]string{"volume", "create"}, migrationLabelArgs(labels)...)
	volumeArgs = append(volumeArgs, fixture.volume)
	require.NoError(t, migrationPodman(ctx, volumeArgs...))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "data"), 0o700))
	require.NoError(t, os.WriteFile(fixture.config, []byte(fixture.configTOML()), 0o600))
	// Start the managed app after the old monolith's one-time recovery cleanup.
	// This models an already running managed workload without letting startup
	// cleanup stop an intentionally direct compatibility fixture.
	fixture.startOldMonolith(labels)
	fixture.startOldApp(labels)
	fixture.waitOldAppReady()
	fixture.waitOldRegistryReady()
	fixture.pushRegistryArtifact()
	return fixture
}

func (f *realMigrationFixture) cleanup() {
	if f.t.Failed() {
		f.t.Logf("migration failure diagnostics (container state and Gordon component logs only):\n%s", f.failureDiagnostics())
	}
	if f.service != nil && f.service.Process != nil {
		_ = f.service.Process.Kill()
		_, _ = f.service.Process.Wait()
	}
	if os.Getenv("GORDON_COMPAT_KEEP_FAILURE") == "1" && f.t.Failed() {
		f.t.Logf("preserving failed migration fixture at %s", f.root)
		return
	}
	_ = migrationPodman(context.Background(), "rm", "--force", f.old)
	cleanupMigrationCandidates(context.Background())
	_ = CleanupRunResources(context.Background(), f.runID)
	_ = migrationPodman(context.Background(), "image", "rm", "--force", f.image)
	_ = os.RemoveAll(f.root)
}

var managedPassSecretVolumePattern = regexp.MustCompile(`^gordon-control-secrets-[0-9a-f]{16}$`)

var migrationComponentRoles = []domain.ComponentRole{
	domain.ComponentRoleControl,
	domain.ComponentRoleRuntime,
	domain.ComponentRoleEdge,
	domain.ComponentRoleRegistry,
}

func migrationComponentName(role domain.ComponentRole) string {
	return domain.FormatComponentID(role, "migration", 1)
}

func migrationInternalNetworkName() string {
	return domain.FormatComponentInternalNetwork("migration", 1)
}

func leakedManagedPassSecretVolumes(volumeListing string) []string {
	var leaked []string
	for _, line := range strings.FieldsFunc(volumeListing, func(r rune) bool { return r == '\n' }) {
		name := strings.TrimSpace(line)
		if managedPassSecretVolumePattern.MatchString(name) {
			leaked = append(leaked, name)
		}
	}
	return leaked
}

func managedPassSecretVolumeNames(ctx context.Context) (string, error) {
	return podmanOutput(ctx, "volume", "ls", "--format", "{{.Name}}")
}

func (f *realMigrationFixture) assertCleaned() {
	f.t.Helper()
	names := []string{f.old, f.app}
	for _, role := range migrationComponentRoles {
		names = append(names, migrationComponentName(role))
	}
	for _, name := range names {
		_, err := podmanOutput(context.Background(), "container", "inspect", name)
		require.Error(f.t, err, "%s must be removed by fixture cleanup", name)
	}
	for _, name := range []string{f.network, migrationInternalNetworkName()} {
		_, err := podmanOutput(context.Background(), "network", "inspect", name)
		require.Error(f.t, err, "%s must be removed by fixture cleanup", name)
	}
	for _, name := range []string{f.volume, migrationComponentName(domain.ComponentRoleControl), migrationComponentName(domain.ComponentRoleRuntime), migrationComponentName(domain.ComponentRoleRegistry)} {
		_, err := podmanOutput(context.Background(), "volume", "inspect", name)
		require.Error(f.t, err, "%s must be removed by fixture cleanup", name)
	}
	_, err := podmanOutput(context.Background(), "image", "inspect", f.image)
	require.Error(f.t, err, "candidate image must be removed by fixture cleanup")
	_, err = os.Stat(f.root)
	require.ErrorIs(f.t, err, os.ErrNotExist, "fixture root and private API socket must be removed")
	listing, err := managedPassSecretVolumeNames(context.Background())
	require.NoError(f.t, err, "managed pass secret volume listing must succeed before asserting cleanup")
	for _, name := range leakedManagedPassSecretVolumes(listing) {
		require.Fail(f.t, "managed pass secret volume must be removed by fixture cleanup", "leaked volume %s", name)
	}
}

func (f *realMigrationFixture) failureDiagnostics() string {
	containers, err := podmanOutput(f.ctx, "ps", "--all", "--format", "{{.Names}} {{.Status}}")
	if err != nil {
		return "list candidate containers: " + err.Error()
	}
	var diagnostics []string
	for _, line := range strings.FieldsFunc(containers, func(r rune) bool { return r == '\n' }) {
		name, _, found := strings.Cut(line, " ")
		if !found || strings.TrimSpace(name) == "" || (name != f.old && !strings.Contains(name, "-migration-g")) {
			continue
		}
		diagnostics = append(diagnostics, f.containerDiagnostics(name))
	}
	if len(diagnostics) == 0 {
		return "no candidate containers remain; state=" + strings.TrimSpace(containers)
	}
	return strings.Join(diagnostics, "\n")
}

func (f *realMigrationFixture) containerDiagnostics(name string) string {
	logs, logsErr := podmanOutput(f.ctx, "logs", name)
	state, stateErr := podmanOutput(f.ctx, "inspect", "--format", "state={{.State.Status}} exit={{.State.ExitCode}} oom={{.State.OOMKilled}} error={{.State.Error}} mounts={{range .Mounts}}{{.Source}}:{{.Destination}};{{end}}", name)
	networks, networksErr := podmanOutput(f.ctx, "inspect", "--format", "{{range $name, $_ := .NetworkSettings.Networks}}{{$name}};{{end}}", name)
	identity, identityErr := podmanOutput(f.ctx, "exec", name, "sh", "-c", "for key in GORDON_COMPONENT_ID GORDON_MIGRATION_EDGE_COMPONENT_ID GORDON_COMPONENT_EDGE_TOKEN GORDON_COMPONENT_RUNTIME_TOKEN; do if printenv \"$key\" >/dev/null; then printf '%s=set\\n' \"$key\"; else printf '%s=unset\\n' \"$key\"; fi; done")
	edgeHealth, edgeHealthErr := "", error(nil)
	if strings.HasPrefix(name, "gordon-edge-") {
		edgeHealth, edgeHealthErr = podmanOutput(f.ctx, "exec", name, "sh", "-c", "wget -S -O /dev/null http://127.0.0.1:18081/healthz; wget -S -O /dev/null http://gordon-control:9443")
	}
	role := ""
	for _, candidateRole := range migrationComponentRoles {
		if strings.Contains(name, "gordon-"+string(candidateRole)+"-") {
			role = string(candidateRole)
			break
		}
	}
	configKeys := ""
	processes, processesErr := "", error(nil)
	listeners, listenersErr := "", error(nil)
	if role != "" {
		configKeys = componentConfigKeyNames(filepath.Join(f.root, "data", "migration", "config", "migration", "1", role+".toml"))
		processes, processesErr = podmanOutput(f.ctx, "exec", name, "ps", "-o", "pid,stat,args")
		listeners, listenersErr = podmanOutput(f.ctx, "exec", name, "sh", "-c", "cat /proc/net/tcp /proc/net/tcp6")
	}
	redact := func(value string) string {
		return redactCapturedOutput(value, "fixture-runtime-handoff-token", "migration-fixture-signing-secret-at-least-32-bytes")
	}
	return fmt.Sprintf("%s state=%q state_err=%v networks=%q networks_err=%v identity_and_token_presence=%q identity_err=%v config_keys=%q processes=%q processes_err=%v listeners=%q listeners_err=%v edge_health=%q edge_health_err=%v logs=%q logs_err=%v", name, redact(state), stateErr, networks, networksErr, identity, identityErr, configKeys, redact(processes), processesErr, listeners, listenersErr, edgeHealth, edgeHealthErr, redact(logs), logsErr)
}

func componentConfigKeyNames(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unavailable"
	}
	keys := make([]string, 0)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			keys = append(keys, line)
			continue
		}
		if key, _, found := strings.Cut(line, "="); found {
			keys = append(keys, strings.TrimSpace(key))
		}
	}
	return strings.Join(keys, ",")
}

func cleanupMigrationCandidates(ctx context.Context) {
	for _, role := range migrationComponentRoles {
		_ = migrationPodman(ctx, "rm", "--force", migrationComponentName(role))
	}
	_ = migrationPodman(ctx, "network", "rm", migrationInternalNetworkName())
	for _, role := range []domain.ComponentRole{domain.ComponentRoleControl, domain.ComponentRoleRuntime, domain.ComponentRoleRegistry} {
		_ = migrationPodman(ctx, "volume", "rm", "--force", migrationComponentName(role))
	}
}

// componentContainers intentionally selects Gordon's migration identity rather
// than harness-only labels: component creation is production-owned by the CLI.
func (f *realMigrationFixture) componentContainers() ([]PodmanResource, error) {
	output, err := podmanOutput(f.ctx, "ps", "--all", "--filter", "label="+domain.LabelComponentMigrationID+"=migration", "--format", "json")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(output) == "" || strings.TrimSpace(output) == "null" {
		return nil, nil
	}
	var containers []PodmanResource
	if err := json.Unmarshal([]byte(output), &containers); err != nil {
		return nil, fmt.Errorf("decode migration component containers: %w", err)
	}
	return containers, nil
}

func (f *realMigrationFixture) buildCandidateImage() {
	binary := filepath.Join(f.root, "gordon")
	require.NoError(f.t, securityBuildCandidate(f.ctx, RepositoryRoot(), binary))
	containerfile := "FROM docker.io/library/alpine:3.20\nRUN apk add --no-cache ca-certificates pass gnupg && adduser -D -s /bin/sh gordon && mkdir -p /app /data /var/lib/gordon/secrets && chown -R gordon:gordon /app /data && chown 21002:21002 /var/lib/gordon/secrets && chmod 0700 /var/lib/gordon/secrets\nWORKDIR /data\nUSER gordon\nCOPY --chown=gordon:gordon gordon /usr/local/bin/gordon\nENTRYPOINT [\"/usr/local/bin/gordon\"]\n"
	require.NoError(f.t, os.WriteFile(filepath.Join(f.root, "Containerfile"), []byte(containerfile), 0o600))
	policy := filepath.Join(f.root, "policy.json")
	require.NoError(f.t, os.WriteFile(policy, []byte(`{"default":[{"type":"insecureAcceptAnything"}]}`), 0o600))
	require.NoError(f.t, migrationPodman(f.ctx, "build", "--signature-policy", policy, "--tag", f.image, f.root))
	entrypoint, err := podmanOutput(f.ctx, "inspect", "--format", "{{json .Config.Entrypoint}}", f.image)
	require.NoError(f.t, err)
	require.Contains(f.t, entrypoint, "/usr/local/bin/gordon", "candidate image must contain the candidate Gordon binary")
}

func migrationAuthDisabledEnvironment(runtimeDir string) []string {
	return []string{
		"XDG_RUNTIME_DIR=" + runtimeDir,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + filepath.Join(runtimeDir, "bus"),
	}
}

func (f *realMigrationFixture) startOldMonolith(labels map[string]string) {
	labels = cloneMigrationLabels(labels)
	// The old serving container is migration-owned but is not an application
	// route. Only the fixture app below carries the route domain; otherwise the
	// runtime snapshot can select the monolith itself as that route's backend.
	labels[domain.LabelManaged] = "true"
	// The old serving Gordon is deliberately a normal rootless container, not
	// host-networked. It creates target components only through its own mounted
	// engine socket; the target handoff is a private Gordon Unix RPC socket.
	// Publish every configured legacy listener. The authenticated runtime probe
	// must recognize these as owned by the running managed monolith rather than
	// attempting a bind inside the monolith's own network namespace.
	args := []string{"run", "--detach", "--replace", "--name", f.old, "--user", "0:0", "--network", f.network, "--publish", fmt.Sprintf("%d:%d", f.port, f.port), "--publish", "15000:15000", "--volume", f.root + ":" + f.root, "--volume", f.socket + ":" + f.socket}
	for _, variable := range migrationAuthDisabledEnvironment(f.root) {
		args = append(args, "--env", variable)
	}
	args = append(args, "--env", "GORDON_MIGRATION_IMAGE="+f.image)
	args = append(args, migrationLabelArgs(labels)...)
	args = append(args, f.image, "serve", "--role", "monolith", "--config", f.config)
	require.NoError(f.t, migrationPodman(f.ctx, args...))
}

func (f *realMigrationFixture) startOldApp(labels map[string]string) {
	labels = cloneMigrationLabels(labels)
	labels[domain.LabelManaged] = "true"
	labels[domain.LabelRoute] = "true"
	labels[domain.LabelDomain] = "app.example.test"
	// Match a Gordon-managed route's runtime contract: snapshots identify the
	// backend through its deterministic network alias and declared proxy port.
	// Without these, the real edge correctly has no route target and returns
	// 403, which would make this fixture test only its own invalid setup.
	labels[domain.LabelProxyPort] = "8080"
	args := append([]string{"run", "--detach", "--name", f.app, "--network", f.network, "--network-alias", "gordon-target-app-example-test", "--expose", "8080", "--volume", f.volume + ":/data"}, migrationLabelArgs(labels)...)
	// BusyBox is only the pre-existing workload fixture; every Gordon target
	// component still comes from the candidate image through `migrate prepare`.
	appImage := "docker.io/library/" + "busy" + "box:1.36"
	args = append(args, appImage, "sh", "-c", "mkdir -p /srv; printf migration-app-ok >/srv/index.html; exec httpd -f -p 8080 -h /srv")
	require.NoError(f.t, migrationPodman(f.ctx, args...))
}

// pushRegistryArtifact stores a real OCI manifest and blobs in the old
// monolith registry before prepare. The post-cutover assertion below fetches
// that exact manifest through the final edge, preventing a new empty registry
// volume from passing this compatibility fixture.
func (f *realMigrationFixture) waitOldRegistryReady() {
	f.t.Helper()
	require.Eventually(f.t, func() bool {
		return migrationPodman(f.ctx, "exec", f.old, "busybox", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:15000/v2/") == nil
	}, 15*time.Second, 100*time.Millisecond, "old monolith registry must be ready before seeding its store")
}

func (f *realMigrationFixture) pushRegistryArtifact() {
	f.t.Helper()
	repository := "gordon-migration-artifact-" + sanitizePart(f.runID)
	config := []byte("{}")
	configDigest := sha256.Sum256(config)
	digest := "sha256:" + hex.EncodeToString(configDigest[:])

	start := f.rawOldRegistryRequest(http.MethodPost, "/v2/"+repository+"/blobs/uploads/", "", nil)
	require.Equal(f.t, http.StatusAccepted, start.StatusCode, "old monolith registry must start the artifact upload")
	location := start.Header.Get("Location")
	require.NoError(f.t, start.Body.Close())
	require.NotEmpty(f.t, location)
	uploadURL, err := url.Parse(location)
	require.NoError(f.t, err)
	query := uploadURL.Query()
	query.Set("digest", digest)
	uploadURL.RawQuery = query.Encode()
	finish := f.rawOldRegistryRequest(http.MethodPut, uploadURL.RequestURI(), "application/octet-stream", config)
	require.Equal(f.t, http.StatusCreated, finish.StatusCode, "old monolith registry must store the artifact config")
	require.NoError(f.t, finish.Body.Close())

	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":%d},"layers":[]}`, digest, len(config)))
	stored := f.rawOldRegistryRequest(http.MethodPut, "/v2/"+repository+"/manifests/fixture", "application/vnd.oci.image.manifest.v1+json", manifest)
	require.Equal(f.t, http.StatusCreated, stored.StatusCode, "old monolith registry must accept the pre-cutover artifact")
	require.NoError(f.t, stored.Body.Close())
}

func (f *realMigrationFixture) rawOldRegistryRequest(method, target, contentType string, body []byte) *http.Response {
	f.t.Helper()
	var request bytes.Buffer
	fmt.Fprintf(&request, "%s %s HTTP/1.1\r\nHost: registry.example.test\r\nConnection: close\r\nContent-Length: %d\r\n", method, target, len(body))
	if contentType != "" {
		fmt.Fprintf(&request, "Content-Type: %s\r\n", contentType)
	}
	request.WriteString("\r\n")
	request.Write(body)

	// Every other Podman call in this harness runs through newIsolatedCommand,
	// which replaces HOME and XDG_DATA_HOME with the isolated subprocess paths.
	// Rootless Podman derives its graph root from those, so a command built with
	// the inherited environment addresses a different container store and cannot
	// see the fixture's own containers.
	command, err := newIsolatedCommand(f.ctx, "podman", []string{"exec", "--interactive", f.old, "busybox", "nc", "127.0.0.1", "15000"}, nil, nil, false)
	require.NoError(f.t, err, "prepare old monolith registry request")
	command.Stdin = &request
	output, err := command.CombinedOutput()
	require.NoError(f.t, err, "old monolith registry request must complete: %s", redactCapturedOutput(string(output), "migration-fixture-signing-secret-at-least-32-bytes"))
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(output)), &http.Request{Method: method})
	require.NoError(f.t, err, "old monolith registry response must be valid HTTP")
	return response
}

func (f *realMigrationFixture) waitOldAppReady() {
	f.t.Helper()
	require.Eventually(f.t, func() bool {
		state, err := podmanOutput(f.ctx, "inspect", "--format", "{{.State.Running}}", f.app)
		if err != nil || strings.TrimSpace(state) != "true" {
			return false
		}
		networks, err := podmanOutput(f.ctx, "inspect", "--format", "{{json .NetworkSettings.Networks}}", f.app)
		return err == nil && strings.Contains(networks, "gordon-target-app-example-test")
	}, 10*time.Second, 100*time.Millisecond, "old managed app and deterministic alias must be ready before migration")
}

func (f *realMigrationFixture) runCLI(args ...string) string {
	f.t.Helper()
	args = append(args, "--config", f.config)
	command := append([]string{"exec", "--env", "GORDON_MIGRATION_IMAGE=" + f.image, f.old, "gordon"}, args...)
	out, err := podmanOutput(f.ctx, command...)
	if err != nil {
		f.t.Logf("candidate CLI %s failed; prepared runtime diagnostics:\n%s", strings.Join(args, " "), f.preparedRuntimeDiagnostics())
	}
	require.NoError(f.t, err, "candidate CLI %s", strings.Join(args, " "))
	require.NotEmpty(f.t, strings.TrimSpace(out), "candidate CLI %s must return JSON", strings.Join(args, " "))
	f.t.Logf("candidate CLI %s: %s", strings.Join(args, " "), strings.TrimSpace(out))
	return out
}

func (f *realMigrationFixture) preparedRuntimeDiagnostics() string {
	containers, err := f.componentContainers()
	if err != nil {
		return "inspect migration containers: " + err.Error()
	}
	var diagnostics []string
	for _, container := range containers {
		if container.Labels[domain.LabelComponentRole] != "runtime" {
			continue
		}
		name := container.resourceName()
		mounts, mountErr := podmanOutput(f.ctx, "inspect", "--format", "{{json .Mounts}}", name)
		logs, logsErr := podmanOutput(f.ctx, "logs", name)
		diagnostics = append(diagnostics, fmt.Sprintf("runtime=%s mounts=%q mount_err=%v logs=%q logs_err=%v", name, mounts, mountErr, logs, logsErr))
	}
	if len(diagnostics) == 0 {
		return "prepared runtime was not created"
	}
	return strings.Join(diagnostics, "\n")
}

func (f *realMigrationFixture) runSwitchExpectCallerTermination() {
	f.t.Helper()
	_, err := podmanOutput(f.ctx, "exec", "--env", "GORDON_MIGRATION_IMAGE="+f.image, f.old, "gordon", "migrate", "switch", "--config", f.config, "--json")
	require.Error(f.t, err, "stopping the old monolith must terminate its in-container CLI before it can report success")
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
	containers, err := f.componentContainers()
	require.NoError(f.t, err)
	roles := map[string]PodmanResource{}
	for _, container := range containers {
		if role := container.Labels[domain.LabelComponentRole]; role != "" {
			roles[role] = container
			require.Equal(f.t, "true", container.Labels[domain.LabelComponent])
			require.Equal(f.t, "1", container.Labels[domain.LabelComponentGeneration])
			require.NotEmpty(f.t, container.Labels[domain.LabelComponentMigrationID])
			require.Eventually(f.t, func() bool {
				running, inspectErr := podmanOutput(f.ctx, "inspect", "--format", "{{.State.Running}}", container.resourceName())
				return inspectErr == nil && strings.TrimSpace(running) == "true"
			}, 3*time.Second, 50*time.Millisecond, "%s must be healthy/running", role)
		}
	}
	require.Len(f.t, roles, 4, "only candidate CLI/runtime may create exactly one target role generation")
	for _, role := range []string{"control", "runtime", "registry", "edge"} {
		require.Contains(f.t, roles, role)
	}
	edgeID := roles["edge"].Labels[domain.LabelComponentRole]
	require.Equal(f.t, "edge", edgeID)
	identity, identityErr := podmanOutput(f.ctx, "exec", roles["edge"].resourceName(), "printenv", "GORDON_COMPONENT_ID")
	require.NoError(f.t, identityErr)
	require.Equal(f.t, migrationComponentName(domain.ComponentRoleEdge), strings.TrimSpace(identity))
	controlIdentity, controlIdentityErr := podmanOutput(f.ctx, "exec", roles["control"].resourceName(), "printenv", "GORDON_MIGRATION_EDGE_COMPONENT_ID")
	require.NoError(f.t, controlIdentityErr)
	require.Equal(f.t, strings.TrimSpace(identity), strings.TrimSpace(controlIdentity))
}

// assertPreparedRoleSecurityAndPrivateAccess proves the production-created
// containers use their fixed, distinct numeric identities and exact rootless
// Podman confinement. It also exercises each role's intended private mount:
// runtime owns the handoff socket, control owns only attestation writes,
// registry owns its migrated store, and edge remains stateless.
func (f *realMigrationFixture) assertPreparedRoleSecurityAndPrivateAccess() {
	f.t.Helper()
	containers, err := f.componentContainers()
	require.NoError(f.t, err)
	seen := make(map[string]bool, 4)
	for _, container := range containers {
		role := container.Labels[domain.LabelComponentRole]
		identity, ok := domain.FixedComponentProcessIdentity(domain.ComponentRole(role))
		if !ok {
			continue
		}
		name := container.resourceName()
		seen[role] = true
		uidGID, execErr := podmanOutput(f.ctx, "exec", name, "sh", "-ec", "printf '%s:%s' \"$(id -u)\" \"$(id -g)\"")
		require.NoError(f.t, execErr)
		require.Equal(f.t, identity.User, strings.TrimSpace(uidGID), "%s must execute as its fixed role identity", role)
		user, inspectErr := podmanOutput(f.ctx, "inspect", "--format", "{{.Config.User}}", name)
		require.NoError(f.t, inspectErr)
		require.Equal(f.t, identity.User, strings.TrimSpace(user))
		userns, inspectErr := podmanOutput(f.ctx, "inspect", "--format", "{{.HostConfig.UsernsMode}}", name)
		require.NoError(f.t, inspectErr)
		require.Equal(f.t, fmt.Sprintf("keep-id:uid=%d,gid=%d", identity.UID, identity.GID), strings.TrimSpace(userns))
		capDrop, inspectErr := podmanOutput(f.ctx, "inspect", "--format", "{{json .HostConfig.CapDrop}}", name)
		require.NoError(f.t, inspectErr)
		require.JSONEq(f.t, `["ALL"]`, strings.TrimSpace(capDrop))
		capAdd, inspectErr := podmanOutput(f.ctx, "inspect", "--format", "{{range .HostConfig.CapAdd}}{{.}}{{end}}", name)
		require.NoError(f.t, inspectErr)
		require.Empty(f.t, strings.TrimSpace(capAdd), "%s must not add capabilities", role)
		securityOpts, inspectErr := podmanOutput(f.ctx, "inspect", "--format", "{{json .HostConfig.SecurityOpt}}", name)
		require.NoError(f.t, inspectErr)
		require.Contains(f.t, securityOpts, "no-new-privileges", "%s must set no-new-privileges", role)

		var accessCommand string
		switch role {
		case "runtime", "control":
			volumeName := "gordon-" + role + "-migration-g1"
			volume, volumeErr := podmanOutput(f.ctx, "inspect", "--format", "{{range .Mounts}}{{if eq .Destination \"/var/lib/gordon\"}}{{.Name}}:{{.Mode}}{{end}}{{end}}", name)
			require.NoError(f.t, volumeErr)
			require.Equal(f.t, volumeName+":", strings.TrimSpace(volume), "%s Podman mount projection must retain the named volume while omitting create-only U", role)
			bindsJSON, bindsErr := podmanOutput(f.ctx, "inspect", "--format", "{{json .HostConfig.Binds}}", name)
			require.NoError(f.t, bindsErr)
			var binds []string
			require.NoError(f.t, json.Unmarshal([]byte(strings.TrimSpace(bindsJSON)), &binds))
			expectedBind := volumeName + ":/var/lib/gordon:U,rprivate,nosuid,nodev,rbind"
			var stateBinds []string
			for _, bind := range binds {
				parts := strings.Split(bind, ":")
				if len(parts) >= 2 && filepath.Clean(strings.TrimSpace(parts[1])) == "/var/lib/gordon" {
					stateBinds = append(stateBinds, bind)
				}
			}
			require.Equal(f.t, []string{expectedBind}, stateBinds, "%s compatible inspect must expose Podman's exact canonical U bind sequence", role)
			stateProbe := "test \"$(stat -c '%u:%g' /var/lib/gordon)\" = \"" + identity.User + "\"; chmod 0700 /var/lib/gordon; test \"$(stat -c '%a' /var/lib/gordon)\" = 700; state_probe=/var/lib/gordon/.identity-write-check; : >$state_probe; test -r $state_probe; rm $state_probe; test ! -e $state_probe; "
			if role == "runtime" {
				accessCommand = stateProbe + "test -S /var/lib/gordon/migration/migration/runtime-control.sock; test -w /var/lib/gordon/migration/migration"
			} else {
				accessCommand = stateProbe + "test -S /var/lib/gordon/migration/migration/runtime-control.sock; test ! -w /var/lib/gordon/migration/migration; probe=/var/lib/gordon/migration/migration/attestation/.identity-write-check; : >$probe; test -r $probe; rm $probe; test ! -e $probe"
			}
		case "registry":
			accessCommand = "probe=/var/lib/gordon/registry/.identity-write-check; : >$probe; test -r $probe; rm $probe; test ! -e $probe"
		case "edge":
			accessCommand = "test -r /etc/gordon/role.toml; test ! -w /etc/gordon/role.toml; test ! -e /run/gordon/runtime.sock"
		}
		mountModes, inspectErr := podmanOutput(f.ctx, "inspect", "--format", "{{range .Mounts}}{{println .Destination .Mode}}{{end}}", name)
		require.NoError(f.t, inspectErr)
		for _, line := range strings.Split(strings.TrimSpace(mountModes), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[1] == domain.ContainerVolumeOptionChown {
				require.Equal(f.t, "/var/lib/gordon", fields[0], "%s must not apply U outside its generation volume", role)
				require.Contains(f.t, []string{"runtime", "control"}, role)
			}
		}
		require.NoError(f.t, migrationPodman(f.ctx, "exec", name, "sh", "-ec", accessCommand), "%s must have exactly its role-private access", role)
	}
	require.Equal(f.t, map[string]bool{"control": true, "runtime": true, "edge": true, "registry": true}, seen, "all four split roles must start and pass identity checks")
}

// assertPreparedEdgeProbeListener verifies the temporary listener is the sole
// host publish during prepare and that both application and registry requests
// really traverse the prepared edge before public cutover.
// assertPreparedAppNetworkHandoff proves that runtime discovery checkpointed
// the legacy app network and connected only the prepared edge to it. The
// checkpoint stores names, never container IDs or engine socket details.
func (f *realMigrationFixture) assertPreparedAppNetworkHandoff() {
	f.t.Helper()
	path := filepath.Join(f.root, "data", "migration", "migration", "attestation", "checkpoint.json")
	var checkpoint struct {
		EdgeAppNetworks       []string `json:"edge_app_networks"`
		ConnectedEdgeNetworks []string `json:"connected_edge_networks"`
	}
	require.Eventually(f.t, func() bool {
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, &checkpoint) != nil {
			return false
		}
		return len(checkpoint.EdgeAppNetworks) == 1 && checkpoint.EdgeAppNetworks[0] == f.network && len(checkpoint.ConnectedEdgeNetworks) == 1 && checkpoint.ConnectedEdgeNetworks[0] == f.network
	}, 10*time.Second, 50*time.Millisecond, "checkpoint must record the discovered old app network and the edge connection")

	containers, err := f.componentContainers()
	require.NoError(f.t, err)
	for _, container := range containers {
		if container.Labels[domain.LabelComponentRole] != "edge" {
			continue
		}
		networks, inspectErr := podmanOutput(f.ctx, "inspect", "--format", "{{range $name, $_ := .NetworkSettings.Networks}}{{$name}};{{end}}", container.resourceName())
		require.NoError(f.t, inspectErr)
		require.Contains(f.t, strings.FieldsFunc(networks, func(r rune) bool { return r == ';' }), f.network, "prepared edge must be attached to the checkpointed app network")
		return
	}
	f.t.Fatal("prepared edge was not found")
}

func (f *realMigrationFixture) assertAuthDisabledRoleIsolation() {
	f.t.Helper()
	containers, err := f.componentContainers()
	require.NoError(f.t, err)
	seen := make(map[string]bool, 4)
	for _, container := range containers {
		role := container.Labels[domain.LabelComponentRole]
		if role == "" {
			continue
		}
		seen[role] = true
		command := "test -z \"${GORDON_AUTH_TOKEN_SECRET+x}\""
		if role == "control" {
			command += "; test \"$GNUPGHOME\" = /var/lib/gordon/secrets/current/gnupg; test \"$PASSWORD_STORE_DIR\" = /var/lib/gordon/secrets/current/password-store"
		} else {
			command += "; test -z \"${GNUPGHOME+x}\"; test -z \"${PASSWORD_STORE_DIR+x}\"; test ! -e /var/lib/gordon/secrets"
		}
		require.NoError(f.t, migrationPodman(f.ctx, "exec", container.resourceName(), "sh", "-ec", command), "%s must not receive disabled JWT auth or control-owned pass state", role)
	}
	require.Equal(f.t, map[string]bool{"control": true, "runtime": true, "edge": true, "registry": true}, seen)
}

func (f *realMigrationFixture) assertPreparedEdgeProbeListener() {
	f.t.Helper()
	containers, err := f.componentContainers()
	require.NoError(f.t, err)
	for _, container := range containers {
		if container.Labels[domain.LabelComponentRole] != "edge" {
			continue
		}
		ports, portErr := podmanOutput(f.ctx, "port", container.resourceName())
		require.NoError(f.t, portErr)
		require.Equal(f.t, fmt.Sprintf("%d/tcp -> 127.0.0.1:18080", f.port), strings.TrimSpace(ports))
		for _, probe := range []struct {
			host string
			path string
		}{
			{"app.example.test", "/"},
			{"registry.example.test", "/v2/"},
		} {
			// Rootless port forwarding does not preserve the loopback peer address.
			// The unauthenticated request proves the final CIDR boundary remains
			// strict; only the migration coordinator's dedicated credential can
			// exercise the prepared listener.
			missing := f.preparedEdgeRequest(probe.host, probe.path, "")
			require.Equal(f.t, http.StatusForbidden, missing.StatusCode, "prepared edge %s must reject a missing migration credential", probe.host)
			missing.Body.Close()

			require.Eventually(f.t, func() bool {
				response := f.preparedEdgeRequest(probe.host, probe.path, fixtureMigrationProbeToken())
				defer response.Body.Close()
				if probe.path == "/v2/" {
					return response.StatusCode == http.StatusOK || response.StatusCode == http.StatusUnauthorized
				}
				return response.StatusCode < http.StatusBadRequest
			}, 10*time.Second, 50*time.Millisecond, "authenticated prepared edge %s probe", probe.host)
		}
		return
	}
	f.t.Fatal("prepared edge was not found")
}

func (f *realMigrationFixture) preparedEdgeRequest(host, path, token string) *http.Response {
	f.t.Helper()
	request, err := http.NewRequestWithContext(f.ctx, http.MethodGet, "http://127.0.0.1:18080"+path, nil)
	require.NoError(f.t, err)
	request.Host = host
	if token != "" {
		request.Header.Set("X-Gordon-Migration-Probe", token)
	}
	response, err := (&http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}).Do(request)
	require.NoError(f.t, err, "prepared edge %s probe", host)
	return response
}

// fixtureMigrationProbeToken mirrors the domain-separated migration protocol
// credential without importing app internals into this black-box compatibility
// fixture. It must never be persisted or printed.
func fixtureMigrationProbeToken() string {
	mac := hmac.New(sha256.New, []byte("fixture-runtime-handoff-token"))
	_, _ = mac.Write([]byte("gordon-migration-probe-token-v1"))
	return "gordon_migration_probe_" + hex.EncodeToString(mac.Sum(nil))
}

func (f *realMigrationFixture) assertRuntimeBootstrapTransport() {
	f.t.Helper()
	containers, err := f.componentContainers()
	require.NoError(f.t, err)
	for _, container := range containers {
		if container.Labels[domain.LabelComponentRole] != "runtime" {
			continue
		}
		ports, portErr := podmanOutput(f.ctx, "port", container.resourceName())
		require.NoError(f.t, portErr)
		require.Empty(f.t, strings.TrimSpace(ports), "runtime Unix bootstrap must not publish TCP")
		return
	}
	f.t.Fatal("prepared runtime was not found")
}

func (f *realMigrationFixture) assertAuthenticatedEdgeAttestation() {
	f.t.Helper()
	path := filepath.Join(f.root, "data", "migration", "migration", "attestation", "checkpoint.json")
	require.Eventually(f.t, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		var checkpoint struct {
			RouteSnapshotGeneration uint64 `json:"route_snapshot_generation"`
			AppliedEdgeComponentID  string `json:"applied_edge_component_id"`
		}
		return json.Unmarshal(data, &checkpoint) == nil && checkpoint.RouteSnapshotGeneration > 0 && checkpoint.AppliedEdgeComponentID == migrationComponentName(domain.ComponentRoleEdge)
	}, 10*time.Second, 50*time.Millisecond, "authenticated edge RPC must persist its matching generation and identity in shared attestation")
}

func (f *realMigrationFixture) assertOldCoordinatorUsesHostRuntimeSocket() {
	f.t.Helper()
	hostPath := filepath.Join(f.root, "data", "migration", "migration", "runtime-control.sock")
	_, err := podmanOutput(f.ctx, "exec", f.old, "test", "-S", hostPath)
	require.NoError(f.t, err, "old coordinator must dial the configured host migration bind source")
	_, err = podmanOutput(f.ctx, "exec", f.old, "test", "!", "-e", "/var/lib/gordon/migration/migration/runtime-control.sock")
	require.NoError(f.t, err, "old coordinator must not see the component namespace socket")
}

func (f *realMigrationFixture) assertRuntimeSocketExclusive() {
	f.t.Helper()
	containers, err := f.componentContainers()
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
	// The durable terminal checkpoint has already been observed by a fresh
	// process. Wait only for rootless Podman's state cache to reflect the
	// completed runtime transaction; this is not a completion shortcut.
	var oldState string
	var inspectErr error
	require.Eventually(f.t, func() bool {
		oldState, inspectErr = podmanOutput(f.ctx, "inspect", "--format", "{{.State.Running}}", f.old)
		return inspectErr == nil && strings.TrimSpace(oldState) == "false"
	}, 10*time.Second, 50*time.Millisecond, "runtime-owned listener cutover must stop the old serving monolith (last state=%q, last error=%v)", strings.TrimSpace(oldState), inspectErr)
	f.assertFinalEdgeBindingsAndNetwork()

	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	for _, probe := range []struct {
		host string
		path string
	}{
		{"app.example.test", "/"},
		{"registry.example.test", "/v2/"},
	} {
		require.Eventually(f.t, func() bool {
			request, requestErr := http.NewRequestWithContext(f.ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", f.port, probe.path), nil)
			if requestErr != nil {
				return false
			}
			request.Host = probe.host
			response, responseErr := client.Do(request)
			if responseErr != nil {
				return false
			}
			defer response.Body.Close()
			if probe.path == "/v2/" {
				return response.StatusCode == http.StatusOK || response.StatusCode == http.StatusUnauthorized
			}
			return response.StatusCode < http.StatusBadRequest
		}, 10*time.Second, 50*time.Millisecond, "post-cutover %s must traverse the final edge", probe.host)
	}
}

// assertFinalEdgeBindingsAndNetwork proves activation replaced the private
// prepared listener rather than merely declaring the old monolith stopped. It
// also proves the final edge retained the managed app network used for routing.
func (f *realMigrationFixture) assertRegistryArtifact() {
	f.t.Helper()
	target := "gordon-migration-artifact-" + sanitizePart(f.runID)
	request, err := http.NewRequestWithContext(f.ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/v2/%s/manifests/fixture", f.port, target), nil)
	require.NoError(f.t, err)
	request.Host = "registry.example.test"
	response, err := (&http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}).Do(request)
	require.NoError(f.t, err)
	defer response.Body.Close()
	require.Equal(f.t, http.StatusOK, response.StatusCode, "pre-cutover registry manifest must remain retrievable through the final edge")
}

func (f *realMigrationFixture) assertFinalEdgeBindingsAndNetwork() {
	f.t.Helper()
	containers, err := f.componentContainers()
	require.NoError(f.t, err)
	for _, container := range containers {
		if container.Labels[domain.LabelComponentRole] != "edge" {
			continue
		}
		ports, portErr := podmanOutput(f.ctx, "port", container.resourceName())
		require.NoError(f.t, portErr)
		bindings := strings.FieldsFunc(strings.TrimSpace(ports), func(r rune) bool { return r == '\n' })
		require.ElementsMatch(f.t, []string{
			fmt.Sprintf("%d/tcp -> 127.0.0.1:%d", f.port, f.port),
			"15000/tcp -> 127.0.0.1:15000",
		}, bindings, "final edge listeners must be confined to the host TLS terminator on loopback")
		networks, networkErr := podmanOutput(f.ctx, "inspect", "--format", "{{range $name, $_ := .NetworkSettings.Networks}}{{$name}};{{end}}", container.resourceName())
		require.NoError(f.t, networkErr)
		require.Equal(f.t, []string{f.network, migrationInternalNetworkName()}, normalizeNetworkSet(networks), "final edge must retain exactly the managed app and internal networks")
		return
	}
	f.t.Fatal("final edge was not found")
}

// runFreshMigrationCLI deliberately uses the candidate binary from outside
// the stopped monolith. It is the observer a homelab operator has after the
// in-container switch caller loses its connection.
func (f *realMigrationFixture) runFreshMigrationCLI(args ...string) (string, error) {
	f.t.Helper()
	command := exec.CommandContext(f.ctx, filepath.Join(f.root, "gordon"), args...) // #nosec G204 -- candidate binary and arguments are fixture-owned.
	environment := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "DOCKER_HOST" && key != "CONTAINER_HOST" && key != "PODMAN_HOST" && key != "GORDON_AUTH_TOKEN_SECRET" {
			environment = append(environment, entry)
		}
	}
	command.Env = append(environment, migrationAuthDisabledEnvironment(f.root)...)
	command.Env = append(command.Env, "GORDON_MIGRATION_IMAGE="+f.image)
	output, err := command.CombinedOutput()
	return string(output), err
}

// awaitInterruptedSwitchTerminalStatus is intentionally before any final
// container assertion: StopContainer is asynchronous in rootless Podman, but
// the runtime only stores switched after the final edge is healthy and its app
// network has been restored. This proves the observed state is a completed
// runtime-owned transaction rather than a fixture timing assumption.
func (f *realMigrationFixture) awaitInterruptedSwitchTerminalStatus() {
	f.t.Helper()
	var lastPhase string
	var lastCommandErr, lastJSONErr error
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, commandErr := f.runFreshMigrationCLI("migrate", "status", "--config", f.config, "--json")
		lastCommandErr = commandErr
		lastJSONErr = nil
		if commandErr == nil {
			lastPhase, lastJSONErr = migrationStatusPhase(status)
			if lastJSONErr == nil && lastPhase == "switched" {
				return
			}
		}
		if time.Now().After(deadline) {
			require.Failf(f.t, "fresh status after terminated caller must read the runtime-durable switched checkpoint", "last phase=%q, last command error=%s, last JSON error=%s", lastPhase, migrationStatusDiagnosticError(lastCommandErr), migrationStatusDiagnosticError(lastJSONErr))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (f *realMigrationFixture) assertInterruptedSwitchRetry() {
	f.t.Helper()
	retry, err := f.runFreshMigrationCLI("migrate", "switch", "--config", f.config, "--json")
	require.NoError(f.t, err, "a retry after a terminated caller must converge without a second cutover")
	phase, err := migrationStatusPhase(retry)
	require.NoError(f.t, err, "retry must return a JSON migration status")
	require.Equal(f.t, "switched", phase, "a retry after a terminated caller must retain the switched checkpoint")
}

func (f *realMigrationFixture) assertManagedPassVolumePersistence() {
	f.t.Helper()
	containers, err := f.componentContainers()
	require.NoError(f.t, err)
	var control PodmanResource
	for _, container := range containers {
		if container.Labels[domain.LabelComponentRole] == "control" {
			control = container
			break
		}
	}
	require.NotEmpty(f.t, control.resourceName(), "candidate migration must create control")
	imageUser, err := podmanOutput(f.ctx, "inspect", "--format", "{{.Config.User}}", f.image)
	require.NoError(f.t, err)
	require.NotEmpty(f.t, strings.TrimSpace(imageUser), "managed-pass smoke must use the image default non-root user")
	require.NotEqual(f.t, "0", strings.TrimSpace(imageUser))

	mounts, err := podmanOutput(f.ctx, "inspect", "--format", "{{range .Mounts}}{{if eq .Destination \"/var/lib/gordon/secrets\"}}{{.Name}}{{end}}{{end}}", control.resourceName())
	require.NoError(f.t, err)
	volume := strings.TrimSpace(mounts)
	require.Regexp(f.t, `^gordon-control-secrets-[0-9a-f]{16}$`, volume)

	doctorConfig := filepath.Join(f.root, "managed-pass-doctor.toml")
	require.NoError(f.t, os.WriteFile(doctorConfig, []byte("[auth]\nenabled = false\nsecrets_backend = \"pass\"\n"), 0o644)) //nolint:gosec // fixture doctor config mirrors release smoke readability.
	controlIdentity, ok := domain.FixedComponentProcessIdentity(domain.ComponentRoleControl)
	require.True(f.t, ok)
	controlUserNS := fmt.Sprintf("keep-id:uid=%d,gid=%d", controlIdentity.UID, controlIdentity.GID)
	runDoctor := func() {
		f.t.Helper()
		args := []string{
			"run", "--rm", "--user", controlIdentity.User, "--userns", controlUserNS,
			"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
			"--volume", volume + ":/var/lib/gordon/secrets",
			"--volume", doctorConfig + ":/tmp/gordon-doctor.toml:ro",
			"--env", "GNUPGHOME=/var/lib/gordon/secrets/current/gnupg",
			"--env", "PASSWORD_STORE_DIR=/var/lib/gordon/secrets/current/password-store",
			f.image, "secrets", "doctor", "--config", "/tmp/gordon-doctor.toml", "--write-check",
		}
		require.NoError(f.t, migrationPodman(f.ctx, args...), "default-user doctor must write, read, and delete without exposing values")
	}

	require.NoError(f.t, migrationPodman(f.ctx, "stop", control.resourceName()))
	require.NoError(f.t, migrationPodman(f.ctx, "run", "--rm", "--user", controlIdentity.User, "--userns", controlUserNS, "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--volume", volume+":/var/lib/gordon/secrets:ro", "--entrypoint", "sh", f.image, "-ec", "test -s /var/lib/gordon/secrets/current/.gordon-managed-pass-fingerprint; test -s /var/lib/gordon/secrets/current/password-store/.gpg-id; test -d /var/lib/gordon/secrets/current/gnupg"), "control startup must initialize the fresh managed-pass volume before doctor runs")
	runDoctor()
	require.NoError(f.t, migrationPodman(f.ctx, "start", control.resourceName()))
	require.Eventually(f.t, func() bool {
		running, inspectErr := podmanOutput(f.ctx, "inspect", "--format", "{{.State.Running}}", control.resourceName())
		return inspectErr == nil && strings.TrimSpace(running) == "true"
	}, 10*time.Second, 100*time.Millisecond, "control must restart with its initialized managed pass volume")
	require.NoError(f.t, migrationPodman(f.ctx, "stop", control.resourceName()))
	runDoctor()
	require.NoError(f.t, migrationPodman(f.ctx, "start", control.resourceName()))
}

func normalizeNetworkSet(networks string) []string {
	parts := strings.Split(networks, ";")
	normalized := make([]string, 0, len(parts))
	for _, network := range parts {
		if network = strings.TrimSpace(network); network != "" {
			normalized = append(normalized, network)
		}
	}
	sort.Strings(normalized)
	return normalized
}

func migrationStatusPhase(status string) (string, error) {
	var result struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal([]byte(status), &result); err != nil {
		return "", fmt.Errorf("decode migration status JSON: %w", err)
	}
	return result.Phase, nil
}

func migrationStatusDiagnosticError(err error) string {
	if err == nil {
		return "<none>"
	}
	return redactCapturedOutput(err.Error(), "fixture-runtime-handoff-token", "migration-fixture-signing-secret-at-least-32-bytes")
}

func (f *realMigrationFixture) configTOML() string {
	return fmt.Sprintf(`[server]
port = %d
registry_port = 15000
data_dir = %q
gordon_domain = "app.example.test"
registry_domain = "registry.example.test"

[auth]
enabled = false
secrets_backend = "pass"

[network_isolation]
enabled = true
network_prefix = "gordon"

[control]
listen_address = "0.0.0.0:9443"
insecure_tls = true

[runtime]
listen_address = "127.0.0.1:19444"
token = "fixture-runtime-handoff-token"

[containers]
security_profile = "compat"
`, f.port, filepath.Join(f.root, "data"))
}

func migrationStartPodmanService(t *testing.T, ctx context.Context, root string) (string, *exec.Cmd) {
	t.Helper()
	socket := filepath.Join(root, "podman", "podman.sock")
	require.NoError(t, os.MkdirAll(filepath.Dir(socket), 0o700))
	// The test's user-local Podman wrapper is a CLI client, not a socket mount
	// source. Start a private rootless API service so the old monolith receives
	// the only engine socket and the target runtime can inherit it. It must use
	// the same isolated environment as every other harness Podman call, or it
	// serves a different container store than the one the fixture created its
	// old monolith and app in.
	service, err := newIsolatedCommand(ctx, "podman", []string{"system", "service", "--time=0", "unix://" + socket}, nil, nil, false)
	require.NoError(t, err, "prepare private rootless Podman API service")
	service.Stdout = os.Stderr
	service.Stderr = os.Stderr
	require.NoError(t, service.Start())
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(socket); err == nil && info.Mode()&os.ModeSocket != 0 {
			// The fixture root is owner-only, and this socket is mounted only into
			// the old monolith and authenticated runtime. Permit the image's
			// default non-root user to open that private runtime-authority socket.
			require.NoError(t, os.Chmod(socket, 0o666)) //nolint:gosec // fixture must allow the image default user to open the private runtime socket.
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
