package traffic

import (
	"context"
	"crypto/tls"
	"errors"
	"maps"
	"net"
	"net/http"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

type SmartTCPHTTPServerConfig struct {
	Handler   http.Handler
	Protocols *http.Protocols
}

type SmartTCPTLSServerConfig struct {
	Handler   http.Handler
	TLSConfig *tls.Config
}

type smartHTTPServers map[string]SmartTCPHTTPServerConfig
type smartTLSServers map[string]SmartTCPTLSServerConfig

func (m *Manager) SetSmartTCPHTTPServer(entryPoint string, handler http.Handler, protocols *http.Protocols) {
	m.mu.Lock()
	current := m.loadSmartHTTPServers()
	next := make(smartHTTPServers, len(current)+1)
	maps.Copy(next, current)
	if handler == nil {
		delete(next, entryPoint)
	} else {
		next[entryPoint] = SmartTCPHTTPServerConfig{Handler: handler, Protocols: protocols}
	}
	m.smartHTTPServers.Store(next)
	runtimes := m.smartTCPRuntimesLocked(entryPoint)
	m.mu.Unlock()
	for _, runtime := range runtimes {
		runtime.refreshSmartTCPHTTPServer(entryPoint)
	}
}

func (m *Manager) SetSmartTCPTLSServer(entryPoint string, handler http.Handler, tlsConfig *tls.Config) {
	m.mu.Lock()
	current := m.loadSmartTLSServers()
	next := make(smartTLSServers, len(current)+1)
	maps.Copy(next, current)
	if handler == nil || tlsConfig == nil {
		delete(next, entryPoint)
	} else {
		next[entryPoint] = SmartTCPTLSServerConfig{Handler: handler, TLSConfig: tlsConfig.Clone()}
	}
	m.smartTLSServers.Store(next)
	runtimes := m.smartTCPRuntimesLocked(entryPoint)
	m.mu.Unlock()
	for _, runtime := range runtimes {
		runtime.refreshSmartTCPTLSServer(entryPoint)
	}
}

func (m *Manager) smartTCPRuntimesLocked(entryPoint string) []*entryPointRuntime {
	runtimes := make([]*entryPointRuntime, 0, len(m.listeners))
	for _, runtime := range m.listeners {
		snapshot := runtime.entryPointSnapshot()
		if snapshot.Name == entryPoint && snapshot.Protocol == domain.EntryPointProtocolSmartTCP {
			runtimes = append(runtimes, runtime)
		}
	}
	return runtimes
}

func (m *Manager) smartHTTPServer(entryPoint string) (SmartTCPHTTPServerConfig, bool) {
	config, ok := m.loadSmartHTTPServers()[entryPoint]
	return config, ok
}

func (m *Manager) smartTLSServer(entryPoint string) (SmartTCPTLSServerConfig, bool) {
	config, ok := m.loadSmartTLSServers()[entryPoint]
	return config, ok
}

func (m *Manager) loadSmartHTTPServers() smartHTTPServers {
	value := m.smartHTTPServers.Load()
	if value == nil {
		return smartHTTPServers{}
	}
	return value.(smartHTTPServers)
}

func (m *Manager) loadSmartTLSServers() smartTLSServers {
	value := m.smartTLSServers.Load()
	if value == nil {
		return smartTLSServers{}
	}
	return value.(smartTLSServers)
}

func (r *entryPointRuntime) startSmartTCPHTTPServers(entryPoint domain.EntryPoint) {
	if entryPoint.Protocol != domain.EntryPointProtocolSmartTCP {
		return
	}
	if config, ok := r.manager.smartHTTPServer(entryPoint.Name); ok && config.Handler != nil {
		r.replaceSmartTCPHTTPServer(entryPoint, config)
	}
	if config, ok := r.manager.smartTLSServer(entryPoint.Name); ok && config.Handler != nil && config.TLSConfig != nil {
		r.replaceSmartTCPTLSServer(entryPoint, config)
	}
}

func (r *entryPointRuntime) refreshSmartTCPHTTPServer(entryPointName string) {
	entryPoint := r.entryPointSnapshot()
	if entryPoint.Name != entryPointName || entryPoint.Protocol != domain.EntryPointProtocolSmartTCP {
		return
	}
	config, ok := r.manager.smartHTTPServer(entryPointName)
	if !ok || config.Handler == nil {
		r.stopSmartTCPHTTPServer(r.ctx, 0)
		return
	}
	r.replaceSmartTCPHTTPServer(entryPoint, config)
}

func (r *entryPointRuntime) refreshSmartTCPTLSServer(entryPointName string) {
	entryPoint := r.entryPointSnapshot()
	if entryPoint.Name != entryPointName || entryPoint.Protocol != domain.EntryPointProtocolSmartTCP {
		return
	}
	config, ok := r.manager.smartTLSServer(entryPointName)
	if !ok || config.Handler == nil || config.TLSConfig == nil {
		r.stopSmartTCPTLSServer(r.ctx, 0)
		return
	}
	r.replaceSmartTCPTLSServer(entryPoint, config)
}

func (r *entryPointRuntime) replaceSmartTCPHTTPServer(entryPoint domain.EntryPoint, config SmartTCPHTTPServerConfig) {
	r.smartHTTPReplaceMu.Lock()
	defer r.smartHTTPReplaceMu.Unlock()
	r.stopSmartTCPHTTPServerLocked(r.ctx, 0)
	if r.isClosed() {
		return
	}
	listener := newTLSHTTPListener(r.listener.Addr())
	server := &http.Server{Handler: config.Handler, Protocols: config.Protocols, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan struct{})
	r.mu.Lock()
	if r.isClosed() {
		r.mu.Unlock()
		_ = listener.Close()
		return
	}
	r.smartHTTPListener = listener
	r.smartHTTPServer = server
	r.smartHTTPDone = done
	r.mu.Unlock()
	go func() {
		defer close(done)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			trafficWarn(r.ctx).Err(err).Str("entrypoint", entryPoint.Name).Msg("smart tcp http server stopped with error")
		}
	}()
}

