package runtime

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	runtimev1 "github.com/bnema/gordon/api/gordon/runtime/v1"
	inruntime "github.com/bnema/gordon/internal/adapters/in/grpc/runtime"
	outMocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
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
	conn := newRuntimeTestConn(t, inruntime.NewServer(worker, "runtime-1"))
	client := NewClient(conn)

	result, err := client.SelfUpdateRuntime(context.Background(), domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: testIdentity("cmd-self"), TargetComponentID: "runtime-1", TargetComponentRole: domain.ComponentRoleRuntime, TargetVersion: "v1.2.3", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "decision-1"})
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, result.Status)
	assert.Equal(t, "runtime-1", worker.self.TargetComponentID)

	require.NoError(t, client.PingRuntime(context.Background()))
	version, err := client.RuntimeVersion(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "runtime service ready", version)
	require.NoError(t, client.AcknowledgeRuntimeDrain(context.Background(), "app.example.com", 7))
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
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	runtimev1.RegisterRuntimeServiceServer(grpcServer, server)
	go func() {
		_ = grpcServer.Serve(listener)
	}()
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

type fakeRuntimeWorker struct {
	deploy domain.DeployRouteCommand
	self   domain.RuntimeSelfUpdateCommand
}

func (f *fakeRuntimeWorker) DeployRoute(_ context.Context, command domain.DeployRouteCommand) (domain.RuntimeCommandResult, error) {
	f.deploy = command
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
