package domain

import (
	"net"
	"regexp"
	"strings"
)

// hostnamePattern matches valid hostname characters (alphanumeric, hyphen, dot).
// Used as a basic sanity check before detailed validation.
// The actual validation uses net.ParseIP and explicit checks.
var hostnamePattern = regexp.MustCompile(`^[a-zA-Z0-9.-]+$`)

// IsValidRouteDomain validates that a domain is a public hostname suitable
// for use as an application route. It rejects IP literals, localhost,
// internal-only names, and authority strings containing ports.
//
// Valid: app.example.com, api.site.org, my-app.co.uk
// Invalid: 192.168.1.1, localhost, myapp.local, example.com:8080
func IsValidRouteDomain(domain string) bool {
	// Reject empty
	if domain == "" {
		return false
	}

	// Basic character sanity check - only allow alphanumeric, hyphen, dot
	// This also rejects paths, query strings, schemes (which contain :/ etc.)
	if !hostnamePattern.MatchString(domain) {
		return false
	}

	// Reject leading/trailing dots and consecutive dots
	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") ||
		strings.Contains(domain, "..") {
		return false
	}

	// Reject total hostname length > 253 (RFC 1035)
	if len(domain) > 253 {
		return false
	}

	// Must have at least one dot (domain + TLD minimum)
	// This also rejects single-label names like "localhost", "myapp"
	if !strings.Contains(domain, ".") {
		return false
	}

	// Check if it looks like an IP address (IPv4 or IPv6)
	// net.ParseIP accepts various formats including IPv6 brackets
	if net.ParseIP(domain) != nil {
		return false
	}

	// Check for internal-only TLDs
	lowerDomain := strings.ToLower(domain)
	if strings.HasSuffix(lowerDomain, ".local") ||
		strings.HasSuffix(lowerDomain, ".internal") {
		return false
	}

	// Check for localhost variants
	if strings.HasPrefix(lowerDomain, "localhost.") {
		return false
	}
	if strings.HasSuffix(lowerDomain, ".localhost") {
		return false
	}

	// Validate each label conforms to RFC 1123
	labels := strings.Split(domain, ".")
	for _, label := range labels {
		if !isValidLabel(label) {
			return false
		}
	}

	return true
}

// isValidLabel checks if a DNS label is valid per RFC 1123:
// - 1-63 characters
// - alphanumeric and hyphens only
// - must not start or end with hyphen
func isValidLabel(label string) bool {
	if label == "" {
		return false
	}
	if len(label) > 63 {
		return false
	}
	if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return false
	}
	return true
}
