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
	"google.golang.org/protobuf/types/known/timestamppb"
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
	f.startControl(t, ctx)
	// Control availability replays the restrictive durable entry through the
	// authenticated production client. Wait for the acknowledgement before
	// interrupting that in-flight delivery; an interrupted RPC can persist a
	// route before its runtime command and is deliberately retried instead.
	f.wait(t, func() bool { return f.runtime.count() == 1 }, "replayed runtime deploy")
	f.wait(t, f.routeUpdated, "safe auto-route snapshot")
	f.wait(t, func() bool { return !splitDirHasFile(filepath.Join(f.data, "registry", "event-outbox")) }, "outbox acknowledgement")
	// A process restart after acknowledgement must not turn the replay into a
	// second runtime mutation.
	f.restartRegistry(t, ctx)
	time.Sleep(300 * time.Millisecond)
	if got := f.runtime.count(); got != 1 {
		t.Fatalf("replay was not exactly once: got %d runtime mutations", got)
	}
	if !f.runtime.has("split.example.test", "split/app:v1") {
		t.Fatal("runtime did not receive exact desired deployment")
	}

	// A preview-tagged OCI manifest is classified by the production dispatcher,
	// not auto-route. Its durable preview route and exact runtime deployment
	// prove the preview annotation/tag path remains connected to control.
	f.push(t, ctx, "split/app", "preview-ci", "split.example.test")
	f.wait(t, func() bool { return f.runtime.has("split--ci.example.test", "split/app:preview-ci") }, "preview runtime deploy")

	// Exercise the production HTTP admin -> dispatcher manual intent path. The
	// matching push must be consumed; an unrelated image still reaches automation.
	beforeManual := f.runtime.count()
	f.manualDeploy(t, ctx, "split.example.test")
	f.wait(t, func() bool { return f.runtime.count() == beforeManual+1 }, "manual runtime deploy")
	manualCount := f.runtime.count()
	f.push(t, ctx, "split/app", "v1", "split.example.test")
	time.Sleep(300 * time.Millisecond)
	if got := f.runtime.count(); got != manualCount {
		t.Fatalf("manual matching push was not suppressed: got %d runtime mutations, want %d", got, manualCount)
	}
	f.push(t, ctx, "split/other", "manual", "other.example.test")
	f.wait(t, func() bool { return f.runtime.count() > manualCount }, "unrelated image automation")
	f.assertWrongScopeDenied(t, ctx)
	f.assertIsolation(t)
}

