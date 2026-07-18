package edgesnapshot

import (
	"context"
	"testing"
	"time"

	edgev1 "github.com/bnema/gordon/api/gordon/edge/v1"
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/testutils/grpctest"
	usecase "github.com/bnema/gordon/internal/usecase/edgesnapshot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestRouteSnapshotTransportRoundTripAndPrivacy(t *testing.T) {
	snapshot := adapterTestSnapshot(t, 7)
	registry, err := domain.NewReadyRouteTargetEntry("registry.example.com", "registry-target.example", 5000, "http", domain.RouteTargetProtocolHTTP1, 7)
	require.NoError(t, err)
	snapshot.RegistryForwardingTarget = &registry
	wire, err := RouteSnapshotToProto(snapshot)
	require.NoError(t, err)
	got, err := RouteSnapshotFromProto(wire)
	require.NoError(t, err)
	assert.Equal(t, snapshot, got)

	assertProtoFields(t, (&edgev1.WatchRouteSnapshotsResponse{}).ProtoReflect().Descriptor(), "generation", "entries", "registry_forwarding_target")
	assertProtoFields(t, (&edgev1.RouteTargetEntry{}).ProtoReflect().Descriptor(), "canonical_domain", "target_host", "target_port", "scheme", "protocol", "status", "unavailable_reason", "generation", "upstream_host", "attachment", "target_key")
	assertProtoFields(t, (&edgev1.ReportDrainStateRequest{}).ProtoReflect().Descriptor(), "canonical_domain", "transition_generation", "old_target_key", "in_flight", "acknowledged_at", "timeout_reason")
}

func TestRouteSnapshotFromProtoRejectsNonCanonicalAndLoopbackTargets(t *testing.T) {
	valid, err := RouteSnapshotToProto(adapterTestSnapshot(t, 1))
	require.NoError(t, err)

	valid.Entries[0].CanonicalDomain = "App.Example.Com"
	_, err = RouteSnapshotFromProto(valid)
	require.Error(t, err)
	valid.Entries[0].CanonicalDomain = "app.example.com"
	valid.Entries[0].TargetHost = "localhost"
	_, err = RouteSnapshotFromProto(valid)
	require.Error(t, err)
}

func TestServerStreamsImmediateCurrentAndUpdatesThenCancels(t *testing.T) {
	hub := usecase.NewSnapshotHub()
	require.NoError(t, hub.Publish(adapterTestSnapshot(t, 1)))
	client := authenticatedClient(t, NewServer(hub), domain.ComponentRoleEdge, domain.ComponentScopeRoutesWatch, domain.ComponentScopeEdgeDrain)
	ctx, cancel := context.WithCancel(grpctest.AuthenticatedContext(context.Background(), grpctest.LocalComponentToken))
	stream, err := client.WatchRouteSnapshots(ctx, &edgev1.WatchRouteSnapshotsRequest{})
	require.NoError(t, err)
	first, err := stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, uint64(1), first.Generation)

	require.NoError(t, hub.Publish(adapterTestSnapshot(t, 2)))
	second, err := stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, uint64(2), second.Generation)
	cancel()
	_, err = stream.Recv()
	require.Error(t, err)
	assert.Equal(t, codes.Canceled, status.Code(err))
}

func TestServerAuthenticationRequiresEdgeRoleAndScopes(t *testing.T) {
	hub := usecase.NewSnapshotHub()
	require.NoError(t, hub.Publish(adapterTestSnapshot(t, 1)))

	cases := []struct {
		name   string
		role   domain.ComponentRole
		scopes []domain.ComponentScope
		ctx    context.Context
		code   codes.Code
	}{
		{name: "missing", ctx: context.Background(), code: codes.Unauthenticated},
		{name: "wrong role", role: domain.ComponentRoleRuntime, scopes: []domain.ComponentScope{domain.ComponentScopeRoutesWatch, domain.ComponentScopeEdgeDrain}, ctx: grpctest.AuthenticatedContext(context.Background(), grpctest.LocalComponentToken), code: codes.PermissionDenied},
		{name: "wrong scope", role: domain.ComponentRoleEdge, scopes: []domain.ComponentScope{domain.ComponentScopeEdgeDrain}, ctx: grpctest.AuthenticatedContext(context.Background(), grpctest.LocalComponentToken), code: codes.PermissionDenied},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			client := authenticatedClient(t, NewServer(hub), test.role, test.scopes...)
			stream, err := client.WatchRouteSnapshots(test.ctx, &edgev1.WatchRouteSnapshotsRequest{})
			require.NoError(t, err)
			_, err = stream.Recv()
			require.Error(t, err)
			assert.Equal(t, test.code, status.Code(err))
		})
	}
}

