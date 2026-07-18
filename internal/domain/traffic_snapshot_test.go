package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrafficGraphSnapshotSplitReachabilityAndClone(t *testing.T) {
	snapshot := splitTrafficSnapshot(1, "app.internal")
	require.NoError(t, snapshot.ValidateSplitReachability())

	clone := snapshot.Clone()
	clone.Graph.Services[0].Backends[0].Host = "changed.internal"
	assert.Equal(t, "app.internal", snapshot.Graph.Services[0].Backends[0].Host)

	for _, host := range []string{"127.0.0.1", "::1", "0.0.0.0", "::", "169.254.1.1", "gordon-runtime", "localhost"} {
		invalid := splitTrafficSnapshot(2, host)
		assert.Error(t, invalid.ValidateSplitReachability(), host)
	}
	assert.NoError(t, splitTrafficSnapshot(3, "10.1.2.3").ValidateSplitReachability())
}

func splitTrafficSnapshot(generation TrafficGraphGeneration, host string) TrafficGraphSnapshot {
	return TrafficGraphSnapshot{Generation: generation, Graph: TrafficGraph{
		EntryPoints: []EntryPoint{{Name: "tcp", Address: ":9000", Protocol: EntryPointProtocolTCP}},
		Routers:     []TrafficRouter{{Name: "app", EntryPoint: "tcp", Protocol: RouterProtocolTCP, Service: "network_service:app:http"}},
		Services:    []TrafficService{{Name: "network_service:app:http", Backends: []TrafficBackend{{Host: host, Port: 8080, Protocol: NetworkProtocolTCP}}}},
	}}
}
