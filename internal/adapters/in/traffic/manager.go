// Package traffic owns Gordon's runtime traffic entrypoints.
package traffic

import (
	"context"
	"fmt"
	"maps"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/rs/zerolog"

	"github.com/bnema/gordon/internal/domain"
)

const (
	reloadStatusOK    = "ok"
	reloadStatusError = "error"
)

func trafficLog(ctx context.Context) zerowrap.Logger {
	return zerowrap.FromCtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "traffic",
	})
}

func trafficInfo(ctx context.Context) *zerolog.Event {
	log := trafficLog(ctx)
	return log.Info()
}

func trafficDebug(ctx context.Context) *zerolog.Event {
	log := trafficLog(ctx)
	return log.Debug()
}

func trafficWarn(ctx context.Context) *zerolog.Event {
	log := trafficLog(ctx)
	return log.Warn()
}

// Manager owns runtime entrypoints for the traffic plane.
type Manager struct {
	mu           sync.Mutex
	snapshot     atomic.Pointer[domain.TrafficGraph]
	listeners    map[string]*entryPointRuntime
	udpListeners map[string]*udpEntryPointRuntime

	lastReloadStatus string
	lastReloadError  string
	tlsHTTPServers   atomic.Value
	smartHTTPServers atomic.Value
	smartTLSServers  atomic.Value
}

// NewManager creates a traffic manager.
func NewManager() *Manager {
	manager := &Manager{
		listeners:        map[string]*entryPointRuntime{},
		udpListeners:     map[string]*udpEntryPointRuntime{},
		lastReloadStatus: reloadStatusOK,
	}
	manager.tlsHTTPServers.Store(tlsHTTPServers{})
	manager.smartHTTPServers.Store(smartHTTPServers{})
	manager.smartTLSServers.Store(smartTLSServers{})
	return manager
}

// Apply validates and applies a new traffic graph snapshot.
func (m *Manager) Apply(ctx context.Context, graph *domain.TrafficGraph) error {
	if graph == nil {
		return m.recordReloadError(ctx, fmt.Errorf("%w: traffic graph is required", domain.ErrTrafficGraphRequired))
	}
	if err := graph.Validate(); err != nil {
		return m.recordReloadError(ctx, fmt.Errorf("validate traffic graph: %w", err))
	}
	trafficDebug(ctx).Int("entrypoints", len(graph.EntryPoints)).Int("routers", len(graph.Routers)).Int("services", len(graph.Services)).Msg("applying traffic graph")

	nextGraph := cloneTrafficGraph(*graph)
	m.mu.Lock()
	newListeners, createdListeners, tcpUpdates, err := m.prepareTCPListeners(ctx, &nextGraph)
	if err != nil {
		m.lastReloadStatus = reloadStatusError
		m.lastReloadError = err.Error()
		m.mu.Unlock()
		trafficWarn(ctx).Err(err).Msg("failed to prepare tcp traffic listeners")
		return err
	}
	newUDPListeners, createdUDPListeners, udpUpdates, err := m.prepareUDPListeners(ctx, &nextGraph)
	if err != nil {
		m.lastReloadStatus = reloadStatusError
		m.lastReloadError = err.Error()
		m.mu.Unlock()
		for _, runtime := range createdListeners {
			runtime.stop(ctx, effectiveTCPOptions(snapshotTCPOptions(&nextGraph)).DrainTimeout)
		}
		trafficWarn(ctx).Err(err).Msg("failed to prepare udp traffic listeners")
		return err
	}

	for _, update := range tcpUpdates {
		update.runtime.updateEntryPoint(update.entryPoint, update.trusted, update.rawTrusted)
	}
	for _, update := range udpUpdates {
		update.runtime.updateEntryPoint(update.entryPoint, update.trusted)
	}

	oldListeners := m.listeners
	oldUDPListeners := m.udpListeners
	m.listeners = newListeners
	m.udpListeners = newUDPListeners
	m.snapshot.Store(&nextGraph)
	for _, runtime := range createdListeners {
		runtime.start()
	}
	for _, runtime := range createdUDPListeners {
		runtime.start()
	}
	m.lastReloadStatus = reloadStatusOK
	m.lastReloadError = ""
	m.mu.Unlock()

	tcpDrainTimeout := effectiveTCPOptions(snapshotTCPOptions(&nextGraph)).DrainTimeout
	stoppedTCP := 0
	for _, runtime := range oldListeners {
		if tcpRuntimeRetained(newListeners, runtime) {
			continue
		}
		stoppedTCP++
		runtime.stop(ctx, tcpDrainTimeout)
	}
	udpDrainTimeout := effectiveUDPOptions(snapshotUDPOptions(&nextGraph)).DrainTimeout
	stoppedUDP := 0
	for _, runtime := range oldUDPListeners {
		if udpRuntimeRetained(newUDPListeners, runtime) {
			if backend, ok := runtime.resolveUDPBackend(); ok {
				runtime.drainSessionsNotMatchingAfter(backend, udpDrainTimeout)
			} else {
				runtime.drainSessionsAfter(udpDrainTimeout)
			}
			continue
		}
		stoppedUDP++
		runtime.stop(ctx, udpDrainTimeout)
	}
	logAppliedTrafficGraph(ctx, newListeners, newUDPListeners, createdListeners, createdUDPListeners, stoppedTCP, stoppedUDP)
	return nil
}

