package apptraffic

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// HostIndex is the proxy-facing read model: canonical HTTP host to the
// resolved loopback backend. It derives from ACTIVE app state (bbolt
// stays authoritative); the daemon rebuilds it after every activation
// and at boot. Lookups never touch container IPs.
type HostIndex struct {
	mu      sync.RWMutex
	byHost  map[string]RouteEntry
	entries []RouteEntry
}

// NewHostIndex creates an empty host index.
func NewHostIndex() *HostIndex {
	return &HostIndex{byHost: map[string]RouteEntry{}}
}

// Replace installs a rebuilt host map.
func (h *HostIndex) Replace(entries []RouteEntry) {
	byHost := make(map[string]RouteEntry, len(entries))
	for _, entry := range entries {
		if entry.Kind != "http" || entry.Host == "" {
			continue
		}
		byHost[entry.Host] = entry
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.byHost = byHost
	h.entries = append([]RouteEntry(nil), entries...)
}

// Entries returns a copy of the projected entries, for a caller that
// builds a candidate index and publishes it through another one.
func (h *HostIndex) Entries() []RouteEntry {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]RouteEntry(nil), h.entries...)
}

// Lookup returns the projected entry for a canonical host.
func (h *HostIndex) Lookup(host string) (RouteEntry, bool) {
	if h == nil {
		return RouteEntry{}, false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	entry, ok := h.byHost[host]
	return entry, ok
}

// LookupHost implements proxy.TargetProvider over the host index.
// The proxy package owns the interface; apptraffic implements it to
// keep app-state interpretation out of the proxy.
func (h *HostIndex) LookupHost(host string) (domain.AppBackend, bool) {
	if h == nil {
		return domain.AppBackend{}, false
	}
	entry, ok := h.Lookup(host)
	if !ok {
		return domain.AppBackend{}, false
	}
	return entry.Backend, true
}

// AppHosts returns the sorted served hosts with resolved backends.
// Unresolved entries (no recorded bind) are excluded: fail closed.
// It implements out.AppHostSource: the shared read model for traffic,
// PKI and public-TLS consumers.
func (h *HostIndex) AppHosts() []out.AppHost {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	hosts := make([]out.AppHost, 0, len(h.byHost))
	for host, entry := range h.byHost {
		if !entry.Backend.Resolved() {
			continue
		}
		hosts = append(hosts, out.AppHost{
			Host:    host,
			App:     entry.App,
			Service: entry.Service,
			TLSMode: entry.TLSMode,
		})
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Host < hosts[j].Host })
	return hosts
}

// L4Entries returns resolved TCP and UDP projections.
func (h *HostIndex) L4Entries() []RouteEntry {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	entries := make([]RouteEntry, 0, len(h.entries))
	for _, entry := range h.entries {
		if (entry.Kind == "tcp" || entry.Kind == "udp") && entry.Backend.Resolved() {
			entries = append(entries, entry)
		}
	}
	return entries
}

// Activator re-projects active app state into the host index.
// It is the coordinated traffic activation for the deployment engine:
// after each deployment the engine rebuilds the host index from ACTIVE
// state so the proxy only serves current backends.
type Activator struct {
	log zerowrap.Logger
}

// NewActivator wires activation with its logger.
func NewActivator(log zerowrap.Logger) *Activator {
	return &Activator{log: log}
}

// RebuildHostIndex re-projects every app's ACTIVE state into the host
// index. The daemon calls it after each activation and at boot.
// STOPPED-intent apps are skipped: their backends must not stay
// routable merely because ACTIVE retains their effective spec.
func (a *Activator) RebuildHostIndex(
	ctx context.Context,
	index *HostIndex,
	state out.AppStateReader,
	entrypoints map[string]EntrypointPolicy,
) error {
	apps, err := state.ListApps(ctx)
	if err != nil {
		return fmt.Errorf("apptraffic: list apps for host index: %w", err)
	}
	actives := make(map[string]domain.AppActive, len(apps))
	for _, app := range apps {
		intent, err := state.LoadIntent(ctx, app)
		if err != nil {
			return fmt.Errorf("apptraffic: load intent for host index: %w", err)
		}
		if intent.Stopped {
			continue
		}
		active, ok, err := state.LoadActive(ctx, app)
		if err != nil {
			return fmt.Errorf("apptraffic: load active for host index: %w", err)
		}
		if !ok {
			continue
		}
		actives[app] = active
	}
	projections, err := ProjectAll(actives, entrypoints)
	if err != nil {
		return err
	}
	var entries []RouteEntry
	for _, app := range sortedKeys(projections) {
		entries = append(entries, projections[app]...)
	}
	index.Replace(entries)
	a.log.Info().Int("hosts", len(entries)).Msg("apptraffic: host index rebuilt")
	return nil
}

// AppStateReader is the ACTIVE/intent subset read paths need.
// Narrower than out.AppState so read paths never gain write access.
// (Re-exported alias: the canonical definition lives in out.)

func sortedKeys(projections map[string][]RouteEntry) []string {
	apps := make([]string, 0, len(projections))
	for app := range projections {
		apps = append(apps, app)
	}
	sort.Strings(apps)
	return apps
}
func ProjectAll(
	apps map[string]domain.AppActive,
	entrypoints map[string]EntrypointPolicy,
) (map[string][]RouteEntry, error) {
	names := make([]string, 0, len(apps))
	for app := range apps {
		names = append(names, app)
	}
	sort.Strings(names)
	projections := make(map[string][]RouteEntry, len(apps))
	for _, app := range names {
		entries, err := Project(app, apps[app], entrypoints)
		if err != nil {
			return nil, err
		}
		projections[app] = entries
	}
	return projections, nil
}
