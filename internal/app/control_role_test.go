package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	edgev1 "github.com/bnema/gordon/api/gordon/edge/v1"
	eventsv1 "github.com/bnema/gordon/api/gordon/events/v1"
	runtimev1 "github.com/bnema/gordon/api/gordon/runtime/v1"
	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	runtimegrpc "github.com/bnema/gordon/internal/adapters/in/grpc/runtime"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/testutils/grpctest"
	"github.com/bnema/gordon/internal/usecase/edgesnapshot"
)

// TestControlRoleBringup exercises the split control process across its real
// HTTP and gRPC listeners. The only injected seams are listener/token setup;
// runtime commands, state streaming, drain relay, event dispatch and edge
// snapshots all use their production gRPC adapters.
func TestControlRoleBringup(t *testing.T) {
	t.Setenv(TokenSecretEnvVar, "control-role-test-token-secret-at-least-32-bytes")
	runtime := startControlRoleRuntime(t)
	defer runtime.stop()

	configPath := writeControlRoleConfig(t, runtime.listener.Addr().String())
	state := newControlRoleStateSubscriber()
	runtime.state = state

	listeners := make(chan net.Listener, 2)
	validator := controlRoleComponentValidator{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runControlWithDependencies(ctx, configPath, controlRoleDependencies{
			listen: func(network, address string) (net.Listener, error) {
				listener, err := net.Listen(network, address)
				if err == nil {
					listeners <- listener
				}
				return listener, err
			},
			newComponentTokenValidator: func(Config, zerowrap.Logger) (interceptors.ComponentTokenValidator, error) {
				return validator, nil
			},
			newSnapshotHub:             edgesnapshot.NewSnapshotHub,
			newEventHub:                productionControlRoleDependencies().newEventHub,
			newTrafficGraphHub:         edgesnapshot.NewTrafficGraphHub,
			newRuntimeStateSubscriber:  createRuntimeStateSubscriber,
			newRuntimeDrainAckReceiver: createRuntimeRouteDrainAckReceiver,
			// Config persistence is exercised below; isolate it from Viper's
			// asynchronous file watcher, whose lifecycle has dedicated tests.
			setupConfigHotReload:    func(context.Context, configWatcher, loadedConfigApplier) error { return nil },
			newSnapshotProducer:     edgesnapshot.NewProducer,
			newTrafficGraphProducer: edgesnapshot.NewTrafficGraphProducer,
		})
	}()

	// Startup must wait for the runtime's initial actual-state snapshot instead
	// of reporting a control listener healthy from a mere runtime TCP connection.
	select {
	case listener := <-listeners:
		listener.Close()
		t.Fatal("control listener started before runtime initial state")
	case <-time.After(75 * time.Millisecond):
	}
	state.Publish(phase4ManagedRuntimeSnapshot(1, "runtime-container"))
	grpcListener := <-listeners
	httpListener := <-listeners

	grpcConn, err := grpc.NewClient(grpcListener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer grpcConn.Close()
	httpBase := "http://" + httpListener.Addr().String()

	// The health endpoint is now aggregate-ready because both the initial state
	// and runtime health RPC have completed.
	health, err := http.Get(httpBase + "/healthz")
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, health.StatusCode)
	health.Body.Close()

	assertControlAdminDenied(t, httpBase, "")
	assertControlAdminDenied(t, httpBase, "Bearer definitely-invalid")
	adminToken := controlRoleAdminToken(t, configPath)
	assertControlAdminConfig(t, httpBase, adminToken)
	assertControlAdminSurfaceResponses(t, httpBase, adminToken)
	assertControlRemoteSmoke(t, httpBase, controlRoleRemoteToken(t, configPath), runtime.worker)

	deploy := controlRoleRequest(t, http.MethodPost, httpBase+"/admin/deploy/app.example.com", adminToken)
	require.Equal(t, http.StatusOK, deploy.StatusCode)
	deploy.Body.Close()
	require.Eventually(t, func() bool { return runtime.worker.calls() == 2 }, time.Second, time.Millisecond)
	command := runtime.worker.command(1)
	assert.Equal(t, "app.example.com", command.Domain)
	assert.Equal(t, "app:v1", command.Image)
	assert.True(t, command.InternalDeploy)

	events := eventsv1.NewEventServiceClient(grpcConn)
	event := controlRoleImageEvent()
	validEventContext := grpctest.AuthenticatedContext(context.Background(), grpctest.LocalComponentToken)
	ack, err := events.PublishEvent(validEventContext, event)
	require.NoError(t, err)
	assert.False(t, ack.GetAck().GetDuplicate())
	// The manual deploy created a durable intent for app:v1, so the matching
	// registry push is consumed without issuing a second runtime command.
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, 2, runtime.worker.calls(), "matching push must be suppressed after manual deploy")
	ack, err = events.PublishEvent(validEventContext, event)
	require.NoError(t, err)
	assert.True(t, ack.GetAck().GetDuplicate())
	assert.Equal(t, 2, runtime.worker.calls(), "duplicate image event must not repeat its production effect")
	assertControlRemoteMethodMatrix(t, remote.NewClient(httpBase, remote.WithToken(controlRoleRemoteToken(t, configPath))), runtime.worker)

	wrongScopeContext := grpctest.AuthenticatedContext(context.Background(), "edge-only-token")
	_, err = events.PublishEvent(wrongScopeContext, controlRoleImageEvent())
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	edges := edgev1.NewEdgeServiceClient(grpcConn)
	edgeBaseContext, cancelEdgeStreams := context.WithCancel(context.Background())
	edgeContext := grpctest.AuthenticatedContext(edgeBaseContext, "edge-token")
	routes, err := edges.WatchRouteSnapshots(edgeContext, &edgev1.WatchRouteSnapshotsRequest{})
	require.NoError(t, err)
	routeSnapshot, err := routes.Recv()
	require.NoError(t, err)
	assert.EqualValues(t, 1, routeSnapshot.GetGeneration())
	traffic, err := edges.WatchTrafficGraphs(edgeContext, &edgev1.WatchTrafficGraphsRequest{})
	require.NoError(t, err)
	trafficSnapshot, err := traffic.Recv()
	require.NoError(t, err)
	assert.NotZero(t, trafficSnapshot.GetGeneration())

	cancelEdgeStreams()
	cancel()
	require.NoError(t, <-done)
	grpcConn.Close()
	for _, address := range []string{grpcListener.Addr().String(), httpListener.Addr().String()} {
		listener, listenErr := net.Listen("tcp", address)
		require.NoError(t, listenErr, "listener was not released: %s", address)
		require.NoError(t, listener.Close())
	}
}

