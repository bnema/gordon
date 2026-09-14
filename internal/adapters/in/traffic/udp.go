package traffic

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

const (
	udpBufferSize      = 64 << 10
	udpWorkerCount     = 16
	udpDatagramBacklog = 1024
)

type udpEntryPointRuntime struct {
	manager    *Manager
	entryPoint domain.EntryPoint
	packetConn net.PacketConn
	counters   trafficCounters
	trusted    []*net.IPNet

	started atomic.Bool
	closed  atomic.Bool

	ctx    context.Context
	cancel context.CancelFunc
	// runWG tracks every goroutine owned by this runtime (read loop, datagram
	// workers, expiry loop and per-session backend loops). Counts are only added
	// while holding mu and before the owning goroutine starts; stop() flips
	// accepting under the same lock before it ever waits, so no Add can race a
	// Wait.
	runWG sync.WaitGroup

	datagrams chan udpDatagram

	// dialContext dials the upstream backend. It is a private instance field so
	// tests can drive in-flight dial interleavings deterministically; production
	// code always uses the standard UDP dialer set at construction and never
	// mutates it after the runtime starts.
	dialContext func(ctx context.Context, network, address string) (net.Conn, error)

	mu        sync.Mutex
	accepting bool
	sessions  map[netip.AddrPort]*udpSession
}

type udpDatagram struct {
	clientAddr netip.AddrPort
	packet     []byte
}

type udpSession struct {
	clientAddr netip.AddrPort
	// remoteAddr is the immutable packet destination precomputed once at
	// publication, so backendLoop never has to allocate or format an address
	// per packet. It is shared read-only across the session's goroutines.
	remoteAddr *net.UDPAddr
	backend    net.Conn
	backendRef domain.TrafficBackend
	lastSeen   atomic.Int64
	once       sync.Once
}

func newUDPEntryPointRuntime(parentCtx context.Context, manager *Manager, entryPoint domain.EntryPoint, packetConn net.PacketConn, trusted []*net.IPNet) *udpEntryPointRuntime {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parentCtx))
	return &udpEntryPointRuntime{
		manager:    manager,
		entryPoint: entryPoint,
		packetConn: packetConn,
		trusted:    trusted,
		ctx:        ctx,
		cancel:     cancel,
		accepting:  true,
		datagrams:  make(chan udpDatagram, udpDatagramBacklog),
		sessions:   map[netip.AddrPort]*udpSession{},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}
}

func (r *udpEntryPointRuntime) start() {
	if !r.started.CompareAndSwap(false, true) {
		return
	}
	r.mu.Lock()
	if !r.accepting {
		r.mu.Unlock()
		return
	}
	r.runWG.Add(udpWorkerCount + 2) // read loop, expiry loop and datagram workers
	r.mu.Unlock()
	entryPoint := r.entryPointSnapshot()
	trafficInfo(r.ctx).Str("entrypoint", entryPoint.Name).Str("address", entryPoint.Address).Str("protocol", string(entryPoint.Protocol)).Msg("started udp traffic entrypoint")
	for range udpWorkerCount {
		go r.datagramWorker()
	}
	go r.readLoop()
	go r.expireLoop()
}

func (r *udpEntryPointRuntime) readLoop() {
	defer r.runWG.Done()
	buf := make([]byte, udpBufferSize)
	for {
		n, clientAddr, err := r.packetConn.ReadFrom(buf)
		if err != nil {
			if r.isClosed() || errors.Is(err, net.ErrClosed) {
				return
			}
			r.counters.totalErrors.Add(1)
			continue
		}
		key, ok := udpAddrPort(clientAddr)
		if !ok {
			r.counters.totalRefused.Add(1)
			continue
		}
		if !trustedAddrPort(r.trustedSnapshot(), key) {
			r.counters.totalRefused.Add(1)
			continue
		}
		select {
		case <-r.ctx.Done():
			return
		default:
		}
		if len(r.datagrams) >= cap(r.datagrams) {
			r.counters.totalRefused.Add(1)
			continue
		}
		packet := append([]byte(nil), buf[:n]...)
		select {
		case r.datagrams <- udpDatagram{clientAddr: key, packet: packet}:
		case <-r.ctx.Done():
			return
		default:
			r.counters.totalRefused.Add(1)
		}
	}
}

func (r *udpEntryPointRuntime) datagramWorker() {
	defer r.runWG.Done()
	for {
		select {
		case <-r.ctx.Done():
			return
		case datagram := <-r.datagrams:
			select {
			case <-r.ctx.Done():
				return
			default:
			}
			r.handleDatagram(datagram.clientAddr, datagram.packet)
		}
	}
}

