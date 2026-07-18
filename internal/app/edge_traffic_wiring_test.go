package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	edgev1 "github.com/bnema/gordon/api/gordon/edge/v1"
	edgesnapshotgrpc "github.com/bnema/gordon/internal/adapters/in/grpc/edgesnapshot"
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	trafficadapter "github.com/bnema/gordon/internal/adapters/in/traffic"
	grpcauth "github.com/bnema/gordon/internal/adapters/out/grpc/auth"
	edgesnapshotclient "github.com/bnema/gordon/internal/adapters/out/grpc/edgesnapshot"
	"github.com/bnema/gordon/internal/adapters/out/tokenstore"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/componentauth"
	"github.com/bnema/gordon/internal/usecase/edgesnapshot"
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

// This calls runEdgeTrafficApplyLoop, the exact production helper used by
// runEdgeTraffic. The test only publishes to the authenticated gRPC hub: it
// never applies a graph to the manager itself.
func TestEdgeTrafficApplyLoopConsumesAuthenticatedGraphStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := zerowrap.New(zerowrap.Config{Level: "disabled", Output: io.Discard})
	backendHost := edgeLoopNonLoopbackHost(t)
	backends := newEdgeLoopBackends(t, backendHost)
	defer backends.close()

	hub, client, stopStream := newAuthenticatedEdgeTrafficStream(t, ctx, log)
	defer stopStream()
	initial := edgeLoopGraph(t, backends, edgeLoopReservedAddresses(t))
	updated := edgeLoopGraph(t, backends, edgeLoopReservedAddresses(t))
	require.NoError(t, hub.Publish(domain.TrafficGraphSnapshot{Generation: 1, Graph: initial}))
	require.NoError(t, client.Start(ctx))
	defer client.Stop()
	waitEdgeLoopGraph(t, client, 1)

	cert, key := writeEdgeCertificate(t)
	cfg := defaultEdgeConfig()
	cfg.Edge.TLS.Mode = edgeTLSModeFiles
	cfg.Edge.TLS.CertFile, cfg.Edge.TLS.KeyFile = cert, key
	manager := trafficadapter.NewManager()
	loopDone := make(chan error, 1)
	go func() {
		loopDone <- runEdgeTrafficApplyLoop(ctx, cfg, productionEdgeRoleDependencies(), log,
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("edge traffic")) }), client, manager)
	}()

	probeEdgeLoopGraph(t, initial)
	require.NoError(t, hub.Publish(domain.TrafficGraphSnapshot{Generation: 2, Graph: updated}))
	waitEdgeLoopGraph(t, client, 2)
	probeEdgeLoopGraph(t, updated)
	assert.Empty(t, manager.Status().LastReloadError)

	cancel()
	require.NoError(t, <-loopDone)
	assert.Empty(t, manager.Status().EntryPoints)
	assertEdgeLoopAddressesReleased(t, updated)
}

type edgeLoopAddresses struct{ smart, tcp, udp string }

type edgeLoopBackends struct {
	tcp, udp string
	closers  []io.Closer
	wg       sync.WaitGroup
}

func newAuthenticatedEdgeTrafficStream(t *testing.T, ctx context.Context, log zerowrap.Logger) (*edgesnapshot.TrafficGraphHub, *edgesnapshotclient.TrafficGraphClient, func()) {
	t.Helper()
	store, err := tokenstore.NewUnsafeStore(t.TempDir(), log)
	require.NoError(t, err)
	auth := componentauth.NewService(store, log, componentauth.Config{})
	token, err := auth.CreateToken(ctx, componentauth.CreateRequest{Name: "edge-traffic-loop", Role: domain.ComponentRoleEdge, Scopes: []domain.ComponentScope{domain.ComponentScopeTrafficWatch}})
	require.NoError(t, err)
	hub := edgesnapshot.NewTrafficGraphHub()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer(grpc.StreamInterceptor(interceptors.ComponentAuthStreamInterceptor(auth, edgesnapshotgrpc.MethodScopes(), edgesnapshotgrpc.MethodRoles())))
	edgev1.RegisterEdgeServiceServer(server, edgesnapshotgrpc.NewServerWithTrafficGraphSource(nil, hub))
	go func() { _ = server.Serve(listener) }()
	credentials, err := grpcauth.NewInsecureBearerTokenCredentials(token.Token)
	require.NoError(t, err)
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithPerRPCCredentials(credentials))
	require.NoError(t, err)
	client := edgesnapshotclient.NewTrafficGraphClient(connection, edgesnapshotclient.WithReconnectBackoff(time.Millisecond, 5*time.Millisecond))
	return hub, client, func() {
		client.Stop()
		require.NoError(t, connection.Close())
		server.Stop()
		_ = listener.Close()
	}
}