// TestControlRoleMigrationPreflightProductionListener exercises the production
// control HTTP listener through the real authenticated runtime gRPC adapter.
// The fake reports only the runtime-owned, sanitized probe boundary; it does
// not provide a socket or permit migration mutations.
func TestControlRoleMigrationPreflightProductionListener(t *testing.T) {
	const (
		rawSocket = "/run/user/1000/podman/podman.sock"
		rawSecret = "fixture-preflight-secret"
	)
	passing := out.RuntimeEnvironment{
		Engine: "podman", Rootless: true, APIReachable: true, ImageAvailable: true,
		ImagePullable: true, NetworkFeasible: true, DiskAvailable: 1 << 30, DiskSufficient: true,
	}
	tests := []struct {
		name       string
		report     out.RuntimeEnvironment
		probeErr   error
		wantStatus int
	}{
		{name: "rootless Podman passes", report: passing, wantStatus: http.StatusOK},
		{name: "Docker is rejected", report: out.RuntimeEnvironment{Engine: "docker", Rootless: true, APIReachable: true, ImageAvailable: true, ImagePullable: true, NetworkFeasible: true, DiskSufficient: true}, wantStatus: http.StatusUnprocessableEntity},
		{name: "rootful Podman is rejected", report: out.RuntimeEnvironment{Engine: "podman", APIReachable: true, ImageAvailable: true, ImagePullable: true, NetworkFeasible: true, DiskSufficient: true}, wantStatus: http.StatusUnprocessableEntity},
		{name: "unavailable runtime is rejected", probeErr: errors.New("runtime unavailable at " + rawSocket + " token=" + rawSecret), wantStatus: http.StatusUnprocessableEntity},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(TokenSecretEnvVar, "migration-production-listener-token-secret-at-least-32-bytes")
			probe := &controlRoleMigrationProbe{report: tc.report, err: tc.probeErr}
			runtime := startControlRoleRuntimeWithProbe(t, probe)
			defer runtime.stop()
			runtime.state = newControlRoleStateSubscriber()
			configPath := writeControlRoleConfig(t, runtime.listener.Addr().String())

			listeners := make(chan net.Listener, 2)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				done <- runControlWithDependencies(ctx, configPath, controlRoleDependencies{
					listen: func(network, address string) (net.Listener, error) {
						listener, err := net.Listen(network, address)
						if err == nil {
							listeners <- listener
						}
						return listener, err
					},
					newComponentTokenValidator: func(Config, zerowrap.Logger) (interceptors.ComponentTokenValidator, error) {
						return controlRoleComponentValidator{}, nil
					},
					newSnapshotHub:             edgesnapshot.NewSnapshotHub,
					newEventHub:                productionControlRoleDependencies().newEventHub,
					newTrafficGraphHub:         edgesnapshot.NewTrafficGraphHub,
					newRuntimeStateSubscriber:  createRuntimeStateSubscriber,
					newRuntimeDrainAckReceiver: createRuntimeRouteDrainAckReceiver,
					setupConfigHotReload:       func(context.Context, configWatcher, loadedConfigApplier) error { return nil },
					newSnapshotProducer:        edgesnapshot.NewProducer,
					newTrafficGraphProducer:    edgesnapshot.NewTrafficGraphProducer,
				})
			}()
			t.Cleanup(func() {
				cancel()
				require.NoError(t, <-done)
			})

			runtime.state.Publish(phase4ManagedRuntimeSnapshot(1, "migration-runtime"))
			_ = controlRoleListener(t, listeners) // production gRPC listener is live too.
			httpListener := controlRoleListener(t, listeners)
			baseURL := "http://" + httpListener.Addr().String()
			adminToken := controlRoleAdminToken(t, configPath)
			cliToken := controlRoleRemoteToken(t, configPath)

			if tc.wantStatus == http.StatusOK {
				assertMigrationPreflightAuthContract(t, baseURL, configPath)
				require.Zero(t, probe.calls(), "authentication failures must not reach runtime")
			}

			configBefore, err := os.ReadFile(configPath)
			require.NoError(t, err)
			checkpointPath := filepath.Join(filepath.Dir(configPath), "migration", "checkpoint.json")
			_, err = os.Stat(checkpointPath)
			require.ErrorIs(t, err, os.ErrNotExist)

			response := controlRoleRequest(t, http.MethodGet, baseURL+"/admin/migration/plan", adminToken)
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, response.StatusCode)
			require.Contains(t, response.Header.Get("Content-Type"), "application/json")
			assert.NotContains(t, string(body), rawSocket)
			assert.NotContains(t, string(body), rawSecret)

			var report MigrationPreflightReport
			require.NoError(t, json.Unmarshal(body, &report))
			assert.Equal(t, tc.wantStatus == http.StatusOK, report.Ready)
			assertMigrationPreflightReport(t, report)
			require.Equal(t, 1, probe.calls(), "plan must make exactly one runtime probe RPC")
			if tc.wantStatus == http.StatusOK {
				probe.reset()
				// This executes the public Cobra command against the production
				// listener rather than a CLI fake or an httptest handler.
				cliBody := controlRoleMigrationPlanCLI(t, baseURL, cliToken)
				assert.NotContains(t, string(cliBody), rawSocket)
				assert.NotContains(t, string(cliBody), rawSecret)
				var cliReport MigrationPreflightReport
				require.NoError(t, json.Unmarshal(cliBody, &cliReport))
				assert.Equal(t, report, cliReport)
				require.Equal(t, 1, probe.calls(), "CLI plan must make exactly one runtime probe RPC")
			}
			assert.Zero(t, runtime.worker.calls(), "dry-run must not issue runtime commands")
			configAfter, err := os.ReadFile(configPath)
			require.NoError(t, err)
			assert.Equal(t, configBefore, configAfter, "preflight must not rewrite config")
			_, err = os.Stat(checkpointPath)
			require.ErrorIs(t, err, os.ErrNotExist, "plan must not create a checkpoint")
		})
	}
}