func (r *udpEntryPointRuntime) handleDatagram(clientAddr netip.AddrPort, packet []byte) {
	options := effectiveUDPOptions(snapshotUDPOptions(r.manager.snapshot.Load()))
	session, ok := r.session(clientAddr, options)
	if !ok {
		return
	}
	n, err := session.backend.Write(packet)
	r.counters.bytesIn.Add(int64(n))
	if err != nil {
		r.counters.totalErrors.Add(1)
		r.removeSession(session)
	}
}

func (r *udpEntryPointRuntime) session(clientAddr netip.AddrPort, options domain.UDPOptions) (*udpSession, bool) {
	r.mu.Lock()
	if existing := r.sessions[clientAddr]; existing != nil {
		existing.touch()
		r.mu.Unlock()
		return existing, true
	}
	if !r.accepting {
		r.mu.Unlock()
		return nil, false
	}
	r.mu.Unlock()

	backend, ok := r.resolveUDPBackend()
	if !ok {
		r.counters.totalRefused.Add(1)
		return nil, false
	}

	r.mu.Lock()
	if options.MaxSessions > 0 && len(r.sessions) >= options.MaxSessions {
		r.mu.Unlock()
		r.counters.totalRefused.Add(1)
		return nil, false
	}
	r.mu.Unlock()
	dialCtx, cancel := context.WithTimeout(r.ctx, udpDialTimeout(options))
	backendConn, err := r.dialContext(dialCtx, "udp", net.JoinHostPort(backend.Host, strconv.Itoa(backend.Port)))
	cancel()
	if err != nil {
		r.counters.totalErrors.Add(1)
		return nil, false
	}
	session := &udpSession{
		clientAddr: clientAddr,
		remoteAddr: net.UDPAddrFromAddrPort(clientAddr),
		backend:    backendConn,
		backendRef: backend,
	}
	session.touch()
	return r.publishSession(session, options)
}

// publishSession runs the locked post-dial admission, re-dedup and publication
// step. It must only be called after the backend dial completes. It returns the
// session callers should use:
//   - the newly dialed session once it is published,
//   - an existing session that won a concurrent creation race, with the newly
//     dialed backend closed and the existing session touched,
//   - nil when admission has closed or the session limit was reached, with the
//     newly dialed backend closed.
//
// The runWG count for a published session is added while holding mu and before
// its goroutine starts, so it can never race stop()'s Wait (see the runWG
// field comment). The backendLoop goroutine starts after the lock is released.
func (r *udpEntryPointRuntime) publishSession(session *udpSession, options domain.UDPOptions) (*udpSession, bool) {
	r.mu.Lock()
	if !r.accepting {
		r.mu.Unlock()
		_ = session.backend.Close()
		return nil, false
	}
	if existing := r.sessions[session.clientAddr]; existing != nil {
		r.mu.Unlock()
		_ = session.backend.Close()
		existing.touch()
		return existing, true
	}
	if options.MaxSessions > 0 && len(r.sessions) >= options.MaxSessions {
		r.mu.Unlock()
		_ = session.backend.Close()
		r.counters.totalRefused.Add(1)
		return nil, false
	}
	r.sessions[session.clientAddr] = session
	r.runWG.Add(1)
	r.counters.activeUDPSessions.Add(1)
	r.counters.totalAccepted.Add(1)
	r.mu.Unlock()

	go r.backendLoop(session)
	return session, true
}

func trafficBackendEqual(left domain.TrafficBackend, right domain.TrafficBackend) bool {
	return left.Name == right.Name && left.Host == right.Host && left.Port == right.Port && left.Protocol == right.Protocol
}

func udpDialTimeout(options domain.UDPOptions) time.Duration {
	if options.IdleTimeout > 0 && options.IdleTimeout < 5*time.Second {
		return options.IdleTimeout
	}
	return 5 * time.Second
}

func (r *udpEntryPointRuntime) backendLoop(session *udpSession) {
	defer r.runWG.Done()
	defer r.removeSession(session)
	buf := make([]byte, udpBufferSize)
	for {
		n, err := session.backend.Read(buf)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) && !r.isClosed() {
				r.counters.totalErrors.Add(1)
			}
			return
		}
		written, err := r.packetConn.WriteTo(buf[:n], session.remoteAddr)
		r.counters.bytesOut.Add(int64(written))
		if err != nil {
			if !r.isClosed() {
				r.counters.totalErrors.Add(1)
			}
			return
		}
	}
}

