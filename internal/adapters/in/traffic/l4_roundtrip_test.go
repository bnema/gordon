package traffic

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// TestAppL4TCPUdpRoundTrip proves one applied graph relays TCP and UDP
// simultaneously through distinct entrypoints to distinct loopback
// backends: the app-plane shape (service:<app>--<svc>:<kind>-<port>)
// round-trips end to end on both protocols.
func TestAppL4TCPUdpRoundTrip(t *testing.T) {
	tcpBackend := startTCPEchoServer(t, 0)
	udpBackend := startUDPEchoServer(t)

	tcpAddr, err := backendFromAddress("game--server", tcpBackend.address)
	require.NoError(t, err)
	udpAddr, err := backendFromAddress("game--server", udpBackend.address)
	require.NoError(t, err)
	udpAddr.Protocol = domain.NetworkProtocolUDP

	graph := domain.TrafficGraph{
		Options: domain.TrafficOptions{
			TCP: domain.TCPOptions{DialTimeout: time.Second, IdleTimeout: time.Minute, DrainTimeout: 50 * time.Millisecond},
			UDP: domain.UDPOptions{IdleTimeout: time.Minute, DrainTimeout: 50 * time.Millisecond},
		},
		EntryPoints: []domain.EntryPoint{
			{Name: "tcp", Address: freeTCPAddress(t), Protocol: domain.EntryPointProtocolTCP},
			{Name: "udp", Address: freeUDPAddress(t), Protocol: domain.EntryPointProtocolUDP},
		},
		Routers: []domain.TrafficRouter{
			{Name: "app-game--server--tcp-9000", EntryPoint: "tcp", Protocol: domain.RouterProtocolTCP, Service: "service:game--server:tcp-9000"},
			{Name: "app-game--server--udp-9000", EntryPoint: "udp", Protocol: domain.RouterProtocolUDP, Service: "service:game--server:udp-9000"},
		},
		Services: []domain.TrafficService{
			{Name: "service:game--server:tcp-9000", Backends: []domain.TrafficBackend{tcpAddr}},
			{Name: "service:game--server:udp-9000", Backends: []domain.TrafficBackend{udpAddr}},
		},
	}
	require.NoError(t, graph.Validate())

	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	tcpConn := dialTCP(t, graph.EntryPoints[0].Address)
	defer tcpConn.Close()
	assertRoundTrip(t, tcpConn, "hello tcp")

	udpConn := dialUDP(t, graph.EntryPoints[1].Address)
	defer udpConn.Close()
	assertUDPRoundTrip(t, udpConn, "hello udp")

	status := manager.Status()
	assert.Equal(t, "ok", status.LastReloadStatus)
	assert.Len(t, status.Routers, 2)
}
