package edgesnapshot

import (
	"context"
	"testing"

	edgev1 "github.com/bnema/gordon/api/gordon/edge/v1"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/testutils/grpctest"
	usecase "github.com/bnema/gordon/internal/usecase/edgesnapshot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTrafficGraphStreamBufconnAuthenticationAndUpdates(t *testing.T) {
	routes := usecase.NewSnapshotHub()
	graphs := usecase.NewTrafficGraphHub()
	require.NoError(t, graphs.Publish(adapterTrafficGraph(1, "one.internal")))
	client := authenticatedClient(t, NewServerWithTrafficGraphSource(routes, graphs), domain.ComponentRoleEdge, domain.ComponentScopeTrafficWatch)
	ctx, cancel := context.WithCancel(grpctest.AuthenticatedContext(context.Background(), grpctest.LocalComponentToken))
	defer cancel()
	stream, err := client.WatchTrafficGraphs(ctx, &edgev1.WatchTrafficGraphsRequest{})
	require.NoError(t, err)
	first, err := stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, uint64(1), first.Generation)
	require.NoError(t, graphs.Publish(adapterTrafficGraph(2, "two.internal")))
	second, err := stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, uint64(2), second.Generation)

	wrongScope := authenticatedClient(t, NewServerWithTrafficGraphSource(routes, graphs), domain.ComponentRoleEdge, domain.ComponentScopeRoutesWatch)
	blocked, err := wrongScope.WatchTrafficGraphs(grpctest.AuthenticatedContext(context.Background(), grpctest.LocalComponentToken), &edgev1.WatchTrafficGraphsRequest{})
	require.NoError(t, err)
	_, err = blocked.Recv()
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

func adapterTrafficGraph(generation domain.TrafficGraphGeneration, host string) domain.TrafficGraphSnapshot {
	return domain.TrafficGraphSnapshot{Generation: generation, Graph: domain.TrafficGraph{
		EntryPoints: []domain.EntryPoint{{Name: "tcp", Address: ":9000", Protocol: domain.EntryPointProtocolTCP}},
		Routers:     []domain.TrafficRouter{{Name: "app", EntryPoint: "tcp", Protocol: domain.RouterProtocolTCP, Service: "network_service:app:http"}},
		Services:    []domain.TrafficService{{Name: "network_service:app:http", Backends: []domain.TrafficBackend{{Host: host, Port: 8080, Protocol: domain.NetworkProtocolTCP}}}},
	}}
}
