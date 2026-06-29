package traffic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

type entryPointRuntime struct {
	manager    *Manager
	entryPoint domain.EntryPoint
	listener   net.Listener
	counters   trafficCounters

	started atomic.Bool
	closed  atomic.Bool

	ctx            context.Context
	cancel         context.CancelFunc
	acceptDone     chan struct{}
	acceptDoneOnce sync.Once
	activeWG       sync.WaitGroup

	mu          sync.Mutex
	activeConns map[*trackedTCPConn]struct{}
}

type trackedTCPConn struct {
	client  net.Conn
	backend net.Conn
}

func newEntryPointRuntime(manager *Manager, entryPoint domain.EntryPoint, listener net.Listener) *entryPointRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	return &entryPointRuntime{
		manager:     manager,
		entryPoint:  entryPoint,
		listener:    listener,
		ctx:         ctx,
		cancel:      cancel,
		acceptDone:  make(chan struct{}),
		activeConns: map[*trackedTCPConn]struct{}{},
	}
}

func (r *entryPointRuntime) start() {
	if !r.started.CompareAndSwap(false, true) {
		return
	}
	go r.acceptLoop()
}

func (r *entryPointRuntime) acceptLoop() {
	defer r.closeAcceptDone()
	for {
		conn, err := r.listener.Accept()
		if err != nil {
			if r.isClosed() || errors.Is(err, net.ErrClosed) {
				return
			}
			r.counters.totalErrors.Add(1)
			if temporary, ok := err.(interface{ Temporary() bool }); ok && temporary.Temporary() {
				continue
			}
			return
		}
		r.activeWG.Add(1)
		go r.handleTCPConn(conn)
	}
}

func (r *entryPointRuntime) handleTCPConn(client net.Conn) {
	defer r.activeWG.Done()

	options := effectiveTCPOptions(snapshotTCPOptions(r.manager.snapshot.Load()))
	if !r.reserveConnection(options.MaxConnections) {
		r.counters.totalRefused.Add(1)
		_ = client.Close()
		return
	}
	defer r.releaseConnection()

	router, backend, ok := r.resolveTCPBackend()
	if !ok {
		r.counters.totalRefused.Add(1)
		_ = client.Close()
		return
	}

	dialCtx, cancel := context.WithTimeout(r.ctx, options.DialTimeout)
	defer cancel()
	backendConn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", net.JoinHostPort(backend.Host, strconv.Itoa(backend.Port)))
	if err != nil {
		r.counters.totalErrors.Add(1)
		_ = client.Close()
		return
	}

	tracked := &trackedTCPConn{client: client, backend: backendConn}
	r.track(tracked)
	defer r.untrack(tracked)

	r.counters.totalAccepted.Add(1)
	_ = router
	r.proxyTCP(client, backendConn, options.IdleTimeout)
}

func (r *entryPointRuntime) resolveTCPBackend() (domain.TrafficRouter, domain.TrafficBackend, bool) {
	graph := r.manager.snapshot.Load()
	if graph == nil {
		return domain.TrafficRouter{}, domain.TrafficBackend{}, false
	}
	var router domain.TrafficRouter
	for _, candidate := range graph.Routers {
		if candidate.EntryPoint == r.entryPoint.Name && candidate.Protocol == domain.RouterProtocolTCP {
			if router.Name != "" {
				return domain.TrafficRouter{}, domain.TrafficBackend{}, false
			}
			router = candidate
		}
	}
	if router.Name == "" {
		return domain.TrafficRouter{}, domain.TrafficBackend{}, false
	}
	for _, service := range graph.Services {
		if service.Name != router.Service || len(service.Backends) != 1 {
			continue
		}
		backend := service.Backends[0]
		if backend.Protocol != domain.NetworkProtocolTCP {
			return domain.TrafficRouter{}, domain.TrafficBackend{}, false
		}
		return router, backend, true
	}
	return domain.TrafficRouter{}, domain.TrafficBackend{}, false
}

