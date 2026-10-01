//go:build live

// Live bounded-network-readiness verification against a real Docker
// daemon. These tests are opt-in: build with `-tags live` and run with the
// daemon reachable. They create and remove their own networks and
// containers, and assert no helper is ever left behind.
//
//	go test -tags live ./internal/adapters/out/docker/ -run TestLiveNetworkProbe -count=1 -v

package docker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// liveProbeNetwork creates an owned private network for one probe scenario.
func liveProbeNetwork(t *testing.T, runtime *Runtime, ctx context.Context) string {
	t.Helper()
	name := liveName("gordon-live-probe")
	require.NoError(t, runtime.CreateNetwork(ctx, name, domain.NetworkConfig{
		Driver: "bridge", Labels: domain.AppPrivateNetworkLabels("live", name),
	}))
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = runtime.RemoveNetwork(cleanup, name)
	})
	return name
}

// liveProbeHelpers counts leftover bounded-readiness helpers.
func liveProbeHelpers(t *testing.T, runtime *Runtime, ctx context.Context) int {
	t.Helper()
	containers, err := runtime.ListContainers(ctx, true)
	require.NoError(t, err)
	count := 0
	for _, c := range containers {
		if c != nil && c.Labels[domain.LabelPurpose] == domain.PurposeNetworkProbe {
			count++
		}
	}
	return count
}

// liveProbeTarget starts one listener container on the network.
func liveProbeTarget(t *testing.T, runtime *Runtime, ctx context.Context, network, listenScript string) *domain.Container {
	t.Helper()
	return liveCreate(t, runtime, ctx, &domain.ContainerConfig{
		Image:       liveImage,
		Name:        liveName("live-probe-target"),
		NetworkMode: network,
		Entrypoint:  []string{"sh", "-c", listenScript},
		AutoRemove:  false,
	})
}

// TestLiveNetworkProbe_PeerFacingReadyAndCleanedUp proves a published-free
// internal listener answers a bounded probe, the helper is removed, and the
// target exposes no host port.
func TestLiveNetworkProbe_PeerFacingReadyAndCleanedUp(t *testing.T) {
	runtime, ctx := liveRuntime(t)
	network := liveProbeNetwork(t, runtime, ctx)
	target := liveProbeTarget(t, runtime, ctx, network,
		`while true; do printf "HTTP/1.0 200 OK\r\nContent-Length: 2\r\n\r\nok" | nc -l -p 8080; done`)

	before := liveProbeHelpers(t, runtime, ctx)
	inspect, err := runtime.InspectContainer(ctx, target.ID)
	require.NoError(t, err)
	require.False(t, inspect.StartedAt.IsZero(), "the target must report an execution start")
	assert.Empty(t, inspect.Ports, "an internal listener must not publish a host port")

	result, err := runtime.ProbeContainerNetwork(ctx, domain.ContainerNetworkProbeRequest{
		TargetContainerID: target.ID,
		ExpectedStartedAt: inspect.StartedAt,
		Network:           network,
		Protocol:          domain.ProbeProtocolHTTP,
		Port:              8080,
		Path:              "/healthz",
		Timeout:           10 * time.Second,
	})
	require.NoError(t, err)
	assert.True(t, result.Ready)
	assert.Equal(t, 200, result.Status)

	assert.Equal(t, before, liveProbeHelpers(t, runtime, ctx), "the helper must always be removed")
}

// TestLiveNetworkProbe_LoopbackOnlyListenerFails proves a target listening
// only on its own loopback never reports ready from the network.
func TestLiveNetworkProbe_LoopbackOnlyListenerFails(t *testing.T) {
	runtime, ctx := liveRuntime(t)
	network := liveProbeNetwork(t, runtime, ctx)
	target := liveProbeTarget(t, runtime, ctx, network,
		`while true; do printf "HTTP/1.0 200 OK\r\nContent-Length: 2\r\n\r\nok" | nc -l -p 8081 -s 127.0.0.1; done`)

	inspect, err := runtime.InspectContainer(ctx, target.ID)
	require.NoError(t, err)

	result, err := runtime.ProbeContainerNetwork(ctx, domain.ContainerNetworkProbeRequest{
		TargetContainerID: target.ID,
		ExpectedStartedAt: inspect.StartedAt,
		Network:           network,
		Protocol:          domain.ProbeProtocolHTTP,
		Port:              8081,
		Path:              "/",
		Timeout:           5 * time.Second,
	})
	require.NoError(t, err)
	assert.False(t, result.Ready, "a loopback-only listener must never be reachable over the private network")
	assert.NotEmpty(t, result.Diagnostic)
}

// TestLiveNetworkProbe_TCPReady proves a TCP readiness session accepts an
// open port and rejects a closed one.
func TestLiveNetworkProbe_TCPReady(t *testing.T) {
	runtime, ctx := liveRuntime(t)
	network := liveProbeNetwork(t, runtime, ctx)
	target := liveProbeTarget(t, runtime, ctx, network,
		`while true; do nc -l -p 5432 >/dev/null 2>&1; done`)

	inspect, err := runtime.InspectContainer(ctx, target.ID)
	require.NoError(t, err)

	ready, err := runtime.ProbeContainerNetwork(ctx, domain.ContainerNetworkProbeRequest{
		TargetContainerID: target.ID,
		ExpectedStartedAt: inspect.StartedAt,
		Network:           network,
		Protocol:          domain.ProbeProtocolTCP,
		Port:              5432,
		Timeout:           5 * time.Second,
	})
	require.NoError(t, err)
	assert.True(t, ready.Ready)

	closed, err := runtime.ProbeContainerNetwork(ctx, domain.ContainerNetworkProbeRequest{
		TargetContainerID: target.ID,
		ExpectedStartedAt: inspect.StartedAt,
		Network:           network,
		Protocol:          domain.ProbeProtocolTCP,
		Port:              5499,
		Timeout:           2 * time.Second,
	})
	require.NoError(t, err)
	assert.False(t, closed.Ready)
}