func controlRoleMigrationPlanCLI(t *testing.T, baseURL, token string) []byte {
	t.Helper()
	command := exec.Command("go", "run", ".", "--remote", baseURL, "--token", token, "migrate", "plan", "--json")
	command.Dir = filepath.Join("..", "..")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	return output
}

func controlRoleListener(t *testing.T, listeners <-chan net.Listener) net.Listener {
	t.Helper()
	select {
	case listener := <-listeners:
		return listener
	case <-time.After(time.Second):
		t.Fatal("control listener did not start")
		return nil
	}
}

func assertMigrationPreflightAuthContract(t *testing.T, baseURL, configPath string) {
	t.Helper()
	limitedToken := controlRoleScopedToken(t, configPath, "migration-limited", []string{"admin:routes:read"})
	for _, tc := range []struct {
		name, authorization string
		want                int
	}{
		{name: "missing", want: http.StatusUnauthorized},
		{name: "malformed", authorization: "Bearer malformed-token", want: http.StatusUnauthorized},
		{name: "forbidden", authorization: limitedToken, want: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := controlRoleRequest(t, http.MethodGet, baseURL+"/admin/migration/plan", tc.authorization)
			defer response.Body.Close()
			require.Equal(t, tc.want, response.StatusCode)
			assertControlJSONShape(t, response, "error")
		})
	}
}

func assertMigrationPreflightReport(t *testing.T, report MigrationPreflightReport) {
	t.Helper()
	require.Len(t, report.Checks, 12)
	for _, check := range report.Checks {
		assert.NotEmpty(t, check.Name)
		assert.NotEmpty(t, check.Category)
		assert.NotEmpty(t, check.Remediation)
		assert.Contains(t, []PreflightStatus{PreflightPass, PreflightFail, PreflightWarning}, check.Status)
	}
	runtimeCheck := report.Checks[0]
	assert.Equal(t, "rootless_podman", runtimeCheck.Name)
	assert.Equal(t, PreflightRuntime, runtimeCheck.Category)
	if report.Ready {
		assert.Equal(t, PreflightPass, runtimeCheck.Status)
	} else {
		assert.Equal(t, PreflightFail, runtimeCheck.Status)
		assert.Contains(t, runtimeCheck.Remediation, "rootless Podman")
	}
}

type controlRoleMigrationProbe struct {
	mu     sync.Mutex
	report out.RuntimeEnvironment
	err    error
	count  int
}

func (p *controlRoleMigrationProbe) ProbeRuntimeEnvironment(context.Context) (out.RuntimeEnvironment, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.count++
	return p.report, p.err
}

func (p *controlRoleMigrationProbe) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.count
}

func (p *controlRoleMigrationProbe) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.count = 0
}

func writeControlRoleConfig(t *testing.T, runtimeEndpoint string) string {
	t.Helper()
	dataDir := t.TempDir()
	configPath := filepath.Join(dataDir, "gordon.toml")
	contents := `
[server]
data_dir = "` + dataDir + `"

[control]
listen_address = "127.0.0.1:0"
insecure_tls = true

[control.http]
listen_address = "127.0.0.1:0"
insecure_tls = true

[runtime]
endpoint = "` + runtimeEndpoint + `"
token = "` + grpctest.LocalComponentToken + `"
insecure = true

[auth]
enabled = true
secrets_backend = "unsafe"

[api.rate_limit]
enabled = true
global_rps = 10000
per_ip_rps = 10000
burst = 10000

[routes]
"app.example.com" = "app:v1"
`
	require.NoError(t, os.WriteFile(configPath, []byte(contents), 0600))
	return configPath
}

func controlRoleAdminToken(t *testing.T, configPath string) string {
	t.Helper()
	return controlRoleScopedToken(t, configPath, "control-role-admin", []string{"admin:*:*"})
}

func controlRoleScopedToken(t *testing.T, configPath, subject string, scopes []string) string {
	t.Helper()
	_, cfg, err := initConfig(configPath)
	require.NoError(t, err)
	_, service, err := createAuthService(context.Background(), cfg, zerowrap.Default())
	require.NoError(t, err)
	token, err := service.GenerateAccessToken(context.Background(), subject, scopes, time.Minute)
	require.NoError(t, err)
	return token
}

func controlRoleRemoteToken(t *testing.T, configPath string) string {
	t.Helper()
	_, cfg, err := initConfig(configPath)
	require.NoError(t, err)
	_, service, err := createAuthService(context.Background(), cfg, zerowrap.Default())
	require.NoError(t, err)
	token, err := service.GenerateToken(context.Background(), "control-role-remote", []string{"admin:*:*"}, 24*time.Hour)
	require.NoError(t, err)
	return token
}

