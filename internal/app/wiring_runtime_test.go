package app

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	runtimev1 "github.com/bnema/gordon/api/gordon/runtime/v1"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
)

func TestRuntimeRoleWiringStartsGRPCWithInjectedWorker(t *testing.T) {
	oldBuild := buildRuntimeRoleWorker
	oldListen := listenRuntimeGRPC
	oldValidator := runtimeRoleComponentTokenValidator
	t.Cleanup(func() {
		buildRuntimeRoleWorker = oldBuild
		listenRuntimeGRPC = oldListen
		runtimeRoleComponentTokenValidator = oldValidator
	})

	buildRuntimeRoleWorker = func(context.Context, *viper.Viper, Config, zerowrap.Logger) (in.RuntimeWorker, func(), error) {
		return fakeRuntimeRoleWorker{}, func() {}, nil
	}
	muxValidator := fakeRuntimeRoleTokenValidator{}
	runtimeRoleComponentTokenValidator = muxValidator
	listenRuntimeGRPC = func(_, _ string) (net.Listener, error) {
		return net.Listen("tcp", "127.0.0.1:0")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runRuntimeImpl(ctx, "")
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("runtime role did not stop after context cancellation")
	}
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

type fakeRuntimeRoleTokenValidator struct{}

func (fakeRuntimeRoleTokenValidator) ValidateToken(context.Context, string, domain.ComponentScope) (*domain.ComponentIdentity, error) {
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
