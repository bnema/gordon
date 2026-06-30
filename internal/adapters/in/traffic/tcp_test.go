package traffic

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestTrafficRuntimeDefaultsApplySafeLimits(t *testing.T) {
	assert.Equal(t, 1024, effectiveTCPOptions(domain.TCPOptions{}).MaxConnections)
	assert.Equal(t, 4096, effectiveUDPOptions(domain.UDPOptions{}).MaxSessions)
}

func TestSmartTCPDispatch(t *testing.T) {
	t.Run("cleartext HTTP request reaches HTTP handler", func(t *testing.T) {
		manager, graph, hits := startSmartTCP(t, nil, nil)
		defer shutdownManager(t, manager)

		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		_, err := conn.Write([]byte("GET /clear HTTP/1.1\r\nHost: app.example.com\r\nConnection: close\r\n\r\n"))
		require.NoError(t, err)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		assert.Contains(t, string(body), "http:/clear")
		assert.Equal(t, int64(1), hits.httpHits.Load())
		assert.Equal(t, int64(0), hits.rawHits.Load())
	})

	t.Run("h2c prior-knowledge reaches HTTP handler", func(t *testing.T) {
		manager, graph, hits := startSmartTCP(t, nil, nil)
		defer shutdownManager(t, manager)
		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		_, err := conn.Write([]byte(h2cPreface))
		require.NoError(t, err)
		assert.Eventually(t, func() bool { return hits.httpHits.Load() == 1 }, time.Second, 10*time.Millisecond)
		assert.Equal(t, int64(0), hits.rawHits.Load())
	})

	t.Run("HTTPS request reaches configured HTTPS fallback", func(t *testing.T) {
		manager, graph, _ := startSmartTCP(t, nil, nil)
		defer shutdownManager(t, manager)
		assertHTTPSBody(t, graph.EntryPoints[0].Address, "app.example.com", "smart https")
	})

	t.Run("TLS passthrough SNI match wins before HTTPS fallback", func(t *testing.T) {
		backend := startTLSGreetingServer(t, "raw.example.com", "raw tls\n")
		manager, graph, _ := startSmartTCP(t, &smartTCPRoute{tlsSNI: "raw.example.com", backendAddress: backend.address}, nil)
		defer shutdownManager(t, manager)
		assertTLSGreeting(t, graph.EntryPoints[0].Address, "raw.example.com", "raw tls\n")
		assertHTTPSBody(t, graph.EntryPoints[0].Address, "app.example.com", "smart https")
	})

	t.Run("no-SNI TLS reaches HTTPS fallback", func(t *testing.T) {
		manager, graph, _ := startSmartTCP(t, nil, nil)
		defer shutdownManager(t, manager)
		assertHTTPSBodyNoSNI(t, graph.EntryPoints[0].Address, "smart https")
	})

	t.Run("unknown bytes reach raw fallback immediately when source policy allows", func(t *testing.T) {
		backend := startTCPEchoServer(t, 0)
		manager, graph, _ := startSmartTCP(t, nil, &smartTCPRoute{backendAddress: backend.address, trustedCIDRs: []string{"127.0.0.0/8"}})
		defer shutdownManager(t, manager)
		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		assertRoundTrip(t, conn, "ssh-raw")
	})

	t.Run("unknown bytes refused without raw fallback", func(t *testing.T) {
		manager, graph, _ := startSmartTCP(t, nil, nil)
		defer shutdownManager(t, manager)
		assertTCPRejected(t, manager, graph.EntryPoints[0].Address, 1)
	})

	t.Run("unknown bytes refused when raw fallback source policy denies peer", func(t *testing.T) {
		backend := startTCPEchoServer(t, 0)
		manager, graph, _ := startSmartTCP(t, nil, &smartTCPRoute{backendAddress: backend.address, trustedCIDRs: []string{"192.0.2.0/24"}})
		defer shutdownManager(t, manager)
		assertTCPRejected(t, manager, graph.EntryPoints[0].Address, 1)
	})

	t.Run("malformed HTTP-looking bytes do not reach raw fallback", func(t *testing.T) {
		backend := startTCPEchoServer(t, 0)
		manager, graph, _ := startSmartTCP(t, nil, &smartTCPRoute{backendAddress: backend.address, allowPublic: true})
		defer shutdownManager(t, manager)
		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		_, err := conn.Write([]byte("GET /broken\r\n"))
		require.NoError(t, err)
		_, err = bufio.NewReader(conn).ReadByte()
		require.Error(t, err)
	})

	t.Run("malformed TLS-looking bytes do not reach raw fallback", func(t *testing.T) {
		backend := startTCPEchoServer(t, 0)
		manager, graph, _ := startSmartTCP(t, nil, &smartTCPRoute{backendAddress: backend.address, allowPublic: true})
		defer shutdownManager(t, manager)
		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		_, err := conn.Write([]byte{22, 3, 3, 0, 8, 2, 0, 0, 4, 0, 0, 0, 0})
		require.NoError(t, err)
		_, err = bufio.NewReader(conn).ReadByte()
		require.Error(t, err)
	})

	t.Run("PROXY v1 and v2 prefixes rejected", func(t *testing.T) {
		manager, graph, _ := startSmartTCP(t, nil, nil)
		defer shutdownManager(t, manager)
		assertSmartTCPWriteRejected(t, graph.EntryPoints[0].Address, []byte("PROXY TCP4 127.0.0.1 127.0.0.1 1 2\r\n"))
		assertSmartTCPWriteRejected(t, graph.EntryPoints[0].Address, append([]byte(proxyV2Signature), 0x21, 0x11, 0, 0))
	})

	t.Run("sniff deadline cleared before backend handoff", func(t *testing.T) {
		backend := startTCPEchoServer(t, 150*time.Millisecond)
		manager, graph, _ := startSmartTCP(t, nil, &smartTCPRoute{backendAddress: backend.address, allowPublic: true})
		defer shutdownManager(t, manager)
		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		assertRoundTrip(t, conn, "deadline-clear")
	})

	t.Run("raw fallback trusted CIDR reload reuses same listener", func(t *testing.T) {
		backend := startTCPEchoServer(t, 0)
		manager, graph, _ := startSmartTCP(t, nil, &smartTCPRoute{backendAddress: backend.address, trustedCIDRs: []string{"192.0.2.0/24"}})
		defer shutdownManager(t, manager)
		assertTCPRejected(t, manager, graph.EntryPoints[0].Address, 1)

		initialRuntime := tcpRuntimeForTest(t, manager, "edge")
		replacement := graph
		replacement.EntryPoints = append([]domain.EntryPoint(nil), graph.EntryPoints...)
		replacement.EntryPoints[0].RawFallbackTrustedCIDRs = []string{"127.0.0.0/8"}
		replacement.Routers = append([]domain.TrafficRouter(nil), graph.Routers...)
		replacement.Services = append([]domain.TrafficService(nil), graph.Services...)
		require.NoError(t, manager.Apply(context.Background(), &replacement))
		assert.Same(t, initialRuntime, tcpRuntimeForTest(t, manager, "edge"))

		conn := dialTCP(t, replacement.EntryPoints[0].Address)
		defer conn.Close()
		assertRoundTrip(t, conn, "raw-reloaded")
	})

	t.Run("raw fallback connection closes when trusted CIDR no longer allows peer", func(t *testing.T) {
		backend := startTCPEchoServer(t, 0)
		manager, graph, _ := startSmartTCP(t, nil, &smartTCPRoute{backendAddress: backend.address, trustedCIDRs: []string{"127.0.0.0/8"}})
		defer shutdownManager(t, manager)

		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		assertRoundTrip(t, conn, "before")

		updated := graph
		updated.EntryPoints = append([]domain.EntryPoint(nil), graph.EntryPoints...)
		updated.EntryPoints[0].RawFallbackTrustedCIDRs = []string{"192.0.2.0/24"}
		require.NoError(t, manager.Apply(context.Background(), &updated))

		assert.Eventually(t, func() bool {
			_, writeErr := conn.Write([]byte("after\n"))
			if writeErr != nil {
				return true
			}
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
			_, readErr := bufio.NewReader(conn).ReadByte()
			return readErr != nil
		}, time.Second, 10*time.Millisecond)
	})

	t.Run("TLS ClientHello read after sniff is bounded", func(t *testing.T) {
		manager, graph, _ := startSmartTCP(t, nil, nil)
		defer shutdownManager(t, manager)
		graph.Options.TCP.DialTimeout = 100 * time.Millisecond
		require.NoError(t, manager.Apply(context.Background(), &graph))

		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		start := time.Now()
		_, err := conn.Write([]byte{22, 3, 3, 0, 42})
		require.NoError(t, err)
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(750*time.Millisecond)))
		_, err = bufio.NewReader(conn).ReadByte()
		require.Error(t, err)
		assert.Less(t, time.Since(start), 500*time.Millisecond)
	})
}

