package httputil_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bnema/gordon/internal/adapters/in/http/httputil"
)

func TestIsLocalhostRequest(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		want       bool
	}{
		{"ipv4 loopback", "127.0.0.1:12345", true},
		{"ipv4 loopback other", "127.0.0.2:9000", true},
		{"ipv6 loopback bracketed", "[::1]:12345", true},
		{"external ipv4", "192.168.1.1:12345", false},
		{"external ipv6", "[2001:db8::1]:12345", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			if got := httputil.IsLocalhostRequest(req); got != tt.want {
				t.Errorf("IsLocalhostRequest(%q) = %v, want %v", tt.remoteAddr, got, tt.want)
			}
		})
	}
}
