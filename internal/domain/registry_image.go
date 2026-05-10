package domain

import "strings"

// KnownGordonRegistryDomains returns the normalized set of Gordon-managed
// registry domains, with the current domain first followed by legacy domains.
// Empty values are ignored, trailing slashes are removed, and duplicates are
// removed while preserving first-seen order.
func KnownGordonRegistryDomains(current string, legacy []string) []string {
	domains := make([]string, 0, 1+len(legacy))
	seen := make(map[string]struct{}, 1+len(legacy))

	add := func(domain string) {
		normalized := normalizeRegistryDomain(domain)
		if normalized == "" {
			return
		}

		key := strings.ToLower(normalized)
		if _, ok := seen[key]; ok {
			return
		}

		seen[key] = struct{}{}
		domains = append(domains, normalized)
	}

	add(current)
	for _, domain := range legacy {
		add(domain)
	}

	return domains
}

// StripKnownGordonRegistry removes the Gordon-managed registry host prefix from
// an image reference when it matches the current or a legacy Gordon registry
// domain. Bare references and external registries are returned unchanged.
func StripKnownGordonRegistry(imageRef string, current string, legacy []string) string {
	host, remainder, ok := splitRegistryImageRef(imageRef)
	if !ok {
		return imageRef
	}
	if !isKnownGordonRegistryDomain(host, current, legacy) {
		return imageRef
	}
	return remainder
}

// ExtractGordonRepoName strips a known Gordon registry host, then removes any
// tag or digest suffix, returning the repository name portion of the image
// reference.
func ExtractGordonRepoName(imageRef string, current string, legacy []string) string {
	repo := StripKnownGordonRegistry(imageRef, current, legacy)

	if idx := strings.Index(repo, "@"); idx != -1 {
		repo = repo[:idx]
	}
	if idx := strings.LastIndex(repo, ":"); idx != -1 {
		slashIdx := strings.LastIndex(repo, "/")
		if idx > slashIdx {
			repo = repo[:idx]
		}
	}

	return repo
}

// CanonicalizeGordonImageRef rewrites Gordon-managed image references to use
// the current Gordon registry domain. External registries and bare references
// are returned unchanged. If the current domain is empty, the original image
// reference is returned unchanged.
func CanonicalizeGordonImageRef(imageRef, current string, legacy []string) string {
	currentDomain := normalizeRegistryDomain(current)
	if currentDomain == "" {
		return imageRef
	}

	host, remainder, ok := splitRegistryImageRef(imageRef)
	if !ok {
		return imageRef
	}
	if !isKnownGordonRegistryDomain(host, currentDomain, legacy) {
		return imageRef
	}

	return currentDomain + "/" + remainder
}

// IsGordonRegistryImageRef reports whether an image reference is explicitly
// qualified with the current or a legacy Gordon registry domain.
func IsGordonRegistryImageRef(imageRef, current string, legacy []string) bool {
	host, _, ok := splitRegistryImageRef(imageRef)
	if !ok {
		return false
	}
	return isKnownGordonRegistryDomain(host, current, legacy)
}

func normalizeRegistryDomain(domain string) string {
	domain = strings.TrimSpace(domain)
	domain = strings.TrimRight(domain, "/")
	return domain
}

func isKnownGordonRegistryDomain(host, current string, legacy []string) bool {
	host = normalizeRegistryDomain(host)
	if host == "" {
		return false
	}

	for _, domain := range KnownGordonRegistryDomains(current, legacy) {
		if strings.EqualFold(domain, host) {
			return true
		}
	}
	return false
}

func splitRegistryImageRef(imageRef string) (host string, remainder string, ok bool) {
	host, remainder, ok = strings.Cut(imageRef, "/")
	if !ok || host == "" || remainder == "" {
		return "", "", false
	}
	if !looksLikeRegistryHost(host) {
		return "", "", false
	}
	return host, remainder, true
}

func looksLikeRegistryHost(host string) bool {
	return strings.Contains(host, ".") || strings.Contains(host, ":") || strings.EqualFold(host, "localhost")
}