func TestSetSmartTCPHTTPServerCopiesProtocols(t *testing.T) {
	manager := NewManager()
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	manager.SetSmartTCPHTTPServer("edge", http.NotFoundHandler(), protocols)

	protocols.SetHTTP1(false)
	protocols.SetUnencryptedHTTP2(true)

	config, ok := manager.smartHTTPServer("edge")
	require.True(t, ok)
	assert.True(t, config.Protocols.HTTP1())
	assert.False(t, config.Protocols.UnencryptedHTTP2())
}

func TestSmartTCPStatusCounters(t *testing.T) {
	t.Run("HTTP accepted", func(t *testing.T) {
		manager, graph, _ := startSmartTCP(t, nil, nil)
		defer shutdownManager(t, manager)
		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		_, err := conn.Write([]byte("GET /status HTTP/1.1\r\nHost: app.example.com\r\nConnection: close\r\n\r\n"))
		require.NoError(t, err)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err)
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		assertSmartTCPStatus(t, manager, func(c domain.SmartTCPCounters) bool { return c.HTTPAccepted == 1 })
	})

	t.Run("h2c accepted", func(t *testing.T) {
		manager, graph, _ := startSmartTCP(t, nil, nil)
		defer shutdownManager(t, manager)
		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		_, err := conn.Write([]byte(h2cPreface))
		require.NoError(t, err)
		assertSmartTCPStatus(t, manager, func(c domain.SmartTCPCounters) bool { return c.H2CAccepted == 1 })
	})

	t.Run("HTTPS fallback accepted", func(t *testing.T) {
		manager, graph, _ := startSmartTCP(t, nil, nil)
		defer shutdownManager(t, manager)
		assertHTTPSBody(t, graph.EntryPoints[0].Address, "app.example.com", "smart https")
		assertSmartTCPStatus(t, manager, func(c domain.SmartTCPCounters) bool { return c.HTTPSFallbackAccepted == 1 })
	})

	t.Run("TLS passthrough accepted", func(t *testing.T) {
		backend := startTLSGreetingServer(t, "raw.example.com", "raw tls\n")
		manager, graph, _ := startSmartTCP(t, &smartTCPRoute{tlsSNI: "raw.example.com", backendAddress: backend.address}, nil)
		defer shutdownManager(t, manager)
		assertTLSGreeting(t, graph.EntryPoints[0].Address, "raw.example.com", "raw tls\n")
		assertSmartTCPStatus(t, manager, func(c domain.SmartTCPCounters) bool { return c.TLSPassthroughAccepted == 1 })
	})

	t.Run("raw fallback accepted", func(t *testing.T) {
		backend := startTCPEchoServer(t, 0)
		manager, graph, _ := startSmartTCP(t, nil, &smartTCPRoute{backendAddress: backend.address, allowPublic: true})
		defer shutdownManager(t, manager)
		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		assertRoundTrip(t, conn, "raw")
		assertSmartTCPStatus(t, manager, func(c domain.SmartTCPCounters) bool { return c.RawFallbackAccepted == 1 })
	})

	t.Run("raw fallback backend dial failure is not accepted", func(t *testing.T) {
		manager, graph, _ := startSmartTCP(t, nil, &smartTCPRoute{backendAddress: freeTCPAddress(t), allowPublic: true})
		defer shutdownManager(t, manager)
		assertSmartTCPWriteRejected(t, graph.EntryPoints[0].Address, []byte{0})
		assertSmartTCPStatus(t, manager, func(c domain.SmartTCPCounters) bool { return c.RawFallbackAccepted == 0 })
		assert.Eventually(t, func() bool {
			return manager.Status().Counters.TotalAccepted == 0 && manager.Status().Counters.TotalErrors == 1
		}, time.Second, 10*time.Millisecond)
	})

	t.Run("refused and rejected classes", func(t *testing.T) {
		backend := startTCPEchoServer(t, 0)
		manager, graph, _ := startSmartTCP(t, nil, &smartTCPRoute{backendAddress: backend.address, trustedCIDRs: []string{"192.0.2.0/24"}})
		defer shutdownManager(t, manager)
		assertSmartTCPWriteRejected(t, graph.EntryPoints[0].Address, []byte("PROXY TCP4 127.0.0.1 127.0.0.1 1 2\r\n"))
		assertSmartTCPWriteRejected(t, graph.EntryPoints[0].Address, []byte{0})
		assertSmartTCPWriteRejected(t, graph.EntryPoints[0].Address, []byte("GET /broken\r\n"))
		assertSmartTCPWriteRejected(t, graph.EntryPoints[0].Address, bytes.Repeat([]byte("G"), maxSmartTCPSniffBytes+1))
		assertSmartTCPStatus(t, manager, func(c domain.SmartTCPCounters) bool {
			return c.PROXYRefused == 1 && c.RawFallbackCIDRRefused == 1 && c.MalformedRejected == 1 && c.ClientHelloTooLarge == 1
		})
	})

	t.Run("unknown without fallback refused", func(t *testing.T) {
		manager, graph, _ := startSmartTCP(t, nil, nil)
		defer shutdownManager(t, manager)
		assertSmartTCPWriteRejected(t, graph.EntryPoints[0].Address, []byte{0})
		assertSmartTCPStatus(t, manager, func(c domain.SmartTCPCounters) bool { return c.UnknownNoFallbackRefused == 1 })
	})

	t.Run("sniff timeout", func(t *testing.T) {
		manager, graph, _ := startSmartTCP(t, nil, nil)
		defer shutdownManager(t, manager)
		graph.Options.TCP.DialTimeout = 50 * time.Millisecond
		require.NoError(t, manager.Apply(context.Background(), &graph))
		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		_, err := conn.Write([]byte("P"))
		require.NoError(t, err)
		_, _ = bufio.NewReader(conn).ReadByte()
		assertSmartTCPStatus(t, manager, func(c domain.SmartTCPCounters) bool { return c.SniffTimeout == 1 })
	})

	t.Run("entrypoint CIDR refused", func(t *testing.T) {
		manager, graph, _ := startSmartTCP(t, nil, nil)
		defer shutdownManager(t, manager)
		graph.EntryPoints[0].TrustedCIDRs = []string{"192.0.2.0/24"}
		require.NoError(t, manager.Apply(context.Background(), &graph))
		conn := dialTCP(t, graph.EntryPoints[0].Address)
		_ = conn.Close()
		assertSmartTCPStatus(t, manager, func(c domain.SmartTCPCounters) bool { return c.EntrypointCIDRRefused == 1 })
	})
}

