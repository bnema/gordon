package traffic

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestUDPPassthroughEcho(t *testing.T) {
	backend := startUDPEchoServer(t)
	graph := udpGraph(t, freeUDPAddress(t), backend.address)
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	conn := dialUDP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	assertUDPRoundTrip(t, conn, "hello udp")
}

func TestUDPRejectsUntrustedCIDR(t *testing.T) {
	backend := startUDPEchoServer(t)
	graph := udpGraph(t, freeUDPAddress(t), backend.address)
	graph.EntryPoints[0].TrustedCIDRs = []string{"192.0.2.0/24"}
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	assertUDPRejected(t, manager, graph.EntryPoints[0].Address, 1)
}

func TestUDPReloadAppliesTrustedCIDRChange(t *testing.T) {
	backend := startUDPEchoServer(t)
	graph := udpGraph(t, freeUDPAddress(t), backend.address)
	graph.EntryPoints[0].TrustedCIDRs = []string{"127.0.0.0/8"}
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	conn := dialUDP(t, graph.EntryPoints[0].Address)
	assertUDPRoundTrip(t, conn, "allowed")
	require.NoError(t, conn.Close())

	updated := graph
	updated.EntryPoints[0].TrustedCIDRs = []string{"192.0.2.0/24"}
	require.NoError(t, manager.Apply(context.Background(), &updated))
	assertUDPRejected(t, manager, graph.EntryPoints[0].Address, 1)
}

func TestUDPReloadClosesActiveUntrustedSessions(t *testing.T) {
	backend := startUDPEchoServer(t)
	graph := udpGraph(t, freeUDPAddress(t), backend.address)
	graph.EntryPoints[0].TrustedCIDRs = []string{"127.0.0.0/8"}
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	conn := dialUDP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	assertUDPRoundTrip(t, conn, "before")
	require.Eventually(t, func() bool { return manager.Status().Counters.ActiveUDPSessions == 1 }, time.Second, 10*time.Millisecond)

	updated := graph
	updated.EntryPoints[0].TrustedCIDRs = []string{"192.0.2.0/24"}
	require.NoError(t, manager.Apply(context.Background(), &updated))

	assert.Eventually(t, func() bool { return manager.Status().Counters.ActiveUDPSessions == 0 }, time.Second, 10*time.Millisecond)
	_, err := conn.Write([]byte("after"))
	require.NoError(t, err)
	_ = conn.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
	buf := make([]byte, 32)
	_, err = conn.Read(buf)
	require.Error(t, err)
}

func TestUDPReloadReplacesSessionWhenBackendChanges(t *testing.T) {
	backendA := startUDPEchoServerWithPrefix(t, "a:")
	backendB := startUDPEchoServerWithPrefix(t, "b:")
	graph := udpGraph(t, freeUDPAddress(t), backendA.address)
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	conn := dialUDP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	assertUDPRoundTripWant(t, conn, "first", "a:first")

	updated := graph
	backend, err := backendFromAddress("echo:udp", backendB.address)
	require.NoError(t, err)
	backend.Protocol = domain.NetworkProtocolUDP
	updated.Services[0].Backends = []domain.TrafficBackend{backend}
	require.NoError(t, manager.Apply(context.Background(), &updated))

	assertUDPRoundTripWant(t, conn, "second", "a:second")
	assert.Eventually(t, func() bool { return manager.Status().Counters.ActiveUDPSessions == 0 }, time.Second, 10*time.Millisecond)

	newConn := dialUDP(t, graph.EntryPoints[0].Address)
	defer newConn.Close()
	assertUDPRoundTripWant(t, newConn, "third", "b:third")
}

func assertUDPRejected(t *testing.T, manager *Manager, address string, refused int64) {
	t.Helper()
	conn := dialUDP(t, address)
	defer conn.Close()
	_, err := conn.Write([]byte("blocked"))
	require.NoError(t, err)
	_ = conn.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
	buf := make([]byte, 32)
	_, err = conn.Read(buf)
	require.Error(t, err)
	assert.Eventually(t, func() bool { return manager.Status().Counters.TotalRefused == refused }, time.Second, 10*time.Millisecond)
}

