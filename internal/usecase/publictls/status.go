package publictls

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// Status returns the current public TLS status.
func (s *Service) Status(ctx context.Context) domain.PublicTLSStatus {
	// Collect route domains outside the lock so route source methods
	// (GetRoutes/GetExternalRoutes) are not called while holding s.mu.
	routeDomains := collectRouteDomains(ctx, s.deps.Routes)

	s.mu.Lock()
	defer s.mu.Unlock()

	status := domain.PublicTLSStatus{
		ACMEEnabled: s.cfg.Enabled,
	}

	if s.deps.Effective.Mode != "" {
		status.ConfiguredMode = s.deps.Effective.ConfiguredMode
		status.EffectiveMode = s.deps.Effective.Mode
		status.SelectionReason = s.deps.Effective.Reason
		status.TokenSource = s.deps.Effective.TokenSource
	}

	status.Certificates = s.buildManagedCertificatesLocked()
	status.Routes = s.buildRouteCoverageLocked(routeDomains, status.Certificates)
	return status
}

// buildManagedCertificatesLocked returns sorted managed certificates.
// Must be called with s.mu held.
func (s *Service) buildManagedCertificatesLocked() []domain.ManagedCertificate {
	certs := make([]domain.ManagedCertificate, 0, len(s.certs))
	now := time.Now()
	for _, cert := range s.certs {
		lastErr := cert.LastError
		if e, ok := s.lastErr[cert.ID]; ok && e != "" {
			lastErr = e
		}
		mc := domain.ManagedCertificate{
			ID:        cert.ID,
			Names:     cert.Names,
			Challenge: cert.Challenge,
			NotAfter:  cert.NotAfter,
			LastError: sanitizeError(lastErr),
		}
		mc.Status = mc.Health(now)
		certs = append(certs, mc)
	}
	sort.Slice(certs, func(i, j int) bool {
		return certs[i].ID < certs[j].ID
	})
	return certs
}

// buildRouteCoverageLocked returns route coverage for the given domains.
// Must be called with s.mu held.
func (s *Service) buildRouteCoverageLocked(routeDomains []string, managedCerts []domain.ManagedCertificate) []domain.TLSRouteCoverage {
	sort.Strings(routeDomains)

	now := time.Now()
	routes := make([]domain.TLSRouteCoverage, 0, len(routeDomains))
	for _, d := range routeDomains {
		coverage := domain.TLSRouteCoverage{
			Domain:       d,
			RequiredACME: s.cfg.Enabled,
		}
		coverage.Covered, coverage.CoveredBy = s.findCoverageLocked(d, managedCerts, now)
		if err, hasErr := s.routeErr[d]; hasErr {
			coverage.Error = sanitizeError(err)
		}
		routes = append(routes, coverage)
	}
	return routes
}

// collectRouteDomains returns all unique canonical route domains.
// Does not hold s.mu so route source methods may be called safely.
func collectRouteDomains(ctx context.Context, routes RouteSource) []string {
	routeSet := make(map[string]struct{})
	var domains []string

	for _, r := range routes.GetRoutes(ctx) {
		canonical, ok := domain.CanonicalRouteDomain(r.Domain)
		if ok {
			if _, exists := routeSet[canonical]; !exists {
				routeSet[canonical] = struct{}{}
				domains = append(domains, canonical)
			}
		}
	}
	for h := range routes.GetExternalRoutes() {
		canonical, ok := domain.CanonicalRouteDomain(h)
		if ok {
			if _, exists := routeSet[canonical]; !exists {
				routeSet[canonical] = struct{}{}
				domains = append(domains, canonical)
			}
		}
	}
	return domains
}

// findCoverageLocked checks if any managed certificate covers the given domain.
// Must be called with s.mu held.
func (s *Service) findCoverageLocked(routeDomain string, managedCerts []domain.ManagedCertificate, now time.Time) (bool, string) {
	for _, mc := range managedCerts {
		if !mc.Covers(routeDomain) {
			continue
		}
		switch mc.Status {
		case domain.TLSCertificateStatusExpired, domain.TLSCertificateStatusMissing, domain.TLSCertificateStatusError:
			continue
		}
		// Double-check expiry from the raw stored cert.
		if sc, ok := s.certs[mc.ID]; ok {
			if !sc.NotAfter.IsZero() && !now.Before(sc.NotAfter) {
				continue
			}
		}
		return true, mc.ID
	}
	return false, ""
}

// sanitizeError removes obvious sensitive strings from error messages.
// At minimum it ensures that the literal "secret" and patterns like
// "token=secret" are not present in the output.
func sanitizeError(err string) string {
	if err == "" {
		return ""
	}

	result := err

	// Remove key=value patterns for well-known sensitive keys.
	result = sanitizeKeyEqValue(result, "token")
	result = sanitizeKeyEqValue(result, "secret")
	result = sanitizeKeyEqValue(result, "key")
	result = sanitizeKeyEqValue(result, "password")
	result = sanitizeKeyEqValue(result, "pass")
	result = sanitizeKeyEqValue(result, "api_key")
	result = sanitizeKeyEqValue(result, "apikey")
	result = sanitizeKeyEqValue(result, "auth")

	// Replace any remaining occurrences of "secret" (case-insensitive).
	lower := strings.ToLower(result)
	if strings.Contains(lower, "secret") {
		result = "redacted"
		return result
	}

	if result == "" {
		return "certificate error"
	}
	return result
}

// sanitizeKeyEqValue removes patterns like "key=value" from the string.
// It replaces the value portion with "redacted".
func sanitizeKeyEqValue(s, key string) string {
	prefix := key + "="
	lowerPrefix := strings.ToLower(prefix)
	result := s
	pos := 0

	for {
		lowerResult := strings.ToLower(result)
		if pos >= len(lowerResult) {
			break
		}
		idx := strings.Index(lowerResult[pos:], lowerPrefix)
		if idx == -1 {
			break
		}
		idx += pos // make absolute

		// Find end of value (space, newline, or end of string).
		valStart := idx + len(prefix)
		rest := result[valStart:]
		end := strings.IndexAny(rest, " \n")

		replacement := key + "=redacted"
		var after string
		if end == -1 {
			after = ""
		} else {
			after = rest[end:]
		}
		result = result[:idx] + replacement + after

		// Advance search position past the replacement to avoid re-matching.
		pos = idx + len(replacement)
	}
	return result
}