func TestManagerStatusAggregatesSmartTCP(t *testing.T) {
	manager, graph, _ := startSmartTCP(t, nil, nil)
	defer shutdownManager(t, manager)
	conn := dialTCP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	_, err := conn.Write([]byte("GET /aggregate HTTP/1.1\r\nHost: app.example.com\r\nConnection: close\r\n\r\n"))
	require.NoError(t, err)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assertSmartTCPStatus(t, manager, func(c domain.SmartTCPCounters) bool { return c.HTTPAccepted == 1 })
}

func assertSmartTCPStatus(t *testing.T, manager *Manager, match func(domain.SmartTCPCounters) bool) {
	t.Helper()
	assert.Eventually(t, func() bool {
		status := manager.Status()
		if len(status.EntryPoints) == 0 || !match(status.EntryPoints[0].SmartTCP) {
			return false
		}
		return match(status.Counters.SmartTCP)
	}, time.Second, 10*time.Millisecond)
}

func TestSmartTCPLifecycleHardening(t *testing.T) {
	t.Run("internal HTTP and HTTPS fallback servers stop when entrypoint removed", func(t *testing.T) {
		manager, graph, _ := startSmartTCP(t, nil, nil)
		initialRuntime := tcpRuntimeForTest(t, manager, "edge")
		require.NotNil(t, initialRuntime)

		require.NoError(t, manager.Apply(context.Background(), &domain.TrafficGraph{Options: graph.Options}))
		assert.Eventually(t, func() bool {
			initialRuntime.mu.Lock()
			defer initialRuntime.mu.Unlock()
			return initialRuntime.smartHTTPServer == nil && initialRuntime.smartTLSServer == nil
		}, time.Second, 10*time.Millisecond)
		shutdownManager(t, manager)
	})

	t.Run("HTTP fallback handoff remains counted until hijacked connection closes", func(t *testing.T) {
		manager := NewManager()
		graph := smartTCPGraph(t, freeTCPAddress(t), nil, nil)
		graph.Options.TCP.MaxConnections = 1
		hijacked := make(chan struct{})
		hijackedClosed := &atomic.Bool{}
		manager.SetSmartTCPHTTPServer("edge", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hj, ok := w.(http.Hijacker)
			require.True(t, ok)
			conn, rw, err := hj.Hijack()
			require.NoError(t, err)
			defer conn.Close()
			_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\nready\n")
			_ = rw.Flush()
			if hijackedClosed.CompareAndSwap(false, true) {
				close(hijacked)
			}
			_, _ = rw.ReadString('\n')
		}), nil)
		manager.SetSmartTCPTLSServer("edge", http.NotFoundHandler(), testTLSConfig(t, "app.example.com"))
		require.NoError(t, manager.Apply(context.Background(), &graph))
		defer shutdownManager(t, manager)

		first := dialTCP(t, graph.EntryPoints[0].Address)
		defer first.Close()
		firstReader := bufio.NewReader(first)
		_, err := first.Write([]byte("GET /ws HTTP/1.1\r\nHost: app.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"))
		require.NoError(t, err)
		resp, err := http.ReadResponse(firstReader, nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
		ready, err := firstReader.ReadString('\n')
		require.NoError(t, err)
		assert.Equal(t, "ready\n", ready)
		<-hijacked
		require.Eventually(t, func() bool { return manager.Status().Counters.ActiveTCPConnections == 1 }, time.Second, 10*time.Millisecond)

		second := dialTCP(t, graph.EntryPoints[0].Address)
		_, err = second.Write([]byte("GET / HTTP/1.1\r\nHost: app.example.com\r\nConnection: close\r\n\r\n"))
		require.NoError(t, err)
		_, err = bufio.NewReader(second).ReadByte()
		_ = second.Close()
		require.Error(t, err)
		assert.Eventually(t, func() bool { return manager.Status().Counters.TotalRefused == 1 }, time.Second, 10*time.Millisecond)

		_, err = first.Write([]byte("close\n"))
		require.NoError(t, err)
		require.Eventually(t, func() bool { return manager.Status().Counters.ActiveTCPConnections == 0 }, time.Second, 10*time.Millisecond)
		third := dialTCP(t, graph.EntryPoints[0].Address)
		defer third.Close()
		_, err = third.Write([]byte("GET /after HTTP/1.1\r\nHost: app.example.com\r\nConnection: close\r\n\r\n"))
		require.NoError(t, err)
		resp, err = http.ReadResponse(bufio.NewReader(third), nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	})

	t.Run("shutdown force drain closes open HTTP fallback handoff", func(t *testing.T) {
		manager := NewManager()
		graph := smartTCPGraph(t, freeTCPAddress(t), nil, nil)
		graph.Options.TCP.DrainTimeout = 50 * time.Millisecond
		manager.SetSmartTCPHTTPServer("edge", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hj, ok := w.(http.Hijacker)
			require.True(t, ok)
			conn, rw, err := hj.Hijack()
			require.NoError(t, err)
			defer conn.Close()
			_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\nready\n")
			_ = rw.Flush()
			_, _ = rw.ReadString('\n')
		}), nil)
		manager.SetSmartTCPTLSServer("edge", http.NotFoundHandler(), testTLSConfig(t, "app.example.com"))
		require.NoError(t, manager.Apply(context.Background(), &graph))

		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		reader := bufio.NewReader(conn)
		_, err := conn.Write([]byte("GET /ws HTTP/1.1\r\nHost: app.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"))
		require.NoError(t, err)
		resp, err := http.ReadResponse(reader, nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
		ready, err := reader.ReadString('\n')
		require.NoError(t, err)
		assert.Equal(t, "ready\n", ready)

		started := time.Now()
		require.NoError(t, manager.Apply(context.Background(), &domain.TrafficGraph{Options: graph.Options}))
		assert.GreaterOrEqual(t, time.Since(started), 40*time.Millisecond)
		_, err = conn.Write([]byte("after\n"))
		if err == nil {
			_, err = reader.ReadByte()
		}
		require.Error(t, err)
	})

	t.Run("trusted CIDR change closes active smart TCP connections no longer allowed", func(t *testing.T) {
		backend := startTCPEchoServer(t, 0)
		manager, graph, _ := startSmartTCP(t, nil, &smartTCPRoute{backendAddress: backend.address, allowPublic: true})
		defer shutdownManager(t, manager)
		graph.EntryPoints[0].TrustedCIDRs = []string{"127.0.0.0/8"}
		require.NoError(t, manager.Apply(context.Background(), &graph))

		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		assertRoundTrip(t, conn, "before")

		updated := graph
		updated.EntryPoints[0].TrustedCIDRs = []string{"192.0.2.0/24"}
		require.NoError(t, manager.Apply(context.Background(), &updated))
		assert.Eventually(t, func() bool {
			_, writeErr := conn.Write([]byte("after\n"))
			if writeErr != nil {
				return true
			}
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
			_, readErr := bufio.NewReader(conn).ReadByte()
			return readErr != nil
		}, time.Second, 10*time.Millisecond)
	})

	t.Run("active upgraded connection remains tied to accepted generation", func(t *testing.T) {
		manager := NewManager()
		graph := smartTCPGraph(t, freeTCPAddress(t), nil, nil)
		manager.SetSmartTCPHTTPServer("edge", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hj, ok := w.(http.Hijacker)
			require.True(t, ok)
			conn, rw, err := hj.Hijack()
			require.NoError(t, err)
			_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\nold-ready\n")
			_ = rw.Flush()
			line, err := rw.ReadString('\n')
			if err == nil {
				_, _ = conn.Write([]byte("old:" + line))
			}
			_ = conn.Close()
		}), nil)
		manager.SetSmartTCPTLSServer("edge", http.NotFoundHandler(), testTLSConfig(t, "app.example.com"))
		require.NoError(t, manager.Apply(context.Background(), &graph))
		defer shutdownManager(t, manager)

		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		reader := bufio.NewReader(conn)
		_, err := conn.Write([]byte("GET /ws HTTP/1.1\r\nHost: app.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"))
		require.NoError(t, err)
		resp, err := http.ReadResponse(reader, nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
		ready, err := reader.ReadString('\n')
		require.NoError(t, err)
		assert.Equal(t, "old-ready\n", ready)

		manager.SetSmartTCPHTTPServer("edge", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("new")) }), nil)
		_, err = conn.Write([]byte("ping\n"))
		require.NoError(t, err)
		got, err := reader.ReadString('\n')
		require.NoError(t, err)
		assert.Equal(t, "old:ping\n", got)
	})

	t.Run("listener is not reused for incompatible address", func(t *testing.T) {
		manager, graph, _ := startSmartTCP(t, nil, nil)
		defer shutdownManager(t, manager)
		initialRuntime := tcpRuntimeForTest(t, manager, "edge")

		updated := graph
		updated.EntryPoints[0].Address = freeTCPAddress(t)
		require.NoError(t, manager.Apply(context.Background(), &updated))
		assert.NotSame(t, initialRuntime, tcpRuntimeForTest(t, manager, "edge"))
	})
}

