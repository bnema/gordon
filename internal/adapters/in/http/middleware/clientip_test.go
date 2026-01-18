package middleware

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetClientIP(t *testing.T) {
	// Parse trusted proxies for tests
	trustedNets := ParseTrustedProxies([]string{"127.0.0.1", "10.0.0.0/8"})
	noTrustedNets := []*net.IPNet{}

	tests := []struct {
		name        string
		remoteAddr  string
		xff         string
		xRealIP     string
		trustedNets []*net.IPNet
		wantIP      string
	}{
		{
			name:        "from RemoteAddr with port",
			remoteAddr:  "192.168.1.100:12345",
			trustedNets: noTrustedNets,
			wantIP:      "192.168.1.100",
		},
		{
			name:        "from RemoteAddr without port",
			remoteAddr:  "192.168.1.100",
			trustedNets: noTrustedNets,
			wantIP:      "192.168.1.100",
		},
		{
			name:        "XFF ignored when no trusted proxies",
			remoteAddr:  "192.168.1.100:12345",
			xff:         "203.0.113.50",
			trustedNets: noTrustedNets,
			wantIP:      "192.168.1.100",
		},
		{
			name:        "XFF ignored when remote is not trusted",
			remoteAddr:  "192.168.1.100:12345",
			xff:         "203.0.113.50",
			trustedNets: trustedNets, // trusts 127.0.0.1 and 10.0.0.0/8
			wantIP:      "192.168.1.100",
		},
		{
			name:        "XFF honored from trusted proxy (single IP)",
			remoteAddr:  "127.0.0.1:12345",
			xff:         "203.0.113.50",
			trustedNets: trustedNets,
			wantIP:      "203.0.113.50",
		},
		{
			name:        "XFF honored from trusted proxy (multiple IPs)",
			remoteAddr:  "127.0.0.1:12345",
			xff:         "203.0.113.50, 10.0.0.1, 172.16.0.1",
			trustedNets: trustedNets,
			wantIP:      "203.0.113.50",
		},
		{
			name:        "XFF honored from trusted CIDR",
			remoteAddr:  "10.1.2.3:12345",
			xff:         "203.0.113.50",
			trustedNets: trustedNets,
			wantIP:      "203.0.113.50",
		},
		{
			name:        "X-Real-IP honored from trusted proxy",
			remoteAddr:  "127.0.0.1:12345",
			xRealIP:     "203.0.113.50",
			trustedNets: trustedNets,
			wantIP:      "203.0.113.50",
		},
		{
			name:        "X-Real-IP ignored when not trusted",
			remoteAddr:  "192.168.1.100:12345",
			xRealIP:     "203.0.113.50",
			trustedNets: trustedNets,
			wantIP:      "192.168.1.100",
		},
		{
			name:        "XFF takes precedence over X-Real-IP",
			remoteAddr:  "127.0.0.1:12345",
			xff:         "203.0.113.50",
			xRealIP:     "203.0.113.60",
			trustedNets: trustedNets,
			wantIP:      "203.0.113.50",
		},
		{
			name:        "IPv6 RemoteAddr",
			remoteAddr:  "[::1]:12345",
			trustedNets: noTrustedNets,
			wantIP:      "::1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.xRealIP != "" {
				req.Header.Set("X-Real-IP", tt.xRealIP)
			}

			ip := GetClientIP(req, tt.trustedNets)
			assert.Equal(t, tt.wantIP, ip)
		})
	}
}

func TestParseTrustedProxies(t *testing.T) {
	tests := []struct {
		name    string
		proxies []string
		testIP  string
		want    bool
	}{
		{
			name:    "empty list",
			proxies: []string{},
			testIP:  "192.168.1.1",
			want:    false,
		},
		{
			name:    "single IP match",
			proxies: []string{"192.168.1.1"},
			testIP:  "192.168.1.1",
			want:    true,
		},
		{
			name:    "single IP no match",
			proxies: []string{"192.168.1.1"},
			testIP:  "192.168.1.2",
			want:    false,
		},
		{
			name:    "CIDR match",
			proxies: []string{"10.0.0.0/8"},
			testIP:  "10.1.2.3",
			want:    true,
		},
		{
			name:    "CIDR no match",
			proxies: []string{"10.0.0.0/8"},
			testIP:  "192.168.1.1",
			want:    false,
		},
		{
			name:    "mixed IP and CIDR",
			proxies: []string{"127.0.0.1", "10.0.0.0/8", "172.16.0.0/12"},
			testIP:  "172.20.1.1",
			want:    true,
		},
		{
			name:    "invalid entries ignored",
			proxies: []string{"not-an-ip", "10.0.0.0/8"},
			testIP:  "10.1.2.3",
			want:    true,
		},
		{
			name:    "IPv6 single IP",
			proxies: []string{"::1"},
			testIP:  "::1",
			want:    true,
		},
		{
			name:    "IPv6 CIDR",
			proxies: []string{"fd00::/8"},
			testIP:  "fd12:3456::1",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nets := ParseTrustedProxies(tt.proxies)
			got := IsTrustedProxy(tt.testIP, nets)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsTrustedProxy(t *testing.T) {
	tests := []struct {
		name        string
		ip          string
		trustedNets []*net.IPNet
		want        bool
	}{
		{
			name:        "empty trusted nets",
			ip:          "192.168.1.1",
			trustedNets: []*net.IPNet{},
			want:        false,
		},
		{
			name:        "nil trusted nets",
			ip:          "192.168.1.1",
			trustedNets: nil,
			want:        false,
		},
		{
			name:        "invalid IP",
			ip:          "not-an-ip",
			trustedNets: ParseTrustedProxies([]string{"192.168.1.0/24"}),
			want:        false,
		},
		{
			name:        "empty IP",
			ip:          "",
			trustedNets: ParseTrustedProxies([]string{"192.168.1.0/24"}),
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsTrustedProxy(tt.ip, tt.trustedNets)
			assert.Equal(t, tt.want, got)
		})
	}
}
