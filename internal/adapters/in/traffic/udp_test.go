package traffic

import (
	"context"
	"net"
	"net/netip"
	"sync"
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

func TestUDPStaleRemovalKeepsReplacementSession(t *testing.T) {
	clientAddr := netip.MustParseAddrPort("127.0.0.1:4100")
	stale := newTestUDPSession(t, clientAddr)
	replacement := newTestUDPSession(t, clientAddr)
	runtime, _ := newTestUDPRuntime(t, freeUDPAddress(t))
	runtime.mu.Lock()
	runtime.sessions[clientAddr] = replacement
	runtime.counters.activeUDPSessions.Store(1)
	runtime.mu.Unlock()

	// A stale remover carries the pointer of the session that used to own the
	// client key. Expiry, backend read/write failures, reload and CIDR cleanup
	// all funnel through removeSession, so none of them may evict the session
	// that replaced it.
	runtime.removeSession(stale)

	runtime.mu.Lock()
	current, ok := runtime.sessions[clientAddr]
	runtime.mu.Unlock()
	require.True(t, ok, "replacement session must stay registered")
	require.Same(t, replacement, current)
	require.Equal(t, int64(1), runtime.counters.activeUDPSessions.Load(), "stale removal must not decrement the active counter")
	require.False(t, testUDPSessionClosed(replacement), "replacement backend must stay open")

	runtime.removeSession(replacement)

	runtime.mu.Lock()
	_, ok = runtime.sessions[clientAddr]
	runtime.mu.Unlock()
	require.False(t, ok, "removing the current session must unregister it")
	require.Equal(t, int64(0), runtime.counters.activeUDPSessions.Load(), "removing the current session must decrement once")
	require.True(t, testUDPSessionClosed(replacement))

	// Removing again must be idempotent: no double decrement and no panic.
	runtime.removeSession(replacement)
	require.Equal(t, int64(0), runtime.counters.activeUDPSessions.Load())
}

func TestUDPExpiryClosesOnlyRegisteredIdleSessionOnce(t *testing.T) {
	clientAddr := netip.MustParseAddrPort("127.0.0.1:4200")
	// stale was replaced under the same key and is no longer registered.
	stale := newTestUDPSession(t, clientAddr)
	stale.lastSeen.Store(time.Now().Add(-time.Hour).UnixNano())
	current := newTestUDPSession(t, clientAddr)
	runtime, _ := newTestUDPRuntime(t, freeUDPAddress(t))
	runtime.mu.Lock()
	runtime.sessions[clientAddr] = current
	runtime.counters.activeUDPSessions.Store(1)
	runtime.mu.Unlock()

	runtime.expireIdleSessions(time.Minute)
	require.False(t, testUDPSessionClosed(current), "fresh registered session must not expire")
	require.False(t, testUDPSessionClosed(stale), "an unregistered session must never be closed by expiry")
	require.Equal(t, int64(1), runtime.counters.activeUDPSessions.Load())

	current.lastSeen.Store(time.Now().Add(-time.Hour).UnixNano())
	runtime.expireIdleSessions(time.Minute)

	runtime.mu.Lock()
	_, ok := runtime.sessions[clientAddr]
	runtime.mu.Unlock()
	require.False(t, ok)
	require.True(t, testUDPSessionClosed(current))
	require.Equal(t, int64(0), runtime.counters.activeUDPSessions.Load(), "expiry must decrement exactly once")

	// The backend loop of the expired session also removes it on return; the
	// identity check keeps the counter coherent and must not double decrement.
	runtime.removeSession(current)
	require.Equal(t, int64(0), runtime.counters.activeUDPSessions.Load())
}

func TestUDPAdmissionGateBlocksPublicationAfterClosingBegins(t *testing.T) {
	backend := startUDPEchoServer(t)
	runtime, _ := newTestUDPRuntime(t, backend.address)
	// Model the instant stop() closes admission but before it cancels the
	// runtime context: a datagram worker that reaches session() now must not
	// publish a new session.
	runtime.mu.Lock()
	runtime.accepting = false
	runtime.mu.Unlock()
	runtime.closed.Store(true)

	session, ok := runtime.session(netip.MustParseAddrPort("127.0.0.1:4300"), effectiveUDPOptions(domain.UDPOptions{}))
	require.False(t, ok)
	require.Nil(t, session)

	runtime.mu.Lock()
	require.Empty(t, runtime.sessions)
	runtime.mu.Unlock()
	require.Equal(t, int64(0), runtime.counters.activeUDPSessions.Load())
	requireUDPRuntimeGoroutinesDone(t, runtime)
}

func TestUDPPublishSessionRejectsCompletedDialAfterAdmissionClosure(t *testing.T) {
	runtime, _ := newTestUDPRuntime(t, freeUDPAddress(t))
	clientAddr := netip.MustParseAddrPort("127.0.0.1:4800")
	dialed := newTestUDPSession(t, clientAddr)
	runtime.mu.Lock()
	runtime.accepting = false
	runtime.mu.Unlock()
	runtime.closed.Store(true)

	session, ok := runtime.publishSession(dialed, effectiveUDPOptions(domain.UDPOptions{}))
	require.False(t, ok)
	require.Nil(t, session)
	require.True(t, testUDPSessionClosed(dialed), "a rejected dial must have its backend closed")
	runtime.mu.Lock()
	require.Empty(t, runtime.sessions)
	runtime.mu.Unlock()
	require.Equal(t, int64(0), runtime.counters.activeUDPSessions.Load())
	require.Equal(t, int64(0), runtime.counters.totalAccepted.Load())
	requireUDPRuntimeGoroutinesDone(t, runtime)
}

func TestUDPPublishSessionReusesExistingAndTouchesIt(t *testing.T) {
	runtime, _ := newTestUDPRuntime(t, freeUDPAddress(t))
	clientAddr := netip.MustParseAddrPort("127.0.0.1:4900")
	existing := newTestUDPSession(t, clientAddr)
	existing.lastSeen.Store(time.Now().Add(-time.Hour).UnixNano())
	dialed := newTestUDPSession(t, clientAddr)
	runtime.mu.Lock()
	runtime.sessions[clientAddr] = existing
	runtime.counters.activeUDPSessions.Store(1)
	runtime.mu.Unlock()

	session, ok := runtime.publishSession(dialed, effectiveUDPOptions(domain.UDPOptions{}))
	require.True(t, ok)
	require.Same(t, existing, session)
	require.True(t, testUDPSessionClosed(dialed), "the losing dial must have its backend closed")
	require.False(t, testUDPSessionClosed(existing))
	require.Greater(t, existing.lastSeen.Load(), time.Now().Add(-time.Minute).UnixNano(), "reuse must touch the existing session")
	runtime.mu.Lock()
	current := runtime.sessions[clientAddr]
	runtime.mu.Unlock()
	require.Same(t, existing, current)
	require.Equal(t, int64(1), runtime.counters.activeUDPSessions.Load(), "reuse must not add a session")
	require.Equal(t, int64(0), runtime.counters.totalAccepted.Load(), "reuse must not count as a new accept")
	requireUDPRuntimeGoroutinesDone(t, runtime)
}

func TestUDPRemoveSessionNilIsSafe(t *testing.T) {
	runtime, _ := newTestUDPRuntime(t, freeUDPAddress(t))
	require.NotPanics(t, func() { runtime.removeSession(nil) })
	runtime.mu.Lock()
	require.Empty(t, runtime.sessions)
	runtime.mu.Unlock()
	require.Equal(t, int64(0), runtime.counters.activeUDPSessions.Load())
}

func TestUDPReadLoopRejectsInvalidSourceAddress(t *testing.T) {
	backend := startUDPEchoServer(t)
	packetConn := newScriptedPacketConn()
	runtime, _ := newTestUDPRuntimeWithPacketConn(t, backend.address, packetConn)
	runtime.start()

	packetConn.packets <- scriptedUDPPacket{payload: []byte("zero"), addr: &net.UDPAddr{}}
	packetConn.packets <- scriptedUDPPacket{payload: []byte("junk"), addr: testUDPNetAddr{network: "udp", address: "not-an-address"}}
	require.Eventually(t, func() bool { return runtime.counters.totalRefused.Load() == 2 }, time.Second, 5*time.Millisecond)
	runtime.mu.Lock()
	require.Empty(t, runtime.sessions, "invalid source addresses must never open a session")
	runtime.mu.Unlock()
	require.Equal(t, int64(0), runtime.counters.activeUDPSessions.Load())

	// A valid datagram after the invalid ones proves the read loop keeps serving.
	packetConn.packets <- scriptedUDPPacket{payload: []byte("ok"), addr: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43210}}
	require.Eventually(t, func() bool { return runtime.counters.activeUDPSessions.Load() == 1 }, time.Second, 5*time.Millisecond)
	require.Equal(t, int64(1), runtime.counters.totalAccepted.Load())
}

