package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	runtimev1 "github.com/bnema/gordon/api/gordon/runtime/v1"
	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	"github.com/bnema/gordon/internal/adapters/out/tokenstore"
	"github.com/bnema/gordon/internal/boundaries/out"
	outMocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/testutils/grpctest"
	"github.com/bnema/gordon/internal/usecase/componentauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type listenerEnvironmentProbe struct {
	report    out.RuntimeEnvironment
	available []bool
}

func (p listenerEnvironmentProbe) ProbeRuntimeEnvironment(context.Context) (out.RuntimeEnvironment, error) {
	return p.report, nil
}

func (p listenerEnvironmentProbe) ProbePublicListeners(_ context.Context, ports []int) ([]bool, error) {
	if len(ports) != len(p.available) {
		return nil, errors.New("unexpected listener request")
	}
	return p.available, nil
}

func TestServerProbeEnvironmentReturnsOnlyListenerAvailability(t *testing.T) {
	probe := listenerEnvironmentProbe{report: out.RuntimeEnvironment{Engine: "podman", Rootless: true, APIReachable: true}, available: []bool{true, false}}
	server := NewServerWithEnvironmentProbe(nil, nil, nil, nil, nil, nil, nil, probe, "runtime-fixture")

	response, err := server.ProbeEnvironment(context.Background(), &runtimev1.ProbeEnvironmentRequest{RequiredPublicPorts: []int32{18443, 15000}})
	require.NoError(t, err)
	assert.Equal(t, "podman", response.GetEngine())
	assert.Equal(t, []bool{true, false}, response.GetPublicListenersAvailable())
	assert.Empty(t, response.ProtoReflect().GetUnknown(), "listener owners must not cross the control RPC")
}

func TestServerApplyCommandTranslatesDeploy(t *testing.T) {
	worker := &fakeRuntimeWorker{}
	server := NewServer(worker, "runtime-1")
	requestedAt := time.Unix(10, 0).UTC()

	resp, err := server.ApplyCommand(context.Background(), &runtimev1.ApplyCommandRequest{Command: &runtimev1.ApplyCommandRequest_DeployRoute{DeployRoute: &runtimev1.DeployRouteCommand{
		Identity:     protoTestIdentity("cmd-1", requestedAt),
		Domain:       "app.example.com",
		Image:        "app:latest",
		RouteVersion: "routes-v1",
		Env:          []string{"A=1"},
	}}})

	require.NoError(t, err)
	require.NotNil(t, resp.GetResult())
	assert.Equal(t, "cmd-1", resp.GetResult().CommandId)
	assert.Equal(t, domain.DeployRouteCommand{RuntimeCommandIdentity: testDomainIdentity("cmd-1", requestedAt), Domain: "app.example.com", Image: "app:latest", RouteVersion: "routes-v1", Env: []string{"A=1"}}, worker.deploy)
}

func TestServerRejectsMissingCommand(t *testing.T) {
	server := NewServer(&fakeRuntimeWorker{}, "runtime-1")

	_, err := server.ApplyCommand(context.Background(), &runtimev1.ApplyCommandRequest{})

	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestServerApplyCommandReconcileRequiresReconcileScope(t *testing.T) {
	worker := &fakeRuntimeWorker{}
	server := NewServer(worker, "runtime-1")
	requestedAt := time.Unix(10, 0).UTC()
	req := &runtimev1.ApplyCommandRequest{Command: &runtimev1.ApplyCommandRequest_Reconcile{Reconcile: &runtimev1.ReconcileRuntimeCommand{
		Identity:           protoTestIdentity("cmd-reconcile", requestedAt),
		ExpectedRouteCount: 0,
	}}}
	ctx := interceptors.ContextWithComponentIdentity(context.Background(), &domain.ComponentIdentity{Scopes: []domain.ComponentScope{domain.ComponentScopeRuntimeDeploy}})

	_, err := server.ApplyCommand(ctx, req)

	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.Empty(t, worker.reconcile.ID)

	ctx = interceptors.ContextWithComponentIdentity(context.Background(), &domain.ComponentIdentity{Scopes: []domain.ComponentScope{domain.ComponentScopeRuntimeDeploy, domain.ComponentScopeRuntimeReconcile}})
	_, err = server.ApplyCommand(ctx, req)

	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandID("cmd-reconcile"), worker.reconcile.ID)
}

func TestServerRuntimeSelfUpdateTranslation(t *testing.T) {
	worker := &fakeRuntimeWorker{}
	server := NewServer(worker, "runtime-1")
	requestedAt := time.Unix(10, 0).UTC()

	resp, err := server.RuntimeSelfUpdate(context.Background(), &runtimev1.RuntimeSelfUpdateRequest{Command: &runtimev1.RuntimeSelfUpdateCommand{
		Identity:            protoTestIdentity("cmd-self", requestedAt),
		TargetComponentId:   "runtime-1",
		TargetComponentRole: string(domain.ComponentRoleRuntime),
		TargetVersion:       "v1.2.3",
		Policy:              string(domain.RuntimeSelfUpdatePolicyManualApproval),
		PolicyDecisionId:    "decision-1",
		LifecycleAction:     string(domain.RuntimeComponentLifecycleStart),
		LifecycleProfile:    testProtoLifecycleProfile(domain.ComponentRoleRuntime),
		DesiredImage:        "example.invalid/gordon:v1.2.3",
		DesiredStateHash:    "fixture-state-hash",
		InternalNetwork:     "gordon-internal-fixture-g1",
		EnvironmentFile:     "/redacted/control.env",
		PreserveVolumes:     true,
	}})

	require.NoError(t, err)
	assert.Equal(t, "cmd-self", resp.GetResult().CommandId)
	assert.Equal(t, domain.ComponentRoleRuntime, worker.self.TargetComponentRole)
	assert.Equal(t, domain.RuntimeComponentLifecycleStart, worker.self.LifecycleAction)
	assert.Equal(t, "example.invalid/gordon:v1.2.3", worker.self.DesiredImage)
	assert.True(t, worker.self.PreserveVolumes)
}

