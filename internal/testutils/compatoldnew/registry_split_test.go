package compatoldnew

// This file deliberately keeps the split registry test outside the old/new OCI
// transcript: split roles did not exist in the baseline and comparing them to a
// monolith would turn a security gate into a compatibility false positive.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	eventsv1 "github.com/bnema/gordon/api/gordon/events/v1"
	runtimev1 "github.com/bnema/gordon/api/gordon/runtime/v1"
	grpcauth "github.com/bnema/gordon/internal/adapters/out/grpc/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// TestCompatibilitySplitRegistryEventFlow is an intentionally current-only
// real-binary gate. Registry and control are independent child processes; the
// only fake is the narrow RuntimeService transport that records commands.
func TestCompatibilitySplitRegistryEventFlow(t *testing.T) {
	requireRealCompatibilityRun(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	f := newSplitRegistryFixture(t, ctx)
	defer f.close()

	// Registry is live while control is deliberately absent: a successful OCI
	// manifest PUT must persist rather than synchronously lose the event.
	f.startRegistry(t, ctx)
	f.push(t, ctx, "split/app", "v1", "split.example.test")
	f.wait(t, func() bool { return splitDirHasFile(filepath.Join(f.data, "registry", "event-outbox")) }, "durable registry outbox")
	f.startRuntime(t, ctx)
	f.startControl(t, ctx)
	// Control availability replays the restrictive durable entry through the
	// authenticated production client. Wait for the acknowledgement before
	// interrupting that in-flight delivery; an interrupted RPC can persist a
	// route before its runtime command and is deliberately retried instead.
	f.wait(t, func() bool {
		return f.engine.deploymentCount("split.example.test", "registry.example.test/split/app:v1") == 1
	}, "replayed runtime deploy")
	f.engine.assertDeploymentProtocol(t, 1)
	f.wait(t, f.routeUpdated, "safe auto-route snapshot")
	f.wait(t, func() bool { return !splitDirHasFile(filepath.Join(f.data, "registry", "event-outbox")) }, "outbox acknowledgement")
	// A process restart after acknowledgement must not turn the replay into a
	// second runtime mutation.
	f.restartRegistry(t, ctx)
	time.Sleep(300 * time.Millisecond)
	if got := f.engine.deploymentCount("split.example.test", "registry.example.test/split/app:v1"); got != 1 {
		t.Fatalf("replay was not exactly once: got %d deployments", got)
	}

	// A preview-tagged OCI manifest is classified by the production dispatcher,
	// not auto-route. Its durable preview route and exact runtime deployment
	// prove the preview annotation/tag path remains connected to control.
	f.push(t, ctx, "split/app", "preview-ci", "split.example.test")
	f.wait(t, func() bool {
		return f.engine.deploymentCount("split--ci.example.test", "registry.example.test/split/app:preview-ci") == 1
	}, "preview runtime deploy")

	// This exercises HTTP -> persisted dispatcher intent. Restart control before
	// the matching push so suppression cannot be an in-memory implementation.
	beforeManual := f.engine.deploymentCount("split.example.test", "registry.example.test/split/app:v1")
	f.manualDeploy(t, ctx, "split.example.test")
	f.wait(t, func() bool {
		return f.engine.deploymentCount("split.example.test", "registry.example.test/split/app:v1") == beforeManual+1
	}, "manual runtime deploy")
	f.restartControl(t, ctx)
	f.push(t, ctx, "split/app", "v1", "split.example.test")
	time.Sleep(300 * time.Millisecond)
	if got := f.engine.deploymentCount("split.example.test", "registry.example.test/split/app:v1"); got != beforeManual+1 {
		t.Fatalf("restarted control did not suppress matching push: got %d deployments", got)
	}
	f.push(t, ctx, "split/other", "manual", "other.example.test")
	f.wait(t, func() bool {
		return f.engine.deploymentCount("other.example.test", "registry.example.test/split/other:manual") == 1
	}, "unrelated image automation")

	// Age a stopped control's durable intent. A new control process must load the
	// expired production record, discard it, and deploy the matching image.
	f.manualDeploy(t, ctx, "split.example.test")
	f.wait(t, func() bool {
		return f.engine.deploymentCount("split.example.test", "registry.example.test/split/app:v1") == beforeManual+2
	}, "second manual deploy")
	f.stopControl(t)
	f.ageManualIntent(t)
	f.startControl(t, ctx)
	f.push(t, ctx, "split/app", "v1", "split.example.test")
	f.wait(t, func() bool {
		return f.engine.deploymentCount("split.example.test", "registry.example.test/split/app:v1") == beforeManual+3
	}, "expired manual intent push")
	f.assertWrongScopeDenied(t, ctx)
	f.assertIsolation(t)
}

type splitRegistryFixture struct {
	t                                                  *testing.T
	root, data, binary, registryConfig, controlConfig  string
	registryPort, controlPort, adminPort, runtimePort  int
	registry, control, runtime                         *GordonInstance
	engine                                             *fakeDockerEngine
	eventToken, runtimeToken, adminToken, lastLocation string
	pushSequence                                       int
}

func newSplitRegistryFixture(t *testing.T, ctx context.Context) *splitRegistryFixture {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	for _, p := range []string{data, filepath.Join(root, "home-registry"), filepath.Join(root, "home-control")} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ports := make([]int, 4)
	for i := range ports {
		ports[i] = splitPort(t)
	}
	f := &splitRegistryFixture{t: t, root: root, data: data, binary: filepath.Join(root, "gordon"), registryPort: ports[0], controlPort: ports[1], adminPort: ports[2], runtimePort: ports[3]}
	if err := securityBuildCandidate(ctx, projectRoot(t), f.binary); err != nil {
		t.Fatal(err)
	}
	f.engine = newFakeDockerEngine(t, filepath.Join(root, "docker.sock"))
	f.controlConfig = filepath.Join(root, "control.toml")
	f.registryConfig = filepath.Join(root, "registry.toml")
	if err := os.WriteFile(f.controlConfig, []byte(f.controlTOML(f.runtimePort)), 0o600); err != nil {
		t.Fatal(err)
	}
	// Generate real persisted component and admin credentials through the candidate CLI.
	f.eventToken = f.commandToken(t, ctx, "registry", "registry", "registry:event:publish")
	f.runtimeToken = f.commandToken(t, ctx, "control-runtime", "control", "runtime:deploy,runtime:status")
	if err := os.WriteFile(f.controlConfig, []byte(f.controlTOML(f.runtimePort)), 0o600); err != nil {
		t.Fatal(err)
	}
	f.adminToken = f.authToken(t, ctx)
	if err := os.WriteFile(f.registryConfig, []byte(f.registryTOML()), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *splitRegistryFixture) controlTOML(runtimePort int) string {
	return fmt.Sprintf(`[server]
data_dir = %q
registry_domain = "registry.example.test"
[auth]
enabled = true
secrets_backend = "unsafe"
token_secret = "split-registry-test-signing-secret-0123456789"
[control]
listen_address = "127.0.0.1:%d"
insecure_tls = true
[control.http]
listen_address = "127.0.0.1:%d"
insecure_tls = true
[runtime]
listen_address = "127.0.0.1:%d"
endpoint = "127.0.0.1:%d"
token = %q
insecure = true
[auto]
enabled = true
allowed_domains = ["*.example.test"]
[auto.preview]
enabled = true
tag_patterns = ["preview-*"]
`, f.data, f.controlPort, f.adminPort, runtimePort, runtimePort, f.runtimeToken)
}
func (f *splitRegistryFixture) registryTOML() string {
	return fmt.Sprintf(`[storage]
data_dir = %q
[listen]
address = "127.0.0.1:%d"
[listen.tls]
mode = "disabled"
[auth]
enabled = true
secrets_backend = "unsafe"
token_secret = "split-registry-test-signing-secret-0123456789"
[limits]
max_blob_chunk_size = "1MB"
max_blob_size = "1MB"
[control]
event_endpoint = "127.0.0.1:%d"
event_token = %q
insecure_tls = true
outbox_max_entries = 64
outbox_max_bytes = "1MB"
`, f.data, f.registryPort, f.controlPort, f.eventToken)
}
func (f *splitRegistryFixture) commandToken(t *testing.T, ctx context.Context, name, role, scope string) string {
	t.Helper()
	args := []string{"auth", "component-token", "create", "--config", f.controlConfig, "--name", name, "--role", role}
	for _, value := range strings.Split(scope, ",") {
		args = append(args, "--scope", value)
	}
	args = append(args, "--json")
	cmd, err := newIsolatedCommand(ctx, f.binary, args, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Dir = f.root
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		t.Fatal("component token creation failed")
	}
	var v struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &v); err != nil || v.Token == "" {
		t.Fatalf("decode component token: %v", err)
	}
	return v.Token
}
func (f *splitRegistryFixture) authToken(t *testing.T, ctx context.Context) string {
	fixture := SideFixture{Root: f.root, HomeDir: filepath.Join(f.root, "home-control"), DataDir: f.data, ConfigPath: f.controlConfig, Env: []string{"HOME=" + filepath.Join(f.root, "home-control")}}
	env := adminAPIEnvironment(fixture)
	sensitive := []SensitiveEnvironment{{Side: SideNew, Key: "GORDON_AUTH_TOKEN_SECRET", Value: "split-registry-test-signing-secret-0123456789"}}
	token, err := generateAdminToken(ctx, f.binary, fixture, "split", env, sensitive)
	if err != nil {
		t.Fatal(err)
	}
	return token
}
func (f *splitRegistryFixture) startRegistry(t *testing.T, ctx context.Context) {
	t.Helper()
	if f.registry != nil {
		return
	}
	f.registry = &GordonInstance{BinaryPath: f.binary, ConfigPath: f.registryConfig, DataDir: f.data, WorkingDir: f.root, Env: []string{"HOME=" + filepath.Join(f.root, "home-registry")}, SensitiveEnv: []SensitiveEnvironment{{Side: SideNew, Key: "GORDON_AUTH_TOKEN_SECRET", Value: "split-registry-test-signing-secret-0123456789"}}, ExcludeEnv: splitNonRuntimeEnvironment, ReadinessProbe: ReadinessProbe{TCPAddress: fmt.Sprintf("127.0.0.1:%d", f.registryPort)}}
	if err := f.registry.Start(ctx, "serve", "--role", "registry", "--config", f.registryConfig); err != nil {
		t.Fatal(err)
	}
	ready, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := f.registry.WaitReady(ready); err != nil {
		t.Fatalf("registry readiness: %v logs=%s", err, f.logs(f.registry))
	}
}
func (f *splitRegistryFixture) restartRegistry(t *testing.T, ctx context.Context) {
	if f.registry != nil {
		stop, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := f.registry.Stop(stop); err != nil {
			t.Fatal(err)
		}
		f.registry = nil
	}
	f.startRegistry(t, ctx)
}
func (f *splitRegistryFixture) startRuntime(t *testing.T, ctx context.Context) {
	t.Helper()
	if f.runtime != nil {
		return
	}
	f.runtime = &GordonInstance{BinaryPath: f.binary, ConfigPath: f.controlConfig, DataDir: f.data, WorkingDir: f.root, Env: []string{"HOME=" + filepath.Join(f.root, "home-runtime"), "DOCKER_HOST=unix://" + f.engine.socket}, SensitiveEnv: []SensitiveEnvironment{{Side: SideNew, Key: "GORDON_AUTH_TOKEN_SECRET", Value: "split-registry-test-signing-secret-0123456789"}}, RuntimeRequired: true, ExcludeEnv: []string{"PODMAN_HOST", "CONTAINER_HOST", "XDG_RUNTIME_DIR"}, ReadinessProbe: ReadinessProbe{TCPAddress: fmt.Sprintf("127.0.0.1:%d", f.runtimePort)}}
	if err := f.runtime.Start(ctx, "serve", "--role", "runtime", "--config", f.controlConfig); err != nil {
		t.Fatal(err)
	}
	ready, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := f.runtime.WaitReady(ready); err != nil {
		t.Fatalf("runtime readiness: %v logs=%s", err, f.logs(f.runtime))
	}
	f.wait(t, func() bool { return f.processOwnsSocket(f.runtime, f.engine.socket) }, "runtime Docker fixture fd")
	f.assertRuntimeStateAuthenticated(t, ctx)
}

func (f *splitRegistryFixture) assertRuntimeStateAuthenticated(t *testing.T, ctx context.Context) {
	t.Helper()
	creds, err := grpcauth.NewInsecureBearerTokenCredentials(f.runtimeToken)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", f.runtimePort), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithPerRPCCredentials(creds))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stream, err := runtimev1.NewRuntimeServiceClient(conn).WatchActualState(ctx, &runtimev1.WatchActualStateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("authenticated runtime state stream: %v", err)
	}
}

