package domain

import (
	"testing"
)

func TestIsValidRouteDomain(t *testing.T) {
	tests := []struct {
		name   string
		domain string
		want   bool
	}{
		// Valid public hostnames
		{"valid domain", "app.example.com", true},
		{"valid subdomain", "api.app.example.com", true},
		{"valid single subdomain", "example.com", true},
		{"valid with hyphens", "my-app.example-site.com", true},

		// Invalid: empty
		{"empty domain", "", false},

		// Invalid: IP literals (SSRF risk)
		{"IPv4 address", "192.168.1.1", false},
		{"IPv4 with path", "10.0.0.1/path", false},
		{"IPv4 localhost", "127.0.0.1", false},
		{"IPv6 address", "::1", false},
		{"IPv6 full", "2001:db8::1", false},
		{"IPv6 bracketed", "[::1]", false},
		{"IPv6 with zone", "fe80::1%eth0", false},

		// Invalid: localhost variants
		{"localhost", "localhost", false},
		{"localhost.localdomain", "localhost.localdomain", false},

		// Invalid: internal-only TLDs
		{"dot local", "myapp.local", false},
		{"dot internal", "service.internal", false},
		{"subdomain local", "api.myapp.local", false},

		// Invalid: port in authority
		{"with port", "example.com:8080", false},
		{"localhost with port", "localhost:3000", false},
		{"IP with port", "192.168.1.1:8080", false},

		// Invalid: scheme or path components
		{"with scheme", "https://example.com", false},
		{"with path", "example.com/path", false},
		{"with query", "example.com?query=1", false},

		// Invalid: wildcards or patterns
		{"wildcard", "*.example.com", false},
		{"star anywhere", "exa*mple.com", false},

		// Edge cases
		{"single label", "localhost", false}, // also matches localhost
		{"single label other", "myapp", false},
		{"trailing dot", "example.com.", false},
		{"leading dot", ".example.com", false},
		{"double dots", "exa..mple.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsValidRouteDomain(tt.domain)
			if got != tt.want {
				t.Errorf("IsValidRouteDomain(%q) = %v; want %v", tt.domain, got, tt.want)
			}
		})
	}
}