func TestServerRuntimeSelfUpdatePreservesValidatedEdgeAppNetworks(t *testing.T) {
	worker := &fakeRuntimeWorker{}
	server := NewServer(worker, "runtime-1")
	command := &runtimev1.RuntimeSelfUpdateCommand{
		Identity: protoTestIdentity("cmd-edge", time.Unix(10, 0).UTC()), TargetComponentId: "gordon-edge-fixture-g1", TargetComponentRole: string(domain.ComponentRoleEdge), TargetVersion: "v1.2.3", Policy: string(domain.RuntimeSelfUpdatePolicyManualApproval), PolicyDecisionId: "migration:fixture", LifecycleAction: string(domain.RuntimeComponentLifecycleActivate), LifecycleProfile: testProtoLifecycleProfile(domain.ComponentRoleEdge), PreserveVolumes: true,
		EdgeAppNetworks: []string{"gordon-app-one", "gordon-app-two"},
	}

	_, err := server.RuntimeSelfUpdate(context.Background(), &runtimev1.RuntimeSelfUpdateRequest{Command: command})
	require.NoError(t, err)
	assert.Equal(t, command.EdgeAppNetworks, worker.self.EdgeAppNetworks)
}

func TestServerRuntimeSelfUpdateRejectsUnsafeEdgeAppNetworks(t *testing.T) {
	worker := &fakeRuntimeWorker{}
	server := NewServer(worker, "runtime-1")
	command := &runtimev1.RuntimeSelfUpdateCommand{
		Identity: protoTestIdentity("cmd-edge", time.Unix(10, 0).UTC()), TargetComponentId: "gordon-edge-fixture-g1", TargetComponentRole: string(domain.ComponentRoleEdge), TargetVersion: "v1.2.3", Policy: string(domain.RuntimeSelfUpdatePolicyManualApproval), PolicyDecisionId: "migration:fixture", LifecycleAction: string(domain.RuntimeComponentLifecycleActivate), LifecycleProfile: testProtoLifecycleProfile(domain.ComponentRoleEdge), PreserveVolumes: true,
		EdgeAppNetworks: []string{"gordon-app-one", "../engine-network"},
	}

	_, err := server.RuntimeSelfUpdate(context.Background(), &runtimev1.RuntimeSelfUpdateRequest{Command: command})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Empty(t, worker.self.EdgeAppNetworks)
}

func TestServerWatchActualStateStreamsSanitizedSnapshots(t *testing.T) {
	snapshot := testActualStateSnapshot()
	state := &fakeRuntimeStateSubscriber{snapshots: []domain.RuntimeActualStateSnapshot{snapshot}}
	server := NewServerWithStateSubscriber(&fakeRuntimeWorker{}, state, "runtime-1")
	stream := &fakeActualStateStream{ctx: context.Background()}

	err := server.WatchActualState(&runtimev1.WatchActualStateRequest{}, stream)

	require.NoError(t, err)
	require.Len(t, stream.snapshots, 1)
	got := stream.snapshots[0]
	assert.Equal(t, uint64(7), got.GetGeneration())
	assert.Equal(t, "app.example.com", got.GetRoutes()[0].GetDomain())
	assert.Equal(t, "gordon-target-app-example-com", got.GetRoutes()[0].GetEdgeTargetAlias())
	assert.Equal(t, "gordon-data", got.GetVolumes()[0].GetName())
	assert.Empty(t, got.GetContainers()[0].GetLabels()["secret.env"])
}

func TestServerWatchActualStateCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	state := &fakeRuntimeStateSubscriber{snapshots: []domain.RuntimeActualStateSnapshot{testActualStateSnapshot()}, beforeSend: cancel}
	server := NewServerWithStateSubscriber(&fakeRuntimeWorker{}, state, "runtime-1")

	err := server.WatchActualState(&runtimev1.WatchActualStateRequest{}, &fakeActualStateStream{ctx: ctx})

	require.Error(t, err)
	assert.Equal(t, codes.Canceled, status.Code(err))
}

func TestServerReportEdgeDrainInvokesReceiver(t *testing.T) {
	receiver := &recordingRouteDrainAckReceiver{}
	server := NewServerWithRouteDrainAckReceiver(&fakeRuntimeWorker{}, receiver, "runtime-1")

	ack, err := server.ReportEdgeDrain(context.Background(), &runtimev1.ReportEdgeDrainRequest{CanonicalDomain: "app.example.com", TransitionGeneration: 7, OldTargetKey: string(testDrainTargetKey), AcknowledgedAt: timestamppb.New(time.Unix(1, 0).UTC())})

	require.NoError(t, err)
	assert.True(t, ack.Ok)
}

func TestServerReportEdgeDrainValidation(t *testing.T) {
	server := NewServer(&fakeRuntimeWorker{}, "runtime-1")

	cases := []*runtimev1.ReportEdgeDrainRequest{
		nil,
		{TransitionGeneration: 7, OldTargetKey: string(testDrainTargetKey), AcknowledgedAt: timestamppb.New(time.Unix(1, 0).UTC())},
		{CanonicalDomain: "app.example.com", OldTargetKey: string(testDrainTargetKey), AcknowledgedAt: timestamppb.New(time.Unix(1, 0).UTC())},
		{CanonicalDomain: "app.example.com", TransitionGeneration: 7, OldTargetKey: string(testDrainTargetKey)},
		{CanonicalDomain: "app.example.com", TransitionGeneration: 7, AcknowledgedAt: timestamppb.New(time.Unix(1, 0).UTC())},
	}
	for _, req := range cases {
		_, err := server.ReportEdgeDrain(context.Background(), req)
		require.Error(t, err)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
	}
}

