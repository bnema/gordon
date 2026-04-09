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

	return true
}