// TestLiveNetworkProbe_RejectsStaleGeneration proves a probe whose expected
// execution start no longer matches the target fails instead of reporting
// readiness for a generation that restarted.
func TestLiveNetworkProbe_RejectsStaleGeneration(t *testing.T) {
	runtime, ctx := liveRuntime(t)
	network := liveProbeNetwork(t, runtime, ctx)
	target := liveProbeTarget(t, runtime, ctx, network,
		`while true; do printf "HTTP/1.0 200 OK\r\nContent-Length: 2\r\n\r\nok" | nc -l -p 8080; done`)

	_, err := runtime.ProbeContainerNetwork(ctx, domain.ContainerNetworkProbeRequest{
		TargetContainerID: target.ID,
		ExpectedStartedAt: time.Now().Add(-time.Hour).UTC(),
		Network:           network,
		Protocol:          domain.ProbeProtocolHTTP,
		Port:              8080,
		Path:              "/",
		Timeout:           5 * time.Second,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrAppStateConflict)
}

// TestLiveNetworkProbe_NonHTTPGreetingIsNotReady proves a listener whose
// first line merely starts with 2 or 3 (a non-HTTP service on a
// misdirected port) is never reported ready.
func TestLiveNetworkProbe_NonHTTPGreetingIsNotReady(t *testing.T) {
	runtime, ctx := liveRuntime(t)
	network := liveProbeNetwork(t, runtime, ctx)
	target := liveProbeTarget(t, runtime, ctx, network,
		`while true; do printf '300 ready\r\n' | nc -l -p 8083; done`)

	inspect, err := runtime.InspectContainer(ctx, target.ID)
	require.NoError(t, err)

	result, err := runtime.ProbeContainerNetwork(ctx, domain.ContainerNetworkProbeRequest{
		TargetContainerID: target.ID,
		ExpectedStartedAt: inspect.StartedAt,
		Network:           network,
		Protocol:          domain.ProbeProtocolHTTP,
		Port:              8083,
		Path:              "/",
		Timeout:           5 * time.Second,
	})
	require.NoError(t, err)
	assert.False(t, result.Ready, "a non-HTTP greeting must never satisfy HTTP readiness")
}

// TestLiveNetworkProbe_HungTargetIsUnhealthyNotInfrastructure proves a
// target that accepts the connection but never answers yields an unhealthy
// result rather than an infrastructure error: the retry loop must keep
// polling instead of aborting the wait.
func TestLiveNetworkProbe_HungTargetIsUnhealthyNotInfrastructure(t *testing.T) {
	runtime, ctx := liveRuntime(t)
	network := liveProbeNetwork(t, runtime, ctx)
	target := liveProbeTarget(t, runtime, ctx, network,
		`while true; do sleep 3600 | nc -l -p 8084; done`)

	inspect, err := runtime.InspectContainer(ctx, target.ID)
	require.NoError(t, err)

	result, err := runtime.ProbeContainerNetwork(ctx, domain.ContainerNetworkProbeRequest{
		TargetContainerID: target.ID,
		ExpectedStartedAt: inspect.StartedAt,
		Network:           network,
		Protocol:          domain.ProbeProtocolHTTP,
		Port:              8084,
		Path:              "/",
		Timeout:           4 * time.Second,
	})
	require.NoError(t, err, "a hanging target is a readiness failure, not an infrastructure error")
	assert.False(t, result.Ready)
	assert.NotEmpty(t, result.Diagnostic)
}

// TestLiveNetworkProbe_RejectsForeignNetwork proves the helper is bound to
// exactly one network: a target attached elsewhere is never probed across
// networks.
func TestLiveNetworkProbe_RejectsForeignNetwork(t *testing.T) {
	runtime, ctx := liveRuntime(t)
	ownerNetwork := liveProbeNetwork(t, runtime, ctx)
	foreignNetwork := liveProbeNetwork(t, runtime, ctx)
	target := liveProbeTarget(t, runtime, ctx, ownerNetwork,
		`while true; do printf "HTTP/1.0 200 OK\r\nContent-Length: 2\r\n\r\nok" | nc -l -p 8080; done`)

	before := liveProbeHelpers(t, runtime, ctx)
	inspect, err := runtime.InspectContainer(ctx, target.ID)
	require.NoError(t, err)

	_, err = runtime.ProbeContainerNetwork(ctx, domain.ContainerNetworkProbeRequest{
		TargetContainerID: target.ID,
		ExpectedStartedAt: inspect.StartedAt,
		Network:           foreignNetwork,
		Protocol:          domain.ProbeProtocolHTTP,
		Port:              8080,
		Path:              "/",
		Timeout:           5 * time.Second,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrAppStateConflict)

	assert.Equal(t, before, liveProbeHelpers(t, runtime, ctx), "no helper may survive a rejected probe")
}