func TestSmartTCPHTTPHandoff(t *testing.T) {
	t.Run("HTTP Upgrade h2c reaches handler and not raw fallback", func(t *testing.T) {
		manager, graph, hits := startSmartTCP(t, nil, &smartTCPRoute{backendAddress: startTCPEchoServer(t, 0).address, allowPublic: true})
		defer shutdownManager(t, manager)
		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		_, err := conn.Write([]byte("GET /upgrade HTTP/1.1\r\nHost: app.example.com\r\nConnection: Upgrade, HTTP2-Settings\r\nUpgrade: h2c\r\nHTTP2-Settings: AAMAAABkAAQAAP__\r\n\r\n"))
		require.NoError(t, err)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err)
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		assert.Equal(t, int64(1), hits.httpHits.Load())
		assert.Equal(t, int64(0), hits.rawHits.Load())
	})

	t.Run("websocket upgrade supports hijacker", func(t *testing.T) {
		manager := NewManager()
		graph := smartTCPGraph(t, freeTCPAddress(t), nil, nil)
		manager.SetSmartTCPHTTPServer("edge", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hj, ok := w.(http.Hijacker)
			require.True(t, ok)
			conn, rw, err := hj.Hijack()
			require.NoError(t, err)
			defer conn.Close()
			_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\nws-ok")
			_ = rw.Flush()
		}), nil)
		manager.SetSmartTCPTLSServer("edge", http.NotFoundHandler(), testTLSConfig(t, "app.example.com"))
		require.NoError(t, manager.Apply(context.Background(), &graph))
		defer shutdownManager(t, manager)
		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		_, err := conn.Write([]byte("GET /ws HTTP/1.1\r\nHost: app.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"))
		require.NoError(t, err)
		body, err := io.ReadAll(conn)
		require.NoError(t, err)
		assert.Contains(t, string(body), "ws-ok")
	})

	t.Run("CONNECT reaches HTTP handler and not raw fallback", func(t *testing.T) {
		manager, graph, hits := startSmartTCP(t, nil, &smartTCPRoute{backendAddress: startTCPEchoServer(t, 0).address, allowPublic: true})
		defer shutdownManager(t, manager)
		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		_, err := conn.Write([]byte("CONNECT db.example.com:443 HTTP/1.1\r\nHost: db.example.com:443\r\nConnection: close\r\n\r\n"))
		require.NoError(t, err)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err)
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		assert.Equal(t, int64(1), hits.httpHits.Load())
		assert.Equal(t, int64(0), hits.rawHits.Load())
	})

	t.Run("handoff connection has cleared deadlines and useful addresses", func(t *testing.T) {
		seen := make(chan string, 1)
		manager := NewManager()
		graph := smartTCPGraph(t, freeTCPAddress(t), nil, nil)
		manager.SetSmartTCPHTTPServer("edge", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(150 * time.Millisecond)
			seen <- r.RemoteAddr + "|" + r.Context().Value(http.LocalAddrContextKey).(net.Addr).String()
			_, _ = w.Write([]byte("addr-ok"))
		}), nil)
		manager.SetSmartTCPTLSServer("edge", http.NotFoundHandler(), testTLSConfig(t, "app.example.com"))
		require.NoError(t, manager.Apply(context.Background(), &graph))
		defer shutdownManager(t, manager)
		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		_, err := conn.Write([]byte("GET /addr HTTP/1.1\r\nHost: app.example.com\r\nConnection: close\r\n\r\n"))
		require.NoError(t, err)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err)
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		got := <-seen
		assert.Contains(t, got, "127.0.0.1")
		assert.Contains(t, got, graph.EntryPoints[0].Address)
	})
}