func assertControlAdminDenied(t *testing.T, baseURL, authorization string) {
	t.Helper()
	response := controlRoleRequest(t, http.MethodGet, baseURL+"/admin/config", authorization)
	defer response.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
	assert.Contains(t, response.Header.Get("Content-Type"), "application/json")
	var body map[string]string
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	assert.NotEmpty(t, body["error"])
}

func assertControlAdminConfig(t *testing.T, baseURL, token string) {
	t.Helper()
	for _, path := range []string{"/admin/config", "/admin/routes"} {
		response := controlRoleRequest(t, http.MethodGet, baseURL+path, token)
		require.Equal(t, http.StatusOK, response.StatusCode, path)
		response.Body.Close()
	}
}

// assertControlAdminSurfaceResponses inventories every handler.matchRoute
// branch.  This is intentionally a listener-level auth matrix, not a handler
// unit test: a route is required to have the same JSON auth contract in the
// split control process as it has in a monolith.
func assertControlAdminSurfaceResponses(t *testing.T, baseURL, token string) {
	t.Helper()
	tests := []adminRouteContract{
		{http.MethodGet, "/admin/networks", "networks", http.StatusOK},
		{http.MethodGet, "/admin/status", "routes", http.StatusOK},
		{http.MethodGet, "/admin/health", "health", http.StatusOK},
		{http.MethodPost, "/admin/bootstrap", "error", http.StatusBadRequest},
		{http.MethodPost, "/admin/reload", "status", http.StatusOK},
		{http.MethodGet, "/admin/config", "server", http.StatusOK},
		{http.MethodGet, "/admin/auth/verify", "valid", http.StatusOK},
		{http.MethodGet, "/admin/volumes", "error", http.StatusServiceUnavailable},
		{http.MethodPost, "/admin/volumes/prune", "error", http.StatusBadRequest},
		{http.MethodGet, "/admin/attachments/orphans", "error", http.StatusServiceUnavailable},
		{http.MethodPost, "/admin/attachments/prune", "error", http.StatusServiceUnavailable},
		{http.MethodGet, "/admin/tls/status", "acme_enabled", http.StatusOK},
		{http.MethodGet, "/admin/traffic/status", "last_reload_status", http.StatusOK},
		{http.MethodGet, "/admin/backups", "error", http.StatusServiceUnavailable},
		{http.MethodGet, "/admin/attachments/by-image/app", "targets", http.StatusOK},
		{http.MethodGet, "/admin/attachments", "attachments", http.StatusOK},
		{http.MethodGet, "/admin/routes/by-image/app", "routes", http.StatusOK},
		{http.MethodGet, "/admin/routes?detailed=true", "routes", http.StatusOK},
		{http.MethodGet, "/admin/routes/app.example.com/attachments", "attachments", http.StatusOK},
		{http.MethodGet, "/admin/routes/app.example.com/cleanup", "error", http.StatusNotImplemented},
		{http.MethodGet, "/admin/secrets/app.example.com", "keys", http.StatusOK},
		{http.MethodPost, "/admin/deploy-intent/app:v1", "error", http.StatusServiceUnavailable},
		{http.MethodGet, "/admin/deploy/app.example.com", "error", http.StatusMethodNotAllowed},
		{http.MethodGet, "/admin/restart/app.example.com", "error", http.StatusMethodNotAllowed},
		{http.MethodGet, "/admin/tags/app", "error", http.StatusServiceUnavailable},
		{http.MethodGet, "/admin/images", "error", http.StatusServiceUnavailable},
		{http.MethodGet, "/admin/logs/app.example.com", "lines", http.StatusOK},
		{http.MethodGet, "/admin/autoroute/allowed-domains", "domains", http.StatusOK},
		{http.MethodGet, "/admin/previews", "error", http.StatusServiceUnavailable},
		{http.MethodDelete, "/admin/preview/preview.example.com", "error", http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			// Missing and malformed credentials must be indistinguishable to every
			// admin endpoint and retain the one-field error JSON shape.
			assertControlAdminAuthResponse(t, baseURL, test, "")
			assertControlAdminAuthResponse(t, baseURL, test, "Bearer definitely-invalid")
			response := controlRoleRequest(t, test.method, baseURL+test.path, token)
			defer response.Body.Close()
			require.Equal(t, test.validStatus, response.StatusCode)
			assertControlJSONShape(t, response, test.validField)
		})
	}
}

type adminRouteContract struct {
	method, path, validField string
	validStatus              int
}

func assertControlAdminAuthResponse(t *testing.T, baseURL string, test adminRouteContract, authorization string) {
	t.Helper()
	response := controlRoleRequest(t, test.method, baseURL+test.path, authorization)
	defer response.Body.Close()
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	assertControlJSONShape(t, response, "error")
}

func assertControlJSONShape(t *testing.T, response *http.Response, requiredField string) {
	t.Helper()
	require.Contains(t, response.Header.Get("Content-Type"), "application/json")
	var body map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	value, ok := body[requiredField]
	require.True(t, ok, "missing JSON field %q: %#v", requiredField, body)
	if requiredField == "error" {
		message, ok := value.(string)
		require.True(t, ok && message != "", "invalid error JSON shape: %#v", body)
		require.Len(t, body, 1, "error responses must not leak an alternate DTO")
	}
}

