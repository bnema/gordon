// Package traffic owns Gordon's runtime traffic entrypoints.
package traffic

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
)

const (
	reloadStatusOK    = "ok"
	reloadStatusError = "error"
)

// Manager owns runtime entrypoints for the traffic plane.
type Manager struct {
	log zerowrap.Logger

	mu        sync.Mutex
	snapshot  atomic.Pointer[domain.TrafficGraph]
	listeners map[string]*entryPointRuntime

	lastReloadStatus string
	lastReloadError  string
}

// NewManager creates a traffic manager.
func NewManager(log zerowrap.Logger) *Manager {
	return &Manager{
		log:              log,
		listeners:        map[string]*entryPointRuntime{},
		lastReloadStatus: reloadStatusOK,
	}
}

// Apply validates and applies a new traffic graph snapshot.
func (m *Manager) Apply(ctx context.Context, graph *domain.TrafficGraph) error {
	if graph == nil {
		return m.recordReloadError(errors.New("traffic graph is required"))
	}
	if err := graph.Validate(); err != nil {
		return m.recordReloadError(err)
	}

	nextGraph := cloneTrafficGraph(*graph)
	m.mu.Lock()
	defer m.mu.Unlock()

	newListeners, createdListeners, err := m.prepareTCPListeners(ctx, &nextGraph)
	if err != nil {
		m.lastReloadStatus = reloadStatusError
		m.lastReloadError = err.Error()
		return err
	}

	oldListeners := m.listeners
	m.listeners = newListeners
	m.snapshot.Store(&nextGraph)
	for _, runtime := range createdListeners {
		runtime.start()
	}
	m.lastReloadStatus = reloadStatusOK
	m.lastReloadError = ""

	drainTimeout := effectiveTCPOptions(snapshotTCPOptions(&nextGraph)).DrainTimeout
	for name, runtime := range oldListeners {
		if newListeners[name] == runtime {
			continue
		}
		if runtime.shouldStopWith(newListeners[name]) {
			runtime.stop(ctx, drainTimeout)
		}
	}
	return nil
}

// Shutdown stops all listeners and active TCP streams.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	listeners := m.listeners
	m.listeners = map[string]*entryPointRuntime{}
	graph := m.snapshot.Load()
	m.snapshot.Store(nil)

	drainTimeout := defaultTCPOptions().DrainTimeout
	if graph != nil {
		drainTimeout = effectiveTCPOptions(graph.Options.TCP).DrainTimeout
	}
	for _, runtime := range listeners {
		runtime.stop(ctx, drainTimeout)
	}
	return nil
}

// Status returns a consistent snapshot of manager state and counters.
func (m *Manager) Status() domain.TrafficStatus {
	m.mu.Lock()
	listeners := make(map[string]*entryPointRuntime, len(m.listeners))
	for name, runtime := range m.listeners {
		listeners[name] = runtime
	}
	status := domain.TrafficStatus{LastReloadStatus: m.lastReloadStatus, LastReloadError: m.lastReloadError}
	graph := m.snapshot.Load()
	m.mu.Unlock()

	if graph == nil {
		return status
	}

	status.EntryPoints = entryPointStatuses(graph.EntryPoints, listeners)
	status.Routers = routerStatuses(graph.Routers, listeners)
	status.Services = serviceStatuses(graph.Services)
	status.Counters = aggregateCounters(status.EntryPoints)
	return status
}

func (m *Manager) recordReloadError(err error) error {
	m.mu.Lock()
	m.lastReloadStatus = reloadStatusError
	m.lastReloadError = err.Error()
	m.mu.Unlock()
	return err
}

func (m *Manager) prepareTCPListeners(ctx context.Context, graph *domain.TrafficGraph) (map[string]*entryPointRuntime, []*entryPointRuntime, error) {
	current := make(map[string]*entryPointRuntime, len(m.listeners))
	for name, runtime := range m.listeners {
		current[name] = runtime
	}

	next := make(map[string]*entryPointRuntime, len(current))
	created := []*entryPointRuntime{}
	for _, entryPoint := range graph.EntryPoints {
		if entryPoint.Protocol != domain.EntryPointProtocolTCP {
			continue
		}
		if runtime := current[entryPoint.Name]; runtime != nil && runtime.matches(entryPoint) {
			next[entryPoint.Name] = runtime
			continue
		}
		runtime, err := m.bindTCPEntryPoint(ctx, entryPoint)
		if err != nil {
			for _, createdRuntime := range created {
				createdRuntime.stop(ctx, effectiveTCPOptions(graph.Options.TCP).DrainTimeout)
			}
			return nil, nil, err
		}
		next[entryPoint.Name] = runtime
		created = append(created, runtime)
	}
	return next, created, nil
}

func (m *Manager) bindTCPEntryPoint(ctx context.Context, entryPoint domain.EntryPoint) (*entryPointRuntime, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", entryPoint.Address)
	if err != nil {
		return nil, fmt.Errorf("bind tcp entrypoint %q on %s: %w", entryPoint.Name, entryPoint.Address, err)
	}
	runtime := newEntryPointRuntime(m, entryPoint, listener)
	return runtime, nil
}

func entryPointStatuses(entries []domain.EntryPoint, listeners map[string]*entryPointRuntime) []domain.EntryPointStatus {
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
		}
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return statuses
}

func routerStatuses(routers []domain.TrafficRouter, listeners map[string]*entryPointRuntime) []domain.TrafficRouterStatus {
	statuses := make([]domain.TrafficRouterStatus, 0, len(routers))
	for _, router := range routers {
		status := domain.TrafficRouterStatus{Name: router.Name, EntryPoint: router.EntryPoint, Protocol: router.Protocol, Active: true}
		if runtime := listeners[router.EntryPoint]; runtime != nil {
			counters := runtime.counters.snapshot()
			status.ActiveTCPConnections = counters.ActiveTCPConnections
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

func serviceStatuses(services []domain.TrafficService) []domain.TrafficServiceStatus {
	statuses := make([]domain.TrafficServiceStatus, 0, len(services))
	for _, service := range services {
		status := domain.TrafficServiceStatus{Name: service.Name, Active: true, Backends: make([]domain.TrafficBackendStatus, 0, len(service.Backends))}
		for _, backend := range service.Backends {
			status.Backends = append(status.Backends, domain.TrafficBackendStatus{Name: backend.Name, Active: true})
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
	}
	return counters
}

func cloneTrafficGraph(graph domain.TrafficGraph) domain.TrafficGraph {
	clone := graph
	clone.EntryPoints = append([]domain.EntryPoint{}, graph.EntryPoints...)
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

type trafficCounters struct {
	activeTCPConnections atomic.Int64
	totalAccepted        atomic.Int64
	totalRefused         atomic.Int64
	totalErrors          atomic.Int64
	bytesIn              atomic.Int64
	bytesOut             atomic.Int64
}

func (c *trafficCounters) snapshot() domain.TrafficCounters {
	return domain.TrafficCounters{
		ActiveTCPConnections: c.activeTCPConnections.Load(),
		TotalAccepted:        c.totalAccepted.Load(),
		TotalRefused:         c.totalRefused.Load(),
		TotalErrors:          c.totalErrors.Load(),
		BytesIn:              c.bytesIn.Load(),
		BytesOut:             c.bytesOut.Load(),
	}
}

func defaultTCPOptions() domain.TCPOptions {
	return domain.TCPOptions{
		DialTimeout:  10 * time.Second,
		IdleTimeout:  5 * time.Minute,
		DrainTimeout: 30 * time.Second,
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
	return options
}
