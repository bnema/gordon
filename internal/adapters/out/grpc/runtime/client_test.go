package runtime

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	runtimev1 "github.com/bnema/gordon/api/gordon/runtime/v1"
	inruntime "github.com/bnema/gordon/internal/adapters/in/grpc/runtime"
	"github.com/bnema/gordon/internal/boundaries/out"
	outMocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/testutils/grpctest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestClientDeployRouteRoundTrip(t *testing.T) {
	worker := &fakeRuntimeWorker{}
	conn := newRuntimeTestConn(t, inruntime.NewServer(worker, "runtime-1"))
	client := NewClient(conn)
	command := domain.DeployRouteCommand{RuntimeCommandIdentity: testIdentity("cmd-1"), Domain: "app.example.com", Image: "app:latest", Env: []string{"A=1"}}

	result, err := client.DeployRoute(context.Background(), command)

	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, result.Status)
	assert.Equal(t, command, worker.deploy)
}

func TestClientSubscribeRuntimeStateRoundTripAndCancel(t *testing.T) {
	state := &fakeRuntimeStateSubscriber{snapshots: []domain.RuntimeActualStateSnapshot{testActualStateSnapshot()}}
	conn := newRuntimeTestConn(t, inruntime.NewServerWithStateSubscriber(&fakeRuntimeWorker{}, state, "runtime-1"))
	client := NewClient(conn)
	ctx, cancel := context.WithCancel(context.Background())

	snapshots, err := client.SubscribeRuntimeState(ctx)
	require.NoError(t, err)
	got := <-snapshots
	assert.Equal(t, uint64(7), got.Generation)
	assert.Equal(t, "gordon-target-app-example-com", got.Routes[0].EdgeTargetAlias)
	assert.Equal(t, "gordon-data", got.Volumes[0].Name)
	assert.NotContains(t, got.Containers[0].Labels, "secret.env")

	cancel()
	select {
	case _, ok := <-snapshots:
		assert.False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("runtime state subscription did not close after cancellation")
	}
}

func TestClientSubscribeRuntimeStateClassifiesInitialSourceErrors(t *testing.T) {
	for _, tc := range []struct {
		code      codes.Code
		retryable bool
	}{
		{codes.Unavailable, true}, {codes.ResourceExhausted, true}, {codes.DeadlineExceeded, true},
		{codes.Unauthenticated, false}, {codes.PermissionDenied, false}, {codes.FailedPrecondition, false},
		{codes.InvalidArgument, false}, {codes.Internal, false}, {codes.Unknown, false},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			conn := newRuntimeTestConn(t, initialStateErrorServer{err: status.Error(tc.code, "credential=super-secret")})

			_, err := NewClient(conn).SubscribeRuntimeState(context.Background())

			require.Error(t, err)
			var sourceErr *out.RuntimeStateSubscriptionError
			require.ErrorAs(t, err, &sourceErr)
			assert.Equal(t, tc.retryable, sourceErr.Retryable)
			assert.Equal(t, "runtime state subscription unavailable", err.Error())
			assert.NotContains(t, err.Error(), "super-secret")
		})
	}
}

func TestClientSubscribeRuntimeStateReturnsClosedBeforeInitialSnapshot(t *testing.T) {
	conn := newRuntimeTestConn(t, initialStateErrorServer{})
	client := NewClient(conn)

	_, err := client.SubscribeRuntimeState(context.Background())

	require.Error(t, err)
	var sourceErr *out.RuntimeStateSubscriptionError
	require.ErrorAs(t, err, &sourceErr)
	assert.False(t, sourceErr.Retryable)
	assert.Equal(t, "runtime state subscription unavailable", err.Error())
}

func TestClientSubscribeRuntimeStateReturnsCancellationBeforeInitialSnapshot(t *testing.T) {
	conn := newRuntimeTestConn(t, initialStateErrorServer{waitForCancellation: true})
	client := NewClient(conn)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.SubscribeRuntimeState(ctx)

	require.Error(t, err)
	assert.Equal(t, codes.Canceled, status.Code(err))
	assert.Equal(t, "context canceled", status.Convert(err).Message())
}