// assertControlRemoteSmoke covers each remote.Client command family against
// the production listener. Read methods verify their DTO shape; mutations are
// followed by a read of the durable config/runtime command rather than merely
// accepting a non-500 response.
func assertControlRemoteSmoke(t *testing.T, baseURL, token string, worker *controlRoleRuntimeWorker) {
	t.Helper()
	ctx := context.Background()
	client := remote.NewClient(baseURL, remote.WithToken(token))
	status, err := client.GetStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, status.Routes)
	routes, err := client.ListRoutes(ctx)
	require.NoError(t, err)
	require.Len(t, routes, 1)
	config, err := client.GetConfig(ctx)
	require.NoError(t, err)
	require.Len(t, config.Routes, 1)
	_, err = client.ListNetworks(ctx)
	require.NoError(t, err)
	_, err = client.GetTrafficStatus(ctx)
	require.NoError(t, err)
	_, err = client.GetTLSStatus(ctx)
	require.NoError(t, err)
	_, err = client.GetHealth(ctx)
	require.NoError(t, err)
	_, err = client.ListVolumes(ctx)
	requireRemoteUnavailable(t, err)
	_, err = client.ListImages(ctx)
	requireRemoteUnavailable(t, err)
	_, err = client.ListTags(ctx, "app")
	requireRemoteUnavailable(t, err)
	_, err = client.ListBackups(ctx, "")
	requireRemoteUnavailable(t, err)
	_, err = client.BackupStatus(ctx)
	requireRemoteUnavailable(t, err)
	_, err = client.ListVolumeBackups(ctx, "")
	requireRemoteUnavailable(t, err)
	_, err = client.VolumeBackupStatus(ctx)
	requireRemoteUnavailable(t, err)
	_, err = client.ListOrphanedAttachments(ctx)
	requireRemoteUnavailable(t, err)
	_, err = client.GetAllAttachmentsConfig(ctx)
	require.NoError(t, err)
	_, err = client.GetAutoRouteAllowedDomains(ctx)
	require.NoError(t, err)
	_, err = client.ListPreviews(ctx)
	requireRemoteUnavailable(t, err)
	verify, err := client.VerifyAuth(ctx)
	require.NoError(t, err)
	require.True(t, verify.Valid)

	// Deploy is the representative remote mutation: asserting the returned DTO
	// alone would miss a disconnected control-to-runtime bridge.
	deploy, err := client.Deploy(ctx, "app.example.com")
	require.NoError(t, err)
	require.Equal(t, "app.example.com", deploy.Domain)
	require.Eventually(t, func() bool { return worker.calls() == 1 }, time.Second, time.Millisecond)
	command := worker.command(0)
	require.Equal(t, "app:v1", command.Image)
	require.True(t, command.InternalDeploy)
}