func TestRouteToHTTPListenerServeFailureReturnsFalseAndLeavesTrackedForCaller(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	runtime := newEntryPointRuntime(context.Background(), NewManager(), domain.EntryPoint{Name: "edge"}, listener, nil, nil)
	httpListener := newTLSHTTPListener(listener.Addr())
	require.NoError(t, httpListener.Close())

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	tracked := &trackedTCPConn{client: clientConn}
	runtime.track(tracked)

	accepted := runtime.routeToHTTPListener(tracked, serverConn, httpListener)
	require.False(t, accepted)

	runtime.mu.Lock()
	_, stillTracked := runtime.activeConns[tracked]
	runtime.mu.Unlock()
	assert.True(t, stillTracked)

	runtime.untrack(tracked)
}

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

func TestTCPManagerReplacesSameAddressEntrypoint(t *testing.T) {
	backend := startTCPEchoServer(t, 0)
	graph := tcpGraph(t, freeTCPAddress(t), backend.address)
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	replacement := graph
	replacement.EntryPoints = []domain.EntryPoint{{Name: "postgres-tls", Address: graph.EntryPoints[0].Address, Protocol: domain.EntryPointProtocolTLSMux}}
	replacement.Routers = nil
	replacement.Services = nil
	require.NoError(t, manager.Apply(context.Background(), &replacement))

	status := manager.Status()
	require.Len(t, status.EntryPoints, 1)
	assert.Equal(t, "postgres-tls", status.EntryPoints[0].Name)
	assert.Equal(t, domain.EntryPointProtocolTLSMux, status.EntryPoints[0].Protocol)
}

