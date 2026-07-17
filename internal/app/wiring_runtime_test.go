package app

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"

	runtimev1 "github.com/bnema/gordon/api/gordon/runtime/v1"
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/componentauth"
	"github.com/bnema/gordon/internal/usecase/container"
)

func TestStandaloneServiceManagerUsesCommandClientForControlRole(t *testing.T) {
	client := fakeRuntimeCommandClientForApp{}

	manager := standaloneServiceManagerForServices(&services{role: RoleControl, runtimeCommandClient: client})

	require.IsType(t, client, manager)
}

func TestStandaloneServiceManagerUsesLocalAdapterForMonolithRole(t *testing.T) {
	client := fakeRuntimeCommandClientForApp{}

	manager := standaloneServiceManagerForServices(&services{role: RoleMonolith, runtimeCommandClient: client})

	require.NotNil(t, manager)
	_, isClient := manager.(fakeRuntimeCommandClientForApp)
	require.False(t, isClient)
}

func TestStandaloneServiceManagerDoesNotBuildLocalAdapterForNonRuntimeRoles(t *testing.T) {
	for _, role := range []Role{RoleControl, RoleEdge, RoleRegistry} {
		t.Run(string(role), func(t *testing.T) {
			require.Nil(t, standaloneServiceManagerForServices(&services{role: role}))
		})
	}
}

func TestRuntimeRoleStandaloneServiceManagerIsPolicyWrapped(t *testing.T) {
	manager := newRuntimeRoleStandaloneServiceManager(nil, Config{}, viper.New())

	require.IsType(t, &container.RuntimeStandaloneServicePolicyManager{}, manager)
}

func TestRuntimeRoleBundleExposesStandaloneServiceManager(t *testing.T) {
	manager := newRuntimeRoleStandaloneServiceManager(nil, Config{}, viper.New())
	bundle := runtimeRoleWorkerBundle{standaloneServiceManager: manager}

	require.Same(t, manager, runtimeRoleStandaloneServiceManager(bundle))
}

func TestRuntimeRoleServiceWiresControlRelayedDrainReceiver(t *testing.T) {
	state := domain.RuntimeRouteState{Domain: "app.example.com", Generation: 1, ContainerAlias: "gordon-target-app-example-com", EdgeTargetAlias: "gordon-target-app-example-com", TargetPort: 8080, Scheme: "http", Protocol: domain.RouteTargetProtocolHTTP1, Status: domain.RouteTargetStatusReady, BackingContainerName: "private-old"}
	key, err := domain.ManagedRouteTargetKeyFromRuntimeState(state)
	require.NoError(t, err)
	registry := container.NewRuntimeDrainRegistry(func(string) (domain.RuntimeRouteState, bool) { return state, true })
	registry.PrepareDrain("old-id")
	server := newRuntimeRoleService(runtimeRoleWorkerBundle{RuntimeWorker: fakeRuntimeRoleWorker{}, routeDrainAckReceiver: registry})

	_, err = server.ReportEdgeDrain(context.Background(), &runtimev1.ReportEdgeDrainRequest{CanonicalDomain: "app.example.com", TransitionGeneration: 4, OldTargetKey: string(key), AcknowledgedAt: timestamppb.Now()})
	require.NoError(t, err)
	require.True(t, registry.WaitForNoInFlight(context.Background(), "old-id", time.Second))
}

func TestRuntimeRoleServiceExposesStandaloneServiceManager(t *testing.T) {
	server := newRuntimeRoleService(runtimeRoleWorkerBundle{
		RuntimeWorker:            fakeRuntimeRoleWorker{},
		standaloneServiceManager: fakeRuntimeCommandClientForApp{},
	})

	response, err := server.ListStandaloneServiceState(context.Background(), &runtimev1.ListStandaloneServiceStateRequest{})

	require.NoError(t, err)
	require.Empty(t, response.Services)
}

func TestRuntimeRoleWiringStartsGRPCWithInjectedDependencies(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	validator := &fakeRuntimeRoleTokenValidator{}
	deps := runtimeRoleDependencies{
		buildWorker: func(context.Context, *viper.Viper, Config, zerowrap.Logger) (in.RuntimeWorker, func(), error) {
			return fakeRuntimeRoleWorker{}, func() {}, nil
		},
		listen: func(_, _ string) (net.Listener, error) {
			return listener, nil
		},
		newComponentTokenValidator: func(Config, zerowrap.Logger) (interceptors.ComponentTokenValidator, error) {
			return validator, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runRuntimeWithDependencies(ctx, "", deps)
	}()

	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	client := runtimev1.NewRuntimeServiceClient(conn)
	rpcCtx, rpcCancel := context.WithTimeout(metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer test-token"), time.Second)
	defer rpcCancel()
	_, err = client.GetHealth(rpcCtx, &runtimev1.GetHealthRequest{})
	require.NoError(t, err)
	require.Equal(t, 1, validator.calls)

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("runtime role did not stop after context cancellation")
	}
}

func TestNewRuntimeRoleComponentTokenValidatorUsesConfiguredBackend(t *testing.T) {
	cfg := Config{}
	cfg.Auth.SecretsBackend = "unsafe"
	cfg.Server.DataDir = t.TempDir()

	validator, err := newRuntimeRoleComponentTokenValidator(cfg, zerowrap.Default())

	require.NoError(t, err)
	service, ok := validator.(*componentauth.Service)
	require.True(t, ok)
	created, err := service.CreateToken(context.Background(), componentauth.CreateRequest{
		Name:   "control-1",
		Role:   domain.ComponentRoleControl,
		Scopes: []domain.ComponentScope{domain.ComponentScopeRuntimeStatus},
	})
	require.NoError(t, err)
	identity, err := validator.ValidateToken(context.Background(), created.Token, domain.ComponentScopeRuntimeStatus)
	require.NoError(t, err)
	require.Equal(t, "control-1", identity.Name)
}

func TestRuntimeRoleStartupFailsClosedWhenComponentTokenStoreCannotBeCreated(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gordon.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[auth]\nsecrets_backend = \"sops\"\n"), 0600))
	workerBuilt := false
	deps := runtimeRoleDependencies{
		buildWorker: func(context.Context, *viper.Viper, Config, zerowrap.Logger) (in.RuntimeWorker, func(), error) {
			workerBuilt = true
			return fakeRuntimeRoleWorker{}, nil, nil
		},
		listen:                     net.Listen,
		newComponentTokenValidator: newRuntimeRoleComponentTokenValidator,
	}

	err := runRuntimeWithDependencies(context.Background(), configPath, deps)

	require.ErrorContains(t, err, "failed to initialize component token validator")
	require.False(t, workerBuilt)
}