func tcpRuntimeRetained(listeners map[string]*entryPointRuntime, runtime *entryPointRuntime) bool {
	for _, candidate := range listeners {
		if candidate == runtime {
			return true
		}
	}
	return false
}

func udpRuntimeRetained(listeners map[string]*udpEntryPointRuntime, runtime *udpEntryPointRuntime) bool {
	for _, candidate := range listeners {
		if candidate == runtime {
			return true
		}
	}
	return false
}

func logAppliedTrafficGraph(
	ctx context.Context,
	newListeners map[string]*entryPointRuntime,
	newUDPListeners map[string]*udpEntryPointRuntime,
	createdListeners []*entryPointRuntime,
	createdUDPListeners []*udpEntryPointRuntime,
	stoppedTCP int,
	stoppedUDP int,
) {
	logEvent := trafficInfo(ctx)
	if len(newListeners) == 0 && len(newUDPListeners) == 0 && stoppedTCP == 0 && stoppedUDP == 0 {
		logEvent = trafficDebug(ctx)
	}
	logEvent.
		Int("tcp_listeners", len(newListeners)).
		Int("udp_listeners", len(newUDPListeners)).
		Int("created_tcp_listeners", len(createdListeners)).
		Int("created_udp_listeners", len(createdUDPListeners)).
		Int("stopped_tcp_listeners", stoppedTCP).
		Int("stopped_udp_listeners", stoppedUDP).
		Msg("traffic graph applied")
}

// Shutdown stops all listeners and active TCP streams.
func (m *Manager) Shutdown(ctx context.Context) error {
	trafficInfo(ctx).Msg("shutting down traffic manager")
	m.mu.Lock()
	defer m.mu.Unlock()

	listeners := m.listeners
	udpListeners := m.udpListeners
	m.listeners = map[string]*entryPointRuntime{}
	m.udpListeners = map[string]*udpEntryPointRuntime{}
	graph := m.snapshot.Load()
	m.snapshot.Store(nil)

	tcpDrainTimeout := defaultTCPOptions().DrainTimeout
	udpDrainTimeout := defaultUDPOptions().DrainTimeout
	if graph != nil {
		tcpDrainTimeout = effectiveTCPOptions(graph.Options.TCP).DrainTimeout
		udpDrainTimeout = effectiveUDPOptions(graph.Options.UDP).DrainTimeout
	}
	for _, runtime := range listeners {
		runtime.stop(ctx, tcpDrainTimeout)
	}
	for _, runtime := range udpListeners {
		runtime.stop(ctx, udpDrainTimeout)
	}
	trafficInfo(ctx).Int("tcp_listeners", len(listeners)).Int("udp_listeners", len(udpListeners)).Msg("traffic manager shut down")
	return nil
}