func TestTCPManagerSameAddressProtocolChangeStartsTLSHTTPServer(t *testing.T) {
	backend := startTCPEchoServer(t, 0)
	graph := tcpGraph(t, freeTCPAddress(t), backend.address)
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	manager.SetTLSHTTPServer("websecure", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secure"))
	}), testTLSConfig(t, "app.example.com"))
	replacement := domain.TrafficGraph{
		Options:     graph.Options,
		EntryPoints: []domain.EntryPoint{{Name: "websecure", Address: graph.EntryPoints[0].Address, Protocol: domain.EntryPointProtocolTLSMux}},
	}
	require.NoError(t, manager.Apply(context.Background(), &replacement))

	assertHTTPSBody(t, graph.EntryPoints[0].Address, "app.example.com", "secure")
}

func TestTCPManagerSameAddressTLSMuxRenameRefreshesHTTPSRoute(t *testing.T) {
	address := freeTCPAddress(t)
	manager := NewManager()
	manager.SetTLSHTTPServer("old-secure", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("old"))
	}), testTLSConfig(t, "app.example.com"))
	manager.SetTLSHTTPServer("new-secure", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("new"))
	}), testTLSConfig(t, "app.example.com"))
	graph := domain.TrafficGraph{
		Options:     domain.TrafficOptions{TCP: domain.TCPOptions{DialTimeout: time.Second, IdleTimeout: time.Minute, DrainTimeout: time.Second}},
		EntryPoints: []domain.EntryPoint{{Name: "old-secure", Address: address, Protocol: domain.EntryPointProtocolTLSMux}},
	}
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)
	assertHTTPSBody(t, address, "app.example.com", "old")

	renamed := graph
	renamed.EntryPoints[0].Name = "new-secure"
	require.NoError(t, manager.Apply(context.Background(), &renamed))

	assertHTTPSBody(t, address, "app.example.com", "new")
}

func TestTCPRuntimeDoesNotPublishTLSHTTPServerAfterClose(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	manager := NewManager()
	runtime := newEntryPointRuntime(context.Background(), manager, domain.EntryPoint{Name: "websecure", Address: listener.Addr().String(), Protocol: domain.EntryPointProtocolTLSMux}, listener, nil, nil)
	runtime.closed.Store(true)

	runtime.replaceTLSHTTPServer(runtime.entryPointSnapshot(), TLSHTTPServerConfig{
		Handler:   http.NotFoundHandler(),
		TLSConfig: testTLSConfig(t, "app.example.com"),
	})

	runtime.mu.Lock()
	server := runtime.tlsHTTPServer
	runtime.mu.Unlock()
	require.Nil(t, server)
}

