package traffic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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

	tlsHTTPReplaceMu sync.Mutex
	tlsHTTPListener  *tlsHTTPListener
	tlsHTTPServer    *http.Server
	tlsHTTPDone      chan struct{}

	smartHTTPReplaceMu sync.Mutex
	smartHTTPListener  *tlsHTTPListener
	smartHTTPServer    *http.Server
	smartHTTPDone      chan struct{}

	smartTLSReplaceMu sync.Mutex
	smartTLSListener  *tlsHTTPListener
	smartTLSServer    *http.Server
	smartTLSDone      chan struct{}

	rawFallbackTrusted []*net.IPNet
}

type trackedTCPConn struct {
	mu              sync.Mutex
	client          net.Conn
	backend         net.Conn
	closing         bool
	onDone          func()
	doneOnce        sync.Once
	rawFallbackName string
}

func (c *trackedTCPConn) setBackend(backend net.Conn) {
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		_ = backend.Close()
		return
	}
	c.backend = backend
	c.mu.Unlock()
}

func (c *trackedTCPConn) close() {
	c.mu.Lock()
	c.closing = true
	client := c.client
	backend := c.backend
	c.mu.Unlock()
	_ = client.Close()
	if backend != nil {
		_ = backend.Close()
	}
}

func (c *trackedTCPConn) complete() {
	c.doneOnce.Do(func() {
		if c.onDone != nil {
			c.onDone()
		}
	})
}

func (c *trackedTCPConn) markRawFallback(name string) {
	c.mu.Lock()
	c.rawFallbackName = name
	c.mu.Unlock()
}

func (c *trackedTCPConn) rawFallback() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rawFallbackName, c.rawFallbackName != ""
}

func newEntryPointRuntime(parentCtx context.Context, manager *Manager, entryPoint domain.EntryPoint, listener net.Listener, trusted []*net.IPNet) *entryPointRuntime {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parentCtx))
	rawTrusted, _ := parseTrustedCIDRs(entryPoint.RawFallbackTrustedCIDRs)
	return &entryPointRuntime{
		manager:            manager,
		entryPoint:         entryPoint,
		listener:           listener,
		trusted:            trusted,
		rawFallbackTrusted: rawTrusted,
		ctx:                ctx,
		cancel:             cancel,
		acceptDone:         make(chan struct{}),
		activeConns:        map[*trackedTCPConn]struct{}{},
	}
}

func (r *entryPointRuntime) start() {
	if !r.started.CompareAndSwap(false, true) {
		return
	}
	entryPoint := r.entryPointSnapshot()
	r.startTLSHTTPServer(entryPoint)
	r.startSmartTCPHTTPServers(entryPoint)
	trafficInfo(r.ctx).Str("entrypoint", entryPoint.Name).Str("address", entryPoint.Address).Str("protocol", string(entryPoint.Protocol)).Msg("started tcp traffic entrypoint")
	go r.acceptLoop()
}

func (r *entryPointRuntime) startTLSHTTPServer(entryPoint domain.EntryPoint) {
	if entryPoint.Protocol != domain.EntryPointProtocolTLSMux {
		return
	}
	config, ok := r.manager.tlsHTTPServer(entryPoint.Name)
	if !ok || config.Handler == nil || config.TLSConfig == nil {
		return
	}
	r.mu.Lock()
	alreadyStarted := r.tlsHTTPServer != nil
	r.mu.Unlock()
	if alreadyStarted {
		return
	}
	r.replaceTLSHTTPServer(entryPoint, config)
}

func (r *entryPointRuntime) refreshTLSHTTPServer(entryPointName string) {
	entryPoint := r.entryPointSnapshot()
	if entryPoint.Name != entryPointName || entryPoint.Protocol != domain.EntryPointProtocolTLSMux {
		return
	}
	config, ok := r.manager.tlsHTTPServer(entryPointName)
	if !ok || config.Handler == nil || config.TLSConfig == nil {
		r.stopTLSHTTPServer(r.ctx, 0)
		return
	}
	r.replaceTLSHTTPServer(entryPoint, config)
}