// Status returns a consistent snapshot of manager state and counters.
func (m *Manager) Status() domain.TrafficStatus {
	m.mu.Lock()
	listeners := make(map[string]*entryPointRuntime, len(m.listeners))
	maps.Copy(listeners, m.listeners)
	udpListeners := make(map[string]*udpEntryPointRuntime, len(m.udpListeners))
	maps.Copy(udpListeners, m.udpListeners)
	status := domain.TrafficStatus{LastReloadStatus: m.lastReloadStatus, LastReloadError: m.lastReloadError}
	graph := m.snapshot.Load()
	m.mu.Unlock()

	if graph == nil {
		return status
	}

	status.EntryPoints = entryPointStatuses(graph.EntryPoints, listeners, udpListeners)
	status.Routers = routerStatuses(graph.Routers, listeners, udpListeners)
	status.Services = serviceStatuses(graph.Services)
	status.Counters = aggregateCounters(status.EntryPoints)
	return status
}

func (m *Manager) recordReloadError(ctx context.Context, err error) error {
	m.mu.Lock()
	m.lastReloadStatus = reloadStatusError
	m.lastReloadError = err.Error()
	m.mu.Unlock()
	trafficWarn(ctx).Err(err).Msg("traffic graph rejected")
	return err
}

type tcpRuntimeUpdate struct {
	runtime    *entryPointRuntime
	entryPoint domain.EntryPoint
	trusted    []*net.IPNet
	rawTrusted []*net.IPNet
}

type udpRuntimeUpdate struct {
	runtime    *udpEntryPointRuntime
	entryPoint domain.EntryPoint
	trusted    []*net.IPNet
}

func (m *Manager) prepareTCPListeners(ctx context.Context, graph *domain.TrafficGraph) (map[string]*entryPointRuntime, []*entryPointRuntime, []tcpRuntimeUpdate, error) {
	current := make(map[string]*entryPointRuntime, len(m.listeners))
	maps.Copy(current, m.listeners)

	next := make(map[string]*entryPointRuntime, len(current))
	created := []*entryPointRuntime{}
	updates := []tcpRuntimeUpdate{}
	for _, entryPoint := range graph.EntryPoints {
		if !isTCPListenerProtocol(entryPoint.Protocol) {
			continue
		}
		if runtime := current[entryPoint.Name]; runtime != nil && runtime.matches(entryPoint) {
			next[entryPoint.Name] = runtime
			delete(current, entryPoint.Name)
			continue
		}
		if runtime := conflictingTCPRuntime(current, entryPoint); runtime != nil {
			trusted, err := parseTrustedCIDRs(entryPoint.TrustedCIDRs)
			if err != nil {
				stopTCPRuntimes(ctx, created, effectiveTCPOptions(graph.Options.TCP).DrainTimeout)
				return nil, nil, nil, fmt.Errorf("parse trusted cidrs for tcp entrypoint %q: %w", entryPoint.Name, err)
			}
			rawTrusted, err := parseTrustedCIDRs(entryPoint.RawFallbackTrustedCIDRs)
			if err != nil {
				stopTCPRuntimes(ctx, created, effectiveTCPOptions(graph.Options.TCP).DrainTimeout)
				return nil, nil, nil, fmt.Errorf("parse raw fallback trusted cidrs for tcp entrypoint %q: %w", entryPoint.Name, err)
			}
			trafficDebug(ctx).Str("entrypoint", entryPoint.Name).Str("address", entryPoint.Address).Msg("reusing tcp traffic listener for same-address entrypoint update")
			updates = append(updates, tcpRuntimeUpdate{runtime: runtime, entryPoint: entryPoint, trusted: trusted, rawTrusted: rawTrusted})
			next[entryPoint.Name] = runtime
			delete(current, runtime.entryPointSnapshot().Name)
			continue
		}
		runtime, err := m.bindTCPEntryPoint(ctx, entryPoint)
		if err != nil {
			stopTCPRuntimes(ctx, created, effectiveTCPOptions(graph.Options.TCP).DrainTimeout)
			return nil, nil, nil, err
		}
		next[entryPoint.Name] = runtime
		created = append(created, runtime)
	}
	return next, created, updates, nil
}