func (f *splitRegistryFixture) startControl(t *testing.T, ctx context.Context) {
	t.Helper()
	if f.control != nil {
		return
	}
	f.control = &GordonInstance{BinaryPath: f.binary, ConfigPath: f.controlConfig, DataDir: f.data, WorkingDir: f.root, Env: []string{"HOME=" + filepath.Join(f.root, "home-control")}, SensitiveEnv: []SensitiveEnvironment{{Side: SideNew, Key: "GORDON_AUTH_TOKEN_SECRET", Value: "split-registry-test-signing-secret-0123456789"}}, ExcludeEnv: splitNonRuntimeEnvironment, ReadinessProbe: ReadinessProbe{TCPAddress: fmt.Sprintf("127.0.0.1:%d", f.controlPort)}}
	if err := f.control.Start(ctx, "serve", "--role", "control", "--config", f.controlConfig); err != nil {
		t.Fatal(err)
	}
	ready, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := f.control.WaitReady(ready); err != nil {
		t.Fatalf("control readiness: %v logs=%s", err, f.logs(f.control))
	}
}
func (f *splitRegistryFixture) stopControl(t *testing.T) {
	t.Helper()
	if f.control == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.control.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	f.control = nil
}

func (f *splitRegistryFixture) restartControl(t *testing.T, ctx context.Context) {
	t.Helper()
	f.stopControl(t)
	f.startControl(t, ctx)
}