func (r *entryPointRuntime) replaceTLSHTTPServer(entryPoint domain.EntryPoint, config TLSHTTPServerConfig) {
	r.tlsHTTPReplaceMu.Lock()
	defer r.tlsHTTPReplaceMu.Unlock()
	r.stopTLSHTTPServerLocked(r.ctx, 0)
	if r.isClosed() {
		return
	}
	listener := newTLSHTTPListener(r.listener.Addr())
	server := &http.Server{
		Handler:           config.Handler,
		TLSConfig:         config.TLSConfig.Clone(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	done := make(chan struct{})

	r.mu.Lock()
	if r.isClosed() {
		r.mu.Unlock()
		_ = listener.Close()
		return
	}
	r.tlsHTTPListener = listener
	r.tlsHTTPServer = server
	r.tlsHTTPDone = done
	r.mu.Unlock()

	go func() {
		defer close(done)
		if err := server.ServeTLS(listener, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			trafficWarn(r.ctx).Err(err).Str("entrypoint", entryPoint.Name).Msg("tls mux https server stopped with error")
		}
	}()
}

func (r *entryPointRuntime) routeToHTTPS(tracked *trackedTCPConn, conn net.Conn) bool {
	r.mu.Lock()
	listener := r.tlsHTTPListener
	r.mu.Unlock()
	return r.routeToHTTPListener(tracked, conn, listener)
}

func (r *entryPointRuntime) routeToSmartHTTP(tracked *trackedTCPConn, conn net.Conn) bool {
	r.mu.Lock()
	listener := r.smartHTTPListener
	r.mu.Unlock()
	return r.routeToHTTPListener(tracked, conn, listener)
}

func (r *entryPointRuntime) routeToSmartHTTPS(tracked *trackedTCPConn, conn net.Conn) bool {
	r.mu.Lock()
	listener := r.smartTLSListener
	r.mu.Unlock()
	return r.routeToHTTPListener(tracked, conn, listener)
}

func (r *entryPointRuntime) routeToHTTPListener(tracked *trackedTCPConn, conn net.Conn, listener *tlsHTTPListener) bool {
	if listener == nil {
		return false
	}
	return listener.serve(&trackedHTTPConn{Conn: conn, onClose: tracked.complete})
}

type trackedHTTPConn struct {
	net.Conn
	once    sync.Once
	onClose func()
}

func (c *trackedHTTPConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.onClose)
	return err
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
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			trafficWarn(r.ctx).Err(err).Str("entrypoint", r.entryPointSnapshot().Name).Msg("tcp traffic accept failed; retrying")
			select {
			case <-time.After(100 * time.Millisecond):
			case <-r.ctx.Done():
				return
			}
			continue
		}
		if !trustedRemoteAddr(r.trustedSnapshot(), conn.RemoteAddr()) {
			r.counters.totalRefused.Add(1)
			if r.entryPointSnapshot().Protocol == domain.EntryPointProtocolSmartTCP {
				r.counters.smartTCP.entrypointCIDRRefused.Add(1)
			}
			_ = conn.Close()
			continue
		}
		r.activeWG.Add(1)
		go r.handleTCPConn(conn)
	}
}