func (r *entryPointRuntime) replaceSmartTCPTLSServer(entryPoint domain.EntryPoint, config SmartTCPTLSServerConfig) {
	r.smartTLSReplaceMu.Lock()
	defer r.smartTLSReplaceMu.Unlock()
	r.stopSmartTCPTLSServerLocked(r.ctx, 0)
	if r.isClosed() {
		return
	}
	listener := newTLSHTTPListener(r.listener.Addr())
	server := &http.Server{Handler: config.Handler, TLSConfig: config.TLSConfig.Clone(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan struct{})
	r.mu.Lock()
	if r.isClosed() {
		r.mu.Unlock()
		_ = listener.Close()
		return
	}
	r.smartTLSListener = listener
	r.smartTLSServer = server
	r.smartTLSDone = done
	r.mu.Unlock()
	go func() {
		defer close(done)
		if err := server.ServeTLS(listener, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			trafficWarn(r.ctx).Err(err).Str("entrypoint", entryPoint.Name).Msg("smart tcp https server stopped with error")
		}
	}()
}

func (r *entryPointRuntime) stopSmartTCPHTTPServer(ctx context.Context, drainTimeout time.Duration) {
	r.smartHTTPReplaceMu.Lock()
	defer r.smartHTTPReplaceMu.Unlock()
	r.stopSmartTCPHTTPServerLocked(ctx, drainTimeout)
}

func (r *entryPointRuntime) stopSmartTCPHTTPServerLocked(ctx context.Context, drainTimeout time.Duration) {
	r.mu.Lock()
	listener := r.smartHTTPListener
	server := r.smartHTTPServer
	done := r.smartHTTPDone
	r.smartHTTPListener = nil
	r.smartHTTPServer = nil
	r.smartHTTPDone = nil
	r.mu.Unlock()
	stopInternalHTTPServer(ctx, listener, server, done, drainTimeout)
}

func (r *entryPointRuntime) stopSmartTCPTLSServer(ctx context.Context, drainTimeout time.Duration) {
	r.smartTLSReplaceMu.Lock()
	defer r.smartTLSReplaceMu.Unlock()
	r.stopSmartTCPTLSServerLocked(ctx, drainTimeout)
}

func (r *entryPointRuntime) stopSmartTCPTLSServerLocked(ctx context.Context, drainTimeout time.Duration) {
	r.mu.Lock()
	listener := r.smartTLSListener
	server := r.smartTLSServer
	done := r.smartTLSDone
	r.smartTLSListener = nil
	r.smartTLSServer = nil
	r.smartTLSDone = nil
	r.mu.Unlock()
	stopInternalHTTPServer(ctx, listener, server, done, drainTimeout)
}

func stopInternalHTTPServer(ctx context.Context, listener *tlsHTTPListener, server *http.Server, done chan struct{}, drainTimeout time.Duration) {
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
		_ = server.Close()
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}