func TestClientMapsPolicyDeniedResult(t *testing.T) {
	denied := domain.RuntimeCommandResult{CommandID: "cmd-denied", IdempotencyKey: "idem-cmd-denied", Generation: 7, Status: domain.RuntimeCommandStatusDenied, Error: &domain.RuntimeCommandError{Code: "policy_denied", Message: "policy denied", Retryable: false}}
	worker := &fakeRuntimeWorker{resultOverride: &denied}
	conn := newRuntimeTestConn(t, inruntime.NewServer(worker, "runtime-1"))
	client := NewClient(conn)

	result, err := client.DeployRoute(context.Background(), domain.DeployRouteCommand{RuntimeCommandIdentity: testIdentity("cmd-denied"), Domain: "app.example.com", Image: "app:latest"})

	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusDenied, result.Status)
	require.NotNil(t, result.Error)
	assert.Equal(t, "policy_denied", result.Error.Code)
}

func TestClientReadRouteLogsCloseCancelsStream(t *testing.T) {
	canceled := make(chan struct{})
	logReader := outMocks.NewMockRuntimeLogReader(t)
	logReader.EXPECT().
		ReadRouteLogs(mock.Anything, "app.example.com", true).
		RunAndReturn(func(ctx context.Context, _ string, _ bool) (io.ReadCloser, error) {
			reader, writer := io.Pipe()
			go func() {
				<-ctx.Done()
				close(canceled)
				_ = writer.Close()
			}()
			return reader, nil
		})
	conn := newRuntimeTestConn(t, inruntime.NewServerWithLogReader(&fakeRuntimeWorker{}, logReader, "runtime-1"))
	client := NewClient(conn)

	reader, err := client.ReadRouteLogs(context.Background(), "app.example.com", true)
	require.NoError(t, err)
	require.NoError(t, reader.Close())

	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("closing log reader did not cancel runtime stream")
	}
}

func TestClientReadRouteLogsRoundTrip(t *testing.T) {
	logReader := outMocks.NewMockRuntimeLogReader(t)
	logReader.EXPECT().
		ReadRouteLogs(mock.Anything, "app.example.com", true).
		Return(io.NopCloser(strings.NewReader("log-a\nlog-b\n")), nil)
	conn := newRuntimeTestConn(t, inruntime.NewServerWithLogReader(&fakeRuntimeWorker{}, logReader, "runtime-1"))
	client := NewClient(conn)

	reader, err := client.ReadRouteLogs(context.Background(), "app.example.com", true)
	require.NoError(t, err)
	defer reader.Close()
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "log-a\nlog-b\n", string(data))
}

func TestClientVolumeManagerRoundTrip(t *testing.T) {
	volumeManager := outMocks.NewMockRuntimeVolumeManager(t)
	volumes := []*domain.VolumeInfo{{Name: "gordon-data", Driver: "local", Size: 1024, InUse: false, Labels: map[string]string{domain.LabelManaged: "true"}}}
	volumeManager.EXPECT().ListRuntimeVolumes(mock.Anything).Return(volumes, nil)
	volumeManager.EXPECT().RemoveRuntimeVolume(mock.Anything, "gordon-data", false).Return(nil)
	conn := newRuntimeTestConn(t, inruntime.NewServerWithVolumeManager(&fakeRuntimeWorker{}, volumeManager, "runtime-1"))
	client := NewClient(conn)

	got, err := client.ListRuntimeVolumes(context.Background())
	require.NoError(t, err)
	assert.Equal(t, volumes, got)
	require.NoError(t, client.RemoveRuntimeVolume(context.Background(), "gordon-data", false))
}

