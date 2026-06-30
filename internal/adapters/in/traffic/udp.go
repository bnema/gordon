package traffic

import (
	"context"
	"errors"
	"net"
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

	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	doneOnce sync.Once

	datagrams chan udpDatagram

	mu       sync.Mutex
	sessions map[string]*udpSession
}

type udpDatagram struct {
	clientAddr net.Addr
	packet     []byte
}

type udpSession struct {
	clientAddr net.Addr
	backend    net.Conn
	backendRef domain.TrafficBackend
	lastSeen   atomic.Int64
	done       chan struct{}
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
		done:       make(chan struct{}),
		datagrams:  make(chan udpDatagram, udpDatagramBacklog),
		sessions:   map[string]*udpSession{},
	}
}

func (r *udpEntryPointRuntime) start() {
	if !r.started.CompareAndSwap(false, true) {
		return
	}
	entryPoint := r.entryPointSnapshot()
	trafficInfo(r.ctx).Str("entrypoint", entryPoint.Name).Str("address", entryPoint.Address).Str("protocol", string(entryPoint.Protocol)).Msg("started udp traffic entrypoint")
	for range udpWorkerCount {
		go r.datagramWorker()
	}
	go r.readLoop()
	go r.expireLoop()
}

func (r *udpEntryPointRuntime) readLoop() {
	defer r.closeDone()
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
		if !trustedRemoteAddr(r.trustedSnapshot(), clientAddr) {
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
		case r.datagrams <- udpDatagram{clientAddr: clientAddr, packet: packet}:
		case <-r.ctx.Done():
			return
		default:
			r.counters.totalRefused.Add(1)
		}
	}
}

func (r *udpEntryPointRuntime) datagramWorker() {
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

func (r *udpEntryPointRuntime) handleDatagram(clientAddr net.Addr, packet []byte) {
	options := effectiveUDPOptions(snapshotUDPOptions(r.manager.snapshot.Load()))
	session, ok := r.session(clientAddr.String(), clientAddr, options)
	if !ok {
		return
	}
	n, err := session.backend.Write(packet)
	r.counters.bytesIn.Add(int64(n))
	if err != nil {
		r.counters.totalErrors.Add(1)
		r.removeSession(clientAddr.String())
	}
}

func (r *udpEntryPointRuntime) session(key string, clientAddr net.Addr, options domain.UDPOptions) (*udpSession, bool) {
	r.mu.Lock()
	if session := r.sessions[key]; session != nil {
		session.touch()
		r.mu.Unlock()
		return session, true
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
	backendConn, err := (&net.Dialer{}).DialContext(dialCtx, "udp", net.JoinHostPort(backend.Host, strconv.Itoa(backend.Port)))
	cancel()
	if err != nil {
		r.counters.totalErrors.Add(1)
		return nil, false
	}
	session := &udpSession{clientAddr: clientAddr, backend: backendConn, backendRef: backend, done: make(chan struct{})}
	session.touch()

	r.mu.Lock()
	if existing := r.sessions[key]; existing != nil {
		r.mu.Unlock()
		_ = backendConn.Close()
		return existing, true
	}
	if options.MaxSessions > 0 && len(r.sessions) >= options.MaxSessions {
		r.mu.Unlock()
		_ = backendConn.Close()
		r.counters.totalRefused.Add(1)
		return nil, false
	}
	r.sessions[key] = session
	r.mu.Unlock()

	r.counters.activeUDPSessions.Add(1)
	r.counters.totalAccepted.Add(1)
	go r.backendLoop(key, session)
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

func (r *udpEntryPointRuntime) backendLoop(key string, session *udpSession) {
	defer r.removeSession(key)
	buf := make([]byte, udpBufferSize)
	for {
		n, err := session.backend.Read(buf)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) && !r.isClosed() {
				r.counters.totalErrors.Add(1)
			}
			return
		}
		written, err := r.packetConn.WriteTo(buf[:n], session.clientAddr)
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

func (r *udpEntryPointRuntime) removeSession(key string) {
	r.mu.Lock()
	session := r.sessions[key]
	if session != nil {
		delete(r.sessions, key)
	}
	r.mu.Unlock()
	if session == nil {
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
		r.cancel()
		_ = r.packetConn.Close()
		if !r.started.Load() {
			r.closeDone()
		}
	}
	select {
	case <-r.done:
	case <-ctx.Done():
		r.closeSessions()
		return
	}
	if r.waitSessions(ctx, drainTimeout) {
		trafficInfo(ctx).Str("entrypoint", r.entryPointSnapshot().Name).Msg("stopped udp traffic entrypoint")
		return
	}
	trafficDebug(ctx).Str("entrypoint", r.entryPointSnapshot().Name).Dur("drain_timeout", drainTimeout).Msg("forcing udp traffic entrypoint drain")
	r.closeSessions()
	select {
	case <-r.sessionsDone():
		trafficInfo(ctx).Str("entrypoint", r.entryPointSnapshot().Name).Msg("stopped udp traffic entrypoint")
	case <-ctx.Done():
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

func (r *udpEntryPointRuntime) waitSessions(ctx context.Context, drainTimeout time.Duration) bool {
	if drainTimeout <= 0 {
		drainTimeout = defaultUDPOptions().DrainTimeout
	}
	drainCtx, cancel := context.WithTimeout(ctx, drainTimeout)
	defer cancel()
	select {
	case <-r.sessionsDone():
		return true
	case <-drainCtx.Done():
		return false
	}
}

func (r *udpEntryPointRuntime) closeDone() {
	r.doneOnce.Do(func() { close(r.done) })
}

func (r *udpEntryPointRuntime) sessionsDone() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		for {
			r.mu.Lock()
			count := len(r.sessions)
			r.mu.Unlock()
			if count == 0 {
				close(done)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	return done
}

func (r *udpEntryPointRuntime) closeSessions() {
	r.closeSessionsMatching(func(*udpSession) bool { return true })
}

func (r *udpEntryPointRuntime) closeSessionsMatching(match func(*udpSession) bool) {
	r.mu.Lock()
	keys := make([]string, 0, len(r.sessions))
	for key, session := range r.sessions {
		if match(session) {
			keys = append(keys, key)
		}
	}
	r.mu.Unlock()
	for _, key := range keys {
		r.removeSession(key)
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
	stale := make([]string, 0)
	for key, session := range r.sessions {
		if !trustedRemoteAddr(trusted, session.clientAddr) {
			stale = append(stale, key)
		}
	}
	r.mu.Unlock()
	for _, key := range stale {
		r.removeSession(key)
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

func (s *udpSession) touch() { s.lastSeen.Store(time.Now().UnixNano()) }

func (s *udpSession) close() {
	s.once.Do(func() {
		_ = s.backend.Close()
		close(s.done)
	})
}