func TestUDPTwoClientsGetIsolatedSessions(t *testing.T) {
	backend := startUDPEchoServer(t)
	graph := udpGraph(t, freeUDPAddress(t), backend.address)
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	first := dialUDP(t, graph.EntryPoints[0].Address)
	defer first.Close()
	second := dialUDP(t, graph.EntryPoints[0].Address)
	defer second.Close()
	assertUDPRoundTrip(t, first, "first")
	assertUDPRoundTrip(t, second, "second")
	assert.Eventually(t, func() bool { return manager.Status().Counters.ActiveUDPSessions == 2 }, time.Second, 10*time.Millisecond)
}

func TestUDPIdleSessionExpires(t *testing.T) {
	backend := startUDPEchoServer(t)
	graph := udpGraph(t, freeUDPAddress(t), backend.address)
	graph.Options.UDP.IdleTimeout = 40 * time.Millisecond
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	conn := dialUDP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	assertUDPRoundTrip(t, conn, "expire")
	assert.Eventually(t, func() bool { return manager.Status().Counters.ActiveUDPSessions == 0 }, time.Second, 10*time.Millisecond)
}

func TestUDPMaxSessionsRejectsOverflow(t *testing.T) {
	backend := startUDPEchoServer(t)
	graph := udpGraph(t, freeUDPAddress(t), backend.address)
	graph.Options.UDP.MaxSessions = 1
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	first := dialUDP(t, graph.EntryPoints[0].Address)
	defer first.Close()
	assertUDPRoundTrip(t, first, "first")

	second := dialUDP(t, graph.EntryPoints[0].Address)
	defer second.Close()
	_, err := second.Write([]byte("second"))
	require.NoError(t, err)
	_ = second.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
	buf := make([]byte, 32)
	_, err = second.Read(buf)
	require.Error(t, err)
	assert.Eventually(t, func() bool { return manager.Status().Counters.TotalRefused == 1 }, time.Second, 10*time.Millisecond)
}

func TestUDPRemovedRouterWithRetainedEntryPointDrainsSession(t *testing.T) {
	backend := startUDPEchoServer(t)
	graph := udpGraph(t, freeUDPAddress(t), backend.address)
	graph.Options.UDP.DrainTimeout = 50 * time.Millisecond
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	conn := dialUDP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	assertUDPRoundTrip(t, conn, "held")

	next := graph
	next.Routers = nil
	next.Services = nil
	require.NoError(t, manager.Apply(context.Background(), &next))
	assertUDPRoundTrip(t, conn, "during-drain")
	assert.Eventually(t, func() bool { return manager.Status().Counters.ActiveUDPSessions == 0 }, time.Second, 10*time.Millisecond)

	_, err := conn.Write([]byte("after"))
	require.NoError(t, err)
	_ = conn.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
	buf := make([]byte, 32)
	_, err = conn.Read(buf)
	require.Error(t, err)
}

func TestUDPBackendChangeDrainsExistingSession(t *testing.T) {
	oldBackend := startUDPEchoServerWithPrefix(t, "old:")
	newBackend := startUDPEchoServerWithPrefix(t, "new:")
	graph := udpGraph(t, freeUDPAddress(t), oldBackend.address)
	graph.Options.UDP.DrainTimeout = 50 * time.Millisecond
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	oldConn := dialUDP(t, graph.EntryPoints[0].Address)
	defer oldConn.Close()
	assertUDPRoundTripWant(t, oldConn, "held", "old:held")

	next := udpGraph(t, graph.EntryPoints[0].Address, newBackend.address)
	next.Options = graph.Options
	require.NoError(t, manager.Apply(context.Background(), &next))
	assertUDPRoundTripWant(t, oldConn, "during-drain", "old:during-drain")
	assert.Eventually(t, func() bool { return manager.Status().Counters.ActiveUDPSessions == 0 }, time.Second, 10*time.Millisecond)

	newConn := dialUDP(t, graph.EntryPoints[0].Address)
	defer newConn.Close()
	assertUDPRoundTripWant(t, newConn, "after", "new:after")
}