func TestUDPStopBeforeStartLeavesNoRuntimeGoroutines(t *testing.T) {
	backend := startUDPEchoServer(t)
	runtime, _ := newTestUDPRuntime(t, backend.address)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	runtime.stop(ctx, 50*time.Millisecond)
	// Starting a runtime whose admission already closed must not launch workers.
	runtime.start()

	runtime.mu.Lock()
	require.Empty(t, runtime.sessions)
	runtime.mu.Unlock()
	requireUDPRuntimeGoroutinesDone(t, runtime)
	require.Equal(t, int64(0), runtime.counters.activeUDPSessions.Load())
}

func TestUDPShutdownLeavesNoSessionsOrRuntimeGoroutines(t *testing.T) {
	backend := startUDPEchoServer(t)
	graph := udpGraph(t, freeUDPAddress(t), backend.address)
	graph.Options.UDP.DrainTimeout = 50 * time.Millisecond
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))

	conn := dialUDP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	assertUDPRoundTrip(t, conn, "held")
	require.Eventually(t, func() bool { return manager.Status().Counters.ActiveUDPSessions == 1 }, time.Second, 10*time.Millisecond)

	manager.mu.Lock()
	runtime := manager.udpListeners["udp"]
	manager.mu.Unlock()
	require.NotNil(t, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, manager.Shutdown(ctx))

	runtime.mu.Lock()
	require.Empty(t, runtime.sessions)
	runtime.mu.Unlock()
	require.Equal(t, int64(0), runtime.counters.activeUDPSessions.Load())
	requireUDPRuntimeGoroutinesDone(t, runtime)
}

