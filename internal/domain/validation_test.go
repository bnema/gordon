package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
		{"single label", "myapp", false},
		{"single label numeric", "api", false},
		{"trailing dot", "example.com.", false},
		{"leading dot", ".example.com", false},
		{"double dots", "exa..mple.com", false},

		// Invalid: label starts or ends with hyphen
		{"label starts with hyphen", "-app.example.com", false},
		{"label ends with hyphen", "app-.example.com", false},
		{"label starts and ends with hyphen", "-app-.example.com", false},
		{"root label starts with hyphen", "-example.com", false},
		{"root label ends with hyphen", "example-.com", false},

		// Invalid: label too long (>63 chars)
		{"label too long", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.example.com", false},

		// Invalid: total hostname too long (>253 chars)
		{"hostname too long", strings.Repeat("a", 55) + "." + strings.Repeat("b", 55) + "." + strings.Repeat("c", 55) + "." + strings.Repeat("d", 55) + "." + strings.Repeat("e", 30) + ".com", false},

		// Invalid: .localhost suffix (DNS rebinding risk)
		{"dot localhost suffix", "app.localhost", false},
		{"deep localhost suffix", "sub.app.localhost", false},
		{"mixed case localhost suffix", "app.Localhost", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsValidRouteDomain(tt.domain)
			assert.Equal(t, tt.want, got, "IsValidRouteDomain(%q)", tt.domain)
		})
	}
}
