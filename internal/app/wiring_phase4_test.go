package app

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	edgev1 "github.com/bnema/gordon/api/gordon/edge/v1"
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	edgesnapshotclient "github.com/bnema/gordon/internal/adapters/out/grpc/edgesnapshot"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/testutils/grpctest"
	"github.com/bnema/gordon/internal/usecase/edgesnapshot"
)

func TestControlTokenPrefersConfigThenNamedEnvironment(t *testing.T) {
	t.Setenv("EDGE_CONTROL_TOKEN", "from-environment")
	assert.Equal(t, "configured", controlToken(ControlConfig{Token: " configured ", TokenEnv: "EDGE_CONTROL_TOKEN"}))
	assert.Equal(t, "from-environment", controlToken(ControlConfig{TokenEnv: "EDGE_CONTROL_TOKEN"}))
}

func TestControlTransportDefaultsToTLSAndPlaintextIsExplicit(t *testing.T) {
	_, err := controlServerTransportCredentials(Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "control.insecure_tls=true")

	credentials, err := controlServerTransportCredentials(Config{Control: ControlConfig{InsecureTLS: true}})
	require.NoError(t, err)
	assert.False(t, credentials.Info().SecurityProtocol == "tls")
}

func TestControlSnapshotServerRequiresEdgeAuthentication(t *testing.T) {
	hub := edgesnapshot.NewSnapshotHub()
	entry, err := domain.NewReadyRouteTargetEntry("app.example.com", "app.edge.internal", 8080, "http", domain.RouteTargetProtocolHTTP1, 1)
	require.NoError(t, err)
	require.NoError(t, hub.Publish(domain.RouteTargetSnapshot{Generation: 1, Entries: []domain.RouteTargetEntry{entry}}))

	validator := grpctest.NewAuthFixture("edge", domain.ComponentRoleEdge, domain.ComponentScopeRoutesWatch)
	server, err := newControlSnapshotServer(Config{Control: ControlConfig{InsecureTLS: true}}, validator, hub)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer connection.Close()
	api := edgev1.NewEdgeServiceClient(connection)
	unauthenticated, err := api.WatchRouteSnapshots(context.Background(), &edgev1.WatchRouteSnapshotsRequest{})
	require.NoError(t, err)
	_, err = unauthenticated.Recv()
	require.Error(t, err)

	authenticated := grpctest.AuthenticatedContext(context.Background(), grpctest.LocalComponentToken)
	stream, err := api.WatchRouteSnapshots(authenticated, &edgev1.WatchRouteSnapshotsRequest{})
	require.NoError(t, err)
	message, err := stream.Recv()
	require.NoError(t, err)
	assert.EqualValues(t, 1, message.Generation)
}

func TestEdgeAndControlStartupCancelWithNarrowDependencies(t *testing.T) {
	configPath := writePhase4RoleConfig(t)
	validator := grpctest.NewAuthFixture("edge", domain.ComponentRoleEdge, domain.ComponentScopeRoutesWatch)

	controlCtx, cancelControl := context.WithCancel(context.Background())
	controlDone := make(chan error, 1)
	go func() {
		controlDone <- runControlWithDependencies(controlCtx, configPath, controlRoleDependencies{
			listen: net.Listen,
			newComponentTokenValidator: func(Config, zerowrap.Logger) (interceptors.ComponentTokenValidator, error) {
				return validator, nil
			},
			newSnapshotHub: edgesnapshot.NewSnapshotHub,
		})
	}()
	time.Sleep(20 * time.Millisecond)
	cancelControl()
	require.NoError(t, <-controlDone)

	edgeCtx, cancelEdge := context.WithCancel(context.Background())
	edgeDone := make(chan error, 1)
	go func() {
		edgeDone <- runEdgeWithDependencies(edgeCtx, configPath, edgeRoleDependencies{
			listen: net.Listen,
			dialSnapshot: func(context.Context, ControlConfig) (*edgesnapshotclient.Client, *grpc.ClientConn, error) {
				conn, err := grpc.NewClient("passthrough:///127.0.0.1:1", grpc.WithTransportCredentials(insecure.NewCredentials()))
				if err != nil {
					return nil, nil, err
				}
				return edgesnapshotclient.NewClient(conn), conn, nil
			},
			newHTTPServer: productionEdgeRoleDependencies().newHTTPServer,
		})
	}()
	time.Sleep(20 * time.Millisecond)
	cancelEdge()
	require.NoError(t, <-edgeDone)
}

func writePhase4RoleConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gordon.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[control]
listen_address = "127.0.0.1:0"
endpoint = "127.0.0.1:1"
token = "test-token"
insecure_tls = true
[server]
port = 0
`), 0600))
	return path
}
