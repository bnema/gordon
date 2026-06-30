package traffic

import (
	"bufio"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestTCPManagerLifecycle(t *testing.T) {
	backend := startTCPEchoServer(t, 0)
	graph := tcpGraph(t, freeTCPAddress(t), backend.address)
	manager := NewManager()

	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	conn := dialTCP(t, graph.EntryPoints[0].Address)
	require.NoError(t, conn.Close())

	status := manager.Status()
	require.Equal(t, reloadStatusOK, status.LastReloadStatus)
	require.Len(t, status.EntryPoints, 1)
	assert.True(t, status.EntryPoints[0].Active)

	require.NoError(t, manager.Shutdown(context.Background()))
	_, err := net.DialTimeout("tcp", graph.EntryPoints[0].Address, 100*time.Millisecond)
	require.Error(t, err)
}

func TestTCPManagerRejectsInvalidGraphWithoutReplacingSnapshot(t *testing.T) {
	backend := startTCPEchoServer(t, 0)
	graph := tcpGraph(t, freeTCPAddress(t), backend.address)
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	invalid := graph
	invalid.EntryPoints = append(invalid.EntryPoints, graph.EntryPoints[0])
	err := manager.Apply(context.Background(), &invalid)
	require.Error(t, err)

	status := manager.Status()
	assert.Equal(t, reloadStatusError, status.LastReloadStatus)
	assert.Contains(t, status.LastReloadError, "duplicate entrypoint")

	conn := dialTCP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	assertRoundTrip(t, conn, "still works")
}

func TestTCPPassthroughEcho(t *testing.T) {
	backend := startTCPEchoServer(t, 0)
	graph := tcpGraph(t, freeTCPAddress(t), backend.address)
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	conn := dialTCP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	assertRoundTrip(t, conn, "hello tcp")
}

func TestTCPPassthroughUnknownRouterCloses(t *testing.T) {
	graph := tcpGraph(t, freeTCPAddress(t), freeTCPAddress(t))
	graph.Routers = nil
	graph.Services = nil
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	conn := dialTCP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	_, err := conn.Write([]byte("hello"))
	require.NoError(t, err)
	_, err = bufio.NewReader(conn).ReadByte()
	require.Error(t, err)
	assert.Eventually(t, func() bool { return manager.Status().Counters.TotalRefused == 1 }, time.Second, 10*time.Millisecond)
}

func TestTCPPassthroughBackendDialFailure(t *testing.T) {
	graph := tcpGraph(t, freeTCPAddress(t), freeTCPAddress(t))
	graph.Options.TCP.DialTimeout = 50 * time.Millisecond
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	conn := dialTCP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	_, err := conn.Write([]byte("hello"))
	require.NoError(t, err)
	_, err = bufio.NewReader(conn).ReadByte()
	require.Error(t, err)
	assert.Eventually(t, func() bool { return manager.Status().Counters.TotalErrors == 1 }, time.Second, 10*time.Millisecond)
}

func TestTCPPassthroughShutdownStopsAccepting(t *testing.T) {
	backend := startTCPEchoServer(t, 0)
	graph := tcpGraph(t, freeTCPAddress(t), backend.address)
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))

	require.NoError(t, manager.Shutdown(context.Background()))
	_, err := net.DialTimeout("tcp", graph.EntryPoints[0].Address, 100*time.Millisecond)
	require.Error(t, err)
}

func TestTCPPassthroughIdleTimeoutClosesConnection(t *testing.T) {
	backend := startTCPEchoServer(t, 0)
	graph := tcpGraph(t, freeTCPAddress(t), backend.address)
	graph.Options.TCP.IdleTimeout = 50 * time.Millisecond
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	conn := dialTCP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	_, err := bufio.NewReader(conn).ReadByte()
	require.Error(t, err)
}

func TestTCPPassthroughMaxConnectionsRejectsOverflow(t *testing.T) {
	backend := startTCPEchoServer(t, 0)
	graph := tcpGraph(t, freeTCPAddress(t), backend.address)
	graph.Options.TCP.MaxConnections = 1
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	first := dialTCP(t, graph.EntryPoints[0].Address)
	defer first.Close()
	assertRoundTrip(t, first, "first")
	require.Eventually(t, func() bool { return manager.Status().Counters.ActiveTCPConnections == 1 }, time.Second, 10*time.Millisecond)

	second := dialTCP(t, graph.EntryPoints[0].Address)
	defer second.Close()
	_, err := second.Write([]byte("second"))
	require.NoError(t, err)
	_, err = bufio.NewReader(second).ReadByte()
	require.Error(t, err)
	assert.Eventually(t, func() bool { return manager.Status().Counters.TotalRefused == 1 }, time.Second, 10*time.Millisecond)
}

