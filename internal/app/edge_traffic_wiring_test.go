package app

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	trafficadapter "github.com/bnema/gordon/internal/adapters/in/traffic"
	"github.com/bnema/gordon/internal/domain"
)

func TestEdgeTrafficExternalRejectsTLSHTTPFallback(t *testing.T) {
	cfg := defaultEdgeConfig()
	cfg.Edge.TLS.Mode = edgeTLSModeExternal
	cfg.Edge.TrustedProxyCIDRs = []string{"127.0.0.0/8"}
	graph := domain.TrafficGraph{
		EntryPoints: []domain.EntryPoint{{Name: "edge", Address: "127.0.0.1:443", Protocol: domain.EntryPointProtocolSmartTCP}},
		Routers:     []domain.TrafficRouter{{Name: "app", EntryPoint: "edge", Protocol: domain.RouterProtocolHTTP, Service: "route:app.example.com"}},
		Services:    []domain.TrafficService{{Name: "route:app.example.com"}},
	}

	err := validateEdgeTrafficTLSMode(cfg, graph)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot serve HTTP router")
}

func TestEdgeTrafficFilesConfiguresSmartAndTLSMuxHTTPFallback(t *testing.T) {
	cert, key := writeEdgeCertificate(t)
	cfg := defaultEdgeConfig()
	cfg.Edge.TLS.Mode = edgeTLSModeFiles
	cfg.Edge.TLS.CertFile, cfg.Edge.TLS.KeyFile = cert, key
	tlsConfig, err := edgeTLSConfig(cfg)
	require.NoError(t, err)

	manager := trafficadapter.NewManager()
	graph := domain.TrafficGraph{EntryPoints: []domain.EntryPoint{
		{Name: "smart", Address: unusedEdgeAddress(t, "tcp"), Protocol: domain.EntryPointProtocolSmartTCP},
		{Name: "mux", Address: unusedEdgeAddress(t, "tcp"), Protocol: domain.EntryPointProtocolTLSMux},
	}}
	_, err = configureEdgeTrafficHandlers(manager, graph, cfg, http.NotFoundHandler(), tlsConfig, edgeTrafficHandlers{})
	require.NoError(t, err)

	// Applying the graph exercises the manager's configured smart-TCP
	// plaintext/TLS and tls_mux HTTP fallback listeners without fixed ports.
	require.NoError(t, manager.Apply(context.Background(), &graph))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, manager.Shutdown(shutdownCtx))
}

// The manager's handler maps are private by design. Applying the graph is the
// public behavioral check: it creates each no-fixed-port listener and shutdown
// drains them under a bounded context.
func TestEdgeManagerOwnsTCPAndUDPEntrypoints(t *testing.T) {
	tcpAddress := unusedEdgeAddress(t, "tcp")
	udpAddress := unusedEdgeAddress(t, "udp")
	manager := trafficadapter.NewManager()
	graph := domain.TrafficGraph{EntryPoints: []domain.EntryPoint{
		{Name: "tcp", Address: tcpAddress, Protocol: domain.EntryPointProtocolTCP},
		{Name: "udp", Address: udpAddress, Protocol: domain.EntryPointProtocolUDP},
	}}
	require.NoError(t, manager.Apply(context.Background(), &graph))
	status := manager.Status()
	require.Len(t, status.EntryPoints, 2)
	assert.True(t, status.EntryPoints[0].Active)
	assert.True(t, status.EntryPoints[1].Active)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, manager.Shutdown(shutdownCtx))
}

func unusedEdgeAddress(t *testing.T, network string) string {
	t.Helper()
	if network == "udp" {
		packet, err := net.ListenPacket("udp", "127.0.0.1:0")
		require.NoError(t, err)
		address := packet.LocalAddr().String()
		require.NoError(t, packet.Close())
		return address
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}