type splitRegistryFixture struct {
	t                                                 *testing.T
	root, data, binary, registryConfig, controlConfig string
	registryPort, controlPort, adminPort              int
	registry, control                                 *GordonInstance
	runtime                                           *splitRuntime
	eventToken, adminToken, lastLocation              string
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
	f := &splitRegistryFixture{t: t, root: root, data: data, binary: filepath.Join(root, "gordon"), registryPort: ports[0], controlPort: ports[1], adminPort: ports[2]}
	if err := securityBuildCandidate(ctx, projectRoot(t), f.binary); err != nil {
		t.Fatal(err)
	}
	f.runtime = newSplitRuntime(t, ports[3])
	defer func() {
		if t.Failed() {
			f.runtime.close()
		}
	}()
	f.controlConfig = filepath.Join(root, "control.toml")
	f.registryConfig = filepath.Join(root, "registry.toml")
	if err := os.WriteFile(f.controlConfig, []byte(f.controlTOML()), 0o600); err != nil {
		t.Fatal(err)
	}
	// Generate real persisted component and admin credentials through the candidate CLI.
	f.eventToken = f.commandToken(t, ctx, "registry", "registry", "registry:event:publish")
	f.adminToken = f.authToken(t, ctx)
	if err := os.WriteFile(f.registryConfig, []byte(f.registryTOML()), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *splitRegistryFixture) controlTOML() string {
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
endpoint = "127.0.0.1:%d"
token = "runtime-test-token"
insecure = true
[auto]
enabled = true
allowed_domains = ["*.example.test"]
[auto.preview]
enabled = true
tag_patterns = ["preview-*"]
`, f.data, f.controlPort, f.adminPort, f.runtime.port)
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
	c, err := CaptureCommand(ctx, CommandCaptureRequest{BinaryPath: f.binary, Args: []string{"auth", "component-token", "create", "--config", f.controlConfig, "--name", name, "--role", role, "--scope", scope, "--json"}, Dir: f.root, Source: "component token", Level: LevelSecurityNegative})
	if err != nil {
		t.Fatal(err)
	}
	raw := c.RawValue().(map[string]any)
	if raw["exitCode"] != 0 {
		t.Fatalf("component token creation failed: %v", raw["stderr"])
	}
	var v struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(raw["stdout"].(string)), &v); err != nil || v.Token == "" {
		t.Fatalf("decode component token: %v", err)
	}
	return v.Token
}
func (f *splitRegistryFixture) authToken(t *testing.T, ctx context.Context) string {
	fixture := SideFixture{Root: f.root, HomeDir: filepath.Join(f.root, "home-control"), DataDir: f.data, ConfigPath: f.controlConfig, Env: []string{"HOME=" + filepath.Join(f.root, "home-control")}}
	token, err := generateAdminToken(ctx, f.binary, fixture, "split")
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
	f.registry = &GordonInstance{BinaryPath: f.binary, ConfigPath: f.registryConfig, DataDir: f.data, WorkingDir: f.root, Env: []string{"HOME=" + filepath.Join(f.root, "home-registry"), "GORDON_AUTH_TOKEN_SECRET=split-registry-test-signing-secret-0123456789"}, ExcludeEnv: splitRuntimeEnvironment, ReadinessProbe: ReadinessProbe{TCPAddress: fmt.Sprintf("127.0.0.1:%d", f.registryPort)}}
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
func (f *splitRegistryFixture) startControl(t *testing.T, ctx context.Context) {
	t.Helper()
	if f.control != nil {
		return
	}
	f.control = &GordonInstance{BinaryPath: f.binary, ConfigPath: f.controlConfig, DataDir: f.data, WorkingDir: f.root, Env: []string{"HOME=" + filepath.Join(f.root, "home-control"), "GORDON_AUTH_TOKEN_SECRET=split-registry-test-signing-secret-0123456789"}, ExcludeEnv: splitRuntimeEnvironment, ReadinessProbe: ReadinessProbe{TCPAddress: fmt.Sprintf("127.0.0.1:%d", f.controlPort)}}
	if err := f.control.Start(ctx, "serve", "--role", "control", "--config", f.controlConfig); err != nil {
		t.Fatal(err)
	}
	ready, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := f.control.WaitReady(ready); err != nil {
		t.Fatalf("control readiness: %v logs=%s", err, f.logs(f.control))
	}
}
func (f *splitRegistryFixture) push(t *testing.T, ctx context.Context, repository, tag, domain string) {
	t.Helper()
	token, err := exchangeRegistryCompatibilityToken(ctx, fmt.Sprintf("http://127.0.0.1:%d", f.registryPort), f.adminToken, "split")
	if err != nil {
		t.Fatal(err)
	}
	auth := "Bearer " + token
	config := []byte(fmt.Sprintf(`{"config":{"Labels":{"gordon.domain":%q,"gordon.port":"8080","org.gordon.test.tag":%q}}}`, domain, tag))
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
	t.Fatalf("timed out waiting for %s; outbox=%s; route_updated=%t; runtime_commands=%d deploys=%d; registry=%s; control=%s", what, f.outboxState(), f.routeUpdated(), f.runtime.commands(), f.runtime.count(), f.logs(f.registry), f.logs(f.control))
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

var splitRuntimeEnvironment = []string{"DOCKER_HOST", "PODMAN_HOST", "CONTAINER_HOST"}

func (f *splitRegistryFixture) assertIsolation(t *testing.T) {
	for _, p := range []*GordonInstance{f.registry, f.control} {
		if p == nil || p.cmd == nil {
			t.Fatal("process unexpectedly absent")
		}
		env, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", p.cmd.Process.Pid))
		if err == nil {
			for _, key := range splitRuntimeEnvironment {
				if strings.Contains(string(env), key+"=") {
					t.Fatalf("split process inherited runtime environment %s", key)
				}
			}
			if strings.Contains(strings.ToLower(string(env)), "docker.sock") {
				t.Fatal("split process inherited docker socket environment")
			}
		}
		fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", p.cmd.Process.Pid))
		if err == nil {
			for _, fd := range fds {
				target, _ := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", p.cmd.Process.Pid, fd.Name()))
				if strings.Contains(strings.ToLower(target), "docker.sock") {
					t.Fatal("split process has runtime socket fd")
				}
			}
		}
	}
}
func (f *splitRegistryFixture) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, instance := range []*GordonInstance{f.registry, f.control} {
		if instance != nil {
			if err := instance.Stop(ctx); err != nil {
				f.t.Errorf("stop split process: %v", err)
			}
		}
	}
	f.runtime.close()
	for _, port := range []int{f.registryPort, f.controlPort, f.adminPort, f.runtime.port} {
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

type splitRuntime struct {
	runtimev1.UnimplementedRuntimeServiceServer
	listener net.Listener
	server   *grpc.Server
	port     int
	mu       sync.Mutex
	deploys  []*runtimev1.DeployRouteCommand
	applied  int
}

func newSplitRuntime(t *testing.T, port int) *splitRuntime {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	r := &splitRuntime{listener: l, server: grpc.NewServer(), port: port}
	runtimev1.RegisterRuntimeServiceServer(r.server, r)
	go func() { _ = r.server.Serve(l) }()
	return r
}
func (r *splitRuntime) ApplyCommand(_ context.Context, req *runtimev1.ApplyCommandRequest) (*runtimev1.ApplyCommandResponse, error) {
	r.mu.Lock()
	r.applied++
	if d := req.GetDeployRoute(); d != nil {
		r.deploys = append(r.deploys, d)
	}
	r.mu.Unlock()
	return &runtimev1.ApplyCommandResponse{Result: &runtimev1.RuntimeCommandResult{CommandId: "split-runtime", Status: "applied", CompletedAt: timestamppb.Now()}}, nil
}
func (r *splitRuntime) WatchActualState(_ *runtimev1.WatchActualStateRequest, s grpc.ServerStreamingServer[runtimev1.ActualStateSnapshot]) error {
	if err := s.Send(&runtimev1.ActualStateSnapshot{Generation: 1, StateVersion: "split-state", SourceComponentId: "runtime-test", ObservedAt: timestamppb.Now()}); err != nil {
		return err
	}
	<-s.Context().Done()
	return s.Context().Err()
}
func (r *splitRuntime) count() int    { r.mu.Lock(); defer r.mu.Unlock(); return len(r.deploys) }
func (r *splitRuntime) commands() int { r.mu.Lock(); defer r.mu.Unlock(); return r.applied }
func (r *splitRuntime) has(domain, image string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.deploys {
		if d.Domain == domain && d.Image == image {
			return true
		}
	}
	return false
}
func (r *splitRuntime) close() { r.server.Stop(); _ = r.listener.Close() }