func TestTCPPassthroughDrainWaitsForActiveConnectionThenTimesOut(t *testing.T) {
	backend := startTCPEchoServer(t, 0)
	graph := tcpGraph(t, freeTCPAddress(t), backend.address)
	graph.Options.TCP.DrainTimeout = 50 * time.Millisecond
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	conn := dialTCP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	assertRoundTrip(t, conn, "held")
	require.Eventually(t, func() bool { return manager.Status().Counters.ActiveTCPConnections == 1 }, time.Second, 10*time.Millisecond)

	empty := domain.TrafficGraph{Options: graph.Options}
	started := time.Now()
	require.NoError(t, manager.Apply(context.Background(), &empty))
	assert.GreaterOrEqual(t, time.Since(started), 40*time.Millisecond)

	_, err := conn.Write([]byte("after drain"))
	if err == nil {
		_, err = bufio.NewReader(conn).ReadByte()
	}
	require.Error(t, err)
}

func TestTCPPassthroughStatusCountersTrackAcceptedRefusedErrorsAndBytes(t *testing.T) {
	backend := startTCPEchoServer(t, 0)
	graph := tcpGraph(t, freeTCPAddress(t), backend.address)
	graph.Options.TCP.MaxConnections = 1
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	conn := dialTCP(t, graph.EntryPoints[0].Address)
	assertRoundTrip(t, conn, "bytes")
	second := dialTCP(t, graph.EntryPoints[0].Address)
	_, err := second.Write([]byte("refused"))
	require.NoError(t, err)
	_, _ = bufio.NewReader(second).ReadByte()
	_ = second.Close()
	_ = conn.Close()

	assert.Eventually(t, func() bool {
		status := manager.Status()
		return status.Counters.TotalAccepted == 1 &&
			status.Counters.TotalRefused == 1 &&
			status.Counters.BytesIn >= int64(len("bytes\n")) &&
			status.Counters.BytesOut >= int64(len("bytes\n"))
	}, time.Second, 10*time.Millisecond)

	bad := tcpGraph(t, freeTCPAddress(t), freeTCPAddress(t))
	bad.Options.TCP.DialTimeout = 50 * time.Millisecond
	require.NoError(t, manager.Apply(context.Background(), &bad))
	badConn := dialTCP(t, bad.EntryPoints[0].Address)
	_, err = badConn.Write([]byte("boom"))
	require.NoError(t, err)
	_, _ = bufio.NewReader(badConn).ReadByte()
	_ = badConn.Close()
	assert.Eventually(t, func() bool { return manager.Status().Counters.TotalErrors == 1 }, time.Second, 10*time.Millisecond)
}

func tcpGraph(t *testing.T, listenAddress string, backendAddress string) domain.TrafficGraph {
	t.Helper()
	backend, err := backendFromAddress("echo:tcp", backendAddress)
	require.NoError(t, err)
	ref := serviceRef("echo", "tcp")
	graph := domain.TrafficGraph{
		Options:     domain.TrafficOptions{TCP: domain.TCPOptions{DialTimeout: time.Second, IdleTimeout: time.Minute, DrainTimeout: time.Second}},
		EntryPoints: []domain.EntryPoint{{Name: "tcp", Address: listenAddress, Protocol: domain.EntryPointProtocolTCP}},
		Routers:     []domain.TrafficRouter{{Name: "echo", EntryPoint: "tcp", Protocol: domain.RouterProtocolTCP, Service: ref}},
		Services:    []domain.TrafficService{{Name: ref, Backends: []domain.TrafficBackend{backend}}},
	}
	require.NoError(t, graph.Validate())
	return graph
}

func freeTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}

func dialTCP(t *testing.T, address string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, time.Second)
	require.NoError(t, err)
	return conn
}

func assertRoundTrip(t *testing.T, conn net.Conn, message string) {
	t.Helper()
	_, err := conn.Write([]byte(message + "\n"))
	require.NoError(t, err)
	line, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, message+"\n", line)
}

func shutdownManager(t *testing.T, manager *Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, manager.Shutdown(ctx))
}

type tcpEchoServer struct {
	address string
	close   func()
}

func startTCPEchoServer(t *testing.T, readDelay time.Duration) tcpEchoServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if readDelay > 0 {
					time.Sleep(readDelay)
				}
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	server := tcpEchoServer{address: listener.Addr().String(), close: func() {
		_ = listener.Close()
		<-done
	}}
	t.Cleanup(server.close)
	return server
}