func newEdgeLoopBackends(t *testing.T, host string) *edgeLoopBackends {
	t.Helper()
	result := &edgeLoopBackends{}
	tcp, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	require.NoError(t, err)
	result.tcp, result.closers = tcp.Addr().String(), append(result.closers, tcp)
	result.wg.Add(1)
	go func() {
		defer result.wg.Done()
		for {
			conn, acceptErr := tcp.Accept()
			if acceptErr != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	udp, err := net.ListenPacket("udp", net.JoinHostPort(host, "0"))
	require.NoError(t, err)
	result.udp, result.closers = udp.LocalAddr().String(), append(result.closers, udp)
	result.wg.Add(1)
	go func() {
		defer result.wg.Done()
		buffer := make([]byte, 64<<10)
		for {
			n, address, readErr := udp.ReadFrom(buffer)
			if readErr != nil {
				return
			}
			_, _ = udp.WriteTo(buffer[:n], address)
		}
	}()
	return result
}

func (b *edgeLoopBackends) close() {
	for _, closer := range b.closers {
		_ = closer.Close()
	}
	b.wg.Wait()
}

func edgeLoopGraph(t *testing.T, backends *edgeLoopBackends, addresses edgeLoopAddresses) domain.TrafficGraph {
	t.Helper()
	tcpHost, tcpPort := edgeLoopBackendAddress(t, backends.tcp)
	udpHost, udpPort := edgeLoopBackendAddress(t, backends.udp)
	return domain.TrafficGraph{
		Options: domain.TrafficOptions{TCP: domain.TCPOptions{DialTimeout: time.Second, IdleTimeout: time.Second, DrainTimeout: time.Millisecond}, UDP: domain.UDPOptions{IdleTimeout: time.Second, DrainTimeout: time.Millisecond}},
		EntryPoints: []domain.EntryPoint{
			{Name: "smart", Address: addresses.smart, Protocol: domain.EntryPointProtocolSmartTCP},
			{Name: "tcp", Address: addresses.tcp, Protocol: domain.EntryPointProtocolTCP},
			{Name: "udp", Address: addresses.udp, Protocol: domain.EntryPointProtocolUDP},
		},
		Routers: []domain.TrafficRouter{
			{Name: "smart-http", EntryPoint: "smart", Protocol: domain.RouterProtocolHTTP, Rule: domain.TrafficRule{Host: "edge.test"}, Service: "route:edge.test"},
			{Name: "tcp", EntryPoint: "tcp", Protocol: domain.RouterProtocolTCP, Service: "network_service:tcp:port"},
			{Name: "udp", EntryPoint: "udp", Protocol: domain.RouterProtocolUDP, Service: "network_service:udp:port"},
		},
		Services: []domain.TrafficService{
			{Name: "route:edge.test"},
			{Name: "network_service:tcp:port", Backends: []domain.TrafficBackend{{Host: tcpHost, Port: tcpPort, Protocol: domain.NetworkProtocolTCP}}},
			{Name: "network_service:udp:port", Backends: []domain.TrafficBackend{{Host: udpHost, Port: udpPort, Protocol: domain.NetworkProtocolUDP}}},
		},
	}
}

func edgeLoopReservedAddresses(t *testing.T) edgeLoopAddresses {
	t.Helper()
	return edgeLoopAddresses{smart: unusedEdgeAddress(t, "tcp"), tcp: unusedEdgeAddress(t, "tcp"), udp: unusedEdgeAddress(t, "udp")}
}

func edgeLoopBackendAddress(t *testing.T, address string) (string, int) {
	t.Helper()
	host, rawPort, err := net.SplitHostPort(address)
	require.NoError(t, err)
	port, err := strconv.Atoi(rawPort)
	require.NoError(t, err)
	return host, port
}

func edgeLoopNonLoopbackHost(t *testing.T) string {
	t.Helper()
	interfaces, err := net.Interfaces()
	require.NoError(t, err)
	for _, iface := range interfaces {
		addresses, addressErr := iface.Addrs()
		require.NoError(t, addressErr)
		for _, address := range addresses {
			ip, _, parseErr := net.ParseCIDR(address.String())
			if parseErr == nil && ip.To4() != nil && !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsLinkLocalUnicast() {
				return ip.String()
			}
		}
	}
	t.Fatal("test requires a non-loopback IPv4 address for split-edge backends")
	return ""
}

func waitEdgeLoopGraph(t *testing.T, client *edgesnapshotclient.TrafficGraphClient, generation domain.TrafficGraphGeneration) {
	t.Helper()
	require.Eventually(t, func() bool {
		health := client.TrafficGraphHealth()
		return health.Healthy && health.LastAcceptedGeneration >= generation
	}, 3*time.Second, 10*time.Millisecond, "authenticated graph stream did not report generation %d", generation)
}

func probeEdgeLoopGraph(t *testing.T, graph domain.TrafficGraph) {
	t.Helper()
	addresses := edgeLoopAddressesFromGraph(t, graph)
	require.Eventually(t, func() bool { return edgeLoopHTTP(addresses.smart, false) == nil }, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return edgeLoopHTTP(addresses.smart, true) == nil }, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return edgeLoopTCPEcho(addresses.tcp) == nil }, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return edgeLoopUDPEcho(addresses.udp) == nil }, 3*time.Second, 10*time.Millisecond)
}

