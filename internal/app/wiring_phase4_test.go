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
	grpcauth "github.com/bnema/gordon/internal/adapters/out/grpc/auth"
	edgesnapshotclient "github.com/bnema/gordon/internal/adapters/out/grpc/edgesnapshot"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/testutils/grpctest"
	"github.com/bnema/gordon/internal/usecase/edgesnapshot"
)

func TestControlTokenPrefersConfigThenNamedEnvironment(t *testing.T) {
	t.Setenv("EDGE_CONTROL_TOKEN", "from-environment")
	assert.Equal(t, "configured", controlToken(EdgeControlConfig{Token: " configured ", TokenEnv: "EDGE_CONTROL_TOKEN"}))
	assert.Equal(t, "from-environment", controlToken(EdgeControlConfig{TokenEnv: "EDGE_CONTROL_TOKEN"}))
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

func TestControlProducerStreamsInitialAndUpdateToEdgeClient(t *testing.T) {
	configPath := writePhase4RoleConfig(t)
	validator := grpctest.NewAuthFixture("edge", domain.ComponentRoleEdge, domain.ComponentScopeRoutesWatch)
	snapshots := make(chan domain.RuntimeActualStateSnapshot, 2)
	snapshots <- phase4ManagedRuntimeSnapshot(1, "private-container-one")
	listenerReady := make(chan net.Listener, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runControlWithDependencies(ctx, configPath, controlRoleDependencies{
			listen: func(network, address string) (net.Listener, error) {
				listener, err := net.Listen(network, address)
				if err == nil {
					listenerReady <- listener
				}
				return listener, err
			},
			newComponentTokenValidator: func(Config, zerowrap.Logger) (interceptors.ComponentTokenValidator, error) {
				return validator, nil
			},
			newSnapshotHub: edgesnapshot.NewSnapshotHub,
			newRuntimeStateSubscriber: func(context.Context, RuntimeControlConfig) (out.RuntimeStateSubscriber, error) {
				return phase4StateSubscriber{snapshots}, nil
			},
			newSnapshotProducer: edgesnapshot.NewProducer,
		})
	}()
	listener := <-listenerReady
	credentials, err := grpcauth.NewInsecureBearerTokenCredentials(grpctest.LocalComponentToken)
	require.NoError(t, err)
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithPerRPCCredentials(credentials))
	require.NoError(t, err)
	defer connection.Close()
	edgeClient := edgesnapshotclient.NewClient(connection, edgesnapshotclient.WithReconnectBackoff(time.Millisecond, 10*time.Millisecond))
	require.NoError(t, edgeClient.Start(ctx))
	defer edgeClient.Stop()
	require.Eventually(t, func() bool {
		snapshot, getErr := edgeClient.CurrentSnapshot(context.Background())
		return getErr == nil && snapshot.Generation == 1 && len(snapshot.Entries) == 1 && snapshot.Entries[0].TargetHost == "gordon-target-app-example-com"
	}, time.Second, time.Millisecond)

	snapshots <- phase4ManagedRuntimeSnapshot(2, "private-container-two")
	require.Eventually(t, func() bool {
		snapshot, getErr := edgeClient.CurrentSnapshot(context.Background())
		return getErr == nil && snapshot.Generation == 2 && snapshot.Entries[0].TargetHost == "gordon-target-app-example-com" && snapshot.Entries[0].UpstreamHost == ""
	}, time.Second, time.Millisecond)
	cancel()
	require.NoError(t, <-done)
}

func phase4ManagedRuntimeSnapshot(generation uint64, privateContainer string) domain.RuntimeActualStateSnapshot {
	const routeDomain = "app.example.com"
	const alias = "gordon-target-app-example-com"
	return domain.RuntimeActualStateSnapshot{
		Generation: generation, StateVersion: "runtime-state", SourceComponentID: "runtime",
		Routes:          []domain.RuntimeRouteState{{Domain: routeDomain, Generation: generation, RouteVersion: "private-route-version", ContainerAlias: alias, EdgeTargetAlias: alias, TargetPort: 8080, Scheme: "http", Protocol: domain.RouteTargetProtocolHTTP1, Status: domain.RouteTargetStatusReady, BackingContainerName: privateContainer}},
		EdgeAttachments: []domain.RuntimeEdgeNetworkAttachmentState{{RouteDomain: routeDomain, NetworkName: "private-network", EdgeAlias: "gordon-edge", RuntimeAlias: alias, TargetAlias: alias, TargetPort: 8080, Attached: true, Generation: generation, SourceComponent: "runtime"}},
	}
}

func TestEdgeAndControlStartupCancelWithNarrowDependencies(t *testing.T) {
	configPath := writePhase4RoleConfig(t)
	validator := grpctest.NewAuthFixture("edge", domain.ComponentRoleEdge, domain.ComponentScopeRoutesWatch)

	controlCtx, cancelControl := context.WithCancel(context.Background())
	controlSnapshots := make(chan domain.RuntimeActualStateSnapshot, 1)
	controlSnapshots <- domain.RuntimeActualStateSnapshot{Generation: 1, StateVersion: "runtime-state:1", SourceComponentID: "runtime"}
	controlDone := make(chan error, 1)
	go func() {
		controlDone <- runControlWithDependencies(controlCtx, configPath, controlRoleDependencies{
			listen: net.Listen,
			newComponentTokenValidator: func(Config, zerowrap.Logger) (interceptors.ComponentTokenValidator, error) {
				return validator, nil
			},
			newSnapshotHub: edgesnapshot.NewSnapshotHub,
			newRuntimeStateSubscriber: func(context.Context, RuntimeControlConfig) (out.RuntimeStateSubscriber, error) {
				return phase4StateSubscriber{controlSnapshots}, nil
			},
			newSnapshotProducer: edgesnapshot.NewProducer,
		})
	}()
	time.Sleep(20 * time.Millisecond)
	cancelControl()
	require.NoError(t, <-controlDone)

	edgeConfigPath := writePhase4EdgeConfig(t)
	edgeCtx, cancelEdge := context.WithCancel(context.Background())
	edgeDone := make(chan error, 1)
	go func() {
		edgeDone <- runEdgeWithDependencies(edgeCtx, edgeConfigPath, edgeRoleDependencies{
			listen: net.Listen,
			dialSnapshot: func(context.Context, EdgeControlConfig) (*edgesnapshotclient.Client, *grpc.ClientConn, error) {
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

type phase4StateSubscriber struct {
	snapshots <-chan domain.RuntimeActualStateSnapshot
}

func (s phase4StateSubscriber) SubscribeRuntimeState(context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
	return s.snapshots, nil
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

func writePhase4EdgeConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "edge.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[control]
endpoint = "127.0.0.1:1"
token = "test-token"
insecure_tls = true
[edge]
listen_address = "127.0.0.1:0"
trusted_proxy_cidrs = ["127.0.0.0/8"]
[edge.tls]
mode = "external"
`), 0600))
	return path
}