func TestUDPConcurrentCreationDuringShutdownLeavesNoSessions(t *testing.T) {
	for range 5 {
		backend := startUDPEchoServer(t)
		runtime, _ := newTestUDPRuntime(t, backend.address)
		runtime.start()

		var creators sync.WaitGroup
		for worker := range 8 {
			creators.Add(1)
			go func(worker int) {
				defer creators.Done()
				for attempt := range 25 {
					key := netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), uint16(5000+worker*100+attempt))
					runtime.session(key, effectiveUDPOptions(domain.UDPOptions{}))
				}
			}(worker)
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		runtime.stop(ctx, 50*time.Millisecond)
		cancel()
		creators.Wait()

		runtime.mu.Lock()
		require.Empty(t, runtime.sessions, "no session may be published after closing began")
		runtime.mu.Unlock()
		require.Equal(t, int64(0), runtime.counters.activeUDPSessions.Load())
		requireUDPRuntimeGoroutinesDone(t, runtime)
	}
}

// TestUDPInFlightDialDoesNotPublishAfterAdmissionClosure blocks the backend dial
// on the runtime's private dial seam, closes admission exactly as stop() does,
// then lets the dial complete: session() must reject the completed dial without
// publishing a session or a backend goroutine. The seam is instance-local, so no
// package-global state is touched.
func TestUDPInFlightDialDoesNotPublishAfterAdmissionClosure(t *testing.T) {
	runtime, _ := newTestUDPRuntime(t, "127.0.0.1:15353")
	release := make(chan struct{})
	dialStarted := make(chan struct{}, 1)
	liveConn := newLiveUDPConn(t)
	runtime.dialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		select {
		case dialStarted <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return liveConn, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	type result struct {
		session *udpSession
		ok      bool
	}
	results := make(chan result, 1)
	go func() {
		session, ok := runtime.session(netip.MustParseAddrPort("127.0.0.1:4700"), effectiveUDPOptions(domain.UDPOptions{}))
		results <- result{session: session, ok: ok}
	}()

	select {
	case <-dialStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("backend dial never reached the dial seam")
	}

	runtime.mu.Lock()
	runtime.accepting = false
	runtime.mu.Unlock()
	runtime.closed.Store(true)
	close(release)

	select {
	case got := <-results:
		require.False(t, got.ok, "a dial completing after admission closure must not open a session")
		require.Nil(t, got.session)
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight session creation did not finish")
	}

	runtime.mu.Lock()
	require.Empty(t, runtime.sessions)
	runtime.mu.Unlock()
	require.Equal(t, int64(0), runtime.counters.activeUDPSessions.Load())
	require.Equal(t, int64(0), runtime.counters.totalAccepted.Load())
	requireUDPRuntimeGoroutinesDone(t, runtime)
}

func TestUDPAddrPortNormalizesIPv4MappedAndPreservesZones(t *testing.T) {
	plainV4, ok := udpAddrPort(&net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9000})
	require.True(t, ok)
	mappedV4, ok := udpAddrPort(&net.UDPAddr{IP: net.ParseIP("::ffff:127.0.0.1"), Port: 9000})
	require.True(t, ok)
	wantV4 := netip.MustParseAddrPort("127.0.0.1:9000")
	require.Equal(t, wantV4, plainV4)
	require.Equal(t, wantV4, mappedV4, "IPv4-mapped addresses must share the IPv4 session key")

	zoned, ok := udpAddrPort(&net.UDPAddr{IP: net.ParseIP("fe80::1"), Zone: "eth0", Port: 9001})
	require.True(t, ok)
	require.Equal(t, netip.MustParseAddrPort("[fe80::1%eth0]:9001"), zoned)
	otherZone, ok := udpAddrPort(&net.UDPAddr{IP: net.ParseIP("fe80::1"), Zone: "eth1", Port: 9001})
	require.True(t, ok)
	require.NotEqual(t, zoned, otherZone, "IPv6 zones must remain part of the key")

	parsed, ok := udpAddrPort(testUDPNetAddr{network: "udp", address: "[2001:db8::1]:9002"})
	require.True(t, ok)
	require.Equal(t, netip.MustParseAddrPort("[2001:db8::1]:9002"), parsed)

	_, ok = udpAddrPort(testUDPNetAddr{network: "udp", address: "not-an-address"})
	require.False(t, ok)
}