func (r *udpEntryPointRuntime) expireLoop() {
	defer r.runWG.Done()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			r.expireIdleSessions(effectiveUDPOptions(snapshotUDPOptions(r.manager.snapshot.Load())).IdleTimeout)
		}
	}
}

func (r *udpEntryPointRuntime) expireIdleSessions(idleTimeout time.Duration) {
	if idleTimeout <= 0 {
		idleTimeout = defaultUDPOptions().IdleTimeout
	}
	cutoff := time.Now().Add(-idleTimeout).UnixNano()
	expired := []*udpSession{}
	r.mu.Lock()
	for key, session := range r.sessions {
		if session.lastSeen.Load() <= cutoff {
			delete(r.sessions, key)
			expired = append(expired, session)
		}
	}
	r.mu.Unlock()
	for _, session := range expired {
		session.close()
		r.counters.activeUDPSessions.Add(-1)
	}
}

// removeSession drops session from the session map and closes its backend only
// when it is still the session currently registered for its client address. A
// stale remover (expiry, backend read/write failure, reload or CIDR cleanup) can
// therefore never evict or close a replacement published under the same key.
// The backend socket is closed outside the lock.
func (r *udpEntryPointRuntime) removeSession(session *udpSession) {
	if session == nil {
		return
	}
	r.mu.Lock()
	current := r.sessions[session.clientAddr]
	if current == session {
		delete(r.sessions, session.clientAddr)
	}
	r.mu.Unlock()
	if current != session {
		return
	}
	session.close()
	r.counters.activeUDPSessions.Add(-1)
}

func (r *udpEntryPointRuntime) resolveUDPBackend() (domain.TrafficBackend, bool) {
	graph := r.manager.snapshot.Load()
	if graph == nil {
		return domain.TrafficBackend{}, false
	}
	entryPoint := r.entryPointSnapshot()
	var router domain.TrafficRouter
	for _, candidate := range graph.Routers {
		if candidate.EntryPoint == entryPoint.Name && candidate.Protocol == domain.RouterProtocolUDP {
			if router.Name != "" {
				return domain.TrafficBackend{}, false
			}
			router = candidate
		}
	}
	if router.Name == "" {
		return domain.TrafficBackend{}, false
	}
	for _, service := range graph.Services {
		if service.Name == router.Service && len(service.Backends) == 1 && service.Backends[0].Protocol == domain.NetworkProtocolUDP {
			return service.Backends[0], true
		}
	}
	return domain.TrafficBackend{}, false
}

func (r *udpEntryPointRuntime) stop(ctx context.Context, drainTimeout time.Duration) {
	if r.closed.CompareAndSwap(false, true) {
		entryPoint := r.entryPointSnapshot()
		trafficInfo(ctx).Str("entrypoint", entryPoint.Name).Str("address", entryPoint.Address).Msg("stopping udp traffic entrypoint")
		// Closing admission under mu serializes with start() and session(),
		// which are the only places that add to runWG. Once this critical
		// section completes no further Add can occur, so waitRuntime may Wait
		// safely.
		r.mu.Lock()
		r.accepting = false
		r.mu.Unlock()
		r.cancel()
		_ = r.packetConn.Close()
	}
	if r.waitRuntime(ctx, drainTimeout) {
		trafficInfo(ctx).Str("entrypoint", r.entryPointSnapshot().Name).Msg("stopped udp traffic entrypoint")
		return
	}
	trafficDebug(ctx).Str("entrypoint", r.entryPointSnapshot().Name).Dur("drain_timeout", drainTimeout).Msg("forcing udp traffic entrypoint drain")
	r.closeSessions()
	if r.waitRuntime(ctx, drainTimeout) {
		trafficInfo(ctx).Str("entrypoint", r.entryPointSnapshot().Name).Msg("stopped udp traffic entrypoint")
	}
}

func (r *udpEntryPointRuntime) drainSessionsAfter(drainTimeout time.Duration) {
	trafficDebug(r.ctx).Str("entrypoint", r.entryPointSnapshot().Name).Dur("drain_timeout", drainTimeout).Msg("scheduled udp session drain after router removal")
	r.drainSessionsMatchingAfter(func(*udpSession) bool { return true }, drainTimeout)
}

func (r *udpEntryPointRuntime) drainSessionsNotMatchingAfter(backend domain.TrafficBackend, drainTimeout time.Duration) {
	trafficDebug(r.ctx).Str("entrypoint", r.entryPointSnapshot().Name).Dur("drain_timeout", drainTimeout).Msg("scheduled stale udp session drain after backend update")
	r.drainSessionsMatchingAfter(func(session *udpSession) bool { return !trafficBackendEqual(session.backendRef, backend) }, drainTimeout)
}