func TestClientImageManagerRoundTrip(t *testing.T) {
	imageManager := outMocks.NewMockRuntimeImageManager(t)
	images := []domain.RuntimeImageDetail{{ID: "sha256:111", RepoTags: []string{"gordon/api:latest"}, Size: 1024, Created: time.Unix(10, 0).UTC()}}
	imageManager.EXPECT().ListRuntimeImages(mock.Anything).Return(images, nil)
	imageManager.EXPECT().PruneRuntimeImages(mock.Anything, true).Return(domain.RuntimePruneResult{DeletedCount: 2, SpaceReclaimed: 4096}, nil)
	conn := newRuntimeTestConn(t, inruntime.NewServerWithImageManager(&fakeRuntimeWorker{}, imageManager, "runtime-1"))
	client := NewClient(conn)

	got, err := client.ListRuntimeImages(context.Background())
	require.NoError(t, err)
	assert.Equal(t, images, got)
	prune, err := client.PruneRuntimeImages(context.Background(), true)
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimePruneResult{DeletedCount: 2, SpaceReclaimed: 4096}, prune)
}

func TestClientSelfUpdateHealthAndDrain(t *testing.T) {
	worker := &fakeRuntimeWorker{}
	receiver := &recordingRouteDrainAckReceiver{}
	conn := newRuntimeTestConn(t, inruntime.NewServerWithRouteDrainAckReceiver(worker, receiver, "runtime-1"))
	client := NewClient(conn)

	profile, ok := domain.FixedRuntimeComponentLifecycleProfile(domain.ComponentRoleRuntime)
	require.True(t, ok)
	result, err := client.SelfUpdateRuntime(context.Background(), domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: testIdentity("cmd-self"), TargetComponentID: "runtime-1", TargetComponentRole: domain.ComponentRoleRuntime, TargetVersion: "v1.2.3", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "decision-1", LifecycleAction: domain.RuntimeComponentLifecycleStart, LifecycleProfile: profile, DesiredImage: "example.invalid/gordon:v1.2.3", DesiredStateHash: "fixture-state-hash", InternalNetwork: "gordon-internal-fixture-g1", PreserveVolumes: true})
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, result.Status)
	assert.Equal(t, "runtime-1", worker.self.TargetComponentID)
	assert.Equal(t, domain.RuntimeComponentLifecycleStart, worker.self.LifecycleAction)
	assert.Equal(t, profile, worker.self.LifecycleProfile)
	assert.True(t, worker.self.PreserveVolumes)

	edgeProfile, ok := domain.FixedRuntimeComponentLifecycleProfile(domain.ComponentRoleEdge)
	require.True(t, ok)
	edgeCommand := domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: testIdentity("cmd-edge"), TargetComponentID: "gordon-edge-fixture-g1", TargetComponentRole: domain.ComponentRoleEdge, TargetVersion: "v1.2.3", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleActivate, LifecycleProfile: edgeProfile, EdgeAppNetworks: []string{"gordon-app-one", "gordon-app-two"}, PreserveVolumes: true}
	_, err = client.SelfUpdateRuntime(context.Background(), edgeCommand)
	require.NoError(t, err)
	assert.Equal(t, edgeCommand.EdgeAppNetworks, worker.self.EdgeAppNetworks)

	require.NoError(t, client.PingRuntime(context.Background()))
	version, err := client.RuntimeVersion(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "runtime service ready", version)
	require.NoError(t, client.AcknowledgeRouteDrain(context.Background(), domain.RouteDrainAck{RouteDrainState: domain.RouteDrainState{CanonicalDomain: "app.example.com", TransitionGeneration: 7, OldTargetKey: testRouteDrainKey, AcknowledgedAt: time.Unix(1, 0).UTC()}, Status: domain.RouteDrainStatusAcknowledged}))
}

func TestClientPreservesContextCancellation(t *testing.T) {
	conn := newRuntimeTestConn(t, inruntime.NewServer(&fakeRuntimeWorker{}, "runtime-1"))
	client := NewClient(conn)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.DeployRoute(ctx, domain.DeployRouteCommand{RuntimeCommandIdentity: testIdentity("cmd-cancel"), Domain: "app.example.com", Image: "app:latest"})

	require.Error(t, err)
	assert.Equal(t, codes.Canceled, status.Code(err))
}