func (r *entryPointRuntime) handleTCPConn(client net.Conn) {
	options := effectiveTCPOptions(snapshotTCPOptions(r.manager.snapshot.Load()))
	if !r.reserveConnection(options.MaxConnections) {
		r.counters.totalRefused.Add(1)
		_ = client.Close()
		r.activeWG.Done()
		return
	}

	tracked := &trackedTCPConn{client: client}
	tracked.onDone = func() {
		r.untrack(tracked)
		r.releaseConnection()
		r.activeWG.Done()
	}
	r.track(tracked)
	completeOnReturn := true
	defer func() {
		if completeOnReturn {
			tracked.complete()
		}
	}()

	switch r.entryPointSnapshot().Protocol {
	case domain.EntryPointProtocolTCP:
		r.handlePlainTCP(tracked, options)
	case domain.EntryPointProtocolTLSMux:
		if r.handleTLSMux(tracked, options) {
			completeOnReturn = false
		}
	case domain.EntryPointProtocolSmartTCP:
		if r.handleSmartTCP(tracked, options) {
			completeOnReturn = false
		}
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

func (r *entryPointRuntime) handleSmartTCP(tracked *trackedTCPConn, options domain.TCPOptions) bool {
	result, err := sniffSmartTCP(tracked.client, clientHelloTimeout(options))
	if err != nil {
		r.counters.totalErrors.Add(1)
		_ = tracked.client.Close()
		return false
	}
	switch result.kind {
	case dispatchHTTP, dispatchH2C:
		return r.handleSmartTCPHTTP(tracked, result)
	case dispatchTLS:
		return r.handleSmartTCPTLS(tracked, result.conn, options)
	case dispatchUnknown:
		return r.handleSmartTCPUnknown(tracked, result.conn, options)
	case dispatchRejectPROXY:
		return r.refuseSmartTCP(result.conn, r.counters.smartTCP.proxyRefused.Add)
	case dispatchSniffTimeout:
		return r.refuseSmartTCP(result.conn, r.counters.smartTCP.sniffTimeout.Add)
	case dispatchReject:
		return r.refuseSmartTCP(result.conn, r.counters.smartTCP.malformedRejected.Add)
	case dispatchRejectLarge:
		return r.refuseSmartTCP(result.conn, r.counters.smartTCP.clientHelloTooLarge.Add)
	default:
		return r.refuseSmartTCP(result.conn, nil)
	}
}

func (r *entryPointRuntime) handleSmartTCPHTTP(tracked *trackedTCPConn, result smartTCPSniffResult) bool {
	if !r.routeToSmartHTTP(tracked, result.conn) {
		return r.refuseSmartTCP(result.conn, nil)
	}
	r.counters.totalAccepted.Add(1)
	if result.kind == dispatchH2C {
		r.counters.smartTCP.h2cAccepted.Add(1)
	} else {
		r.counters.smartTCP.httpAccepted.Add(1)
	}
	return true
}

func (r *entryPointRuntime) handleSmartTCPTLS(tracked *trackedTCPConn, conn net.Conn, options domain.TCPOptions) bool {
	peeked, err := r.peekTLSClientHello(conn, options)
	if err != nil {
		return r.handleSmartTCPTLSPeekError(conn, err)
	}
	if backend, ok := r.resolveTLSBackend(peeked.sni); ok {
		r.counters.smartTCP.tlsPassthroughAccepted.Add(1)
		r.proxyToBackend(tracked, peeked.conn, backend, options)
		return false
	}
	if r.routeToSmartHTTPS(tracked, peeked.conn) {
		r.counters.totalAccepted.Add(1)
		r.counters.smartTCP.httpsFallbackAccepted.Add(1)
		return true
	}
	return r.refuseSmartTCP(peeked.conn, nil)
}

func (r *entryPointRuntime) handleSmartTCPTLSPeekError(conn net.Conn, err error) bool {
	if isClientHelloTooLargeError(err) {
		r.counters.totalRefused.Add(1)
		r.counters.smartTCP.clientHelloTooLarge.Add(1)
	} else {
		r.counters.totalErrors.Add(1)
	}
	_ = conn.Close()
	return false
}

func (r *entryPointRuntime) handleSmartTCPUnknown(tracked *trackedTCPConn, conn net.Conn, options domain.TCPOptions) bool {
	backend, rawFallbackName, ok, rawCIDRRefused := r.resolveRawFallbackBackendDetailed(conn.RemoteAddr())
	if ok {
		tracked.markRawFallback(rawFallbackName)
		r.counters.smartTCP.rawFallbackAccepted.Add(1)
		r.proxyToBackend(tracked, conn, backend, options)
		return false
	}
	if rawCIDRRefused {
		return r.refuseSmartTCP(conn, r.counters.smartTCP.rawFallbackCIDRRefused.Add)
	}
	return r.refuseSmartTCP(conn, r.counters.smartTCP.unknownNoFallbackRefused.Add)
}

func (r *entryPointRuntime) refuseSmartTCP(conn net.Conn, increment func(int64) int64) bool {
	r.counters.totalRefused.Add(1)
	if increment != nil {
		increment(1)
	}
	_ = conn.Close()
	return false
}

func (r *entryPointRuntime) handleTLSMux(tracked *trackedTCPConn, options domain.TCPOptions) bool {
	peeked, err := r.peekTLSClientHello(tracked.client, options)
	if err != nil {
		r.counters.totalErrors.Add(1)
		_ = tracked.client.Close()
		return false
	}
	if backend, ok := r.resolveTLSBackend(peeked.sni); ok {
		r.proxyToBackend(tracked, peeked.conn, backend, options)
		return false
	}
	if r.routeToHTTPS(tracked, peeked.conn) {
		r.counters.totalAccepted.Add(1)
		return true
	}
	r.counters.totalRefused.Add(1)
	_ = peeked.conn.Close()
	return false
}

func (r *entryPointRuntime) peekTLSClientHello(client net.Conn, options domain.TCPOptions) (peekedTLSConn, error) {
	if err := client.SetReadDeadline(time.Now().Add(clientHelloTimeout(options))); err != nil {
		return peekedTLSConn{}, fmt.Errorf("set client hello read deadline for %s: %w", client.RemoteAddr(), err)
	}
	sni, replayed, err := peekClientHelloSNI(client)
	if clearErr := client.SetReadDeadline(time.Time{}); err == nil && clearErr != nil {
		return peekedTLSConn{}, fmt.Errorf("clear client hello read deadline for %s: %w", client.RemoteAddr(), clearErr)
	}
	if err != nil {
		return peekedTLSConn{}, fmt.Errorf("peek client hello from %s: %w", client.RemoteAddr(), err)
	}
	return peekedTLSConn{sni: sni, conn: replayed}, nil
}

func isClientHelloTooLargeError(err error) bool {
	return strings.Contains(err.Error(), "client hello exceeds")
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

func (r *entryPointRuntime) proxyToBackend(tracked *trackedTCPConn, client net.Conn, backend domain.TrafficBackend, options domain.TCPOptions) bool {
	dialCtx, cancel := context.WithTimeout(r.ctx, options.DialTimeout)
	defer cancel()
	backendConn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", net.JoinHostPort(backend.Host, strconv.Itoa(backend.Port)))
	if err != nil {
		r.counters.totalErrors.Add(1)
		_ = client.Close()
		return false
	}

	r.setBackend(tracked, backendConn)
	r.counters.totalAccepted.Add(1)
	r.proxyTCP(client, backendConn, options.IdleTimeout)
	return true
}

func (r *entryPointRuntime) resolveRawFallbackBackendDetailed(remote net.Addr) (domain.TrafficBackend, string, bool, bool) {
	graph := r.manager.snapshot.Load()
	if graph == nil {
		return domain.TrafficBackend{}, "", false, false
	}
	entryPoint := r.entryPointSnapshot()
	if entryPoint.RawFallback == "" {
		return domain.TrafficBackend{}, "", false, false
	}
	if !entryPoint.AllowPublicRawFallback {
		trusted := r.rawFallbackTrustedSnapshot()
		if len(trusted) == 0 || !trustedRemoteAddr(trusted, remote) {
			return domain.TrafficBackend{}, "", false, true
		}
	}
	for _, router := range graph.Routers {
		if router.Name == entryPoint.RawFallback && router.EntryPoint == entryPoint.Name && router.Protocol == domain.RouterProtocolTCP {
			backend, ok := r.backendForRouter(graph, router)
			return backend, entryPoint.RawFallback, ok, false
		}
	}
	return domain.TrafficBackend{}, "", false, false
}

func (r *entryPointRuntime) resolveTCPBackend() (domain.TrafficRouter, domain.TrafficBackend, bool) {
	graph := r.manager.snapshot.Load()
	if graph == nil {
		return domain.TrafficRouter{}, domain.TrafficBackend{}, false
	}
	entryPoint := r.entryPointSnapshot()
	var router domain.TrafficRouter
	for _, candidate := range graph.Routers {
		if candidate.EntryPoint == entryPoint.Name && candidate.Protocol == domain.RouterProtocolTCP {
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
	sni = normalizeTLSName(sni)
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
	entryPoint := r.entryPointSnapshot()
	for _, router := range graph.Routers {
		if router.EntryPoint == entryPoint.Name && router.Protocol == domain.RouterProtocolTLSPassthrough && normalizeTLSName(router.Rule.SNI) == sni {
			return router, true
		}
	}
	return domain.TrafficRouter{}, false
}

func (r *entryPointRuntime) findWildcardTLSRouter(graph *domain.TrafficGraph, sni string) (domain.TrafficRouter, bool) {
	entryPoint := r.entryPointSnapshot()
	for _, router := range graph.Routers {
		wildcard := normalizeTLSName(router.Rule.SNI)
		if router.EntryPoint == entryPoint.Name && router.Protocol == domain.RouterProtocolTLSPassthrough && hostMatchesTLSWildcard(sni, wildcard) {
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
	prefix, ok := strings.CutSuffix(host, "."+suffix)
	return ok && prefix != "" && !strings.Contains(prefix, ".")
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
	return io.Copy(deadlineWriter{Conn: dst, idleTimeout: idleTimeout}, idleConn{Conn: src, idleTimeout: idleTimeout})
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

type deadlineWriter struct {
	net.Conn
	idleTimeout time.Duration
}

func (c deadlineWriter) Write(p []byte) (int, error) {
	if err := c.SetWriteDeadline(time.Now().Add(c.idleTimeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
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

func (r *entryPointRuntime) setBackend(conn *trackedTCPConn, backend net.Conn) {
	r.mu.Lock()
	_, tracked := r.activeConns[conn]
	r.mu.Unlock()
	if !tracked {
		_ = backend.Close()
		return
	}
	conn.setBackend(backend)
}

func (r *entryPointRuntime) untrack(conn *trackedTCPConn) {
	r.mu.Lock()
	delete(r.activeConns, conn)
	r.mu.Unlock()
}

func (r *entryPointRuntime) stop(ctx context.Context, drainTimeout time.Duration) {
	if r.closed.CompareAndSwap(false, true) {
		entryPoint := r.entryPointSnapshot()
		trafficInfo(ctx).Str("entrypoint", entryPoint.Name).Str("address", entryPoint.Address).Msg("stopping tcp traffic entrypoint")
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
	r.stopTLSHTTPServer(ctx, drainTimeout)
	r.stopSmartTCPHTTPServer(ctx, drainTimeout)
	r.stopSmartTCPTLSServer(ctx, drainTimeout)
	if r.waitActive(ctx, drainTimeout) {
		trafficInfo(ctx).Str("entrypoint", r.entryPointSnapshot().Name).Msg("stopped tcp traffic entrypoint")
		return
	}
	trafficDebug(ctx).Str("entrypoint", r.entryPointSnapshot().Name).Dur("drain_timeout", drainTimeout).Msg("forcing tcp traffic entrypoint drain")
	r.closeActiveConns()
	select {
	case <-r.activeDone():
		trafficInfo(ctx).Str("entrypoint", r.entryPointSnapshot().Name).Msg("stopped tcp traffic entrypoint")
	case <-ctx.Done():
	}
}

func (r *entryPointRuntime) stopTLSHTTPServer(ctx context.Context, drainTimeout time.Duration) {
	r.tlsHTTPReplaceMu.Lock()
	defer r.tlsHTTPReplaceMu.Unlock()
	r.stopTLSHTTPServerLocked(ctx, drainTimeout)
}

func (r *entryPointRuntime) stopTLSHTTPServerLocked(ctx context.Context, drainTimeout time.Duration) {
	r.mu.Lock()
	listener := r.tlsHTTPListener
	server := r.tlsHTTPServer
	done := r.tlsHTTPDone
	r.tlsHTTPListener = nil
	r.tlsHTTPServer = nil
	r.tlsHTTPDone = nil
	r.mu.Unlock()

	if listener == nil || server == nil || done == nil {
		return
	}
	_ = listener.Close()
	if drainTimeout <= 0 {
		drainTimeout = defaultTCPOptions().DrainTimeout
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, drainTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		trafficDebug(ctx).Err(err).Str("entrypoint", r.entryPointSnapshot().Name).Msg("forcing tls mux https server close")
		_ = server.Close()
	}
	select {
	case <-done:
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
		conn.close()
	}
}

func (r *entryPointRuntime) matches(entryPoint domain.EntryPoint) bool {
	current := r.entryPointSnapshot()
	return current.Name == entryPoint.Name && current.Address == entryPoint.Address && current.Protocol == entryPoint.Protocol && trustedCIDRsEqual(current.TrustedCIDRs, entryPoint.TrustedCIDRs) && current.RawFallback == entryPoint.RawFallback && trustedCIDRsEqual(current.RawFallbackTrustedCIDRs, entryPoint.RawFallbackTrustedCIDRs) && current.AllowPublicRawFallback == entryPoint.AllowPublicRawFallback
}

func (r *entryPointRuntime) sameAddress(entryPoint domain.EntryPoint) bool {
	return r.entryPointSnapshot().Address == entryPoint.Address
}

func (r *entryPointRuntime) updateEntryPoint(entryPoint domain.EntryPoint, trusted []*net.IPNet, rawTrusted []*net.IPNet) {
	r.mu.Lock()
	previousName := r.entryPoint.Name
	previousProtocol := r.entryPoint.Protocol
	r.entryPoint = entryPoint
	r.trusted = trusted
	r.rawFallbackTrusted = rawTrusted
	stale := make([]*trackedTCPConn, 0)
	for conn := range r.activeConns {
		if !trustedRemoteAddr(trusted, conn.client.RemoteAddr()) || rawFallbackConnStale(conn, entryPoint, rawTrusted) {
			stale = append(stale, conn)
		}
	}
	r.mu.Unlock()
	for _, conn := range stale {
		conn.close()
	}
	if previousProtocol == domain.EntryPointProtocolTLSMux && entryPoint.Protocol != domain.EntryPointProtocolTLSMux {
		r.stopTLSHTTPServer(r.ctx, 0)
	}
	if previousProtocol == domain.EntryPointProtocolSmartTCP && entryPoint.Protocol != domain.EntryPointProtocolSmartTCP {
		r.stopSmartTCPHTTPServer(r.ctx, 0)
		r.stopSmartTCPTLSServer(r.ctx, 0)
	}
	if entryPoint.Protocol == domain.EntryPointProtocolSmartTCP {
		r.refreshSmartTCPHTTPServer(entryPoint.Name)
		r.refreshSmartTCPTLSServer(entryPoint.Name)
	}
	if entryPoint.Protocol == domain.EntryPointProtocolTLSMux {
		if previousProtocol == domain.EntryPointProtocolTLSMux && previousName != entryPoint.Name {
			r.refreshTLSHTTPServer(entryPoint.Name)
			return
		}
		r.startTLSHTTPServer(entryPoint)
	}
}

func rawFallbackConnStale(conn *trackedTCPConn, entryPoint domain.EntryPoint, rawTrusted []*net.IPNet) bool {
	rawFallbackName, ok := conn.rawFallback()
	if !ok {
		return false
	}
	if entryPoint.Protocol != domain.EntryPointProtocolSmartTCP || entryPoint.RawFallback == "" || entryPoint.RawFallback != rawFallbackName {
		return true
	}
	if entryPoint.AllowPublicRawFallback {
		return false
	}
	return len(rawTrusted) == 0 || !trustedRemoteAddr(rawTrusted, conn.client.RemoteAddr())
}

func (r *entryPointRuntime) entryPointSnapshot() domain.EntryPoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.entryPoint
}

func (r *entryPointRuntime) trustedSnapshot() []*net.IPNet {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.trusted
}

func (r *entryPointRuntime) rawFallbackTrustedSnapshot() []*net.IPNet {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rawFallbackTrusted
}

func (r *entryPointRuntime) isClosed() bool {
	return r.closed.Load()
}

func backendFromAddress(name string, address string) (domain.TrafficBackend, error) {
	host, portValue, err := net.SplitHostPort(address)
	if err != nil {
		return domain.TrafficBackend{}, fmt.Errorf("split backend address %q: %w", address, err)
	}
	port, err := strconv.Atoi(portValue)
	if err != nil {
		return domain.TrafficBackend{}, fmt.Errorf("parse backend port %q from address %q: %w", portValue, address, err)
	}
	return domain.TrafficBackend{Name: name, Host: host, Port: port, Protocol: domain.NetworkProtocolTCP}, nil
}

func serviceRef(name string, portName string) string {
	return fmt.Sprintf("network_service:%s:%s", name, portName)
}