func TestUDPAddrPortRejectsInvalidAddresses(t *testing.T) {
	_, ok := udpAddrPort(&net.UDPAddr{})
	require.False(t, ok, "zero UDPAddr must be rejected")
	_, ok = udpAddrPort(&net.UDPAddr{IP: nil, Port: 53})
	require.False(t, ok, "UDPAddr without an IP must be rejected")
	_, ok = udpAddrPort(testUDPNetAddr{network: "udp", address: "not-an-address"})
	require.False(t, ok)

	// The trusted-list path must not admit an invalid address either, even when
	// the list is empty and would otherwise trust every client.
	require.False(t, trustedAddrPort(nil, netip.AddrPort{}))
}

func TestUDPSessionPrecomputesImmutableRemoteAddr(t *testing.T) {
	backend := startUDPEchoServer(t)
	runtime, _ := newTestUDPRuntime(t, backend.address)
	options := effectiveUDPOptions(domain.UDPOptions{})

	v4, ok := runtime.session(netip.MustParseAddrPort("127.0.0.1:4600"), options)
	require.True(t, ok)
	require.NotNil(t, v4.remoteAddr)
	require.Equal(t, "127.0.0.1:4600", v4.remoteAddr.String())
	runtime.removeSession(v4)

	zoned, ok := runtime.session(netip.MustParseAddrPort("[fe80::1%eth0]:4601"), options)
	require.True(t, ok)
	require.NotNil(t, zoned.remoteAddr)
	require.Equal(t, netip.MustParseAddrPort("[fe80::1%eth0]:4601"), zoned.remoteAddr.AddrPort(), "precomputed destination must preserve the IPv6 zone")
	runtime.removeSession(zoned)
}