func (r *udpEntryPointRuntime) drainSessionsMatchingAfter(match func(*udpSession) bool, drainTimeout time.Duration) {
	go func() {
		if drainTimeout <= 0 {
			drainTimeout = defaultUDPOptions().DrainTimeout
		}
		select {
		case <-time.After(drainTimeout):
			r.closeSessionsMatching(match)
		case <-r.ctx.Done():
		}
	}()
}

func (r *udpEntryPointRuntime) waitRuntime(ctx context.Context, drainTimeout time.Duration) bool {
	if drainTimeout <= 0 {
		drainTimeout = defaultUDPOptions().DrainTimeout
	}
	drainCtx, cancel := context.WithTimeout(ctx, drainTimeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		r.runWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-drainCtx.Done():
		return false
	}
}

func (r *udpEntryPointRuntime) closeSessions() {
	r.closeSessionsMatching(func(*udpSession) bool { return true })
}

func (r *udpEntryPointRuntime) closeSessionsMatching(match func(*udpSession) bool) {
	r.mu.Lock()
	sessions := make([]*udpSession, 0, len(r.sessions))
	for _, session := range r.sessions {
		if match(session) {
			sessions = append(sessions, session)
		}
	}
	r.mu.Unlock()
	for _, session := range sessions {
		r.removeSession(session)
	}
}

func (r *udpEntryPointRuntime) matches(entryPoint domain.EntryPoint) bool {
	current := r.entryPointSnapshot()
	return current.Name == entryPoint.Name && current.Address == entryPoint.Address && current.Protocol == entryPoint.Protocol && trustedCIDRsEqual(current.TrustedCIDRs, entryPoint.TrustedCIDRs)
}

func (r *udpEntryPointRuntime) sameAddress(entryPoint domain.EntryPoint) bool {
	return r.entryPointSnapshot().Address == entryPoint.Address
}

func (r *udpEntryPointRuntime) updateEntryPoint(entryPoint domain.EntryPoint, trusted []*net.IPNet) {
	r.mu.Lock()
	r.entryPoint = entryPoint
	r.trusted = trusted
	stale := make([]*udpSession, 0)
	for _, session := range r.sessions {
		if !trustedAddrPort(trusted, session.clientAddr) {
			stale = append(stale, session)
		}
	}
	r.mu.Unlock()
	for _, session := range stale {
		r.removeSession(session)
	}
}

func (r *udpEntryPointRuntime) entryPointSnapshot() domain.EntryPoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.entryPoint
}

func (r *udpEntryPointRuntime) trustedSnapshot() []*net.IPNet {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.trusted
}

func (r *udpEntryPointRuntime) isClosed() bool { return r.closed.Load() }

// udpAddrPort converts a packet source address into a typed, comparable session
// key. IPv4-mapped addresses are normalized to their IPv4 form so that both
// representations share a single session; IPv6 zones are preserved. Invalid
// addresses are rejected before any trusted-list handling so they can never be
// admitted as a zero-valued session key.
func udpAddrPort(addr net.Addr) (netip.AddrPort, bool) {
	if udpAddr, ok := addr.(*net.UDPAddr); ok {
		key := normalizeUDPAddrPort(udpAddr.AddrPort())
		return key, key.IsValid()
	}
	parsed, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return netip.AddrPort{}, false
	}
	key := normalizeUDPAddrPort(parsed)
	return key, key.IsValid()
}

func normalizeUDPAddrPort(addrPort netip.AddrPort) netip.AddrPort {
	if !addrPort.IsValid() {
		return addrPort
	}
	return netip.AddrPortFrom(addrPort.Addr().Unmap(), addrPort.Port())
}

// trustedAddrPort reports whether addr is within the trusted CIDRs. An empty
// trusted list admits every client.
//
// Matching is intentionally by IP only: the IPv6 zone identifies the local
// interface a packet arrived on, not the peer, so a zoned link-local client
// matches the same CIDR as its zoneless form. Zones remain part of the session
// key (see udpAddrPort) and are dropped only for trust evaluation. The address
// is unmapped before matching so a v4-mapped client matches an IPv4 CIDR.
func trustedAddrPort(trusted []*net.IPNet, addr netip.AddrPort) bool {
	if !addr.IsValid() {
		return false
	}
	if len(trusted) == 0 {
		return true
	}
	ip := addr.Addr().Unmap().AsSlice()
	for _, network := range trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *udpSession) touch() { s.lastSeen.Store(time.Now().UnixNano()) }

func (s *udpSession) close() {
	s.once.Do(func() {
		_ = s.backend.Close()
	})
}