// assertControlRemoteMethodMatrix is a concrete inventory of every exported
// remote.Client operation. It intentionally invokes methods (including aliases)
// through the production control listener: this catches mismatched paths, DTOs,
// nil services, and mutations that silently do nothing.
func assertControlRemoteMethodMatrix(t *testing.T, client *remote.Client, worker *controlRoleRuntimeWorker) {
	t.Helper()
	ctx := context.Background()
	const domainName = "remote-matrix.example.com"
	const image = "remote/matrix:v1"
	mustOK := func(name string, call func() error) {
		t.Helper()
		t.Run(name, func(t *testing.T) { require.NoError(t, call()) })
	}
	mustStatus := func(name string, want int, call func() error) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			err := call()
			var responseErr *remote.HTTPError
			require.ErrorAs(t, err, &responseErr)
			require.Equal(t, want, responseErr.StatusCode)
			require.NotEmpty(t, responseErr.Body)
		})
	}
	mustError := func(name, message string, call func() error) {
		t.Helper()
		t.Run(name, func(t *testing.T) { require.ErrorContains(t, call(), message) })
	}

	// Read-only route/config/status methods assert their actual DTO content.
	mustOK("ListRoutes", func() error { routes, err := client.ListRoutes(ctx); require.NotEmpty(t, routes); return err })
	mustOK("ListRoutesWithDetails", func() error {
		routes, err := client.ListRoutesWithDetails(ctx)
		require.NotEmpty(t, routes)
		return err
	})
	mustOK("ListNetworks", func() error { _, err := client.ListNetworks(ctx); return err })
	mustOK("ListAttachments", func() error { _, err := client.ListAttachments(ctx, "app.example.com"); return err })
	mustOK("GetRoute", func() error {
		route, err := client.GetRoute(ctx, "app.example.com")
		if err == nil {
			require.Equal(t, "app:v1", route.Image)
		}
		return err
	})
	mustStatus("GetRouteCleanupPreview", http.StatusNotImplemented, func() error { _, err := client.GetRouteCleanupPreview(ctx, "app.example.com"); return err })
	mustOK("FindRoutesByImage", func() error {
		routes, err := client.FindRoutesByImage(ctx, "app")
		if err == nil {
			require.NotEmpty(t, routes)
		}
		return err
	})
	mustOK("GetConfig", func() error {
		cfg, err := client.GetConfig(ctx)
		if err == nil {
			require.NotEmpty(t, cfg.Routes)
		}
		return err
	})
	mustOK("GetStatus", func() error {
		status, err := client.GetStatus(ctx)
		if err == nil {
			require.GreaterOrEqual(t, status.Routes, 1)
		}
		return err
	})
	mustOK("GetTLSStatus", func() error {
		status, err := client.GetTLSStatus(ctx)
		if err == nil {
			require.NotNil(t, status)
		}
		return err
	})
	mustOK("GetTrafficStatus", func() error {
		status, err := client.GetTrafficStatus(ctx)
		if err == nil {
			require.NotNil(t, status)
		}
		return err
	})
	mustOK("GetHealth", func() error {
		health, err := client.GetHealth(ctx)
		if err == nil {
			require.NotNil(t, health)
		}
		return err
	})
	mustOK("Ping", func() error { return client.Ping(ctx) })
	mustOK("VerifyAuth", func() error {
		result, err := client.VerifyAuth(ctx)
		if err == nil {
			require.True(t, result.Valid)
		}
		return err
	})
	// The control token deliberately has admin-only scope, so registry exchange
	// must fail as an explicit authorization error rather than being accepted.
	mustError("ExchangeRegistryToken", "forbidden: insufficient scope", func() error { _, err := client.ExchangeRegistryToken(ctx, "control-role-remote"); return err })

	// Route mutations must be observable in durable config, and deploy/restart
	// must reach the runtime command port rather than only return HTTP 200.
	mustOK("AddRoute", func() error { return client.AddRoute(ctx, domain.Route{Domain: domainName, Image: image}) })
	mustOK("UpdateRoute", func() error {
		return client.UpdateRoute(ctx, domain.Route{Domain: domainName, Image: "remote/matrix:v2"})
	})
	mustOK("AddRoute effect", func() error {
		route, err := client.GetRoute(ctx, domainName)
		if err == nil {
			require.Equal(t, "remote/matrix:v2", route.Image)
		}
		return err
	})
	mustOK("RemoveRouteWithCleanup", func() error {
		result, err := client.RemoveRouteWithCleanup(ctx, domainName)
		if err == nil {
			require.NotNil(t, result)
		}
		return err
	})
	mustOK("RemoveRoute effect", func() error {
		_, err := client.GetRoute(ctx, domainName)
		require.ErrorIs(t, err, domain.ErrRouteNotFound)
		return nil
	})
	mustOK("AddRoute for RemoveRoute", func() error { return client.AddRoute(ctx, domain.Route{Domain: domainName, Image: image}) })
	mustOK("RemoveRoute", func() error { return client.RemoveRoute(ctx, domainName) })
	// Bootstrap requires a registry domain, intentionally absent from this split
	// fixture; retain its exact validation contract instead of accepting a 500.
	mustStatus("Bootstrap", http.StatusBadRequest, func() error {
		_, err := client.Bootstrap(ctx, dto.BootstrapRequest{Domain: domainName, Image: image})
		return err
	})
	mustOK("AddRoute for config mutation", func() error { return client.AddRoute(ctx, domain.Route{Domain: domainName, Image: image}) })
	mustOK("Reload", func() error { return client.Reload(ctx) })
	beforeCommands := worker.calls()
	mustOK("Deploy", func() error {
		result, err := client.Deploy(ctx, "app.example.com")
		if err == nil {
			require.Equal(t, "app.example.com", result.Domain)
		}
		return err
	})
	mustOK("Deploy runtime effect", func() error {
		require.Eventually(t, func() bool { return worker.calls() > beforeCommands }, time.Second, time.Millisecond)
		return nil
	})
	mustStatus("DeployIntent", http.StatusServiceUnavailable, func() error { return client.DeployIntent(ctx, image) })
	mustOK("Restart", func() error {
		result, err := client.Restart(ctx, "app.example.com", false)
		if err == nil {
			require.Equal(t, "app.example.com", result.Domain)
		}
		return err
	})

	// Config mutations are read back to prove the remote client did not merely
	// parse a successful response body.
	mustOK("SetSecrets", func() error { return client.SetSecrets(ctx, domainName, map[string]string{"REMOTE_MATRIX": "value"}) })
	mustOK("ListSecretsWithAttachments", func() error {
		result, err := client.ListSecretsWithAttachments(ctx, domainName)
		if err == nil {
			require.Contains(t, result.Keys, "REMOTE_MATRIX")
		}
		return err
	})
	mustOK("ListSecrets", func() error {
		keys, err := client.ListSecrets(ctx, domainName)
		if err == nil {
			require.Contains(t, keys, "REMOTE_MATRIX")
		}
		return err
	})
	mustOK("DeleteSecret", func() error { return client.DeleteSecret(ctx, domainName, "REMOTE_MATRIX") })
	mustOK("AddAttachment", func() error { return client.AddAttachment(ctx, domainName, "sidecar:v1") })
	mustOK("GetAttachmentsConfig", func() error {
		images, err := client.GetAttachmentsConfig(ctx, domainName)
		if err == nil {
			require.Contains(t, images, "sidecar:v1")
		}
		return err
	})
	mustOK("GetAllAttachmentsConfig", func() error {
		all, err := client.GetAllAttachmentsConfig(ctx)
		if err == nil {
			require.Contains(t, all[domainName], "sidecar:v1")
		}
		return err
	})
	mustOK("FindAttachmentTargetsByImage", func() error {
		targets, err := client.FindAttachmentTargetsByImage(ctx, "sidecar:v1")
		if err == nil {
			require.Contains(t, targets, domainName)
		}
		return err
	})
	// Attachment-secret persistence needs an attachment service identity, which
	// this config-only fixture cannot create; its public validation error is
	// stable and ensures these client paths cannot panic in split mode.
	mustStatus("SetAttachmentSecrets", http.StatusBadRequest, func() error {
		return client.SetAttachmentSecrets(ctx, domainName, "sidecar:v1", map[string]string{"MATRIX": "value"})
	})
	mustStatus("DeleteAttachmentSecret", http.StatusBadRequest, func() error { return client.DeleteAttachmentSecret(ctx, domainName, "sidecar:v1", "MATRIX") })
	mustOK("RemoveAttachment", func() error { return client.RemoveAttachment(ctx, domainName, "sidecar:v1") })
	mustOK("AddAutoRouteAllowedDomain", func() error { return client.AddAutoRouteAllowedDomain(ctx, "*.remote-matrix.test") })
	mustOK("GetAutoRouteAllowedDomains", func() error {
		domains, err := client.GetAutoRouteAllowedDomains(ctx)
		if err == nil {
			require.Contains(t, domains, "*.remote-matrix.test")
		}
		return err
	})
	mustOK("RemoveAutoRouteAllowedDomain", func() error { return client.RemoveAutoRouteAllowedDomain(ctx, "*.remote-matrix.test") })

	// These services are deliberately absent in the split control fixture. The
	// public, documented capability result is 503 JSON—not a nil panic or 500.
	mustStatus("ListImages", http.StatusServiceUnavailable, func() error { _, err := client.ListImages(ctx); return err })
	mustStatus("PruneImages", http.StatusNotImplemented, func() error { _, err := client.PruneImages(ctx, dto.ImagePruneRequest{}); return err })
	mustStatus("ListTags", http.StatusServiceUnavailable, func() error { _, err := client.ListTags(ctx, "app"); return err })
	mustStatus("ListBackups", http.StatusServiceUnavailable, func() error { _, err := client.ListBackups(ctx, ""); return err })
	mustStatus("BackupStatus", http.StatusServiceUnavailable, func() error { _, err := client.BackupStatus(ctx); return err })
	mustStatus("RunBackup", http.StatusServiceUnavailable, func() error { _, err := client.RunBackup(ctx, domainName, "db"); return err })
	mustStatus("DetectDatabases", http.StatusServiceUnavailable, func() error { _, err := client.DetectDatabases(ctx, domainName); return err })
	mustStatus("ListVolumeBackups", http.StatusServiceUnavailable, func() error { _, err := client.ListVolumeBackups(ctx, ""); return err })
	mustStatus("VolumeBackupStatus", http.StatusServiceUnavailable, func() error { _, err := client.VolumeBackupStatus(ctx); return err })
	mustStatus("RunVolumeBackups", http.StatusServiceUnavailable, func() error { _, err := client.RunVolumeBackups(ctx, domainName, "volume"); return err })
	mustStatus("ListVolumes", http.StatusServiceUnavailable, func() error { _, err := client.ListVolumes(ctx); return err })
	mustStatus("PruneVolumes", http.StatusServiceUnavailable, func() error { _, err := client.PruneVolumes(ctx, dto.VolumePruneRequest{DryRun: true}); return err })
	mustStatus("ListOrphanedAttachments", http.StatusServiceUnavailable, func() error { _, err := client.ListOrphanedAttachments(ctx); return err })
	mustStatus("CleanupOrphanedAttachments", http.StatusServiceUnavailable, func() error { _, err := client.CleanupOrphanedAttachments(ctx, "", false); return err })
	mustStatus("ListPreviews", http.StatusServiceUnavailable, func() error { _, err := client.ListPreviews(ctx); return err })
	mustStatus("GetPreview", http.StatusServiceUnavailable, func() error { _, err := client.GetPreview(ctx, "preview"); return err })
	mustStatus("DeletePreview", http.StatusServiceUnavailable, func() error { return client.DeletePreview(ctx, "preview") })
	mustStatus("ExtendPreview", http.StatusServiceUnavailable, func() error { return client.ExtendPreview(ctx, "preview", "1h") })

	mustOK("GetProcessLogs", func() error { _, err := client.GetProcessLogs(ctx, 1); return err })
	mustOK("GetContainerLogs", func() error { _, err := client.GetContainerLogs(ctx, "app.example.com", 1); return err })
	// The fixture's log follower has no producer. Cancellation is the defined
	// lifecycle for both streaming methods and proves their remote paths return
	// without a nil service panic or leaking the HTTP request.
	mustError("StreamProcessLogs", "context deadline exceeded", func() error {
		streamCtx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
		defer cancel()
		_, err := client.StreamProcessLogs(streamCtx, 1)
		return err
	})
	mustOK("StreamContainerLogs", func() error {
		streamCtx, cancel := context.WithCancel(ctx)
		stream, err := client.StreamContainerLogs(streamCtx, "app.example.com", 1)
		if err != nil {
			cancel()
			return err
		}
		require.NotNil(t, stream)
		cancel()
		select {
		case _, ok := <-stream:
			require.False(t, ok, "canceled stream must close")
		case <-time.After(time.Second):
			t.Fatal("canceled stream did not close")
		}
		return nil
	})
}