func newRuntimeTestConn(t *testing.T, server runtimev1.RuntimeServiceServer) *grpc.ClientConn {
	t.Helper()
	harness := grpctest.NewHarness(t, func(registrar grpc.ServiceRegistrar) {
		runtimev1.RegisterRuntimeServiceServer(registrar, server)
	})
	return harness.Conn(t)
}

type initialStateErrorServer struct {
	runtimev1.UnimplementedRuntimeServiceServer
	err                 error
	waitForCancellation bool
}

func (s initialStateErrorServer) WatchActualState(_ *runtimev1.WatchActualStateRequest, stream grpc.ServerStreamingServer[runtimev1.WatchActualStateResponse]) error {
	if s.waitForCancellation {
		<-stream.Context().Done()
		return stream.Context().Err()
	}
	return s.err
}

type fakeRuntimeStateSubscriber struct {
	snapshots []domain.RuntimeActualStateSnapshot
}

func (f *fakeRuntimeStateSubscriber) SubscribeRuntimeState(ctx context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
	ch := make(chan domain.RuntimeActualStateSnapshot)
	go func() {
		defer close(ch)
		for _, snapshot := range f.snapshots {
			select {
			case <-ctx.Done():
				return
			case ch <- snapshot:
			}
		}
		<-ctx.Done()
	}()
	return ch, nil
}

func testActualStateSnapshot() domain.RuntimeActualStateSnapshot {
	return domain.RuntimeActualStateSnapshot{
		Generation:        7,
		StateVersion:      "state-v1",
		SourceComponentID: "runtime-1",
		ObservedAt:        time.Unix(11, 0).UTC(),
		Routes:            []domain.RuntimeRouteState{{Domain: "app.example.com", Generation: 7, RouteVersion: "route-v1", ContainerAlias: "app-example-com", EdgeTargetAlias: "gordon-target-app-example-com", TargetPort: 8080, Scheme: "http", Protocol: domain.RouteTargetProtocolHTTP1, Status: domain.RouteTargetStatusReady}},
		Containers:        []domain.RuntimeContainerState{{Name: "gordon-app-example-com", Alias: "app-example-com", Image: "app:latest", Status: domain.ContainerStatusRunning, Labels: map[string]string{domain.LabelDomain: "app.example.com", "secret.env": "DATABASE_URL=secret"}, Generation: 7}},
		Networks:          []domain.RuntimeNetworkState{{Name: "gordon-app", Driver: "bridge", Aliases: []string{"app-example-com"}, Generation: 7}},
		Volumes:           []domain.RuntimeVolumeState{{Name: "gordon-data", AttachedTo: []string{"gordon-app-example-com"}, Generation: 7}},
		EdgeAttachments:   []domain.RuntimeEdgeNetworkAttachmentState{{RouteDomain: "app.example.com", NetworkName: "gordon-app", EdgeAlias: "edge-1", RuntimeAlias: "runtime-1", TargetAlias: "gordon-target-app-example-com", TargetPort: 8080, Attached: true, Generation: 7, SourceComponent: "runtime-1"}},
	}
}

type fakeRuntimeWorker struct {
	deploy         domain.DeployRouteCommand
	self           domain.RuntimeSelfUpdateCommand
	resultOverride *domain.RuntimeCommandResult
}

func (f *fakeRuntimeWorker) DeployRoute(_ context.Context, command domain.DeployRouteCommand) (domain.RuntimeCommandResult, error) {
	f.deploy = command
	if f.resultOverride != nil {
		return *f.resultOverride, nil
	}
	return testResult(command.RuntimeCommandIdentity), nil
}

func (f *fakeRuntimeWorker) RestartRoute(_ context.Context, command domain.RestartRouteCommand) (domain.RuntimeCommandResult, error) {
	return testResult(command.RuntimeCommandIdentity), nil
}

func (f *fakeRuntimeWorker) RemoveRoute(_ context.Context, command domain.RemoveRouteCommand) (domain.RuntimeCommandResult, error) {
	return testResult(command.RuntimeCommandIdentity), nil
}