func TestTCPManagerSameAddressProtocolChangeStopsTLSHTTPServer(t *testing.T) {
	backend := startTCPEchoServer(t, 0)
	graph := domain.TrafficGraph{
		Options:     domain.TrafficOptions{TCP: domain.TCPOptions{DialTimeout: time.Second, IdleTimeout: time.Minute, DrainTimeout: time.Second}},
		EntryPoints: []domain.EntryPoint{{Name: "websecure", Address: freeTCPAddress(t), Protocol: domain.EntryPointProtocolTLSMux}},
	}
	manager := NewManager()
	manager.SetTLSHTTPServer("websecure", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secure"))
	}), testTLSConfig(t, "app.example.com"))
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)
	assertHTTPSBody(t, graph.EntryPoints[0].Address, "app.example.com", "secure")

	replacement := tcpGraph(t, graph.EntryPoints[0].Address, backend.address)
	require.NoError(t, manager.Apply(context.Background(), &replacement))

	_, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", graph.EntryPoints[0].Address, &tls.Config{ServerName: "app.example.com", InsecureSkipVerify: true})
	require.Error(t, err)
}

func TestTrackedTCPConnClosesLateBackendAfterStaleClose(t *testing.T) {
	clientA, clientB := net.Pipe()
	backendA, backendB := net.Pipe()
	defer clientA.Close()
	defer backendB.Close()

	conn := &trackedTCPConn{client: clientB}
	conn.close()
	conn.setBackend(backendA)

	_, err := backendB.Write([]byte("late backend"))
	if err == nil {
		_ = backendA.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		_, err = backendA.Read(make([]byte, 1))
	}
	require.Error(t, err)
}

func TestTCPPassthroughRejectsUntrustedCIDR(t *testing.T) {
	backend := startTCPEchoServer(t, 0)
	graph := tcpGraph(t, freeTCPAddress(t), backend.address)
	graph.EntryPoints[0].TrustedCIDRs = []string{"192.0.2.0/24"}
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	assertTCPRejected(t, manager, graph.EntryPoints[0].Address, 1)
}

func TestTCPPassthroughReloadAppliesTrustedCIDRChange(t *testing.T) {
	backend := startTCPEchoServer(t, 0)
	graph := tcpGraph(t, freeTCPAddress(t), backend.address)
	graph.EntryPoints[0].TrustedCIDRs = []string{"127.0.0.0/8"}
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	conn := dialTCP(t, graph.EntryPoints[0].Address)
	assertRoundTrip(t, conn, "allowed")
	require.NoError(t, conn.Close())

	updated := graph
	updated.EntryPoints[0].TrustedCIDRs = []string{"192.0.2.0/24"}
	require.NoError(t, manager.Apply(context.Background(), &updated))
	assertTCPRejected(t, manager, graph.EntryPoints[0].Address, 1)
}

func TestTLSMuxReloadClosesActiveUntrustedHTTPSFallbackConnections(t *testing.T) {
	graph := domain.TrafficGraph{
		Options:     domain.TrafficOptions{TCP: domain.TCPOptions{DialTimeout: time.Second, IdleTimeout: time.Minute, DrainTimeout: time.Second}},
		EntryPoints: []domain.EntryPoint{{Name: "websecure", Address: freeTCPAddress(t), Protocol: domain.EntryPointProtocolTLSMux, TrustedCIDRs: []string{"127.0.0.0/8"}}},
	}
	manager := NewManager()
	manager.SetTLSHTTPServer("websecure", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := "secure"
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		_, _ = w.Write([]byte(body))
	}), testTLSConfig(t, "app.example.com"))
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", graph.EntryPoints[0].Address, &tls.Config{ServerName: "app.example.com", InsecureSkipVerify: true})
	require.NoError(t, err)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: app.example.com\r\nConnection: keep-alive\r\n\r\n"))
	require.NoError(t, err)
	resp, err := http.ReadResponse(reader, nil)
	require.NoError(t, err)
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	updated := graph
	updated.EntryPoints[0].TrustedCIDRs = []string{"192.0.2.0/24"}
	require.NoError(t, manager.Apply(context.Background(), &updated))

	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: app.example.com\r\nConnection: close\r\n\r\n"))
	if err == nil {
		_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, err = http.ReadResponse(reader, nil)
	}
	require.Error(t, err)
}

func TestTCPPassthroughReloadClosesActiveUntrustedConnections(t *testing.T) {
	backend := startTCPEchoServer(t, 0)
	graph := tcpGraph(t, freeTCPAddress(t), backend.address)
	graph.EntryPoints[0].TrustedCIDRs = []string{"127.0.0.0/8"}
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	conn := dialTCP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	assertRoundTrip(t, conn, "before")

	updated := graph
	updated.EntryPoints[0].TrustedCIDRs = []string{"192.0.2.0/24"}
	require.NoError(t, manager.Apply(context.Background(), &updated))

	assert.Eventually(t, func() bool {
		_, writeErr := conn.Write([]byte("after"))
		if writeErr != nil {
			return true
		}
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
		_, readErr := bufio.NewReader(conn).ReadByte()
		return readErr != nil
	}, time.Second, 10*time.Millisecond)
}