// Services intentionally absent from the narrow runtime test double must
// advertise their capability gap at the public boundary. A 503 JSON error is
// a supported split-mode result; a nil dereference or 500 is not.
func requireRemoteUnavailable(t *testing.T, err error) {
	t.Helper()
	var responseErr *remote.HTTPError
	require.ErrorAs(t, err, &responseErr)
	require.Equal(t, http.StatusServiceUnavailable, responseErr.StatusCode)
	require.NotEmpty(t, responseErr.Body)
}

func controlRoleRequest(t *testing.T, method, url, token string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, nil)
	require.NoError(t, err)
	if token != "" {
		if len(token) < len("Bearer ") || token[:len("Bearer ")] != "Bearer " {
			token = "Bearer " + token
		}
		request.Header.Set("Authorization", token)
	}
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	return response
}

func controlRoleImageEvent() *eventsv1.PublishEventRequest {
	return &eventsv1.PublishEventRequest{Event: &eventsv1.EventEnvelope{
		Id:                  "control-role-image-v1",
		Type:                string(domain.ComponentEventTypeRegistryImagePushed),
		Origin:              string(domain.ComponentRoleRegistry),
		Timestamp:           timestamppb.Now(),
		IdempotencyKey:      "registry:app:v1:sha256:abc",
		AuditClassification: string(domain.ComponentEventAuditWrite),
		Payload: &eventsv1.EventEnvelope_RegistryImagePushed{RegistryImagePushed: &eventsv1.RegistryImagePushedPayload{
			Repository: "app", Reference: "v1", Digest: "sha256:abc",
		}},
	}}
}

type controlRoleComponentValidator struct{}

