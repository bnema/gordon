package app

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	"github.com/bnema/gordon/internal/usecase/edgesnapshot"
)

// ControlCLIHarness exposes the production split-control listener to external
// CLI tests. It is compiled only into the app test binary, so it is not a
// production API.
type ControlCLIHarness struct {
	URL   string
	Token string

	cancel    context.CancelFunc
	done      <-chan error
	listeners []net.Listener
	runtime   *controlRoleRuntime
}

// StartControlCLIHarness starts the same production HTTP/gRPC control wiring
// as TestControlRoleBringup, backed only by the runtime gRPC test double.
func StartControlCLIHarness(t *testing.T) *ControlCLIHarness {
	t.Helper()
	t.Setenv(TokenSecretEnvVar, "control-cli-test-token-secret-at-least-32-bytes")
	runtime := startControlRoleRuntime(t)
	configPath := writeControlRoleConfig(t, runtime.listener.Addr().String())
	state := newControlRoleStateSubscriber()
	runtime.state = state

	listeners := make(chan net.Listener, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runControlWithDependencies(ctx, configPath, controlRoleDependencies{
			listen: func(network, address string) (net.Listener, error) {
				listener, err := net.Listen(network, address)
				if err == nil {
					listeners <- listener
				}
				return listener, err
			},
			newComponentTokenValidator: func(Config, zerowrap.Logger) (interceptors.ComponentTokenValidator, error) {
				return controlRoleComponentValidator{}, nil
			},
			newSnapshotHub:             edgesnapshot.NewSnapshotHub,
			newEventHub:                productionControlRoleDependencies().newEventHub,
			newTrafficGraphHub:         edgesnapshot.NewTrafficGraphHub,
			newRuntimeStateSubscriber:  createRuntimeStateSubscriber,
			newRuntimeDrainAckReceiver: createRuntimeRouteDrainAckReceiver,
			setupConfigHotReload:       func(context.Context, configWatcher, loadedConfigApplier) error { return nil },
			newSnapshotProducer:        edgesnapshot.NewProducer,
			newTrafficGraphProducer:    edgesnapshot.NewTrafficGraphProducer,
		})
	}()

	state.Publish(phase4ManagedRuntimeSnapshot(1, "runtime-container"))
	first := <-listeners
	second := <-listeners
	// runControlWithDependencies opens gRPC before the HTTP admin listener.
	grpcListener, httpListener := first, second
	_ = grpcListener
	return &ControlCLIHarness{
		URL:       "http://" + httpListener.Addr().String(),
		Token:     controlRoleRemoteToken(t, configPath),
		cancel:    cancel,
		done:      done,
		listeners: []net.Listener{grpcListener, httpListener},
		runtime:   runtime,
	}
}

// Close stops the production control listener and its runtime test double.
func (h *ControlCLIHarness) Close(t *testing.T) {
	t.Helper()
	h.cancel()
	require.NoError(t, <-h.done)
	h.runtime.stop()
	for _, listener := range h.listeners {
		probe, err := net.Listen("tcp", listener.Addr().String())
		require.NoError(t, err, "listener was not released: %s", listener.Addr())
		require.NoError(t, probe.Close())
	}
}

// RuntimeDeployCalls reports commands accepted by the runtime gRPC port.
func (h *ControlCLIHarness) RuntimeDeployCalls() int { return h.runtime.worker.calls() }

// WaitForRuntimeDeploy verifies a CLI mutation crossed the production control
// listener and reached the runtime command port.
func (h *ControlCLIHarness) WaitForRuntimeDeploy(t *testing.T, minimum int) {
	t.Helper()
	require.Eventually(t, func() bool { return h.RuntimeDeployCalls() >= minimum }, time.Second, time.Millisecond)
}