func TestNewRuntimeRoleComponentTokenValidatorRejectsUnsupportedBackend(t *testing.T) {
	cfg := Config{}
	cfg.Auth.SecretsBackend = "sops"
	cfg.Server.DataDir = t.TempDir()

	_, err := newRuntimeRoleComponentTokenValidator(cfg, zerowrap.Default())

	require.ErrorContains(t, err, "create component token store")
	require.ErrorContains(t, err, "not yet implemented")
}

func TestRuntimeRoleServiceWiresActualStateSubscriberFromSnapshotWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stream := &fakeActualStateStream{ctx: ctx, cancel: cancel}

	err := newRuntimeRoleService(fakeSnapshotRuntimeRoleWorker{}).WatchActualState(&runtimev1.WatchActualStateRequest{}, stream)
	require.Error(t, err)
	require.Len(t, stream.snapshots, 1)
	require.Equal(t, uint64(1), stream.snapshots[0].Generation)
	require.Equal(t, "gordon-runtime", stream.snapshots[0].SourceComponentId)
}

func TestPollingRuntimeStateSubscriberSharesGenerationsAcrossSubscriptions(t *testing.T) {
	subscriber := pollingRuntimeStateSubscriber{
		snapshotter:       fakeSnapshotRuntimeRoleWorker{},
		interval:          time.Hour,
		sourceComponentID: "gordon-runtime",
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstSnapshots, err := subscriber.SubscribeRuntimeState(firstCtx)
	require.NoError(t, err)
	first := <-firstSnapshots
	cancelFirst()

	secondCtx, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	secondSnapshots, err := subscriber.SubscribeRuntimeState(secondCtx)
	require.NoError(t, err)
	second := <-secondSnapshots

	require.Equal(t, uint64(1), first.Generation)
	require.Equal(t, uint64(2), second.Generation)
}

func TestRuntimeRoleServiceLeavesActualStateUnconfiguredWithoutSnapshotWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &fakeActualStateStream{ctx: ctx, cancel: cancel}

	err := newRuntimeRoleService(fakeRuntimeRoleWorker{}).WatchActualState(&runtimev1.WatchActualStateRequest{}, stream)
	require.ErrorContains(t, err, "runtime state subscriber not configured")
}

type fakeRuntimeRoleTokenValidator struct {
	calls int
}

func (f *fakeRuntimeRoleTokenValidator) ValidateToken(context.Context, string, domain.ComponentScope) (*domain.ComponentIdentity, error) {
	f.calls++
	return &domain.ComponentIdentity{Name: "control", Role: domain.ComponentRoleControl}, nil
}

type fakeActualStateStream struct {
	grpc.ServerStream
	ctx       context.Context
	cancel    context.CancelFunc
	snapshots []*runtimev1.ActualStateSnapshot
}

func (f *fakeActualStateStream) Context() context.Context { return f.ctx }

func (f *fakeActualStateStream) Send(snapshot *runtimev1.ActualStateSnapshot) error {
	f.snapshots = append(f.snapshots, snapshot)
	f.cancel()
	return nil
}

type fakeSnapshotRuntimeRoleWorker struct {
	fakeRuntimeRoleWorker
}

func (fakeSnapshotRuntimeRoleWorker) Snapshot(_ context.Context, generation uint64, stateVersion, sourceComponentID string) (domain.RuntimeActualStateSnapshot, error) {
	return domain.RuntimeActualStateSnapshot{Generation: generation, StateVersion: stateVersion, SourceComponentID: sourceComponentID}, nil
}

type fakeRuntimeRoleWorker struct{}

func (fakeRuntimeRoleWorker) DeployRoute(context.Context, domain.DeployRouteCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}

func (fakeRuntimeRoleWorker) RestartRoute(context.Context, domain.RestartRouteCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}

func (fakeRuntimeRoleWorker) RemoveRoute(context.Context, domain.RemoveRouteCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}

func (fakeRuntimeRoleWorker) Reconcile(context.Context, domain.ReconcileRuntimeCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}

func (fakeRuntimeRoleWorker) SelfUpdate(context.Context, domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}
