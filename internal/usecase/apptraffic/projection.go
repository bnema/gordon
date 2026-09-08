// Package apptraffic projects active app definitions into traffic
// snapshot entries. It is preparation work for the cutover gate:
// pure functions with no wiring into the daemon, proxy, or traffic
// manager. Frozen by docs/plans/v2.50.0/04-network.md.
package apptraffic

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

// EntrypointPolicy is the installation policy inherited by generated
// listeners: trusted CIDRs, raw-fallback behavior, and transport limits.
// The code never reads installation config; the caller supplies it.
type EntrypointPolicy struct {
	Name                    string
	TrustedCIDRs            []string
	RawFallback             string
	RawFallbackTrustedCIDRs []string
	AllowPublicRawFallback  bool
}

// RouteEntry is one projected snapshot entry.
type RouteEntry struct {
	// Kind is http, tcp, or udp (rcon travels as tcp with Policy=rcon).
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
	// Policy tags rcon entries for policy enforcement.
	Policy string
	// App and Service identify the owning workload.
	App     string
	Service string
	// BackendPort is the container port to dial on the private network.
	BackendPort int
}

// OpOverlay is the journaled, ready, operation-owned service transition.
// It exists only inside a non-terminal op whose replacement passed
// readiness, carries the op id, and dies with the op. It is explicitly
// distinguished from pending desired state.
type OpOverlay struct {
	Op       string
	App      string
	Service  string
	Spec     domain.AppService
	Revision string
}

// Project maps one app's active definitions plus an optional op-owned
// overlay to snapshot entries. Entrypoints supplies installation policy
// by name; unknown entrypoint references are validation errors.
func Project(app string, active domain.AppActive, overlay *OpOverlay, entrypoints map[string]EntrypointPolicy) ([]RouteEntry, error) {
	if active.App != "" && active.App != app {
		return nil, fmt.Errorf("%w: projection for %q got active state of %q", domain.ErrAppTrafficProjection, app, active.App)
	}
	var entries []RouteEntry
	for name, svc := range active.Services {
		spec := svc.Spec
		spec.Name = name
		svcEntries, err := projectService(app, spec, entrypoints)
		if err != nil {
			return nil, err
		}
		entries = append(entries, svcEntries...)
	}
	if overlay != nil {
		if overlay.App != app {
			return nil, fmt.Errorf("%w: overlay for %q does not belong to %q", domain.ErrAppTrafficProjection, overlay.App, app)
		}
		overlayEntries, err := projectService(app, overlay.Spec, entrypoints)
		if err != nil {
			return nil, err
		}
		entries = mergeOverlay(entries, overlayEntries, overlay.Service)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].RouterName != entries[j].RouterName {
			return entries[i].RouterName < entries[j].RouterName
		}
		return entries[i].BackendPort < entries[j].BackendPort
	})
	return entries, nil
}