func (controlRoleComponentValidator) ValidateToken(_ context.Context, token string, required domain.ComponentScope) (*domain.ComponentIdentity, error) {
	var identity domain.ComponentIdentity
	switch token {
	case grpctest.LocalComponentToken:
		identity = domain.ComponentIdentity{Name: "registry", Role: domain.ComponentRoleRegistry, Scopes: []domain.ComponentScope{domain.ComponentScopeRegistryEventPublish}}
	case "edge-token":
		identity = domain.ComponentIdentity{Name: "edge", Role: domain.ComponentRoleEdge, Scopes: []domain.ComponentScope{domain.ComponentScopeRoutesWatch, domain.ComponentScopeTrafficWatch}}
	case "edge-only-token":
		identity = domain.ComponentIdentity{Name: "edge", Role: domain.ComponentRoleEdge, Scopes: []domain.ComponentScope{domain.ComponentScopeRoutesWatch}}
	default:
		return nil, domain.ErrInvalidToken
	}
	if !domain.ComponentScopesContain(identity.Scopes, required) {
		return nil, domain.ErrInsufficientScope
	}
	return &identity, nil
}

type controlRoleRuntime struct {
	listener net.Listener
	server   *grpc.Server
	worker   *controlRoleRuntimeWorker
	state    *controlRoleStateSubscriber
}

func startControlRoleRuntime(t *testing.T) *controlRoleRuntime {
	t.Helper()
	return startControlRoleRuntimeWithProbe(t, nil)
}

func startControlRoleRuntimeWithProbe(t *testing.T, probe out.RuntimeEnvironmentProbe) *controlRoleRuntime {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	runtime := &controlRoleRuntime{listener: listener, worker: &controlRoleRuntimeWorker{}}
	validator := grpctest.NewAuthFixture("control", domain.ComponentRoleControl, domain.AllComponentScopes()...)
	runtime.server = grpc.NewServer(
		grpc.Creds(insecure.NewCredentials()),
		grpc.UnaryInterceptor(interceptors.ComponentAuthUnaryInterceptor(validator, runtimegrpc.MethodScopes(), runtimegrpc.MethodRoles())),
		grpc.StreamInterceptor(interceptors.ComponentAuthStreamInterceptor(validator, runtimegrpc.MethodScopes(), runtimegrpc.MethodRoles())),
	)
	runtimev1.RegisterRuntimeServiceServer(runtime.server, runtimegrpc.NewServerWithEnvironmentProbe(runtime.worker, nil, nil, nil, runtime, controlRoleDrainReceiver{}, nil, probe, "runtime-test"))
	go func() { _ = runtime.server.Serve(listener) }()
	return runtime
}

func (r *controlRoleRuntime) SubscribeRuntimeState(ctx context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
	return r.state.SubscribeRuntimeState(ctx)
}

func (r *controlRoleRuntime) stop() {
	r.server.Stop()
	_ = r.listener.Close()
}

type controlRoleStateSubscriber struct {
	mu       sync.RWMutex
	snapshot domain.RuntimeActualStateSnapshot
	ready    chan struct{}
	once     sync.Once
}

func newControlRoleStateSubscriber() *controlRoleStateSubscriber {
	return &controlRoleStateSubscriber{ready: make(chan struct{})}
}

func (s *controlRoleStateSubscriber) Publish(snapshot domain.RuntimeActualStateSnapshot) {
	s.mu.Lock()
	s.snapshot = snapshot
	s.mu.Unlock()
	s.once.Do(func() { close(s.ready) })
}

func (s *controlRoleStateSubscriber) SubscribeRuntimeState(ctx context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
	updates := make(chan domain.RuntimeActualStateSnapshot, 1)
	go func() {
		select {
		case <-ctx.Done():
			close(updates)
			return
		case <-s.ready:
		}
		s.mu.RLock()
		snapshot := s.snapshot
		s.mu.RUnlock()
		updates <- snapshot
		<-ctx.Done()
		close(updates)
	}()
	return updates, nil
}

type controlRoleDrainReceiver struct{}

func (controlRoleDrainReceiver) AcknowledgeRouteDrain(context.Context, domain.RouteDrainAck) error {
	return nil
}

type controlRoleRuntimeWorker struct {
	mu              sync.Mutex
	commands        []domain.DeployRouteCommand
	restartCommands []domain.RestartRouteCommand
}

func (w *controlRoleRuntimeWorker) DeployRoute(_ context.Context, command domain.DeployRouteCommand) (domain.RuntimeCommandResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.commands = append(w.commands, command)
	return domain.RuntimeCommandResult{CommandID: command.ID, Status: domain.RuntimeCommandStatusSucceeded}, nil
}

func (w *controlRoleRuntimeWorker) RestartRoute(_ context.Context, command domain.RestartRouteCommand) (domain.RuntimeCommandResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.restartCommands = append(w.restartCommands, command)
	return domain.RuntimeCommandResult{CommandID: command.ID, Status: domain.RuntimeCommandStatusSucceeded}, nil
}
func (w *controlRoleRuntimeWorker) RemoveRoute(context.Context, domain.RemoveRouteCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusSucceeded}, nil
}
func (w *controlRoleRuntimeWorker) Reconcile(context.Context, domain.ReconcileRuntimeCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusSucceeded}, nil
}
func (w *controlRoleRuntimeWorker) SelfUpdate(context.Context, domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusSucceeded}, nil
}
func (w *controlRoleRuntimeWorker) calls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.commands)
}
func (w *controlRoleRuntimeWorker) command(index int) domain.DeployRouteCommand {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.commands[index]
}
func (w *controlRoleRuntimeWorker) restartCalls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.restartCommands)
}
func (w *controlRoleRuntimeWorker) restartCommand(index int) domain.RestartRouteCommand {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.restartCommands[index]
}

var _ out.RuntimeStateSubscriber = (*controlRoleStateSubscriber)(nil)
