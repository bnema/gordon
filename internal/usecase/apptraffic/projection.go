// Package apptraffic projects active app definitions into traffic
// snapshot entries consumed by the traffic manager.
package apptraffic

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

// EntrypointPolicy is the installation policy inherited by generated
// listeners: the canonical public bind, trusted CIDRs, raw-fallback
// behavior, and transport limits. The code never reads installation
// config; the caller supplies it.
type EntrypointPolicy struct {
	Name                    string
	Address                 string
	Protocol                domain.EntryPointProtocol
	TrustedCIDRs            []string
	RawFallback             string
	RawFallbackTrustedCIDRs []string
	AllowPublicRawFallback  bool
}

// listener projects the policy to the domain listener contract used for
// exact publish compatibility checks.
func (p EntrypointPolicy) listener() domain.EntryPointListener {
	return domain.EntryPointListener{Address: p.Address, Protocol: p.Protocol}
}

// RouteEntry is one projected traffic entry.
type RouteEntry struct {
	// Kind is http, tcp, or udp.
	Kind string
	// RouterName is the deterministic generated router name.
	RouterName string
	// Entrypoint names the installation entrypoint (empty for http).
	Entrypoint string
	// Host is the canonical HTTP host (http only).
	Host string
	// BindIP and BindPort are the literal public bind (tcp/udp only).
	BindIP   string
	BindPort int
	// TLSMode is auto, always, or never (http only).
	TLSMode string
	// App and Service identify the owning workload.
	App     string
	Service string
	// Backend is the resolved loopback endpoint for the interface's
	// container port. Zero when unbound (proxy fails closed).
	Backend domain.AppBackend
}

// Project maps one app's active definitions to traffic entries.
// Entrypoints supplies installation policy by name; unknown entrypoint references are validation errors.
// Loopback endpoints resolve from each service's recorded backend binds;
// services without a recorded bind for an interface port project with an
// empty BackendHost (proxy fails closed).
func Project(app string, active domain.AppActive, entrypoints map[string]EntrypointPolicy) ([]RouteEntry, error) {
	if active.App != "" && active.App != app {
		return nil, fmt.Errorf("%w: projection for %q got active state of %q", domain.ErrAppTrafficProjection, app, active.App)
	}
	var entries []RouteEntry
	for name, svc := range active.Services {
		svcEntries, err := projectService(app, name, svc, entrypoints)
		if err != nil {
			return nil, err
		}
		entries = append(entries, svcEntries...)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].RouterName != entries[j].RouterName {
			return entries[i].RouterName < entries[j].RouterName
		}
		return entries[i].Backend.ContainerPort < entries[j].Backend.ContainerPort
	})
	return entries, nil
}

// projectService maps one effective service to entries. Endpoints
// resolve from the recorded backend binds; missing binds stay empty
// (proxy fails closed, never falls back to container IPs).
func projectService(app, name string, eff domain.AppEffectiveService, entrypoints map[string]EntrypointPolicy) ([]RouteEntry, error) {
	spec := eff.Spec
	spec.Name = name
	var entries []RouteEntry
	for _, h := range spec.HTTP {
		entries = append(entries, RouteEntry{
			Kind:       "http",
			RouterName: "app-" + app + "--" + spec.Name + "--http-" + sanitizeHost(h.Host),
			Host:       h.Host,
			TLSMode:    h.TLS,
			App:        app,
			Service:    spec.Name,
			Backend:    eff.BackendFor(h.Port),
		})
	}
	for _, t := range spec.TCP {
		policy, err := lookupEntrypoint(entrypoints, t.Entrypoint, app, spec.Name, "tcp")
		if err != nil {
			return nil, err
		}
		host, port, err := domain.ParsePublish(t.Publish)
		if err != nil {
			return nil, err
		}
		if err := domain.ValidatePublishForListener(t.Publish, domain.NetworkProtocolTCP, policy.listener(), t.Entrypoint); err != nil {
			return nil, fmt.Errorf("%w: service %q tcp interface: %s", domain.ErrAppTrafficProjection, spec.Name, err)
		}
		entries = append(entries, RouteEntry{
			Kind:       "tcp",
			RouterName: "app-" + app + "--" + spec.Name + "--tcp-" + itoa(t.Port),
			Entrypoint: t.Entrypoint,
			BindIP:     host,
			BindPort:   port,
			App:        app,
			Service:    spec.Name,
			Backend:    eff.BackendFor(t.Port),
		})
	}
	for _, u := range spec.UDP {
		policy, err := lookupEntrypoint(entrypoints, u.Entrypoint, app, spec.Name, "udp")
		if err != nil {
			return nil, err
		}
		host, port, err := domain.ParsePublish(u.Publish)
		if err != nil {
			return nil, err
		}
		if err := domain.ValidatePublishForListener(u.Publish, domain.NetworkProtocolUDP, policy.listener(), u.Entrypoint); err != nil {
			return nil, fmt.Errorf("%w: service %q udp interface: %s", domain.ErrAppTrafficProjection, spec.Name, err)
		}
		entries = append(entries, RouteEntry{
			Kind:       "udp",
			RouterName: "app-" + app + "--" + spec.Name + "--udp-" + itoa(u.Port),
			Entrypoint: u.Entrypoint,
			BindIP:     host,
			BindPort:   port,
			App:        app,
			Service:    spec.Name,
			Backend:    eff.BackendForProtocol(u.Port, domain.NetworkProtocolUDP),
		})
	}
	return entries, nil
}

// lookupEntrypoint resolves a named installation entrypoint.
func lookupEntrypoint(entrypoints map[string]EntrypointPolicy, name, app, service, kind string) (EntrypointPolicy, error) {
	policy, ok := entrypoints[name]
	if !ok {
		return EntrypointPolicy{}, fmt.Errorf(
			"%w: service %q %s references unknown entrypoint %q",
			domain.ErrAppTrafficProjection, service, kind, name,
		)
	}
	_ = app
	return policy, nil
}

// sanitizeHost maps a hostname to a router-name fragment.
func sanitizeHost(host string) string {
	fragment := strings.ReplaceAll(host, ".", "-")
	fragment = strings.ReplaceAll(fragment, ":", "-")
	return strings.ReplaceAll(fragment, "/", "-")
}

// itoa formats a port number.
func itoa(n int) string {
	return strconv.Itoa(n)
}