func (f *splitRegistryFixture) ageManualIntent(t *testing.T) {
	t.Helper()
	path := filepath.Join(f.data, "component-events.json")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var store struct {
		Processed map[string]time.Time `json:"processed"`
		Intents   map[string]time.Time `json:"intents"`
	}
	if err := json.Unmarshal(contents, &store); err != nil {
		t.Fatal(err)
	}
	if len(store.Intents) == 0 {
		t.Fatal("manual deployment intent was not persisted")
	}
	for image := range store.Intents {
		store.Intents[image] = time.Now().Add(-time.Minute).UTC()
	}
	aged, err := json.Marshal(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, aged, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *splitRegistryFixture) push(t *testing.T, ctx context.Context, repository, tag, domain string) {
	t.Helper()
	token, err := exchangeRegistryCompatibilityToken(ctx, fmt.Sprintf("http://127.0.0.1:%d", f.registryPort), f.adminToken, "split")
	if err != nil {
		t.Fatal(err)
	}
	auth := "Bearer " + token
	f.pushSequence++
	config := []byte(fmt.Sprintf(`{"config":{"Labels":{"gordon.domain":%q,"gordon.port":"8080","org.gordon.test.tag":%q,"org.gordon.test.push":%q}}}`, domain, tag, fmt.Sprintf("%d", f.pushSequence)))
	digest := "sha256:" + fmt.Sprintf("%x", sha256.Sum256(config))
	f.oci(t, ctx, http.MethodPost, "/v2/"+repository+"/blobs/uploads/", auth, nil, "", http.StatusAccepted)
	start := f.lastLocation
	f.oci(t, ctx, http.MethodPut, start+"?digest="+digest, auth, config, "application/octet-stream", http.StatusCreated)
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","size":%d,"digest":%q},"layers":[],"annotations":{"org.opencontainers.image.ref.name":%q}}`, len(config), digest, tag))
	f.oci(t, ctx, http.MethodPut, "/v2/"+repository+"/manifests/"+tag, auth, manifest, "application/vnd.oci.image.manifest.v1+json", http.StatusCreated)
}

var splitLastLocationMu sync.Mutex

func (f *splitRegistryFixture) oci(t *testing.T, ctx context.Context, method, path, auth string, body []byte, contentType string, want int) {
	req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%d", f.registryPort)+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", contentType)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("OCI %s %s: want %d got %d: %s", method, path, want, resp.StatusCode, b)
	}
	splitLastLocationMu.Lock()
	f.lastLocation = resp.Header.Get("Location")
	splitLastLocationMu.Unlock()
}