// projectService maps one service spec to entries.
func projectService(app string, spec domain.AppService, entrypoints map[string]EntrypointPolicy) ([]RouteEntry, error) {
	var entries []RouteEntry
	for _, h := range spec.HTTP {
		entries = append(entries, RouteEntry{
			Kind:        "http",
			RouterName:  "app-" + app + "--" + spec.Name + "--http-" + sanitizeHost(h.Host),
			Host:        h.Host,
			TLSMode:     h.TLS,
			App:         app,
			Service:     spec.Name,
			BackendPort: h.Port,
		})
	}
	for _, t := range spec.TCP {
		policy, err := lookupEntrypoint(entrypoints, t.Entrypoint, app, spec.Name, "tcp")
		if err != nil {
			return nil, err
		}
		_ = policy
		host, port, err := domain.ParsePublish(t.Publish)
		if err != nil {
			return nil, err
		}
		entries = append(entries, RouteEntry{
			Kind:        "tcp",
			RouterName:  "app-" + app + "--" + spec.Name + "--tcp-" + itoa(t.Port),
			Entrypoint:  t.Entrypoint,
			BindIP:      host,
			BindPort:    port,
			App:         app,
			Service:     spec.Name,
			BackendPort: t.Port,
		})
	}
	for _, u := range spec.UDP {
		policy, err := lookupEntrypoint(entrypoints, u.Entrypoint, app, spec.Name, "udp")
		if err != nil {
			return nil, err
		}
		_ = policy
		host, port, err := domain.ParsePublish(u.Publish)
		if err != nil {
			return nil, err
		}
		entries = append(entries, RouteEntry{
			Kind:        "udp",
			RouterName:  "app-" + app + "--" + spec.Name + "--udp-" + itoa(u.Port),
			Entrypoint:  u.Entrypoint,
			BindIP:      host,
			BindPort:    port,
			App:         app,
			Service:     spec.Name,
			BackendPort: u.Port,
		})
	}
	for _, r := range spec.RCON {
		policy, err := lookupEntrypoint(entrypoints, r.Entrypoint, app, spec.Name, "rcon")
		if err != nil {
			return nil, err
		}
		if err := checkRCONPolicy(r, policy, app, spec.Name); err != nil {
			return nil, err
		}
		host, port, err := domain.ParsePublish(r.Publish)
		if err != nil {
			return nil, err
		}
		entries = append(entries, RouteEntry{
			Kind:        "tcp",
			RouterName:  "app-" + app + "--" + spec.Name + "--rcon-" + itoa(r.Port),
			Entrypoint:  r.Entrypoint,
			BindIP:      host,
			BindPort:    port,
			Policy:      "rcon",
			App:         app,
			Service:     spec.Name,
			BackendPort: r.Port,
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

// checkRCONPolicy enforces the private default and the public pair.
// A service CIDR set must not exceed the entrypoint set: the app must
// never relax installation restrictions.
func checkRCONPolicy(r domain.AppRCONInterface, policy EntrypointPolicy, app, service string) error {
	if !r.Public {
		if len(r.TrustedCIDRs) > 0 {
			return fmt.Errorf("%w: service %q private rcon must not set trusted_cidrs without public", domain.ErrAppTrafficProjection, service)
		}
		return nil
	}
	if len(r.TrustedCIDRs) == 0 {
		return fmt.Errorf("%w: service %q public rcon requires trusted_cidrs", domain.ErrAppTrafficProjection, service)
	}
	for _, cidr := range r.TrustedCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("%w: service %q rcon trusted_cidrs %q invalid: %v", domain.ErrAppTrafficProjection, service, cidr, err)
		}
	}
	if len(policy.TrustedCIDRs) > 0 && !cidrSubset(r.TrustedCIDRs, policy.TrustedCIDRs) {
		return fmt.Errorf(
			"%w: service %q rcon trusted_cidrs exceed entrypoint %q policy (app %q must not relax installation)",
			domain.ErrAppTrafficProjection, service, policy.Name, app,
		)
	}
	return nil
}

// cidrSubset reports whether every CIDR in sub is covered by super.
// Coverage is exact-match on canonical form (V2 sameCIDRSet semantics
// for rcon); containment across prefix lengths is a contract change.
func cidrSubset(sub, super []string) bool {
	allowed := map[string]struct{}{}
	for _, cidr := range super {
		_, parsed, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		allowed[parsed.String()] = struct{}{}
	}
	for _, cidr := range sub {
		_, parsed, err := net.ParseCIDR(cidr)
		if err != nil {
			return false
		}
		if _, ok := allowed[parsed.String()]; !ok {
			return false
		}
	}
	return true
}

// mergeOverlay replaces the service's active entries with the overlay's.
// Entries of all other services are preserved untouched.
func mergeOverlay(active, overlay []RouteEntry, service string) []RouteEntry {
	var merged []RouteEntry
	for _, entry := range active {
		if entry.Service != service {
			merged = append(merged, entry)
		}
	}
	return append(merged, overlay...)
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
