package publictls

import (
	"context"
	"regexp"
	"sort"
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
	status.Errors = buildStatusErrors(status.Certificates, status.Routes)
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

func buildStatusErrors(certs []domain.ManagedCertificate, routes []domain.TLSRouteCoverage) []string {
	seen := make(map[string]struct{})
	var errorsList []string
	add := func(message string) {
		message = sanitizeError(message)
		if message == "" {
			return
		}
		if _, ok := seen[message]; ok {
			return
		}
		seen[message] = struct{}{}
		errorsList = append(errorsList, message)
	}
	for _, cert := range certs {
		add(cert.LastError)
	}
	for _, route := range routes {
		add(route.Error)
	}
	return errorsList
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
		case domain.TLSCertificateStatusExpired, domain.TLSCertificateStatusMissing:
			continue
		}
		// Double-check the raw stored cert expiry to guard against stale
		// ManagedCertificate.Status values that may not reflect real expiry.
		if sc, ok := s.certs[mc.ID]; ok {
			if !sc.NotAfter.IsZero() && !now.Before(sc.NotAfter) {
				continue
			}
		}
		return true, mc.ID
	}
	return false, ""
}

// Sensitive key names that may appear in error messages.
var sensitiveKeys = `(token|secret|key|password|pass|api_key|apikey|auth)`

// Patterns for redacting sensitive values in common formats.
var (
	// key=value (unquoted, case-insensitive)
	keyEqValueRe = regexp.MustCompile(`(?i)` + sensitiveKeys + `=[^\s"']+`)
	// key="value" (double-quoted)
	keyEqQuotedRe = regexp.MustCompile(`(?i)` + sensitiveKeys + `="[^"]*"`)
	// key='value' (single-quoted)
	keyEqSingleQuotedRe = regexp.MustCompile(`(?i)` + sensitiveKeys + `='[^']*'`)
	// JSON "key": "value"
	jsonKeyValueRe = regexp.MustCompile(`(?i)"` + sensitiveKeys + `"\s*:\s*"[^"]*"`)
	// YAML key: value (start of line or after indent)
	yamlKeyValueRe = regexp.MustCompile(`(?i)(?m)^\s*` + sensitiveKeys + `\s*:\s+\S+`)
)

// sanitizeError removes obvious sensitive strings from error messages.
// It redacts key=value, key="value", key='value', JSON "key": "value",
// and YAML key: value patterns for known sensitive keys while preserving
// safe context. It does not blanket-redact messages just because they
// contain the word "secret" or similar.
func sanitizeError(err string) string {
	if err == "" {
		return ""
	}

	result := keyEqValueRe.ReplaceAllString(err, "$1=redacted")
	result = keyEqQuotedRe.ReplaceAllString(result, `$1="redacted"`)
	result = keyEqSingleQuotedRe.ReplaceAllString(result, "$1='redacted'")
	result = jsonKeyValueRe.ReplaceAllString(result, `"$1":"redacted"`)
	result = yamlKeyValueRe.ReplaceAllString(result, "$1: redacted")
	return result
}
