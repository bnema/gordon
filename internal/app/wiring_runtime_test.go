package app

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

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