func stopTCPRuntimes(ctx context.Context, runtimes []*entryPointRuntime, drainTimeout time.Duration) {
	for _, runtime := range runtimes {
		runtime.stop(ctx, drainTimeout)
	}
}

func stopUDPRuntimes(ctx context.Context, runtimes []*udpEntryPointRuntime, drainTimeout time.Duration) {
	for _, runtime := range runtimes {
		runtime.stop(ctx, drainTimeout)
	}
}

func isTCPListenerProtocol(protocol domain.EntryPointProtocol) bool {
	return protocol == domain.EntryPointProtocolTCP || protocol == domain.EntryPointProtocolTLSMux || protocol == domain.EntryPointProtocolSmartTCP
}

func conflictingTCPRuntime(current map[string]*entryPointRuntime, entryPoint domain.EntryPoint) *entryPointRuntime {
	for _, runtime := range current {
		if runtime.sameAddress(entryPoint) {
			return runtime
		}
	}
	return nil
}

func (m *Manager) bindTCPEntryPoint(ctx context.Context, entryPoint domain.EntryPoint) (*entryPointRuntime, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", entryPoint.Address)
	if err != nil {
		return nil, fmt.Errorf("bind tcp entrypoint %q on %s: %w", entryPoint.Name, entryPoint.Address, err)
	}
	trusted, err := parseTrustedCIDRs(entryPoint.TrustedCIDRs)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("parse trusted cidrs for tcp entrypoint %q: %w", entryPoint.Name, err)
	}
	if _, err := parseTrustedCIDRs(entryPoint.RawFallbackTrustedCIDRs); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("parse raw fallback trusted cidrs for tcp entrypoint %q: %w", entryPoint.Name, err)
	}
	trafficInfo(ctx).Str("entrypoint", entryPoint.Name).Str("address", entryPoint.Address).Str("protocol", string(entryPoint.Protocol)).Msg("bound tcp traffic entrypoint")
	runtime := newEntryPointRuntime(ctx, m, entryPoint, listener, trusted)
	return runtime, nil
}

func (m *Manager) prepareUDPListeners(ctx context.Context, graph *domain.TrafficGraph) (map[string]*udpEntryPointRuntime, []*udpEntryPointRuntime, []udpRuntimeUpdate, error) {
	current := make(map[string]*udpEntryPointRuntime, len(m.udpListeners))
	maps.Copy(current, m.udpListeners)

	next := make(map[string]*udpEntryPointRuntime, len(current))
	created := []*udpEntryPointRuntime{}
	updates := []udpRuntimeUpdate{}
	for _, entryPoint := range graph.EntryPoints {
		if entryPoint.Protocol != domain.EntryPointProtocolUDP {
			continue
		}
		if runtime := current[entryPoint.Name]; runtime != nil && runtime.matches(entryPoint) {
			next[entryPoint.Name] = runtime
			delete(current, entryPoint.Name)
			continue
		}
		if runtime := conflictingUDPRuntime(current, entryPoint); runtime != nil {
			trusted, err := parseTrustedCIDRs(entryPoint.TrustedCIDRs)
			if err != nil {
				stopUDPRuntimes(ctx, created, effectiveUDPOptions(graph.Options.UDP).DrainTimeout)
				return nil, nil, nil, fmt.Errorf("parse trusted cidrs for udp entrypoint %q: %w", entryPoint.Name, err)
			}
			trafficDebug(ctx).Str("entrypoint", entryPoint.Name).Str("address", entryPoint.Address).Msg("reusing udp traffic listener for same-address entrypoint update")
			updates = append(updates, udpRuntimeUpdate{runtime: runtime, entryPoint: entryPoint, trusted: trusted})
			next[entryPoint.Name] = runtime
			delete(current, runtime.entryPointSnapshot().Name)
			continue
		}
		runtime, err := m.bindUDPEntryPoint(ctx, entryPoint)
		if err != nil {
			stopUDPRuntimes(ctx, created, effectiveUDPOptions(graph.Options.UDP).DrainTimeout)
			return nil, nil, nil, err
		}
		next[entryPoint.Name] = runtime
		created = append(created, runtime)
	}
	return next, created, updates, nil
}