func (f *fakeRuntimeWorker) Reconcile(_ context.Context, command domain.ReconcileRuntimeCommand) (domain.RuntimeCommandResult, error) {
	return testResult(command.RuntimeCommandIdentity), nil
}

func (f *fakeRuntimeWorker) SelfUpdate(_ context.Context, command domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error) {
	f.self = command
	return testResult(command.RuntimeCommandIdentity), nil
}

func testIdentity(id string) domain.RuntimeCommandIdentity {
	return domain.RuntimeCommandIdentity{ID: domain.RuntimeCommandID(id), IdempotencyKey: "idem-" + id, Generation: 7, SourceComponentID: "control-1", RequestedAt: time.Unix(10, 0).UTC()}
}

func testResult(identity domain.RuntimeCommandIdentity) domain.RuntimeCommandResult {
	return domain.RuntimeCommandResult{CommandID: identity.ID, IdempotencyKey: identity.IdempotencyKey, Generation: identity.Generation, Status: domain.RuntimeCommandStatusSucceeded, StartedAt: identity.RequestedAt, CompletedAt: identity.RequestedAt}
}

func TestClientStandaloneServiceManagerRejectsMissingResults(t *testing.T) {
	tests := []struct {
		name   string
		client runtimev1.RuntimeServiceClient
		call   func(*Client) error
	}{
		{
			name:   "apply missing response",
			client: missingResultRuntimeClient{},
			call: func(client *Client) error {
				_, err := client.ApplyStandaloneService(context.Background(), testApplyStandaloneServiceCommand())
				return err
			},
		},
		{
			name:   "apply missing result",
			client: missingResultRuntimeClient{applyResponse: &runtimev1.ApplyStandaloneServiceResponse{}},
			call: func(client *Client) error {
				_, err := client.ApplyStandaloneService(context.Background(), testApplyStandaloneServiceCommand())
				return err
			},
		},
		{
			name:   "remove missing response",
			client: missingResultRuntimeClient{},
			call: func(client *Client) error {
				_, err := client.RemoveStandaloneService(context.Background(), domain.RemoveStandaloneServiceCommand{Name: "game"})
				return err
			},
		},
		{
			name:   "remove missing result",
			client: missingResultRuntimeClient{removeResponse: &runtimev1.RemoveStandaloneServiceResponse{}},
			call: func(client *Client) error {
				_, err := client.RemoveStandaloneService(context.Background(), domain.RemoveStandaloneServiceCommand{Name: "game"})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(NewClientWithRuntimeService(tt.client))
			require.Error(t, err)
			assert.ErrorContains(t, err, "runtime response missing result")
		})
	}
}

type missingResultRuntimeClient struct {
	runtimev1.RuntimeServiceClient
	applyResponse  *runtimev1.ApplyStandaloneServiceResponse
	removeResponse *runtimev1.RemoveStandaloneServiceResponse
}

func (c missingResultRuntimeClient) ApplyStandaloneService(context.Context, *runtimev1.ApplyStandaloneServiceRequest, ...grpc.CallOption) (*runtimev1.ApplyStandaloneServiceResponse, error) {
	return c.applyResponse, nil
}

func (c missingResultRuntimeClient) RemoveStandaloneService(context.Context, *runtimev1.RemoveStandaloneServiceRequest, ...grpc.CallOption) (*runtimev1.RemoveStandaloneServiceResponse, error) {
	return c.removeResponse, nil
}