// lastLocation is serially accessed by this linear fixture.
func (f *splitRegistryFixture) manualDeploy(t *testing.T, ctx context.Context, domain string) {
	accessToken, err := exchangeAdminToken(ctx, fmt.Sprintf("http://127.0.0.1:%d", f.registryPort), f.adminToken, "split")
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/admin/deploy/%s", f.adminPort, domain), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manual deploy HTTP %d", resp.StatusCode)
	}
}
func (f *splitRegistryFixture) wait(t *testing.T, check func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; outbox=%s; route_updated=%t; runtime_deploys=%d; registry=%s; control=%s; runtime=%s", what, f.outboxState(), f.routeUpdated(), f.engine.count(), f.logs(f.registry), f.logs(f.control), f.logs(f.runtime))
}

// outboxState intentionally reports only file count and basenames: diagnostics
// must establish durable replay state without leaking event payloads or paths.
func (f *splitRegistryFixture) outboxState() string {
	entries, err := os.ReadDir(filepath.Join(f.data, "registry", "event-outbox"))
	if err != nil {
		return "unavailable"
	}
	return fmt.Sprintf("count=%d", len(entries))
}

func (f *splitRegistryFixture) routeUpdated() bool {
	data, err := os.ReadFile(f.controlConfig)
	return err == nil && strings.Contains(string(data), "split/app:v1")
}
func (f *splitRegistryFixture) logs(instance *GordonInstance) string {
	if instance == nil {
		return "absent"
	}
	// Keep captured process diagnostics available to a debugger without
	// exporting mutable paths, listener ports, OCI digests, or credentials.
	return fmt.Sprintf("captured=%t", instance.Logs() != "")
}