func TestServerReportDrainStateValidatesAndRelaysOpaqueData(t *testing.T) {
	hub := usecase.NewSnapshotHub()
	receiver := &recordingDrainReceiver{}
	client := authenticatedClient(t, NewServerWithDrainStateReceiver(hub, receiver), domain.ComponentRoleEdge, domain.ComponentScopeRoutesWatch, domain.ComponentScopeEdgeDrain)
	ctx := grpctest.AuthenticatedContext(context.Background(), grpctest.LocalComponentToken)
	key := adapterTestSnapshot(t, 1).Entries[0].TargetKey

	_, err := client.ReportDrainState(ctx, &edgev1.ReportDrainStateRequest{CanonicalDomain: "app.example.com", TransitionGeneration: 3, OldTargetKey: string(key), InFlight: 2, AcknowledgedAt: timestamppb.New(time.Unix(9, 0).UTC()), TimeoutReason: edgev1.DrainTimeoutReason_DRAIN_TIMEOUT_REASON_EDGE})
	require.NoError(t, err)
	assert.Equal(t, domain.RouteTargetGeneration(3), receiver.state.TransitionGeneration)
	assert.Equal(t, key, receiver.state.OldTargetKey)
	assert.Equal(t, uint64(2), receiver.state.InFlight)
	identity, ok := interceptors.ComponentIdentityFromContext(receiver.ctx)
	require.True(t, ok)
	assert.Equal(t, domain.ComponentRoleEdge, identity.Role)

	_, err = client.ReportDrainState(ctx, &edgev1.ReportDrainStateRequest{CanonicalDomain: "app.example.com", TransitionGeneration: 3, OldTargetKey: "container-id"})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Equal(t, 1, receiver.calls)
}

func TestServerReportDrainStateRequiresDrainScope(t *testing.T) {
	client := authenticatedClient(t, NewServerWithDrainStateReceiver(usecase.NewSnapshotHub(), &recordingDrainReceiver{}), domain.ComponentRoleEdge, domain.ComponentScopeRoutesWatch)
	key := adapterTestSnapshot(t, 1).Entries[0].TargetKey
	_, err := client.ReportDrainState(grpctest.AuthenticatedContext(context.Background(), grpctest.LocalComponentToken), &edgev1.ReportDrainStateRequest{CanonicalDomain: "app.example.com", TransitionGeneration: 1, OldTargetKey: string(key)})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestMethodAuthCoverageIncludesEveryEdgeRPC(t *testing.T) {
	scopes, roles := MethodScopes(), MethodRoles()
	service := edgev1.EdgeService_ServiceDesc
	require.Len(t, scopes, len(service.Methods)+len(service.Streams))
	require.Len(t, roles, len(service.Methods)+len(service.Streams))
	for _, method := range service.Methods {
		fullMethod := "/" + service.ServiceName + "/" + method.MethodName
		assert.Equal(t, domain.ComponentRoleEdge, roles[fullMethod])
		assert.NotEmpty(t, scopes[fullMethod])
	}
	for _, stream := range service.Streams {
		fullMethod := "/" + service.ServiceName + "/" + stream.StreamName
		assert.Equal(t, domain.ComponentRoleEdge, roles[fullMethod])
		assert.NotEmpty(t, scopes[fullMethod])
	}
}

func authenticatedClient(t *testing.T, server edgev1.EdgeServiceServer, role domain.ComponentRole, scopes ...domain.ComponentScope) edgev1.EdgeServiceClient {
	t.Helper()
	validator := grpctest.NewAuthFixture("edge-test", role, scopes...)
	harness := grpctest.NewHarness(t, func(registrar grpc.ServiceRegistrar) {
		edgev1.RegisterEdgeServiceServer(registrar, server)
	},
		grpc.UnaryInterceptor(interceptors.ComponentAuthUnaryInterceptor(validator, MethodScopes(), MethodRoles())),
		grpc.StreamInterceptor(interceptors.ComponentAuthStreamInterceptor(validator, MethodScopes(), MethodRoles())),
	)
	return edgev1.NewEdgeServiceClient(harness.Conn(t))
}

func adapterTestSnapshot(t *testing.T, generation domain.RouteTargetGeneration) domain.RouteTargetSnapshot {
	t.Helper()
	entry, err := domain.NewReadyRouteTargetEntry("app.example.com", "target.example", 8080, "http", domain.RouteTargetProtocolHTTP1, generation)
	require.NoError(t, err)
	return domain.RouteTargetSnapshot{Generation: generation, Entries: []domain.RouteTargetEntry{entry}}
}

func assertProtoFields(t *testing.T, descriptor protoreflect.MessageDescriptor, expected ...string) {
	t.Helper()
	fields := make([]string, 0, descriptor.Fields().Len())
	for index := range descriptor.Fields().Len() {
		fields = append(fields, string(descriptor.Fields().Get(index).Name()))
	}
	assert.Equal(t, expected, fields)
}

type recordingDrainReceiver struct {
	ctx   context.Context
	state usecase.DrainState
	calls int
}

func (r *recordingDrainReceiver) ReportDrainState(ctx context.Context, state usecase.DrainState) error {
	r.ctx = ctx
	r.state = state
	r.calls++
	return nil
}