func TestUDPRemovedRouterDrainsThenClosesSessions(t *testing.T) {
	backend := startUDPEchoServer(t)
	graph := udpGraph(t, freeUDPAddress(t), backend.address)
	graph.Options.UDP.DrainTimeout = 50 * time.Millisecond
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	conn := dialUDP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	assertUDPRoundTrip(t, conn, "held")
	require.Eventually(t, func() bool { return manager.Status().Counters.ActiveUDPSessions == 1 }, time.Second, 10*time.Millisecond)

	empty := domain.TrafficGraph{Options: graph.Options}
	started := time.Now()
	require.NoError(t, manager.Apply(context.Background(), &empty))
	assert.GreaterOrEqual(t, time.Since(started), 40*time.Millisecond)
	assert.Equal(t, int64(0), manager.Status().Counters.ActiveUDPSessions)
}

func TestUDPShutdownClosesSocket(t *testing.T) {
	backend := startUDPEchoServer(t)
	graph := udpGraph(t, freeUDPAddress(t), backend.address)
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	require.NoError(t, manager.Shutdown(context.Background()))

	conn := dialUDP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	_, err := conn.Write([]byte("closed"))
	require.NoError(t, err)
	_ = conn.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
	buf := make([]byte, 32)
	_, err = conn.Read(buf)
	require.Error(t, err)
}

func TestUDPStatusCountersTrackDatagrams(t *testing.T) {
	backend := startUDPEchoServer(t)
	graph := udpGraph(t, freeUDPAddress(t), backend.address)
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	conn := dialUDP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	assertUDPRoundTrip(t, conn, "bytes")

	assert.Eventually(t, func() bool {
		status := manager.Status()
		return status.Counters.TotalAccepted == 1 &&
			status.Counters.ActiveUDPSessions == 1 &&
			status.Counters.BytesIn >= int64(len("bytes")) &&
			status.Counters.BytesOut >= int64(len("bytes"))
	}, time.Second, 10*time.Millisecond)
}

func udpGraph(t *testing.T, listenAddress string, backendAddress string) domain.TrafficGraph {
	t.Helper()
	backend, err := backendFromAddress("echo:udp", backendAddress)
	require.NoError(t, err)
	backend.Protocol = domain.NetworkProtocolUDP
	ref := serviceRef("echo", "udp")
	graph := domain.TrafficGraph{
		Options:     domain.TrafficOptions{UDP: domain.UDPOptions{IdleTimeout: time.Minute, DrainTimeout: 50 * time.Millisecond}},
		EntryPoints: []domain.EntryPoint{{Name: "udp", Address: listenAddress, Protocol: domain.EntryPointProtocolUDP}},
		Routers:     []domain.TrafficRouter{{Name: "echo", EntryPoint: "udp", Protocol: domain.RouterProtocolUDP, Service: ref}},
		Services:    []domain.TrafficService{{Name: ref, Backends: []domain.TrafficBackend{backend}}},
	}
	require.NoError(t, graph.Validate())
	return graph
}

func freeUDPAddress(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	address := conn.LocalAddr().String()
	require.NoError(t, conn.Close())
	return address
}

func dialUDP(t *testing.T, address string) *net.UDPConn {
	t.Helper()
	udpAddr, err := net.ResolveUDPAddr("udp", address)
	require.NoError(t, err)
	conn, err := net.DialUDP("udp", nil, udpAddr)
	require.NoError(t, err)
	return conn
}

func assertUDPRoundTrip(t *testing.T, conn *net.UDPConn, message string) {
	t.Helper()
	assertUDPRoundTripWant(t, conn, message, message)
}

func assertUDPRoundTripWant(t *testing.T, conn *net.UDPConn, message string, want string) {
	t.Helper()
	_, err := conn.Write([]byte(message))
	require.NoError(t, err)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 128)
	n, err := conn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, want, string(buf[:n]))
}

type udpEchoServer struct{ address string }

func startUDPEchoServer(t *testing.T) udpEchoServer {
	t.Helper()
	return startUDPEchoServerWithPrefix(t, "")
}

func startUDPEchoServerWithPrefix(t *testing.T, prefix string) udpEchoServer {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, udpBufferSize)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = conn.WriteTo(append([]byte(prefix), buf[:n]...), addr)
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		<-done
	})
	return udpEchoServer{address: conn.LocalAddr().String()}
}