func conflictingUDPRuntime(current map[string]*udpEntryPointRuntime, entryPoint domain.EntryPoint) *udpEntryPointRuntime {
	for _, runtime := range current {
		if runtime.sameAddress(entryPoint) {
			return runtime
		}
	}
	return nil
}

func (m *Manager) bindUDPEntryPoint(ctx context.Context, entryPoint domain.EntryPoint) (*udpEntryPointRuntime, error) {
	packetConn, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp", entryPoint.Address)
	if err != nil {
		return nil, fmt.Errorf("bind udp entrypoint %q on %s: %w", entryPoint.Name, entryPoint.Address, err)
	}
	trusted, err := parseTrustedCIDRs(entryPoint.TrustedCIDRs)
	if err != nil {
		_ = packetConn.Close()
		return nil, fmt.Errorf("parse trusted cidrs for udp entrypoint %q: %w", entryPoint.Name, err)
	}
	trafficInfo(ctx).Str("entrypoint", entryPoint.Name).Str("address", entryPoint.Address).Str("protocol", string(entryPoint.Protocol)).Msg("bound udp traffic entrypoint")
	return newUDPEntryPointRuntime(ctx, m, entryPoint, packetConn, trusted), nil
}

func parseTrustedCIDRs(values []string) ([]*net.IPNet, error) {
	if len(values) == 0 {
		return nil, nil
	}
	trusted := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(strings.TrimSpace(value))
		if err != nil {
			return nil, err
		}
		trusted = append(trusted, network)
	}
	return trusted, nil
}

func trustedCIDRsEqual(left []string, right []string) bool {
	left = normalizedCIDRs(left)
	right = normalizedCIDRs(right)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func normalizedCIDRs(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strings.TrimSpace(value))
	}
	sort.Strings(out)
	return out
}

