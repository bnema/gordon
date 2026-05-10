package auto

import (
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

// MatchesDomainAllowlist reports whether domain matches any of the given patterns.
// Patterns may be exact domains, wildcard subdomains (*.example.com), or "*" to
// allow everything. Wildcard patterns match a single subdomain level only, per
// DNS/TLS conventions.
func MatchesDomainAllowlist(domain string, patterns []string) bool {
	domain = strings.ToLower(domain)
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if pattern == "*" {
			return true
		}
		if pattern == domain {
			return true
		}
		if !strings.HasPrefix(pattern, "*.") {
			continue
		}
		// Wildcard patterns match single-level subdomains only (per DNS/TLS conventions).
		// e.g., "*.example.com" matches "foo.example.com" but not "bar.foo.example.com"
		suffix := strings.TrimPrefix(pattern, "*.")
		if !strings.HasSuffix(domain, "."+suffix) {
			continue
		}
		prefix := strings.TrimSuffix(domain, "."+suffix)
		if prefix != "" && !strings.Contains(prefix, ".") {
			return true
		}
	}
	return false
}

// ExtractRepoName strips the current or legacy Gordon registry domain, tag,
// and digest from an image reference and returns the bare repository name
// (e.g. "org/myapp").
func ExtractRepoName(imageRef, registryDomain string, legacyRegistryDomains ...string) string {
	return strings.ToLower(domain.ExtractGordonRepoName(imageRef, registryDomain, legacyRegistryDomains))
}