func assertTCPRejected(t *testing.T, manager *Manager, address string, refused int64) {
	t.Helper()
	conn := dialTCP(t, address)
	defer conn.Close()
	_, writeErr := conn.Write([]byte("blocked"))
	if writeErr == nil {
		_, readErr := bufio.NewReader(conn).ReadByte()
		require.Error(t, readErr)
	}
	assert.Eventually(t, func() bool { return manager.Status().Counters.TotalRefused == refused }, time.Second, 10*time.Millisecond)
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
	_, writeErr := conn.Write([]byte("hello"))
	if writeErr == nil {
		_, readErr := bufio.NewReader(conn).ReadByte()
		require.Error(t, readErr)
	}
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
	_, writeErr := conn.Write([]byte("hello"))
	if writeErr == nil {
		_, readErr := bufio.NewReader(conn).ReadByte()
		require.Error(t, readErr)
	}
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
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(250*time.Millisecond)))
	started := time.Now()
	_, err := bufio.NewReader(conn).ReadByte()
	require.Error(t, err)
	var netErr net.Error
	require.False(t, errors.As(err, &netErr) && netErr.Timeout(), "read hit the client deadline instead of the proxy idle timeout")
	assert.Less(t, time.Since(started), 250*time.Millisecond)
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

type smartTCPRoute struct {
	tlsSNI         string
	backendAddress string
	trustedCIDRs   []string
	allowPublic    bool
}

type smartTCPHits struct {
	httpHits *atomic.Int64
	rawHits  *atomic.Int64
}

func startSmartTCP(t *testing.T, tlsRoute *smartTCPRoute, rawRoute *smartTCPRoute) (*Manager, domain.TrafficGraph, smartTCPHits) {
	t.Helper()
	manager := NewManager()
	graph := smartTCPGraph(t, freeTCPAddress(t), tlsRoute, rawRoute)
	httpHits := &atomic.Int64{}
	rawHits := &atomic.Int64{}
	manager.SetSmartTCPHTTPServer("edge", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpHits.Add(1)
		_, _ = w.Write([]byte("http:" + r.URL.Path))
	}), nil)
	manager.SetSmartTCPTLSServer("edge", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("smart https"))
	}), testTLSConfig(t, "app.example.com"))
	require.NoError(t, manager.Apply(context.Background(), &graph))
	return manager, graph, smartTCPHits{httpHits: httpHits, rawHits: rawHits}
}

func smartTCPGraph(t *testing.T, listenAddress string, tlsRoute *smartTCPRoute, rawRoute *smartTCPRoute) domain.TrafficGraph {
	t.Helper()
	entry := domain.EntryPoint{Name: "edge", Address: listenAddress, Protocol: domain.EntryPointProtocolSmartTCP}
	routers := []domain.TrafficRouter{{Name: "route:app", EntryPoint: "edge", Protocol: domain.RouterProtocolHTTP, Rule: domain.TrafficRule{Host: "app.example.com"}, Service: "route:app"}}
	services := []domain.TrafficService{{Name: "route:app"}}
	if tlsRoute != nil {
		backend, err := backendFromAddress("tls:raw", tlsRoute.backendAddress)
		require.NoError(t, err)
		routers = append(routers, domain.TrafficRouter{Name: "raw-tls", EntryPoint: "edge", Protocol: domain.RouterProtocolTLSPassthrough, Rule: domain.TrafficRule{SNI: tlsRoute.tlsSNI}, Service: "network_service:tls:raw"})
		services = append(services, domain.TrafficService{Name: "network_service:tls:raw", Backends: []domain.TrafficBackend{backend}})
	}
	if rawRoute != nil {
		backend, err := backendFromAddress("raw:tcp", rawRoute.backendAddress)
		require.NoError(t, err)
		entry.RawFallback = "raw-fallback"
		entry.RawFallbackTrustedCIDRs = rawRoute.trustedCIDRs
		entry.AllowPublicRawFallback = rawRoute.allowPublic
		routers = append(routers, domain.TrafficRouter{Name: "raw-fallback", EntryPoint: "edge", Protocol: domain.RouterProtocolTCP, Service: "network_service:raw:tcp"})
		services = append(services, domain.TrafficService{Name: "network_service:raw:tcp", Backends: []domain.TrafficBackend{backend}})
	}
	graph := domain.TrafficGraph{Options: domain.TrafficOptions{TCP: domain.TCPOptions{DialTimeout: time.Second, IdleTimeout: time.Minute, DrainTimeout: time.Second}}, EntryPoints: []domain.EntryPoint{entry}, Routers: routers, Services: services}
	require.NoError(t, graph.Validate())
	return graph
}

func assertSmartTCPWriteRejected(t *testing.T, address string, payload []byte) {
	t.Helper()
	conn := dialTCP(t, address)
	defer conn.Close()
	_, err := conn.Write(payload)
	require.NoError(t, err)
	_, err = bufio.NewReader(conn).ReadByte()
	require.Error(t, err)
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

func tcpRuntimeForTest(t *testing.T, manager *Manager, name string) *entryPointRuntime {
	t.Helper()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	runtime := manager.listeners[name]
	require.NotNil(t, runtime)
	return runtime
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