func (r *entryPointRuntime) proxyTCP(client net.Conn, backend net.Conn, idleTimeout time.Duration) {
	defer client.Close()
	defer backend.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		n, err := copyWithIdleTimeout(backend, client, idleTimeout)
		r.counters.bytesIn.Add(n)
		if err != nil && !isExpectedCopyError(err) {
			r.counters.totalErrors.Add(1)
		}
		closeWrite(backend)
	}()
	go func() {
		defer wg.Done()
		n, err := copyWithIdleTimeout(client, backend, idleTimeout)
		r.counters.bytesOut.Add(n)
		if err != nil && !isExpectedCopyError(err) {
			r.counters.totalErrors.Add(1)
		}
		closeWrite(client)
	}()
	wg.Wait()
}

func copyWithIdleTimeout(dst net.Conn, src net.Conn, idleTimeout time.Duration) (int64, error) {
	if idleTimeout <= 0 {
		return io.Copy(dst, src)
	}
	return io.Copy(dst, idleConn{Conn: src, idleTimeout: idleTimeout})
}

type idleConn struct {
	net.Conn
	idleTimeout time.Duration
}

func (c idleConn) Read(p []byte) (int, error) {
	if err := c.SetReadDeadline(time.Now().Add(c.idleTimeout)); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}

func closeWrite(conn net.Conn) {
	if writeCloser, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = writeCloser.CloseWrite()
	}
}

func isExpectedCopyError(err error) bool {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

func (r *entryPointRuntime) reserveConnection(maxConnections int) bool {
	active := r.counters.activeTCPConnections.Add(1)
	if maxConnections > 0 && active > int64(maxConnections) {
		r.counters.activeTCPConnections.Add(-1)
		return false
	}
	return true
}

func (r *entryPointRuntime) releaseConnection() {
	r.counters.activeTCPConnections.Add(-1)
}

func (r *entryPointRuntime) track(conn *trackedTCPConn) {
	r.mu.Lock()
	r.activeConns[conn] = struct{}{}
	r.mu.Unlock()
}

func (r *entryPointRuntime) untrack(conn *trackedTCPConn) {
	r.mu.Lock()
	delete(r.activeConns, conn)
	r.mu.Unlock()
}

func (r *entryPointRuntime) stop(ctx context.Context, drainTimeout time.Duration) {
	if r.closed.CompareAndSwap(false, true) {
		r.cancel()
		_ = r.listener.Close()
		if !r.started.Load() {
			r.closeAcceptDone()
		}
	}
	select {
	case <-r.acceptDone:
	case <-ctx.Done():
		return
	}
	if r.waitActive(ctx, drainTimeout) {
		return
	}
	r.closeActiveConns()
	select {
	case <-r.activeDone():
	case <-ctx.Done():
	}
}

func (r *entryPointRuntime) waitActive(ctx context.Context, drainTimeout time.Duration) bool {
	if drainTimeout <= 0 {
		drainTimeout = defaultTCPOptions().DrainTimeout
	}
	drainCtx, cancel := context.WithTimeout(ctx, drainTimeout)
	defer cancel()
	select {
	case <-r.activeDone():
		return true
	case <-drainCtx.Done():
		return false
	}
}

func (r *entryPointRuntime) closeAcceptDone() {
	r.acceptDoneOnce.Do(func() { close(r.acceptDone) })
}

func (r *entryPointRuntime) activeDone() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		r.activeWG.Wait()
		close(done)
	}()
	return done
}

func (r *entryPointRuntime) closeActiveConns() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for conn := range r.activeConns {
		_ = conn.client.Close()
		_ = conn.backend.Close()
	}
}

func (r *entryPointRuntime) matches(entryPoint domain.EntryPoint) bool {
	return r.entryPoint.Name == entryPoint.Name && r.entryPoint.Address == entryPoint.Address && r.entryPoint.Protocol == entryPoint.Protocol
}

func (r *entryPointRuntime) shouldStopWith(next *entryPointRuntime) bool {
	return next == nil || next != r
}

func (r *entryPointRuntime) isClosed() bool {
	return r.closed.Load()
}

func backendFromAddress(name string, address string) (domain.TrafficBackend, error) {
	host, portValue, err := net.SplitHostPort(address)
	if err != nil {
		return domain.TrafficBackend{}, err
	}
	port, err := strconv.Atoi(portValue)
	if err != nil {
		return domain.TrafficBackend{}, err
	}
	return domain.TrafficBackend{Name: name, Host: host, Port: port, Protocol: domain.NetworkProtocolTCP}, nil
}

func serviceRef(name string, portName string) string {
	return fmt.Sprintf("network_service:%s:%s", name, portName)
}
