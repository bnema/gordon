package edgesnapshot

import (
	"testing"

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrafficGraphSnapshotProtoRoundTripAndPrivacy(t *testing.T) {
	snapshot := domain.TrafficGraphSnapshot{Generation: 1, Graph: domain.TrafficGraph{
		EntryPoints: []domain.EntryPoint{{Name: "tcp", Address: ":9000", Protocol: domain.EntryPointProtocolTCP}},
		Routers:     []domain.TrafficRouter{{Name: "app", EntryPoint: "tcp", Protocol: domain.RouterProtocolTCP, Service: "network_service:app:http"}},
		Services:    []domain.TrafficService{{Name: "network_service:app:http", Backends: []domain.TrafficBackend{{Host: "app.internal", Port: 8080, Protocol: domain.NetworkProtocolTCP}}}},
	}}
	message, err := TrafficGraphSnapshotToProto(snapshot)
	require.NoError(t, err)
	assert.NotContains(t, message.String(), "cert")
	assert.NotContains(t, message.String(), "key")
	roundTrip, err := TrafficGraphSnapshotFromProto(message)
	require.NoError(t, err)
	assert.Equal(t, snapshot, roundTrip)

	message.Services[0].Backends[0].Host = "127.0.0.1"
	_, err = TrafficGraphSnapshotFromProto(message)
	assert.Error(t, err)
}