func edgeLoopAddressesFromGraph(t *testing.T, graph domain.TrafficGraph) edgeLoopAddresses {
	t.Helper()
	var addresses edgeLoopAddresses
	for _, entry := range graph.EntryPoints {
		switch entry.Name {
		case "smart":
			addresses.smart = entry.Address
		case "tcp":
			addresses.tcp = entry.Address
		case "udp":
			addresses.udp = entry.Address
		}
	}
	require.NotEmpty(t, addresses.smart)
	require.NotEmpty(t, addresses.tcp)
	require.NotEmpty(t, addresses.udp)
	return addresses
}

func edgeLoopHTTP(address string, secure bool) error {
	client := &http.Client{Timeout: time.Second}
	scheme := "http"
	if secure {
		scheme = "https"
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} // #nosec G402 -- generated test certificate.
	}
	response, err := client.Get(scheme + "://" + address)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "edge traffic" {
		return fmt.Errorf("unexpected edge HTTP response %q: %w", body, err)
	}
	return nil
}

func edgeLoopTCPEcho(address string) error {
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.Write([]byte("tcp")); err != nil {
		return err
	}
	response := make([]byte, 3)
	if _, err = io.ReadFull(conn, response); err != nil || string(response) != "tcp" {
		return fmt.Errorf("unexpected TCP response %q: %w", response, err)
	}
	return nil
}

func edgeLoopUDPEcho(address string) error {
	conn, err := net.DialTimeout("udp", address, time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.Write([]byte("udp")); err != nil {
		return err
	}
	response := make([]byte, 3)
	if err = conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	if _, err = io.ReadFull(conn, response); err != nil || string(response) != "udp" {
		return fmt.Errorf("unexpected UDP response %q: %w", response, err)
	}
	return nil
}

func assertEdgeLoopAddressesReleased(t *testing.T, graph domain.TrafficGraph) {
	t.Helper()
	addresses := edgeLoopAddressesFromGraph(t, graph)
	listener, err := net.Listen("tcp", addresses.smart)
	require.NoError(t, err)
	require.NoError(t, listener.Close())
	listener, err = net.Listen("tcp", addresses.tcp)
	require.NoError(t, err)
	require.NoError(t, listener.Close())
	packet, err := net.ListenPacket("udp", addresses.udp)
	require.NoError(t, err)
	require.NoError(t, packet.Close())
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