func (f *splitRegistryFixture) assertWrongScopeDenied(t *testing.T, ctx context.Context) {
	t.Helper()
	wrongScope := f.commandToken(t, ctx, "registry-status", "registry", "registry:status")
	creds, err := grpcauth.NewInsecureBearerTokenCredentials(wrongScope)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", f.controlPort), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithPerRPCCredentials(creds))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = eventsv1.NewEventServiceClient(conn).PublishEvent(ctx, &eventsv1.PublishEventRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("wrong component event scope: want permission denied, got %v", status.Code(err))
	}
}

var splitNonRuntimeEnvironment = []string{"DOCKER_HOST", "PODMAN_HOST", "CONTAINER_HOST", "XDG_RUNTIME_DIR"}

var splitRuntimeAuthorities = []string{"docker.sock", "podman.sock", "/run/docker", "/run/podman", "containerd", "crio"}

func (f *splitRegistryFixture) assertIsolation(t *testing.T) {
	for _, p := range []*GordonInstance{f.registry, f.control} {
		if p == nil || p.cmd == nil {
			t.Fatal("process unexpectedly absent")
		}
		env, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", p.cmd.Process.Pid))
		if err == nil {
			for _, entry := range strings.Split(string(env), "\x00") {
				lower := strings.ToLower(entry)
				for _, authority := range splitRuntimeAuthorities {
					if strings.Contains(lower, authority) {
						t.Fatalf("split process inherited runtime authority %q", authority)
					}
				}
			}
		}
		for _, target := range processSocketTargets(p.cmd.Process.Pid) {
			for _, authority := range splitRuntimeAuthorities {
				if strings.Contains(strings.ToLower(target), authority) {
					t.Fatalf("split process has runtime authority fd %q", authority)
				}
			}
		}
	}
	if f.runtime == nil || !f.processOwnsSocket(f.runtime, f.engine.socket) {
		t.Fatal("runtime does not own the exact Docker fixture fd")
	}
}
func (f *splitRegistryFixture) processOwnsSocket(instance *GordonInstance, socket string) bool {
	if instance == nil || instance.cmd == nil || instance.cmd.Process == nil {
		return false
	}
	if f.engine != nil && socket == f.engine.socket && f.engine.sawProcess(instance.cmd.Process.Pid) {
		return true
	}
	for _, target := range processSocketTargets(instance.cmd.Process.Pid) {
		if target == socket {
			return true
		}
	}
	return false
}

// processSocketTargets resolves socket fd inodes through procfs' Unix socket
// table. Direct fd links only say socket:[inode], which is insufficient for an
// isolation assertion.
func processSocketTargets(pid int) []string {
	contents, err := os.ReadFile(fmt.Sprintf("/proc/%d/net/unix", pid))
	if err != nil {
		return nil
	}
	paths := make(map[string]string)
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 7 && strings.HasPrefix(fields[6], "/") {
			paths[fields[5]] = fields[6]
		}
	}
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return nil
	}
	result := make([]string, 0, len(fds))
	for _, fd := range fds {
		target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name()))
		if err != nil {
			continue
		}
		if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
			inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
			if socketPath := paths[inode]; socketPath != "" {
				result = append(result, socketPath)
				continue
			}
		}
		result = append(result, target)
	}
	return result
}

func (f *splitRegistryFixture) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, instance := range []*GordonInstance{f.registry, f.control, f.runtime} {
		if instance != nil {
			if err := instance.Stop(ctx); err != nil {
				f.t.Errorf("stop split process: %v", err)
			}
		}
	}
	f.engine.close()
	for _, port := range []int{f.registryPort, f.controlPort, f.adminPort} {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			f.t.Errorf("split listener leaked on port %d", port)
			continue
		}
		_ = listener.Close()
	}
}
func splitPort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
func splitDirHasFile(root string) bool {
	found := false
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			found = true
		}
		return nil
	})
	return found
}
