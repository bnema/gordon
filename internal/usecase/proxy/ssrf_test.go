package proxy

import (
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestIsBlockedTarget_IPAddresses(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		blocked bool
	}{
		// IPv4 loopback - should block
		{"IPv4 loopback 127.0.0.1", "127.0.0.1", true},
		{"IPv4 loopback 127.0.0.2", "127.0.0.2", true},
		{"IPv4 loopback 127.255.255.255", "127.255.255.255", true},

		// IPv4 private networks - should block
		{"10.0.0.0/8 network", "10.0.0.1", true},
		{"10.0.0.0/8 network edge", "10.255.255.255", true},
		{"172.16.0.0/12 network start", "172.16.0.1", true},
		{"172.16.0.0/12 network middle", "172.20.0.1", true},
		{"172.16.0.0/12 network end", "172.31.255.255", true},
		{"192.168.0.0/16 network", "192.168.1.1", true},
		{"192.168.0.0/16 network edge", "192.168.255.255", true},

		// AWS/GCP/Azure metadata endpoint - should block
		{"AWS metadata endpoint", "169.254.169.254", true},
		{"Link-local address", "169.254.0.1", true},

		// IPv6 loopback - should block
		{"IPv6 loopback", "::1", true},

		// IPv6 unique local - should block
		{"IPv6 unique local fc00", "fc00::1", true},
		{"IPv6 unique local fd00", "fd00::1", true},

		// IPv6 link-local - should block
		{"IPv6 link-local", "fe80::1", true},

		// Public IPs - should allow
		{"Google DNS", "8.8.8.8", false},
		{"Cloudflare DNS", "1.1.1.1", false},
		{"Public IP", "203.0.113.1", false},
		{"Public IPv6", "2001:4860:4860::8888", false},

		// Edge cases for 172.16.0.0/12
		{"Just outside 172.16.0.0/12", "172.15.255.255", false},
		{"Just outside 172.16.0.0/12 upper", "172.32.0.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocked, err := IsBlockedTarget(tt.host)
			require.NoError(t, err)
			assert.Equal(t, tt.blocked, blocked, "IsBlockedTarget(%q) = %v, want %v", tt.host, blocked, tt.blocked)
		})
	}
}

func TestIsBlockedTarget_Hostnames(t *testing.T) {
	tests := []struct {
		name        string
		host        string
		wantBlocked bool
		wantErr     bool
	}{
		// Localhost hostname - should block
		{
			name:        "localhost",
			host:        "localhost",
			wantBlocked: true,
			wantErr:     false,
		},

		// Public domains - should allow (assuming they resolve to public IPs)
		{
			name:        "google.com",
			host:        "google.com",
			wantBlocked: false,
			wantErr:     false,
		},

		// Non-existent domain - should error and block
		{
			name:        "non-existent domain",
			host:        "this-domain-definitely-does-not-exist-12345.invalid",
			wantBlocked: true,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocked, err := IsBlockedTarget(tt.host)

			if tt.wantErr {
				assert.Error(t, err)
			}

			assert.Equal(t, tt.wantBlocked, blocked, "IsBlockedTarget(%q) blocked = %v, want %v", tt.host, blocked, tt.wantBlocked)
		})
	}
}

func TestValidateExternalRouteTarget(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		wantErr error
	}{
		// Blocked targets
		{
			name:    "loopback IP",
			host:    "127.0.0.1",
			wantErr: domain.ErrSSRFBlocked,
		},
		{
			name:    "private network",
			host:    "10.0.0.1",
			wantErr: domain.ErrSSRFBlocked,
		},
		{
			name:    "AWS metadata",
			host:    "169.254.169.254",
			wantErr: domain.ErrSSRFBlocked,
		},
		{
			name:    "localhost",
			host:    "localhost",
			wantErr: domain.ErrSSRFBlocked,
		},

		// Allowed targets
		{
			name:    "public IP",
			host:    "8.8.8.8",
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExternalRouteTarget(tt.host)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr), "expected error %v, got %v", tt.wantErr, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestIsBlockedIP(t *testing.T) {
	tests := []struct {
		ip      string
		blocked bool
	}{
		// Should block
		{"127.0.0.1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true},
		{"0.0.0.0", true},

		// Should allow
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"203.0.113.1", false},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			require.NotNil(t, ip, "failed to parse IP: %s", tt.ip)

			blocked := isBlockedIP(ip)
			assert.Equal(t, tt.blocked, blocked, "isBlockedIP(%s) = %v, want %v", tt.ip, blocked, tt.blocked)
		})
	}
}
