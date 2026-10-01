//go:build live

package docker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// TestLiveRuntime_SharedMembershipAndDetachment proves cross-app traffic is
// possible only for services explicitly enrolled in the same shared
// network, and that unrelated services stay detached.
func TestLiveRuntime_SharedMembershipAndDetachment(t *testing.T) {
	runtime, ctx := liveRuntime(t)
	netA := liveProbeNetwork(t, runtime, ctx)
	netB := liveProbeNetwork(t, runtime, ctx)

	sharedName := liveName("gordon-live-shared")
	require.NoError(t, runtime.CreateNetwork(ctx, sharedName, domain.NetworkConfig{
		Driver: "bridge", Labels: domain.AppSharedNetworkLabels("live-shared"),
	}))
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = runtime.RemoveNetwork(cleanup, sharedName)
	})

	listenerName := liveName("live-shared-listener")
	listener := liveCreate(t, runtime, ctx, &domain.ContainerConfig{
		Image: liveImage, Name: listenerName, NetworkMode: netA,
		Entrypoint: []string{"sh", "-c", "while true; do nc -l -p 9100 >/dev/null 2>&1; done"},
		AutoRemove: false,
	})
	require.NoError(t, runtime.ConnectContainerToNetwork(ctx, listener.ID, sharedName))

	// Enrolled on the shared network from another app network.
	enrolled := liveCreate(t, runtime, ctx, &domain.ContainerConfig{
		Image: liveImage, Name: liveName("live-shared-enrolled"), NetworkMode: netB,
		Entrypoint: []string{"sleep", "180"}, AutoRemove: false,
	})
	require.NoError(t, runtime.ConnectContainerToNetwork(ctx, enrolled.ID, sharedName))

	// An unrelated service on the same app network but NOT enrolled.
	outsider := liveCreate(t, runtime, ctx, &domain.ContainerConfig{
		Image: liveImage, Name: liveName("live-shared-outsider"), NetworkMode: netB,
		Entrypoint: []string{"sleep", "180"}, AutoRemove: false,
	})

	probe := []string{"sh", "-c", "nc -z -w 3 " + listenerName + " 9100"}

	require.Eventually(t, func() bool {
		result, err := runtime.ExecInContainer(ctx, enrolled.ID, probe)
		return err == nil && result.ExitCode == 0
	}, 60*time.Second, 2*time.Second, "an enrolled service must reach the peer through the shared network")

	result, err := runtime.ExecInContainer(ctx, outsider.ID, probe)
	require.NoError(t, err, "the exec itself must run; a transport error is not proof of isolation")
	assert.NotZero(t, result.ExitCode, "an unrelated service must not reach a peer it is not enrolled with")
}
