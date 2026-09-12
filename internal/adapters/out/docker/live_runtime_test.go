//go:build live

// Live runtime verification against a real Docker daemon. These tests are
// opt-in: build with `-tags live` and run with the daemon reachable. They
// create and remove their own networks, volumes, and containers.
//
//	go test -tags live ./internal/adapters/out/docker/ -run TestLive -count=1 -v

package docker

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

const liveImage = "golang:1.27-alpine"

func liveRuntime(t *testing.T) (*Runtime, context.Context) {
	t.Helper()
	cli, err := client.New(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	require.NoError(t, err)
	runtime := NewRuntimeWithClient(cli)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	require.NoError(t, runtime.Ping(ctx), "a live Docker daemon is required")
	return runtime, ctx
}

func liveName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func liveCreate(t *testing.T, runtime *Runtime, ctx context.Context, config *domain.ContainerConfig) *domain.Container {
	t.Helper()
	created, err := runtime.CreateContainer(ctx, config)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = runtime.StopContainer(cleanup, created.ID, domain.AppDefaultStopGrace)
		_ = runtime.RemoveContainer(cleanup, created.ID, true)
	})
	require.NoError(t, runtime.StartContainer(ctx, created.ID))
	return created
}

// TestLiveRuntime_PrivateNetworksIsolateApps proves containers on two
// app-owned networks cannot reach each other, while containers sharing one
// network can.
func TestLiveRuntime_PrivateNetworksIsolateApps(t *testing.T) {
	runtime, ctx := liveRuntime(t)
	netA := liveName("gordon-live-a")
	netB := liveName("gordon-live-b")
	for _, name := range []string{netA, netB} {
		require.NoError(t, runtime.CreateNetwork(ctx, name, domain.NetworkConfig{
			Driver: "bridge", Labels: domain.AppPrivateNetworkLabels("live", name),
		}))
		networkName := name
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = runtime.RemoveNetwork(cleanup, networkName)
		})
	}

	listenerName := liveName("live-a")
	peerName := liveName("live-b")
	foreignName := liveName("live-c")
	listenCmd := []string{"sh", "-c", "while true; do nc -l -p 1234 >/dev/null 2>&1; done"}
	probeCmd := []string{"sh", "-c", "nc -z -w 2 " + listenerName + " 1234"}

	liveCreate(t, runtime, ctx, &domain.ContainerConfig{
		Image: liveImage, Name: listenerName, NetworkMode: netA,
		Entrypoint: listenCmd, AutoRemove: false,
	})
	liveCreate(t, runtime, ctx, &domain.ContainerConfig{
		Image: liveImage, Name: peerName, NetworkMode: netA,
		Entrypoint: []string{"sleep", "120"}, AutoRemove: false,
	})
	liveCreate(t, runtime, ctx, &domain.ContainerConfig{
		Image: liveImage, Name: foreignName, NetworkMode: netB,
		Entrypoint: []string{"sleep", "120"}, AutoRemove: false,
	})

	// Give the listener a moment, then prove same-network reachability.
	require.Eventually(t, func() bool {
		result, err := runtime.ExecInContainer(ctx, peerName, probeCmd)
		return err == nil && result.ExitCode == 0
	}, 30*time.Second, time.Second, "containers on the same app network must reach each other")

	// A container on another app network must not reach the listener: the
	// name does not resolve and the network is not routable.
	result, err := runtime.ExecInContainer(ctx, foreignName, probeCmd)
	if err == nil {
		assert.NotZero(t, result.ExitCode, "cross-app network traffic must be impossible")
	}
}

// TestLiveRuntime_LoopbackPublishAndObservedBind proves a backend port is
// published only on loopback and that the adapter reports exactly that
// binding.
func TestLiveRuntime_LoopbackPublishAndObservedBind(t *testing.T) {
	runtime, ctx := liveRuntime(t)
	network := liveName("gordon-live-l4")
	require.NoError(t, runtime.CreateNetwork(ctx, network, domain.NetworkConfig{
		Driver: "bridge", Labels: domain.AppPrivateNetworkLabels("live", network),
	}))
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = runtime.RemoveNetwork(cleanup, network)
	})

	created := liveCreate(t, runtime, ctx, &domain.ContainerConfig{
		Image: liveImage, Name: liveName("live-l4"), NetworkMode: network,
		Entrypoint: []string{"sh", "-c", "while true; do nc -l -p 8080 >/dev/null 2>&1; done"},
		PortPublishes: []domain.ContainerPortPublish{{
			HostIP: "127.0.0.1", HostPort: 0, ContainerPort: 8080, Protocol: domain.NetworkProtocolTCP,
		}},
		AutoRemove: false,
	})

	binds, err := runtime.GetContainerBackendBinds(ctx, created.ID, []domain.ContainerBackendPort{
		{ContainerPort: 8080, Protocol: domain.NetworkProtocolTCP},
	})
	require.NoError(t, err)
	require.Len(t, binds, 1)
	hostPort := binds[0].HostPort
	require.Positive(t, hostPort)

	// The loopback bind accepts a connection from the host. A wildcard or
	// container-address bind would have been rejected by
	// GetContainerBackendBinds above, which requires exactly one 127.0.0.1
	// mapping.
	require.Eventually(t, func() bool {
		conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", hostPort), time.Second)
		if dialErr != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 30*time.Second, time.Second, "the loopback bind must accept connections")
}

// TestLiveRuntime_ReadOnlyVolumeIsMountedReadOnly proves a volume declared
// read-only rejects writes while a writable volume accepts them.
func TestLiveRuntime_ReadOnlyVolumeIsMountedReadOnly(t *testing.T) {
	runtime, ctx := liveRuntime(t)
	network := liveName("gordon-live-ro")
	require.NoError(t, runtime.CreateNetwork(ctx, network, domain.NetworkConfig{
		Driver: "bridge", Labels: domain.AppPrivateNetworkLabels("live", network),
	}))
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = runtime.RemoveNetwork(cleanup, network)
	})

	roVolume := liveName("live-ro-vol")
	rwVolume := liveName("live-rw-vol")
	for _, name := range []string{roVolume, rwVolume} {
		require.NoError(t, runtime.CreateVolume(ctx, name, map[string]string{domain.LabelManaged: "true"}))
		volumeName := name
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = runtime.RemoveVolume(cleanup, volumeName, true)
		})
	}

	created := liveCreate(t, runtime, ctx, &domain.ContainerConfig{
		Image: liveImage, Name: liveName("live-ro"), NetworkMode: network,
		Entrypoint: []string{"sleep", "120"},
		Volumes:    map[string]string{"/rw": rwVolume},
		ReadOnlyVolumes: map[string]string{
			"/ro": roVolume,
		},
		AutoRemove: false,
	})

	writable, err := runtime.ExecInContainer(ctx, created.ID, []string{"sh", "-c", "echo data > /rw/file && cat /rw/file"})
	require.NoError(t, err)
	assert.Zero(t, writable.ExitCode, "a writable volume must accept writes")

	readOnly, err := runtime.ExecInContainer(ctx, created.ID, []string{"sh", "-c", "echo data > /ro/file"})
	require.NoError(t, err)
	assert.NotZero(t, readOnly.ExitCode, "a read-only volume must reject writes")
	assert.True(t, strings.Contains(string(readOnly.Stderr), "Read-only") ||
		strings.Contains(string(readOnly.Stderr), "read-only") ||
		strings.Contains(string(readOnly.Stderr), "Permission denied"),
		"unexpected read-only failure output: %s", string(readOnly.Stderr))
}
