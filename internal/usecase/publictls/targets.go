package publictls

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// CertificateTarget represents a single certificate to obtain or renew.
type CertificateTarget struct {
	ID        string
	Names     []string
	Challenge domain.ACMEChallengeMode
}

// zoneResolver resolves a hostname to its Cloudflare DNS zone.
type zoneResolver interface {
	FindZone(ctx context.Context, host string) (out.CloudflareZone, error)
}

// DeriveCertificateTargets derives certificate targets from routes and external
// routes based on the given ACME challenge mode.
func DeriveCertificateTargets(
	ctx context.Context,
	mode domain.ACMEChallengeMode,
	routes []domain.Route,
	external map[string]string,
	resolver zoneResolver,
) ([]CertificateTarget, error) {
	hosts := routeHosts(routes, external)

	switch mode {
	case domain.ACMEChallengeHTTP01:
		return deriveHTTP01Targets(hosts), nil
	case domain.ACMEChallengeCloudflareDNS01:
		return deriveDNS01Targets(ctx, hosts, resolver)
	default:
		return nil, fmt.Errorf("unsupported challenge mode: %s", mode)
	}
}

// routeHosts collects all unique canonical hosts from routes and external keys,
// sorted alphabetically. Trailing dots are stripped before canonicalization.
func routeHosts(routes []domain.Route, external map[string]string) []string {
	seen := make(map[string]struct{})
	var hosts []string

	addHost := func(raw string) {
		raw = strings.TrimSuffix(raw, ".")
		canonical, ok := domain.CanonicalRouteDomain(raw)
		if !ok {
			return
		}
		if _, exists := seen[canonical]; !exists {
			seen[canonical] = struct{}{}
			hosts = append(hosts, canonical)
		}
	}

	for _, r := range routes {
		addHost(r.Domain)
	}
	for h := range external {
		addHost(h)
	}

	sort.Strings(hosts)
	return hosts
}

// deriveHTTP01Targets creates one target per host for HTTP-01 challenge.
func deriveHTTP01Targets(hosts []string) []CertificateTarget {
	targets := make([]CertificateTarget, len(hosts))
	for i, host := range hosts {
		targets[i] = CertificateTarget{
			ID:        "http01-" + host,
			Names:     []string{host},
			Challenge: domain.ACMEChallengeHTTP01,
		}
	}
	return targets
}

// deriveDNS01Targets creates targets grouped by wildcard base for DNS-01 challenge.
func deriveDNS01Targets(ctx context.Context, hosts []string, resolver zoneResolver) ([]CertificateTarget, error) {
	if resolver == nil {
		return nil, fmt.Errorf("dns-01 resolver is nil: cannot resolve DNS zones")
	}
	baseSeen := make(map[string]struct{})
	var bases []string

	for _, host := range hosts {
		zone, err := resolver.FindZone(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("find zone for %s: %w", host, err)
		}
		base := wildcardBase(host, zone.Name)
		if _, exists := baseSeen[base]; !exists {
			baseSeen[base] = struct{}{}
			bases = append(bases, base)
		}
	}

	sort.Strings(bases)

	targets := make([]CertificateTarget, len(bases))
	for i, base := range bases {
		targets[i] = CertificateTarget{
			ID:        "dns01-" + base,
			Names:     []string{base, "*." + base},
			Challenge: domain.ACMEChallengeCloudflareDNS01,
		}
	}
	return targets, nil
}

// wildcardBase determines the wildcard base for a host within a zone.
//
// If host == zone, returns zone.
// If host is a direct child of zone, returns zone.
// If host is deeper (e.g. api.prod.example.com within example.com),
// returns the parent label under the zone (prod.example.com).
func wildcardBase(host, zone string) string {
	if host == zone {
		return zone
	}

	// Remove the zone suffix (prefixed by a dot) to isolate subdomain parts.
	trimmed := strings.TrimSuffix(host, "."+zone)

	if !strings.Contains(trimmed, ".") {
		// Direct child of the zone (e.g. app.example.com).
		return zone
	}

	// Deeper than direct child: extract the parent under the zone.
	// e.g. "api.prod.example.com" -> trimmed "api.prod" -> parent "prod"
	_, parent, _ := strings.Cut(trimmed, ".")
	return parent + "." + zone
}
