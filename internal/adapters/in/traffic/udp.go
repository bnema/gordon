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

const udpBufferSize = 64 << 10

type udpEntryPointRuntime struct {
	manager    *Manager
	entryPoint domain.EntryPoint
	packetConn net.PacketConn
	counters   trafficCounters

	started atomic.Bool
	closed  atomic.Bool

	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	doneOnce sync.Once

	mu       sync.Mutex
	sessions map[string]*udpSession
}

type udpSession struct {
	clientAddr net.Addr
	backend    net.Conn
	lastSeen   atomic.Int64
	done       chan struct{}
	once       sync.Once
}

func newUDPEntryPointRuntime(manager *Manager, entryPoint domain.EntryPoint, packetConn net.PacketConn) *udpEntryPointRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	return &udpEntryPointRuntime{
		manager:    manager,
		entryPoint: entryPoint,
		packetConn: packetConn,
		ctx:        ctx,
		cancel:     cancel,
		done:       make(chan struct{}),
		sessions:   map[string]*udpSession{},
	}
}

func (r *udpEntryPointRuntime) start() {
	if !r.started.CompareAndSwap(false, true) {
		return
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
		packet := append([]byte(nil), buf[:n]...)
		r.handleDatagram(clientAddr, packet)
	}
}

func (r *udpEntryPointRuntime) handleDatagram(clientAddr net.Addr, packet []byte) {
	options := effectiveUDPOptions(snapshotUDPOptions(r.manager.snapshot.Load()))
	session, ok := r.session(clientAddr.String(), clientAddr, options)
	if !ok {
		return
	}
	session.touch()
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
		r.mu.Unlock()
		return session, true
	}
	if options.MaxSessions > 0 && len(r.sessions) >= options.MaxSessions {
		r.mu.Unlock()
		r.counters.totalRefused.Add(1)
		return nil, false
	}
	r.mu.Unlock()

	backend, ok := r.resolveUDPBackend()
	if !ok {
		r.counters.totalRefused.Add(1)
		return nil, false
	}
	backendConn, err := net.Dial("udp", net.JoinHostPort(backend.Host, strconv.Itoa(backend.Port)))
	if err != nil {
		r.counters.totalErrors.Add(1)
		return nil, false
	}
	session := &udpSession{clientAddr: clientAddr, backend: backendConn, done: make(chan struct{})}
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
	r.mu.Lock()
	keys := make([]string, 0)
	for key, session := range r.sessions {
		if session.lastSeen.Load() <= cutoff {
			keys = append(keys, key)
		}
	}
	r.mu.Unlock()
	for _, key := range keys {
		r.removeSession(key)
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
	var router domain.TrafficRouter
	for _, candidate := range graph.Routers {
		if candidate.EntryPoint == r.entryPoint.Name && candidate.Protocol == domain.RouterProtocolUDP {
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
		r.cancel()
		_ = r.packetConn.Close()
		if !r.started.Load() {
			r.closeDone()
		}
	}
	select {
	case <-r.done:
	case <-ctx.Done():
		return
	}
	if r.waitSessions(ctx, drainTimeout) {
		return
	}
	r.closeSessions()
	select {
	case <-r.sessionsDone():
	case <-ctx.Done():
	}
}

func (r *udpEntryPointRuntime) drainSessionsAfter(drainTimeout time.Duration) {
	go func() {
		if drainTimeout <= 0 {
			drainTimeout = defaultUDPOptions().DrainTimeout
		}
		select {
		case <-time.After(drainTimeout):
			r.closeSessions()
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
	r.mu.Lock()
	keys := make([]string, 0, len(r.sessions))
	for key := range r.sessions {
		keys = append(keys, key)
	}
	r.mu.Unlock()
	for _, key := range keys {
		r.removeSession(key)
	}
}

func (r *udpEntryPointRuntime) matches(entryPoint domain.EntryPoint) bool {
	return r.entryPoint.Name == entryPoint.Name && r.entryPoint.Address == entryPoint.Address && r.entryPoint.Protocol == entryPoint.Protocol
}

func (r *udpEntryPointRuntime) isClosed() bool { return r.closed.Load() }

func (s *udpSession) touch() { s.lastSeen.Store(time.Now().UnixNano()) }

func (s *udpSession) close() {
	s.once.Do(func() {
		_ = s.backend.Close()
		close(s.done)
	})
}