func TestUDPTrustedAddrPortMatchesCIDRs(t *testing.T) {
	ipv4Trusted, err := parseTrustedCIDRs([]string{"127.0.0.0/8"})
	require.NoError(t, err)
	require.True(t, trustedAddrPort(ipv4Trusted, netip.MustParseAddrPort("127.0.0.1:1")))
	require.True(t, trustedAddrPort(ipv4Trusted, netip.MustParseAddrPort("[::ffff:127.0.0.1]:1")), "v4-mapped client must match IPv4 CIDR")
	require.False(t, trustedAddrPort(ipv4Trusted, netip.MustParseAddrPort("192.0.2.1:1")))
	require.False(t, trustedAddrPort(ipv4Trusted, netip.MustParseAddrPort("[2001:db8::1]:1")), "IPv6 client must not match IPv4 CIDR")

	ipv6Trusted, err := parseTrustedCIDRs([]string{"2001:db8::/32"})
	require.NoError(t, err)
	require.True(t, trustedAddrPort(ipv6Trusted, netip.MustParseAddrPort("[2001:db8::1]:1")))
	// Intentional UDP semantic: CIDR trust is evaluated on the IP only. The
	// zone names the local interface a packet arrived on, not the peer, so a
	// zoned and zoneless client of the same address are equally trusted. Zones
	// stay part of the session key; do not "fix" this to compare zones.
	require.True(t, trustedAddrPort(ipv6Trusted, netip.MustParseAddrPort("[2001:db8::1%eth0]:1")), "zone must not affect CIDR matching")
	require.True(t, trustedAddrPort(ipv6Trusted, netip.MustParseAddrPort("[2001:db8::1%eth1]:1")), "any zone on a trusted address is trusted")
	require.False(t, trustedAddrPort(ipv6Trusted, netip.MustParseAddrPort("[2001:dead::2%eth0]:1")), "an untrusted address is untrusted regardless of zone")
	require.False(t, trustedAddrPort(ipv6Trusted, netip.MustParseAddrPort("127.0.0.1:1")))

	require.True(t, trustedAddrPort(nil, netip.MustParseAddrPort("203.0.113.9:1")), "empty trusted list admits every client")
	require.False(t, trustedAddrPort(ipv4Trusted, netip.AddrPort{}), "invalid client address is not trusted")
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

// newTestUDPRuntime builds a UDP runtime backed by a valid graph snapshot
// without binding through Manager.Apply, so tests can drive publication,
// expiry and shutdown interleavings directly.
func newTestUDPRuntime(t *testing.T, backendAddress string) (*udpEntryPointRuntime, *Manager) {
	t.Helper()
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	return newTestUDPRuntimeWithPacketConn(t, backendAddress, packetConn)
}

// newTestUDPRuntimeWithPacketConn builds a runtime over a caller-supplied
// PacketConn so the read loop can be driven deterministically.
func newTestUDPRuntimeWithPacketConn(t *testing.T, backendAddress string, packetConn net.PacketConn) (*udpEntryPointRuntime, *Manager) {
	t.Helper()
	graph := udpGraph(t, freeUDPAddress(t), backendAddress)
	manager := NewManager()
	manager.snapshot.Store(&graph)
	runtime := newUDPEntryPointRuntime(context.Background(), manager, graph.EntryPoints[0], packetConn, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		runtime.stop(ctx, 50*time.Millisecond)
		_ = packetConn.Close()
	})
	return runtime, manager
}

// scriptedPacketConn feeds the read loop a fixed sequence of packets, including
// source addresses that cannot be converted to a session key.
type scriptedUDPPacket struct {
	payload []byte
	addr    net.Addr
}

type scriptedPacketConn struct {
	packets chan scriptedUDPPacket
	closed  chan struct{}
	once    sync.Once
}

func newScriptedPacketConn() *scriptedPacketConn {
	return &scriptedPacketConn{packets: make(chan scriptedUDPPacket, 16), closed: make(chan struct{})}
}

func (c *scriptedPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	select {
	case packet := <-c.packets:
		return copy(p, packet.payload), packet.addr, nil
	case <-c.closed:
		return 0, nil, net.ErrClosed
	}
}

func (c *scriptedPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) { return len(p), nil }

func (c *scriptedPacketConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *scriptedPacketConn) LocalAddr() net.Addr              { return &net.UDPAddr{IP: net.IPv4zero} }
func (c *scriptedPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *scriptedPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *scriptedPacketConn) SetWriteDeadline(time.Time) error { return nil }

func newTestUDPSession(t *testing.T, clientAddr netip.AddrPort) *udpSession {
	t.Helper()
	session := &udpSession{clientAddr: clientAddr, remoteAddr: net.UDPAddrFromAddrPort(clientAddr), backend: newLiveUDPConn(t)}
	session.touch()
	return session
}

// newLiveUDPConn dials a live loopback UDP listener so probe writes never fail
// with ICMP port-unreachable errors, and tears both down with the test.
func newLiveUDPConn(t *testing.T) net.Conn {
	t.Helper()
	listener, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		buf := make([]byte, 64)
		for {
			if _, _, err := listener.ReadFrom(buf); err != nil {
				return
			}
		}
	}()
	conn, err := net.Dial("udp", listener.LocalAddr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// testUDPSessionClosed probes the backend with a write. Synthetic sessions are
// connected to a live loopback listener (see newLiveUDPConn), so an open socket
// always accepts the probe and a closed one always errors, without the ICMP
// port-unreachable flakiness of writing to an unbound port.
func testUDPSessionClosed(session *udpSession) bool {
	_, err := session.backend.Write([]byte("probe"))
	return err != nil
}

func requireUDPRuntimeGoroutinesDone(t *testing.T, runtime *udpEntryPointRuntime) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		runtime.runWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("udp runtime goroutines did not complete")
	}
}

type testUDPNetAddr struct {
	network string
	address string
}

func (a testUDPNetAddr) Network() string { return a.network }
func (a testUDPNetAddr) String() string  { return a.address }

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