func trustedRemoteAddr(trusted []*net.IPNet, addr net.Addr) bool {
	if len(trusted) == 0 {
		return true
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, network := range trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func entryPointStatuses(entries []domain.EntryPoint, listeners map[string]*entryPointRuntime, udpListeners map[string]*udpEntryPointRuntime) []domain.EntryPointStatus {
	statuses := make([]domain.EntryPointStatus, 0, len(entries))
	for _, entry := range entries {
		status := domain.EntryPointStatus{Name: entry.Name, Address: entry.Address, Protocol: entry.Protocol}
		if runtime := listeners[entry.Name]; runtime != nil {
			counters := runtime.counters.snapshot()
			status.Active = !runtime.isClosed()
			status.ActiveTCPConnections = counters.ActiveTCPConnections
			status.TotalAccepted = counters.TotalAccepted
			status.TotalRefused = counters.TotalRefused
			status.TotalErrors = counters.TotalErrors
			status.BytesIn = counters.BytesIn
			status.BytesOut = counters.BytesOut
			status.SmartTCP = counters.SmartTCP
		}
		if runtime := udpListeners[entry.Name]; runtime != nil {
			counters := runtime.counters.snapshot()
			status.Active = !runtime.isClosed()
			status.ActiveUDPSessions = counters.ActiveUDPSessions
			status.TotalAccepted = counters.TotalAccepted
			status.TotalRefused = counters.TotalRefused
			status.TotalErrors = counters.TotalErrors
			status.BytesIn = counters.BytesIn
			status.BytesOut = counters.BytesOut
		}
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return statuses
}

func routerStatuses(routers []domain.TrafficRouter, listeners map[string]*entryPointRuntime, udpListeners map[string]*udpEntryPointRuntime) []domain.TrafficRouterStatus {
	statuses := make([]domain.TrafficRouterStatus, 0, len(routers))
	for _, router := range routers {
		tcpActive := false
		if runtime := listeners[router.EntryPoint]; runtime != nil {
			tcpActive = !runtime.isClosed()
		}
		udpActive := false
		if runtime := udpListeners[router.EntryPoint]; runtime != nil {
			udpActive = !runtime.isClosed()
		}
		statuses = append(statuses, domain.TrafficRouterStatus{
			Name: router.Name, EntryPoint: router.EntryPoint, Protocol: router.Protocol,
			Rule: router.Rule, Service: router.Service, Active: tcpActive || udpActive,
		})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return statuses
}

func serviceStatuses(services []domain.TrafficService) []domain.TrafficServiceStatus {
	statuses := make([]domain.TrafficServiceStatus, 0, len(services))
	for _, service := range services {
		status := domain.TrafficServiceStatus{Name: service.Name, Active: true, Backends: make([]domain.TrafficBackendStatus, 0, len(service.Backends))}
		for _, backend := range service.Backends {
			status.Backends = append(status.Backends, domain.TrafficBackendStatus{Name: backend.Name, Host: backend.Host, Port: backend.Port, Protocol: backend.Protocol, Active: true})
		}
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return statuses
}

func aggregateCounters(entries []domain.EntryPointStatus) domain.TrafficCounters {
	var counters domain.TrafficCounters
	for _, entry := range entries {
		counters.ActiveTCPConnections += entry.ActiveTCPConnections
		counters.ActiveUDPSessions += entry.ActiveUDPSessions
		counters.TotalAccepted += entry.TotalAccepted
		counters.TotalRefused += entry.TotalRefused
		counters.TotalErrors += entry.TotalErrors
		counters.BytesIn += entry.BytesIn
		counters.BytesOut += entry.BytesOut
		counters.SmartTCP.HTTPAccepted += entry.SmartTCP.HTTPAccepted
		counters.SmartTCP.H2CAccepted += entry.SmartTCP.H2CAccepted
		counters.SmartTCP.HTTPSFallbackAccepted += entry.SmartTCP.HTTPSFallbackAccepted
		counters.SmartTCP.TLSPassthroughAccepted += entry.SmartTCP.TLSPassthroughAccepted
		counters.SmartTCP.RawFallbackAccepted += entry.SmartTCP.RawFallbackAccepted
		counters.SmartTCP.EntrypointCIDRRefused += entry.SmartTCP.EntrypointCIDRRefused
		counters.SmartTCP.RawFallbackCIDRRefused += entry.SmartTCP.RawFallbackCIDRRefused
		counters.SmartTCP.PROXYRefused += entry.SmartTCP.PROXYRefused
		counters.SmartTCP.UnknownNoFallbackRefused += entry.SmartTCP.UnknownNoFallbackRefused
		counters.SmartTCP.MalformedRejected += entry.SmartTCP.MalformedRejected
		counters.SmartTCP.SniffTimeout += entry.SmartTCP.SniffTimeout
		counters.SmartTCP.ClientHelloTooLarge += entry.SmartTCP.ClientHelloTooLarge
	}
	return counters
}

func cloneTrafficGraph(graph domain.TrafficGraph) domain.TrafficGraph {
	clone := graph
	clone.EntryPoints = append([]domain.EntryPoint{}, graph.EntryPoints...)
	for i := range clone.EntryPoints {
		clone.EntryPoints[i].TrustedCIDRs = append([]string(nil), graph.EntryPoints[i].TrustedCIDRs...)
		clone.EntryPoints[i].RawFallbackTrustedCIDRs = append([]string(nil), graph.EntryPoints[i].RawFallbackTrustedCIDRs...)
	}
	clone.Routers = append([]domain.TrafficRouter{}, graph.Routers...)
	clone.Services = append([]domain.TrafficService{}, graph.Services...)
	for i := range clone.Services {
		clone.Services[i].Backends = append([]domain.TrafficBackend{}, graph.Services[i].Backends...)
	}
	return clone
}

func snapshotTCPOptions(graph *domain.TrafficGraph) domain.TCPOptions {
	if graph == nil {
		return domain.TCPOptions{}
	}
	return graph.Options.TCP
}

func snapshotUDPOptions(graph *domain.TrafficGraph) domain.UDPOptions {
	if graph == nil {
		return domain.UDPOptions{}
	}
	return graph.Options.UDP
}

type trafficCounters struct {
	activeTCPConnections atomic.Int64
	activeUDPSessions    atomic.Int64
	totalAccepted        atomic.Int64
	totalRefused         atomic.Int64
	totalErrors          atomic.Int64
	bytesIn              atomic.Int64
	bytesOut             atomic.Int64
	smartTCP             smartTCPCounterSet
}

type smartTCPCounterSet struct {
	httpAccepted             atomic.Int64
	h2cAccepted              atomic.Int64
	httpsFallbackAccepted    atomic.Int64
	tlsPassthroughAccepted   atomic.Int64
	rawFallbackAccepted      atomic.Int64
	entrypointCIDRRefused    atomic.Int64
	rawFallbackCIDRRefused   atomic.Int64
	proxyRefused             atomic.Int64
	unknownNoFallbackRefused atomic.Int64
	malformedRejected        atomic.Int64
	sniffTimeout             atomic.Int64
	clientHelloTooLarge      atomic.Int64
}

func (c *trafficCounters) snapshot() domain.TrafficCounters {
	return domain.TrafficCounters{
		ActiveTCPConnections: c.activeTCPConnections.Load(),
		ActiveUDPSessions:    c.activeUDPSessions.Load(),
		TotalAccepted:        c.totalAccepted.Load(),
		TotalRefused:         c.totalRefused.Load(),
		TotalErrors:          c.totalErrors.Load(),
		BytesIn:              c.bytesIn.Load(),
		BytesOut:             c.bytesOut.Load(),
		SmartTCP: domain.SmartTCPCounters{
			HTTPAccepted:             c.smartTCP.httpAccepted.Load(),
			H2CAccepted:              c.smartTCP.h2cAccepted.Load(),
			HTTPSFallbackAccepted:    c.smartTCP.httpsFallbackAccepted.Load(),
			TLSPassthroughAccepted:   c.smartTCP.tlsPassthroughAccepted.Load(),
			RawFallbackAccepted:      c.smartTCP.rawFallbackAccepted.Load(),
			EntrypointCIDRRefused:    c.smartTCP.entrypointCIDRRefused.Load(),
			RawFallbackCIDRRefused:   c.smartTCP.rawFallbackCIDRRefused.Load(),
			PROXYRefused:             c.smartTCP.proxyRefused.Load(),
			UnknownNoFallbackRefused: c.smartTCP.unknownNoFallbackRefused.Load(),
			MalformedRejected:        c.smartTCP.malformedRejected.Load(),
			SniffTimeout:             c.smartTCP.sniffTimeout.Load(),
			ClientHelloTooLarge:      c.smartTCP.clientHelloTooLarge.Load(),
		},
	}
}

func defaultTCPOptions() domain.TCPOptions {
	return domain.TCPOptions{
		DialTimeout:    10 * time.Second,
		IdleTimeout:    5 * time.Minute,
		DrainTimeout:   30 * time.Second,
		MaxConnections: 1024,
	}
}

func effectiveTCPOptions(options domain.TCPOptions) domain.TCPOptions {
	defaults := defaultTCPOptions()
	if options.DialTimeout == 0 {
		options.DialTimeout = defaults.DialTimeout
	}
	if options.IdleTimeout == 0 {
		options.IdleTimeout = defaults.IdleTimeout
	}
	if options.DrainTimeout == 0 {
		options.DrainTimeout = defaults.DrainTimeout
	}
	if options.MaxConnections <= 0 {
		options.MaxConnections = defaults.MaxConnections
	}
	return options
}

func defaultUDPOptions() domain.UDPOptions {
	return domain.UDPOptions{IdleTimeout: 30 * time.Second, DrainTimeout: 30 * time.Second, MaxSessions: 4096}
}

func effectiveUDPOptions(options domain.UDPOptions) domain.UDPOptions {
	defaults := defaultUDPOptions()
	if options.IdleTimeout == 0 {
		options.IdleTimeout = defaults.IdleTimeout
	}
	if options.DrainTimeout == 0 {
		options.DrainTimeout = defaults.DrainTimeout
	}
	if options.MaxSessions <= 0 {
		options.MaxSessions = defaults.MaxSessions
	}
	return options
}
