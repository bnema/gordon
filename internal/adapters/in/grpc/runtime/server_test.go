package runtime

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	runtimev1 "github.com/bnema/gordon/api/gordon/runtime/v1"
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	outMocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

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
	}})

	require.NoError(t, err)
	assert.Equal(t, "cmd-self", resp.GetResult().CommandId)
	assert.Equal(t, domain.ComponentRoleRuntime, worker.self.TargetComponentRole)
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
	server := NewServer(&fakeRuntimeWorker{}, "runtime-1")

	health, err := server.GetHealth(context.Background(), &runtimev1.GetHealthRequest{})
	require.NoError(t, err)
	assert.True(t, health.Ok)
	assert.Equal(t, "runtime-1", health.ComponentId)

	ack, err := server.ReportEdgeDrain(context.Background(), &runtimev1.ReportEdgeDrainRequest{RouteDomain: "app.example.com", Generation: 7, TargetAlias: "gordon-target-app-example-com"})
	require.NoError(t, err)
	assert.True(t, ack.Ok)

	_, err = server.ReportEdgeDrain(context.Background(), &runtimev1.ReportEdgeDrainRequest{RouteDomain: "app.example.com"})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestRuntimeServerRequiresComponentAuth(t *testing.T) {
	conn := newAuthenticatedRuntimeServerConn(t, NewServer(&fakeRuntimeWorker{}, "runtime-1"), &fakeComponentValidator{identity: &domain.ComponentIdentity{Name: "control-1", Role: domain.ComponentRoleControl, Scopes: domain.AllComponentScopes()}})
	client := runtimev1.NewRuntimeServiceClient(conn)

	_, err := client.GetHealth(context.Background(), &runtimev1.GetHealthRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))

	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer token")
	resp, err := client.GetHealth(ctx, &runtimev1.GetHealthRequest{})
	require.NoError(t, err)
	assert.True(t, resp.Ok)
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
}

type fakeLogStream struct {
	runtimev1.RuntimeService_StreamLogsServer
	ctx    context.Context
	chunks []*runtimev1.LogChunk
}

func (f *fakeLogStream) Context() context.Context { return f.ctx }

func (f *fakeLogStream) SendHeader(metadata.MD) error { return nil }

func (f *fakeLogStream) Send(chunk *runtimev1.LogChunk) error {
	f.chunks = append(f.chunks, chunk)
	return nil
}

type fakeRuntimeWorker struct {
	deploy domain.DeployRouteCommand
	self   domain.RuntimeSelfUpdateCommand
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
	return testRuntimeResult(command.RuntimeCommandIdentity), nil
}

func (f *fakeRuntimeWorker) SelfUpdate(_ context.Context, command domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error) {
	f.self = command
	return testRuntimeResult(command.RuntimeCommandIdentity), nil
}

func newAuthenticatedRuntimeServerConn(t *testing.T, server runtimev1.RuntimeServiceServer, validator interceptors.ComponentTokenValidator) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(interceptors.ComponentAuthUnaryInterceptor(validator, MethodScopes())),
		grpc.StreamInterceptor(interceptors.ComponentAuthStreamInterceptor(validator, MethodScopes())),
	)
	runtimev1.RegisterRuntimeServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

type fakeComponentValidator struct {
	identity *domain.ComponentIdentity
	err      error
}

func (f *fakeComponentValidator) ValidateToken(_ context.Context, _ string, _ domain.ComponentScope) (*domain.ComponentIdentity, error) {
	return f.identity, f.err
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
