package traffic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
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
	trusted    []*net.IPNet

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

func newEntryPointRuntime(parentCtx context.Context, manager *Manager, entryPoint domain.EntryPoint, listener net.Listener, trusted []*net.IPNet) *entryPointRuntime {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parentCtx))
	return &entryPointRuntime{
		manager:     manager,
		entryPoint:  entryPoint,
		listener:    listener,
		trusted:     trusted,
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
	trafficInfo(r.ctx).Str("entrypoint", r.entryPoint.Name).Str("address", r.entryPoint.Address).Str("protocol", string(r.entryPoint.Protocol)).Msg("started tcp traffic entrypoint")
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
			trafficWarn(r.ctx).Err(err).Str("entrypoint", r.entryPoint.Name).Msg("tcp traffic accept loop stopped")
			return
		}
		if !trustedRemoteAddr(r.trusted, conn.RemoteAddr()) {
			r.counters.totalRefused.Add(1)
			_ = conn.Close()
			continue
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

	tracked := &trackedTCPConn{client: client}
	r.track(tracked)
	defer r.untrack(tracked)

	switch r.entryPoint.Protocol {
	case domain.EntryPointProtocolTCP:
		r.handlePlainTCP(tracked, options)
	case domain.EntryPointProtocolTLSMux:
		r.handleTLSMux(tracked, options)
	default:
		r.counters.totalRefused.Add(1)
		_ = tracked.client.Close()
	}
}

func (r *entryPointRuntime) handlePlainTCP(tracked *trackedTCPConn, options domain.TCPOptions) {
	_, backend, ok := r.resolveTCPBackend()
	if !ok {
		r.counters.totalRefused.Add(1)
		_ = tracked.client.Close()
		return
	}
	r.proxyToBackend(tracked, tracked.client, backend, options)
}

func (r *entryPointRuntime) handleTLSMux(tracked *trackedTCPConn, options domain.TCPOptions) {
	peeked, err := r.peekTLSClientHello(tracked.client, options)
	if err != nil {
		r.counters.totalErrors.Add(1)
		_ = tracked.client.Close()
		return
	}
	if backend, ok := r.resolveTLSBackend(peeked.sni); ok {
		r.proxyToBackend(tracked, peeked.conn, backend, options)
		return
	}
	if fallback := r.manager.tlsFallback(r.entryPoint.Name); fallback != nil {
		r.counters.totalAccepted.Add(1)
		fallback(r.ctx, peeked.conn)
		return
	}
	r.counters.totalRefused.Add(1)
	_ = peeked.conn.Close()
}

func (r *entryPointRuntime) peekTLSClientHello(client net.Conn, options domain.TCPOptions) (peekedTLSConn, error) {
	if err := client.SetReadDeadline(time.Now().Add(clientHelloTimeout(options))); err != nil {
		return peekedTLSConn{}, err
	}
	sni, replayed, err := peekClientHelloSNI(client)
	if clearErr := client.SetReadDeadline(time.Time{}); err == nil && clearErr != nil {
		return peekedTLSConn{}, clearErr
	}
	if err != nil {
		return peekedTLSConn{}, err
	}
	return peekedTLSConn{sni: sni, conn: replayed}, nil
}

func clientHelloTimeout(options domain.TCPOptions) time.Duration {
	const maxTimeout = 5 * time.Second
	if options.DialTimeout > 0 && options.DialTimeout < maxTimeout {
		return options.DialTimeout
	}
	return maxTimeout
}

type peekedTLSConn struct {
	sni  string
	conn net.Conn
}

func (r *entryPointRuntime) proxyToBackend(tracked *trackedTCPConn, client net.Conn, backend domain.TrafficBackend, options domain.TCPOptions) {
	dialCtx, cancel := context.WithTimeout(r.ctx, options.DialTimeout)
	defer cancel()
	backendConn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", net.JoinHostPort(backend.Host, strconv.Itoa(backend.Port)))
	if err != nil {
		r.counters.totalErrors.Add(1)
		_ = client.Close()
		return
	}

	tracked.backend = backendConn
	r.counters.totalAccepted.Add(1)
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

func (r *entryPointRuntime) resolveTLSBackend(sni string) (domain.TrafficBackend, bool) {
	graph := r.manager.snapshot.Load()
	if graph == nil || sni == "" {
		return domain.TrafficBackend{}, false
	}
	if router, ok := r.findExactTLSRouter(graph, sni); ok {
		return r.backendForRouter(graph, router)
	}
	if router, ok := r.findWildcardTLSRouter(graph, sni); ok {
		return r.backendForRouter(graph, router)
	}
	return domain.TrafficBackend{}, false
}

func (r *entryPointRuntime) findExactTLSRouter(graph *domain.TrafficGraph, sni string) (domain.TrafficRouter, bool) {
	for _, router := range graph.Routers {
		if router.EntryPoint == r.entryPoint.Name && router.Protocol == domain.RouterProtocolTLSPassthrough && normalizeTLSName(router.Rule.SNI) == sni {
			return router, true
		}
	}
	return domain.TrafficRouter{}, false
}

func (r *entryPointRuntime) findWildcardTLSRouter(graph *domain.TrafficGraph, sni string) (domain.TrafficRouter, bool) {
	for _, router := range graph.Routers {
		wildcard := normalizeTLSName(router.Rule.SNI)
		if router.EntryPoint == r.entryPoint.Name && router.Protocol == domain.RouterProtocolTLSPassthrough && hostMatchesTLSWildcard(sni, wildcard) {
			return router, true
		}
	}
	return domain.TrafficRouter{}, false
}

func (r *entryPointRuntime) backendForRouter(graph *domain.TrafficGraph, router domain.TrafficRouter) (domain.TrafficBackend, bool) {
	for _, service := range graph.Services {
		if service.Name == router.Service && len(service.Backends) == 1 && service.Backends[0].Protocol == domain.NetworkProtocolTCP {
			return service.Backends[0], true
		}
	}
	return domain.TrafficBackend{}, false
}

func hostMatchesTLSWildcard(host string, wildcard string) bool {
	suffix, ok := strings.CutPrefix(wildcard, "*.")
	if !ok || host == suffix {
		return false
	}
	return strings.HasSuffix(host, "."+suffix)
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
		trafficInfo(ctx).Str("entrypoint", r.entryPoint.Name).Str("address", r.entryPoint.Address).Msg("stopping tcp traffic entrypoint")
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
		trafficInfo(ctx).Str("entrypoint", r.entryPoint.Name).Msg("stopped tcp traffic entrypoint")
		return
	}
	trafficDebug(ctx).Str("entrypoint", r.entryPoint.Name).Dur("drain_timeout", drainTimeout).Msg("forcing tcp traffic entrypoint drain")
	r.closeActiveConns()
	select {
	case <-r.activeDone():
		trafficInfo(ctx).Str("entrypoint", r.entryPoint.Name).Msg("stopped tcp traffic entrypoint")
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
		if conn.backend != nil {
			_ = conn.backend.Close()
		}
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
