package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
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
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	runtimegrpc "github.com/bnema/gordon/internal/adapters/in/grpc/runtime"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/testutils/grpctest"
	authusecase "github.com/bnema/gordon/internal/usecase/auth"
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
			newSnapshotProducer:        edgesnapshot.NewProducer,
			newTrafficGraphProducer:    edgesnapshot.NewTrafficGraphProducer,
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
	adminToken := controlRoleAdminToken(t)
	assertControlAdminConfig(t, httpBase, adminToken)

	deploy := controlRoleRequest(t, http.MethodPost, httpBase+"/admin/deploy/app.example.com", adminToken)
	require.Equal(t, http.StatusOK, deploy.StatusCode)
	deploy.Body.Close()
	require.Eventually(t, func() bool { return runtime.worker.calls() == 1 }, time.Second, time.Millisecond)
	command := runtime.worker.command(0)
	assert.Equal(t, "app.example.com", command.Domain)
	assert.Equal(t, "app:v1", command.Image)
	assert.True(t, command.InternalDeploy)

	events := eventsv1.NewEventServiceClient(grpcConn)
	event := controlRoleImageEvent()
	validEventContext := grpctest.AuthenticatedContext(context.Background(), grpctest.LocalComponentToken)
	ack, err := events.PublishEvent(validEventContext, event)
	require.NoError(t, err)
	assert.False(t, ack.GetAck().GetDuplicate())
	require.Eventually(t, func() bool { return runtime.worker.calls() == 2 }, time.Second, time.Millisecond)
	ack, err = events.PublishEvent(validEventContext, event)
	require.NoError(t, err)
	assert.True(t, ack.GetAck().GetDuplicate())
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, 2, runtime.worker.calls(), "duplicate image event must not repeat its production effect")

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

[routes]
"app.example.com" = "app:v1"
`
	require.NoError(t, os.WriteFile(configPath, []byte(contents), 0600))
	return configPath
}

func controlRoleAdminToken(t *testing.T) string {
	t.Helper()
	service := authusecase.NewService(authusecase.Config{
		Enabled: true, AuthType: domain.AuthTypeToken,
		TokenSecret: []byte("control-role-test-token-secret-at-least-32-bytes"),
	}, nil, zerowrap.Default())
	token, err := service.GenerateAccessToken(context.Background(), "control-role-admin", []string{"admin:*:*"}, time.Minute)
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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	runtime := &controlRoleRuntime{listener: listener, worker: &controlRoleRuntimeWorker{}}
	validator := grpctest.NewAuthFixture("control", domain.ComponentRoleControl, domain.AllComponentScopes()...)
	runtime.server = grpc.NewServer(
		grpc.Creds(insecure.NewCredentials()),
		grpc.UnaryInterceptor(interceptors.ComponentAuthUnaryInterceptor(validator, runtimegrpc.MethodScopes(), runtimegrpc.MethodRoles())),
		grpc.StreamInterceptor(interceptors.ComponentAuthStreamInterceptor(validator, runtimegrpc.MethodScopes(), runtimegrpc.MethodRoles())),
	)
	runtimev1.RegisterRuntimeServiceServer(runtime.server, runtimegrpc.NewServerWithAllRuntimePortsAndRouteDrainAckReceiverAndStandaloneServiceManager(runtime.worker, nil, nil, nil, runtime, controlRoleDrainReceiver{}, nil, "runtime-test"))
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
	mu       sync.Mutex
	commands []domain.DeployRouteCommand
}

func (w *controlRoleRuntimeWorker) DeployRoute(_ context.Context, command domain.DeployRouteCommand) (domain.RuntimeCommandResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.commands = append(w.commands, command)
	return domain.RuntimeCommandResult{CommandID: command.ID, Status: domain.RuntimeCommandStatusSucceeded}, nil
}

func (w *controlRoleRuntimeWorker) RestartRoute(context.Context, domain.RestartRouteCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusSucceeded}, nil
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

var _ out.RuntimeStateSubscriber = (*controlRoleStateSubscriber)(nil)