func TestClientStandaloneServiceManagerRoundTrip(t *testing.T) {
	manager := outMocks.NewMockRuntimeStandaloneServiceManager(t)
	apply := testApplyStandaloneServiceCommand()
	remove := domain.RemoveStandaloneServiceCommand{RuntimeCommandIdentity: testIdentity("remove-game"), Name: "game", Reason: "disabled", Cleanup: domain.StandaloneServiceCleanup{PreserveVolumes: false, RemoveContainer: true}}
	applyResult := testResult(apply.RuntimeCommandIdentity)
	removeResult := testResult(remove.RuntimeCommandIdentity)
	states := []domain.RuntimeStandaloneServiceState{{
		Name: "game", ContainerID: "container-1", ContainerName: "gordon-service-game", Status: domain.ContainerStatusRunning, ConfigHash: "config-hash", Cleanup: domain.StandaloneServiceCleanup{PreserveVolumes: false, RemoveContainer: true},
	}}
	manager.EXPECT().ApplyStandaloneService(mock.Anything, apply).Return(applyResult, nil)
	manager.EXPECT().RemoveStandaloneService(mock.Anything, remove).Return(removeResult, nil)
	manager.EXPECT().ListStandaloneServiceState(mock.Anything).Return(states, nil)
	conn := newRuntimeTestConn(t, inruntime.NewServerWithStandaloneServiceManager(&fakeRuntimeWorker{}, manager, "runtime-1"))
	client := NewClient(conn)

	gotApply, err := client.ApplyStandaloneService(context.Background(), apply)
	require.NoError(t, err)
	assert.Equal(t, applyResult, gotApply)
	assert.Nil(t, gotApply.Error)

	gotRemove, err := client.RemoveStandaloneService(context.Background(), remove)
	require.NoError(t, err)
	assert.Equal(t, removeResult, gotRemove)

	gotStates, err := client.ListStandaloneServiceState(context.Background())
	require.NoError(t, err)
	assert.Equal(t, states, gotStates)
}

func TestClientStandaloneServiceManagerPreservesCancellationAndSanitizesErrors(t *testing.T) {
	apply := testApplyStandaloneServiceCommand()
	manager := outMocks.NewMockRuntimeStandaloneServiceManager(t)
	manager.EXPECT().ApplyStandaloneService(mock.Anything, apply).Return(domain.RuntimeCommandResult{}, errors.New("TOKEN=super-secret"))
	conn := newRuntimeTestConn(t, inruntime.NewServerWithStandaloneServiceManager(&fakeRuntimeWorker{}, manager, "runtime-1"))
	client := NewClient(conn)

	_, err := client.ApplyStandaloneService(context.Background(), apply)
	require.Error(t, err)
	assert.Equal(t, codes.Internal, status.Code(err))
	assert.NotContains(t, status.Convert(err).Message(), "super-secret")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.RemoveStandaloneService(ctx, domain.RemoveStandaloneServiceCommand{RuntimeCommandIdentity: testIdentity("cancel-game"), Name: "game"})
	require.Error(t, err)
	assert.Equal(t, codes.Canceled, status.Code(err))
}

func testApplyStandaloneServiceCommand() domain.ApplyStandaloneServiceCommand {
	return domain.ApplyStandaloneServiceCommand{
		RuntimeCommandIdentity: testIdentity("apply-game"),
		Service: domain.StandaloneService{
			Name: "game", Image: "game:latest", Enabled: true,
			Ports: []domain.StandaloneServicePort{{
				Name: "game", Container: 28015, Protocol: domain.NetworkProtocolUDP, Publish: "127.0.0.1:38015", Private: true, Public: false, TrustedCIDRs: []string{"10.0.0.0/8"},
			}},
			Volumes:   []domain.StandaloneServiceVolume{{Source: "game-data", Target: "/data", ReadOnly: true}},
			Readiness: domain.StandaloneServiceReadiness{Type: domain.StandaloneServiceReadinessLog, Path: "/logs/game.log", Contains: "ready", Timeout: time.Second, TimeoutSet: true},
			Cleanup:   domain.StandaloneServiceCleanup{PreserveVolumes: true, RemoveContainer: true},
		},
		ResolvedEnv: []string{"TOKEN=super-secret"},
		ConfigHash:  "config-hash",
	}
}

const testRouteDrainKey domain.RouteTargetKey = "rtk_abcdefghijklmnopqrstuvwxyz234567"

type recordingRouteDrainAckReceiver struct{ acks []domain.RouteDrainAck }

func (r *recordingRouteDrainAckReceiver) AcknowledgeRouteDrain(_ context.Context, ack domain.RouteDrainAck) error {
	r.acks = append(r.acks, ack)
	return nil
}