func TestServerReportEdgeDrainRejectsInvalidRouteDomainBeforeReceiver(t *testing.T) {
	receiver := &recordingRouteDrainAckReceiver{}
	server := NewServerWithRouteDrainAckReceiver(&fakeRuntimeWorker{}, receiver, "runtime-1")

	invalidDomains := []string{"localhost", "127.0.0.1", "app.example.com/path", "app.example.com\\path", "app.local"}
	for _, routeDomain := range invalidDomains {
		t.Run(routeDomain, func(t *testing.T) {
			_, err := server.ReportEdgeDrain(context.Background(), &runtimev1.ReportEdgeDrainRequest{CanonicalDomain: routeDomain, TransitionGeneration: 7, OldTargetKey: string(testDrainTargetKey), AcknowledgedAt: timestamppb.New(time.Unix(1, 0).UTC())})

			require.Error(t, err)
			assert.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
	assert.Empty(t, receiver.acks)
}

func TestServerReportEdgeDrainRejectsInvalidTargetAliasBeforeReceiver(t *testing.T) {
	receiver := &recordingRouteDrainAckReceiver{}
	server := NewServerWithRouteDrainAckReceiver(&fakeRuntimeWorker{}, receiver, "runtime-1")

	invalidAliases := []string{"localhost", "127.0.0.1", "127.10.0.1", "::1", "gordon/target", "gordon\\target"}
	for _, targetAlias := range invalidAliases {
		t.Run(targetAlias, func(t *testing.T) {
			_, err := server.ReportEdgeDrain(context.Background(), &runtimev1.ReportEdgeDrainRequest{CanonicalDomain: "app.example.com", TransitionGeneration: 7, OldTargetKey: targetAlias, AcknowledgedAt: timestamppb.New(time.Unix(1, 0).UTC())})

			require.Error(t, err)
			assert.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
	assert.Empty(t, receiver.acks)
}

func TestServerStreamLogsUsesRuntimeLogReader(t *testing.T) {
	reader := outMocks.NewMockRuntimeLogReader(t)
	reader.EXPECT().
		ReadRouteLogs(mock.Anything, "app.example.com", true).
		Return(io.NopCloser(strings.NewReader("log-a\nlog-b\n")), nil)
	server := NewServerWithLogReader(&fakeRuntimeWorker{}, reader, "runtime-1")
	stream := &fakeLogStream{ctx: context.Background()}

	err := server.StreamLogs(&runtimev1.StreamLogsRequest{RouteDomain: "app.example.com", Follow: true}, stream)

	require.NoError(t, err)
	require.Len(t, stream.chunks, 1)
	assert.Equal(t, "log-a\nlog-b\n", string(stream.chunks[0].Data))
}

func TestServerVolumeManager(t *testing.T) {
	volumeManager := outMocks.NewMockRuntimeVolumeManager(t)
	volumes := []*domain.VolumeInfo{{Name: "gordon-data", Driver: "local", Size: 1024, Labels: map[string]string{domain.LabelManaged: "true"}}}
	volumeManager.EXPECT().ListRuntimeVolumes(mock.Anything).Return(volumes, nil)
	volumeManager.EXPECT().RemoveRuntimeVolume(mock.Anything, "gordon-data", false).Return(nil)
	server := NewServerWithVolumeManager(&fakeRuntimeWorker{}, volumeManager, "runtime-1")

	resp, err := server.ListVolumes(context.Background(), &runtimev1.ListVolumesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetVolumes(), 1)
	assert.Equal(t, "gordon-data", resp.GetVolumes()[0].GetName())
	assert.Equal(t, "true", resp.GetVolumes()[0].GetLabels()[domain.LabelManaged])

	ack, err := server.RemoveVolume(context.Background(), &runtimev1.RemoveVolumeRequest{Name: "gordon-data"})
	require.NoError(t, err)
	assert.True(t, ack.Ok)
}

func TestServerImageManager(t *testing.T) {
	imageManager := outMocks.NewMockRuntimeImageManager(t)
	images := []domain.RuntimeImageDetail{{ID: "sha256:111", RepoTags: []string{"gordon/api:latest"}, Size: 1024, Created: time.Unix(10, 0).UTC()}}
	imageManager.EXPECT().ListRuntimeImages(mock.Anything).Return(images, nil)
	imageManager.EXPECT().PruneRuntimeImages(mock.Anything, true).Return(domain.RuntimePruneResult{DeletedCount: 2, SpaceReclaimed: 4096}, nil)
	server := NewServerWithImageManager(&fakeRuntimeWorker{}, imageManager, "runtime-1")

	resp, err := server.ListImages(context.Background(), &runtimev1.ListImagesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetImages(), 1)
	assert.Equal(t, "sha256:111", resp.GetImages()[0].GetId())
	assert.Equal(t, []string{"gordon/api:latest"}, resp.GetImages()[0].GetRepoTags())

	prune, err := server.PruneImages(context.Background(), &runtimev1.PruneImagesRequest{DanglingOnly: true})
	require.NoError(t, err)
	assert.Equal(t, int32(2), prune.GetDeletedCount())
	assert.Equal(t, int64(4096), prune.GetSpaceReclaimed())
}

func TestServerHealthAndDrainAck(t *testing.T) {
	receiver := &recordingRouteDrainAckReceiver{}
	server := NewServerWithRouteDrainAckReceiver(&fakeRuntimeWorker{}, receiver, "runtime-1")

	health, err := server.GetHealth(context.Background(), &runtimev1.GetHealthRequest{})
	require.NoError(t, err)
	assert.True(t, health.Ok)
	assert.Equal(t, "runtime-1", health.ComponentId)

	ack, err := server.ReportEdgeDrain(context.Background(), &runtimev1.ReportEdgeDrainRequest{CanonicalDomain: "app.example.com", TransitionGeneration: 7, OldTargetKey: string(testDrainTargetKey), AcknowledgedAt: timestamppb.New(time.Unix(1, 0).UTC())})
	require.NoError(t, err)
	assert.True(t, ack.Ok)

	_, err = server.ReportEdgeDrain(context.Background(), &runtimev1.ReportEdgeDrainRequest{CanonicalDomain: "app.example.com"})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestRuntimeServerRequiresComponentAuth(t *testing.T) {
	conn := newAuthenticatedRuntimeServerConn(t, NewServer(&fakeRuntimeWorker{}, "runtime-1"))
	client := runtimev1.NewRuntimeServiceClient(conn)

	_, err := client.GetHealth(context.Background(), &runtimev1.GetHealthRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))

	ctx := grpctest.AuthenticatedContext(context.Background(), grpctest.LocalComponentToken)
	resp, err := client.GetHealth(ctx, &runtimev1.GetHealthRequest{})
	require.NoError(t, err)
	assert.True(t, resp.Ok)
}

func TestRuntimeServerAuthenticatesRealPersistedComponentTokens(t *testing.T) {
	store, err := tokenstore.NewComponentTokenStore(domain.SecretsBackendUnsafe, t.TempDir(), zerowrap.Default())
	require.NoError(t, err)
	service := componentauth.NewService(store, zerowrap.Default(), componentauth.Config{})
	worker := &countingRuntimeWorker{}
	server := NewServerWithStateSubscriber(worker, &fakeRuntimeStateSubscriber{snapshots: []domain.RuntimeActualStateSnapshot{testActualStateSnapshot()}}, "runtime-1")
	harness := grpctest.NewHarness(t, func(registrar grpc.ServiceRegistrar) {
		runtimev1.RegisterRuntimeServiceServer(registrar, server)
	},
		grpc.UnaryInterceptor(interceptors.ComponentAuthUnaryInterceptor(service, MethodScopes(), MethodRoles())),
		grpc.StreamInterceptor(interceptors.ComponentAuthStreamInterceptor(service, MethodScopes(), MethodRoles())),
	)
	client := runtimev1.NewRuntimeServiceClient(harness.Conn(t))
	request := &runtimev1.ApplyCommandRequest{Command: &runtimev1.ApplyCommandRequest_DeployRoute{DeployRoute: &runtimev1.DeployRouteCommand{
		Identity: protoTestIdentity("command-1", time.Unix(10, 0).UTC()),
		Domain:   "app.example.com",
		Image:    "app:latest",
	}}}
	create := func(scopes []domain.ComponentScope, expiresAt time.Time) *componentauth.CreateResult {
		t.Helper()
		created, createErr := service.CreateToken(context.Background(), componentauth.CreateRequest{
			Name:      "control-1",
			Role:      domain.ComponentRoleControl,
			Scopes:    scopes,
			ExpiresAt: expiresAt,
		})
		require.NoError(t, createErr)
		return created
	}
	call := func(token string) error {
		t.Helper()
		_, callErr := client.ApplyCommand(grpctest.AuthenticatedContext(context.Background(), token), request)
		return callErr
	}

	_, err = client.ApplyCommand(context.Background(), request)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.Zero(t, worker.deployCalls)

	valid := create([]domain.ComponentScope{domain.ComponentScopeRuntimeDeploy, domain.ComponentScopeRuntimeStatus}, time.Time{})
	require.Equal(t, codes.Unauthenticated, status.Code(call(valid.Token+"wrong")))
	require.Zero(t, worker.deployCalls)

	revoked := create([]domain.ComponentScope{domain.ComponentScopeRuntimeDeploy}, time.Time{})
	require.NoError(t, service.RevokeToken(context.Background(), revoked.Metadata.KeyID))
	require.Equal(t, codes.PermissionDenied, status.Code(call(revoked.Token)))
	require.Zero(t, worker.deployCalls)

	expired := create([]domain.ComponentScope{domain.ComponentScopeRuntimeDeploy}, time.Now().Add(-time.Hour))
	require.Equal(t, codes.PermissionDenied, status.Code(call(expired.Token)))
	require.Zero(t, worker.deployCalls)

	insufficient := create([]domain.ComponentScope{domain.ComponentScopeRuntimeStatus}, time.Time{})
	require.Equal(t, codes.PermissionDenied, status.Code(call(insufficient.Token)))
	require.Zero(t, worker.deployCalls)

	require.NoError(t, call(valid.Token))
	require.Equal(t, 1, worker.deployCalls)

	stream, err := client.WatchActualState(grpctest.AuthenticatedContext(context.Background(), valid.Token), &runtimev1.WatchActualStateRequest{})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)

	missingStream, err := client.WatchActualState(context.Background(), &runtimev1.WatchActualStateRequest{})
	require.NoError(t, err)
	_, err = missingStream.Recv()
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestRuntimeServerAuthenticatesStandaloneServiceRPCsWithPersistedComponentTokens(t *testing.T) {
	store, err := tokenstore.NewComponentTokenStore(domain.SecretsBackendUnsafe, t.TempDir(), zerowrap.Default())
	require.NoError(t, err)
	service := componentauth.NewService(store, zerowrap.Default(), componentauth.Config{})
	manager := &fakeRuntimeStandaloneServiceManager{}
	server := NewServerWithStandaloneServiceManager(&fakeRuntimeWorker{}, manager, "runtime-1")
	harness := grpctest.NewHarness(t, func(registrar grpc.ServiceRegistrar) {
		runtimev1.RegisterRuntimeServiceServer(registrar, server)
	}, grpc.UnaryInterceptor(interceptors.ComponentAuthUnaryInterceptor(service, MethodScopes(), MethodRoles())))
	client := runtimev1.NewRuntimeServiceClient(harness.Conn(t))

	createControlToken := func(name string, scope domain.ComponentScope) string {
		t.Helper()
		created, createErr := service.CreateToken(context.Background(), componentauth.CreateRequest{
			Name:   name,
			Role:   domain.ComponentRoleControl,
			Scopes: []domain.ComponentScope{scope},
		})
		require.NoError(t, createErr)
		return created.Token
	}
	deployToken := createControlToken("control-deploy", domain.ComponentScopeRuntimeDeploy)
	statusToken := createControlToken("control-status", domain.ComponentScopeRuntimeStatus)
	wrongRoleToken := persistForgedRuntimeAuthorityToken(t, store, domain.ComponentRoleRuntime)
	requestedAt := time.Unix(10, 0).UTC()

	tests := []struct {
		name            string
		wrongScopeToken string
		validToken      string
		calls           func() int
		call            func(context.Context) error
	}{
		{
			name:            "apply requires runtime deploy",
			wrongScopeToken: statusToken,
			validToken:      deployToken,
			calls:           func() int { return manager.applyCalls },
			call: func(ctx context.Context) error {
				_, callErr := client.ApplyStandaloneService(ctx, &runtimev1.ApplyStandaloneServiceRequest{Command: &runtimev1.ApplyStandaloneServiceCommand{
					Identity: protoTestIdentity("apply-auth", requestedAt),
					Service:  &runtimev1.StandaloneServiceSpec{Name: "game", Image: "game:latest", Enabled: true},
				}})
				return callErr
			},
		},
		{
			name:            "remove requires runtime deploy",
			wrongScopeToken: statusToken,
			validToken:      deployToken,
			calls:           func() int { return manager.removeCalls },
			call: func(ctx context.Context) error {
				_, callErr := client.RemoveStandaloneService(ctx, &runtimev1.RemoveStandaloneServiceRequest{Command: &runtimev1.RemoveStandaloneServiceCommand{
					Identity: protoTestIdentity("remove-auth", requestedAt),
					Name:     "game",
				}})
				return callErr
			},
		},
		{
			name:            "list requires runtime status",
			wrongScopeToken: deployToken,
			validToken:      statusToken,
			calls:           func() int { return manager.listCalls },
			call: func(ctx context.Context) error {
				_, callErr := client.ListStandaloneServiceState(ctx, &runtimev1.ListStandaloneServiceStateRequest{})
				return callErr
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.calls()

			require.Equal(t, codes.Unauthenticated, status.Code(tt.call(context.Background())))
			require.Equal(t, before, tt.calls(), "missing token invoked manager")

			wrongScopeCtx := grpctest.AuthenticatedContext(context.Background(), tt.wrongScopeToken)
			require.Equal(t, codes.PermissionDenied, status.Code(tt.call(wrongScopeCtx)))
			require.Equal(t, before, tt.calls(), "wrong scope invoked manager")

			wrongRoleCtx := grpctest.AuthenticatedContext(context.Background(), wrongRoleToken)
			require.Equal(t, codes.PermissionDenied, status.Code(tt.call(wrongRoleCtx)))
			require.Equal(t, before, tt.calls(), "wrong role invoked manager")

			validCtx := grpctest.AuthenticatedContext(context.Background(), tt.validToken)
			require.NoError(t, tt.call(validCtx))
			require.Equal(t, before+1, tt.calls(), "valid control token did not invoke manager")
		})
	}
}

func TestRuntimeServerRejectsForgedNonControlRoleTokensWithRuntimeScopes(t *testing.T) {
	store, err := tokenstore.NewComponentTokenStore(domain.SecretsBackendUnsafe, t.TempDir(), zerowrap.Default())
	require.NoError(t, err)
	service := componentauth.NewService(store, zerowrap.Default(), componentauth.Config{})
	server := NewServerWithStateSubscriber(&fakeRuntimeWorker{}, &fakeRuntimeStateSubscriber{snapshots: []domain.RuntimeActualStateSnapshot{testActualStateSnapshot()}}, "runtime-1")
	harness := grpctest.NewHarness(t, func(registrar grpc.ServiceRegistrar) {
		runtimev1.RegisterRuntimeServiceServer(registrar, server)
	},
		grpc.UnaryInterceptor(interceptors.ComponentAuthUnaryInterceptor(service, MethodScopes(), MethodRoles())),
		grpc.StreamInterceptor(interceptors.ComponentAuthStreamInterceptor(service, MethodScopes(), MethodRoles())),
	)
	client := runtimev1.NewRuntimeServiceClient(harness.Conn(t))
	deploy := &runtimev1.ApplyCommandRequest{Command: &runtimev1.ApplyCommandRequest_DeployRoute{DeployRoute: &runtimev1.DeployRouteCommand{
		Identity: protoTestIdentity("forged-command", time.Unix(10, 0).UTC()),
		Domain:   "app.example.com",
		Image:    "app:latest",
	}}}
	selfUpdate := &runtimev1.RuntimeSelfUpdateRequest{Command: &runtimev1.RuntimeSelfUpdateCommand{
		Identity:          protoTestIdentity("forged-update", time.Unix(10, 0).UTC()),
		TargetComponentId: "runtime-1",
	}}

	for _, role := range []domain.ComponentRole{domain.ComponentRoleRuntime, domain.ComponentRoleEdge, domain.ComponentRoleRegistry} {
		t.Run(string(role), func(t *testing.T) {
			ctx := grpctest.AuthenticatedContext(context.Background(), persistForgedRuntimeAuthorityToken(t, store, role))

			_, callErr := client.ApplyCommand(ctx, deploy)
			require.Equal(t, codes.PermissionDenied, status.Code(callErr))
			_, callErr = client.GetHealth(ctx, &runtimev1.GetHealthRequest{})
			require.Equal(t, codes.PermissionDenied, status.Code(callErr))
			_, callErr = client.RuntimeSelfUpdate(ctx, selfUpdate)
			require.Equal(t, codes.PermissionDenied, status.Code(callErr))

			logs, callErr := client.StreamLogs(ctx, &runtimev1.StreamLogsRequest{RouteDomain: "app.example.com"})
			require.NoError(t, callErr)
			_, callErr = logs.Recv()
			require.Equal(t, codes.PermissionDenied, status.Code(callErr))

			state, callErr := client.WatchActualState(ctx, &runtimev1.WatchActualStateRequest{})
			require.NoError(t, callErr)
			_, callErr = state.Recv()
			require.Equal(t, codes.PermissionDenied, status.Code(callErr))
		})
	}
}

func persistForgedRuntimeAuthorityToken(t *testing.T, store interface {
	CreateComponentToken(context.Context, *domain.ComponentTokenRecord) error
}, role domain.ComponentRole) string {
	t.Helper()
	keyID := string(role) + "-forged"
	token := "gordon_component." + keyID + ".matching-runtime-scopes"
	hash := sha256.Sum256([]byte(token))
	err := store.CreateComponentToken(context.Background(), &domain.ComponentTokenRecord{
		KeyID:     keyID,
		Prefix:    "gordon_component",
		Name:      string(role) + "-1",
		Role:      role,
		Scopes:    domain.AllComponentScopes(),
		TokenHash: hex.EncodeToString(hash[:]),
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	return token
}

func TestRuntimeUnaryAuthCoverageIncludesEveryRPC(t *testing.T) {
	scopes := MethodScopes()
	roles := MethodRoles()
	service := runtimev1.RuntimeService_ServiceDesc
	require.Len(t, scopes, len(service.Methods)+len(service.Streams))
	require.Len(t, roles, len(service.Methods)+len(service.Streams))

	for _, method := range service.Methods {
		fullMethod := "/" + service.ServiceName + "/" + method.MethodName
		assert.Contains(t, scopes, fullMethod, "unary RPC missing scope coverage")
		assert.Equal(t, domain.ComponentRoleControl, roles[fullMethod], "unary RPC missing control-role coverage")
	}
	for _, stream := range service.Streams {
		fullMethod := "/" + service.ServiceName + "/" + stream.StreamName
		assert.Contains(t, scopes, fullMethod, "streaming RPC missing scope coverage")
		assert.Equal(t, domain.ComponentRoleControl, roles[fullMethod], "streaming RPC missing control-role coverage")
	}
}

func TestMethodScopesRequireRuntimePermissions(t *testing.T) {
	scopes := MethodScopes()
	assert.Equal(t, domain.ComponentScopeRuntimeDeploy, scopes[runtimev1.RuntimeService_ApplyCommand_FullMethodName])
	assert.Equal(t, domain.ComponentScopeRuntimeStatus, scopes[runtimev1.RuntimeService_ListVolumes_FullMethodName])
	assert.Equal(t, domain.ComponentScopeRuntimeDeploy, scopes[runtimev1.RuntimeService_RemoveVolume_FullMethodName])
	assert.Equal(t, domain.ComponentScopeRuntimeStatus, scopes[runtimev1.RuntimeService_ListImages_FullMethodName])
	assert.Equal(t, domain.ComponentScopeRuntimeDeploy, scopes[runtimev1.RuntimeService_PruneImages_FullMethodName])
	assert.Equal(t, domain.ComponentScopeRuntimeSelfUpdate, scopes[runtimev1.RuntimeService_RuntimeSelfUpdate_FullMethodName])
	assert.Equal(t, domain.ComponentScopeRuntimeDrainAck, scopes[runtimev1.RuntimeService_ReportEdgeDrain_FullMethodName])
	assert.Equal(t, domain.ComponentScopeRuntimeDeploy, scopes[runtimev1.RuntimeService_ApplyStandaloneService_FullMethodName])
	assert.Equal(t, domain.ComponentScopeRuntimeDeploy, scopes[runtimev1.RuntimeService_RemoveStandaloneService_FullMethodName])
	assert.Equal(t, domain.ComponentScopeRuntimeStatus, scopes[runtimev1.RuntimeService_ListStandaloneServiceState_FullMethodName])
}

func TestServerStandaloneServiceHandlersMapCommandsAndState(t *testing.T) {
	requestedAt := time.Unix(10, 0).UTC()
	manager := &fakeRuntimeStandaloneServiceManager{
		applyResult:  testRuntimeResult(testDomainIdentity("apply-game", requestedAt)),
		removeResult: testRuntimeResult(testDomainIdentity("remove-game", requestedAt)),
		states: []domain.RuntimeStandaloneServiceState{{
			Name: "game", ContainerID: "container-1", ContainerName: "gordon-service-game", Status: domain.ContainerStatusRunning, ConfigHash: "config-hash", Cleanup: domain.StandaloneServiceCleanup{PreserveVolumes: false, RemoveContainer: true},
		}},
	}
	server := NewServerWithStandaloneServiceManager(&fakeRuntimeWorker{}, manager, "runtime-1")
	applyRequest := &runtimev1.ApplyStandaloneServiceRequest{Command: &runtimev1.ApplyStandaloneServiceCommand{
		Identity: protoTestIdentity("apply-game", requestedAt),
		Service: &runtimev1.StandaloneServiceSpec{
			Name: "game", Image: "game:latest", Enabled: true,
			Ports:     []*runtimev1.StandaloneServicePortSpec{{Name: "game", Container: 28015, Protocol: string(domain.NetworkProtocolUDP), Publish: "127.0.0.1:38015", Private: true, Public: false, TrustedCidrs: []string{"10.0.0.0/8"}}},
			Volumes:   []*runtimev1.StandaloneServiceVolumeSpec{{Source: "game-data", Target: "/data", ReadOnly: true}},
			Readiness: &runtimev1.StandaloneServiceReadinessSpec{Type: domain.StandaloneServiceReadinessLog, Path: "/logs/game.log", Contains: "ready", TimeoutNs: int64(time.Second), TimeoutSet: true},
			Cleanup:   &runtimev1.StandaloneServiceCleanupSpec{PreserveVolumes: true, RemoveContainer: true},
		},
		ResolvedEnv: []string{"TOKEN=super-secret"},
		ConfigHash:  "config-hash",
	}}

	applyResponse, err := server.ApplyStandaloneService(context.Background(), applyRequest)
	require.NoError(t, err)
	assert.Equal(t, "apply-game", applyResponse.GetResult().GetCommandId())
	assert.Equal(t, domain.ApplyStandaloneServiceCommand{
		RuntimeCommandIdentity: testDomainIdentity("apply-game", requestedAt),
		Service: domain.StandaloneService{
			Name: "game", Image: "game:latest", Enabled: true,
			Ports:     []domain.StandaloneServicePort{{Name: "game", Container: 28015, Protocol: domain.NetworkProtocolUDP, Publish: "127.0.0.1:38015", Private: true, TrustedCIDRs: []string{"10.0.0.0/8"}}},
			Volumes:   []domain.StandaloneServiceVolume{{Source: "game-data", Target: "/data", ReadOnly: true}},
			Readiness: domain.StandaloneServiceReadiness{Type: domain.StandaloneServiceReadinessLog, Path: "/logs/game.log", Contains: "ready", Timeout: time.Second, TimeoutSet: true},
			Cleanup:   domain.StandaloneServiceCleanup{PreserveVolumes: true, RemoveContainer: true},
		},
		ResolvedEnv: []string{"TOKEN=super-secret"}, ConfigHash: "config-hash",
	}, manager.apply)
	assert.NotContains(t, applyResponse.String(), "super-secret")

	removeResponse, err := server.RemoveStandaloneService(context.Background(), &runtimev1.RemoveStandaloneServiceRequest{Command: &runtimev1.RemoveStandaloneServiceCommand{Identity: protoTestIdentity("remove-game", requestedAt), Name: "game", Reason: "disabled", Cleanup: &runtimev1.StandaloneServiceCleanupSpec{PreserveVolumes: false, RemoveContainer: true}}})
	require.NoError(t, err)
	assert.Equal(t, "remove-game", removeResponse.GetResult().GetCommandId())
	assert.Equal(t, domain.RemoveStandaloneServiceCommand{RuntimeCommandIdentity: testDomainIdentity("remove-game", requestedAt), Name: "game", Reason: "disabled", Cleanup: domain.StandaloneServiceCleanup{PreserveVolumes: false, RemoveContainer: true}}, manager.remove)

	listResponse, err := server.ListStandaloneServiceState(context.Background(), &runtimev1.ListStandaloneServiceStateRequest{})
	require.NoError(t, err)
	assert.Equal(t, []*runtimev1.RuntimeStandaloneServiceState{{Name: "game", ContainerId: "container-1", ContainerName: "gordon-service-game", Status: string(domain.ContainerStatusRunning), ConfigHash: "config-hash", Cleanup: &runtimev1.StandaloneServiceCleanupSpec{PreserveVolumes: false, RemoveContainer: true}}}, listResponse.GetServices())
}

func TestServerStandaloneServiceHandlersRequireManagerAndCommand(t *testing.T) {
	server := NewServer(&fakeRuntimeWorker{}, "runtime-1")

	_, err := server.ApplyStandaloneService(context.Background(), &runtimev1.ApplyStandaloneServiceRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	_, err = server.RemoveStandaloneService(context.Background(), &runtimev1.RemoveStandaloneServiceRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	_, err = server.ListStandaloneServiceState(context.Background(), &runtimev1.ListStandaloneServiceStateRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))

	configured := NewServerWithStandaloneServiceManager(&fakeRuntimeWorker{}, &fakeRuntimeStandaloneServiceManager{}, "runtime-1")
	_, err = configured.ApplyStandaloneService(context.Background(), nil)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = configured.ApplyStandaloneService(context.Background(), &runtimev1.ApplyStandaloneServiceRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = configured.RemoveStandaloneService(context.Background(), nil)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = configured.RemoveStandaloneService(context.Background(), &runtimev1.RemoveStandaloneServiceRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestServerStandaloneServiceHandlersReturnGenericManagerErrors(t *testing.T) {
	manager := &fakeRuntimeStandaloneServiceManager{
		applyErr:  errors.New("TOKEN=super-secret"),
		removeErr: errors.New("TOKEN=super-secret"),
		listErr:   errors.New("TOKEN=super-secret"),
	}
	server := NewServerWithStandaloneServiceManager(&fakeRuntimeWorker{}, manager, "runtime-1")

	_, err := server.ApplyStandaloneService(context.Background(), &runtimev1.ApplyStandaloneServiceRequest{Command: &runtimev1.ApplyStandaloneServiceCommand{}})
	require.Error(t, err)
	assert.Equal(t, codes.Internal, status.Code(err))
	assert.NotContains(t, status.Convert(err).Message(), "super-secret")
	_, err = server.RemoveStandaloneService(context.Background(), &runtimev1.RemoveStandaloneServiceRequest{Command: &runtimev1.RemoveStandaloneServiceCommand{}})
	require.Error(t, err)
	assert.Equal(t, codes.Internal, status.Code(err))
	assert.NotContains(t, status.Convert(err).Message(), "super-secret")
	_, err = server.ListStandaloneServiceState(context.Background(), &runtimev1.ListStandaloneServiceStateRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.Internal, status.Code(err))
	assert.NotContains(t, status.Convert(err).Message(), "super-secret")
}

type fakeActualStateStream struct {
	runtimev1.RuntimeService_WatchActualStateServer
	ctx       context.Context
	snapshots []*runtimev1.WatchActualStateResponse
}

func (f *fakeActualStateStream) Context() context.Context { return f.ctx }

func (f *fakeActualStateStream) Send(snapshot *runtimev1.WatchActualStateResponse) error {
	f.snapshots = append(f.snapshots, snapshot)
	return nil
}

type fakeRuntimeStateSubscriber struct {
	snapshots  []domain.RuntimeActualStateSnapshot
	beforeSend func()
}

func (f *fakeRuntimeStateSubscriber) SubscribeRuntimeState(ctx context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
	ch := make(chan domain.RuntimeActualStateSnapshot)
	go func() {
		defer close(ch)
		for _, snapshot := range f.snapshots {
			if f.beforeSend != nil {
				f.beforeSend()
			}
			select {
			case <-ctx.Done():
				return
			case ch <- snapshot:
			}
		}
	}()
	return ch, nil
}

func testActualStateSnapshot() domain.RuntimeActualStateSnapshot {
	return domain.RuntimeActualStateSnapshot{
		Generation:        7,
		StateVersion:      "state-v1",
		SourceComponentID: "runtime-1",
		ObservedAt:        time.Unix(11, 0).UTC(),
		Routes: []domain.RuntimeRouteState{{
			Domain:               "app.example.com",
			Generation:           7,
			RouteVersion:         "route-v1",
			ContainerAlias:       "app-example-com",
			EdgeTargetAlias:      "gordon-target-app-example-com",
			TargetPort:           8080,
			Scheme:               "http",
			Protocol:             domain.RouteTargetProtocolHTTP1,
			Status:               domain.RouteTargetStatusReady,
			BackingContainerName: "gordon-app-example-com",
		}},
		Containers: []domain.RuntimeContainerState{{
			Name:       "gordon-app-example-com",
			Alias:      "app-example-com",
			Image:      "app:latest",
			Status:     domain.ContainerStatusRunning,
			Labels:     map[string]string{domain.LabelDomain: "app.example.com", "secret.env": "DATABASE_URL=secret"},
			Generation: 7,
		}},
		Networks:        []domain.RuntimeNetworkState{{Name: "gordon-app", Driver: "bridge", Aliases: []string{"app-example-com"}, Generation: 7}},
		Volumes:         []domain.RuntimeVolumeState{{Name: "gordon-data", AttachedTo: []string{"gordon-app-example-com"}, Generation: 7}},
		EdgeAttachments: []domain.RuntimeEdgeNetworkAttachmentState{{RouteDomain: "app.example.com", NetworkName: "gordon-app", EdgeAlias: "edge-1", RuntimeAlias: "runtime-1", TargetAlias: "gordon-target-app-example-com", TargetPort: 8080, Attached: true, Generation: 7, SourceComponent: "runtime-1"}},
	}
}

type fakeLogStream struct {
	runtimev1.RuntimeService_StreamLogsServer
	ctx    context.Context
	chunks []*runtimev1.StreamLogsResponse
}

func (f *fakeLogStream) Context() context.Context { return f.ctx }

func (f *fakeLogStream) SendHeader(metadata.MD) error { return nil }

func (f *fakeLogStream) Send(chunk *runtimev1.StreamLogsResponse) error {
	f.chunks = append(f.chunks, chunk)
	return nil
}

type fakeRuntimeStandaloneServiceManager struct {
	apply        domain.ApplyStandaloneServiceCommand
	applyResult  domain.RuntimeCommandResult
	applyErr     error
	applyCalls   int
	remove       domain.RemoveStandaloneServiceCommand
	removeResult domain.RuntimeCommandResult
	removeErr    error
	removeCalls  int
	states       []domain.RuntimeStandaloneServiceState
	listErr      error
	listCalls    int
}

func (f *fakeRuntimeStandaloneServiceManager) ApplyStandaloneService(_ context.Context, command domain.ApplyStandaloneServiceCommand) (domain.RuntimeCommandResult, error) {
	f.applyCalls++
	f.apply = command
	return f.applyResult, f.applyErr
}

func (f *fakeRuntimeStandaloneServiceManager) RemoveStandaloneService(_ context.Context, command domain.RemoveStandaloneServiceCommand) (domain.RuntimeCommandResult, error) {
	f.removeCalls++
	f.remove = command
	return f.removeResult, f.removeErr
}

func (f *fakeRuntimeStandaloneServiceManager) ListStandaloneServiceState(context.Context) ([]domain.RuntimeStandaloneServiceState, error) {
	f.listCalls++
	return f.states, f.listErr
}

type fakeRuntimeWorker struct {
	deploy    domain.DeployRouteCommand
	reconcile domain.ReconcileRuntimeCommand
	self      domain.RuntimeSelfUpdateCommand
}

type countingRuntimeWorker struct {
	fakeRuntimeWorker
	deployCalls int
}

func (f *countingRuntimeWorker) DeployRoute(ctx context.Context, command domain.DeployRouteCommand) (domain.RuntimeCommandResult, error) {
	f.deployCalls++
	return f.fakeRuntimeWorker.DeployRoute(ctx, command)
}

func (f *fakeRuntimeWorker) DeployRoute(_ context.Context, command domain.DeployRouteCommand) (domain.RuntimeCommandResult, error) {
	f.deploy = command
	return testRuntimeResult(command.RuntimeCommandIdentity), nil
}

func (f *fakeRuntimeWorker) RestartRoute(_ context.Context, command domain.RestartRouteCommand) (domain.RuntimeCommandResult, error) {
	return testRuntimeResult(command.RuntimeCommandIdentity), nil
}

func (f *fakeRuntimeWorker) RemoveRoute(_ context.Context, command domain.RemoveRouteCommand) (domain.RuntimeCommandResult, error) {
	return testRuntimeResult(command.RuntimeCommandIdentity), nil
}

func (f *fakeRuntimeWorker) Reconcile(_ context.Context, command domain.ReconcileRuntimeCommand) (domain.RuntimeCommandResult, error) {
	f.reconcile = command
	return testRuntimeResult(command.RuntimeCommandIdentity), nil
}

func (f *fakeRuntimeWorker) SelfUpdate(_ context.Context, command domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error) {
	f.self = command
	return testRuntimeResult(command.RuntimeCommandIdentity), nil
}

func newAuthenticatedRuntimeServerConn(t *testing.T, server runtimev1.RuntimeServiceServer) *grpc.ClientConn {
	t.Helper()
	validator := grpctest.NewAuthFixture("control-local", domain.ComponentRoleControl, domain.AllComponentScopes()...)
	harness := grpctest.NewHarness(t, func(registrar grpc.ServiceRegistrar) {
		runtimev1.RegisterRuntimeServiceServer(registrar, server)
	},
		grpc.UnaryInterceptor(interceptors.ComponentAuthUnaryInterceptor(validator, MethodScopes(), MethodRoles())),
		grpc.StreamInterceptor(interceptors.ComponentAuthStreamInterceptor(validator, MethodScopes(), MethodRoles())),
	)
	return harness.Conn(t)
}

func testProtoLifecycleProfile(role domain.ComponentRole) *runtimev1.RuntimeComponentLifecycleProfile {
	profile, _ := domain.FixedRuntimeComponentLifecycleProfile(role)
	return &runtimev1.RuntimeComponentLifecycleProfile{
		Uid: int64(profile.ProcessIdentity.UID), Gid: int64(profile.ProcessIdentity.GID), User: profile.ProcessIdentity.User,
		UsernsMode: profile.UsernsMode, CapDrop: append([]string(nil), profile.CapDrop...), NoNewPrivileges: profile.NoNewPrivileges,
		GenerationVolumeOptions: append([]string(nil), profile.GenerationVolumeOptions...),
	}
}

func protoTestIdentity(id string, requestedAt time.Time) *runtimev1.RuntimeCommandIdentity {
	return &runtimev1.RuntimeCommandIdentity{Id: id, IdempotencyKey: "idem-" + id, Generation: 7, SourceComponentId: "control-1", RequestedAt: timestamppb.New(requestedAt)}
}

func testDomainIdentity(id string, requestedAt time.Time) domain.RuntimeCommandIdentity {
	return domain.RuntimeCommandIdentity{ID: domain.RuntimeCommandID(id), IdempotencyKey: "idem-" + id, Generation: 7, SourceComponentID: "control-1", RequestedAt: requestedAt}
}

func testRuntimeResult(identity domain.RuntimeCommandIdentity) domain.RuntimeCommandResult {
	return domain.RuntimeCommandResult{CommandID: identity.ID, IdempotencyKey: identity.IdempotencyKey, Generation: identity.Generation, Status: domain.RuntimeCommandStatusSucceeded, StartedAt: identity.RequestedAt, CompletedAt: identity.RequestedAt}
}

const testDrainTargetKey domain.RouteTargetKey = "rtk_abcdefghijklmnopqrstuvwxyz234567"

type recordingRouteDrainAckReceiver struct{ acks []domain.RouteDrainAck }

func (r *recordingRouteDrainAckReceiver) AcknowledgeRouteDrain(_ context.Context, ack domain.RouteDrainAck) error {
	r.acks = append(r.acks, ack)
	return nil
}
