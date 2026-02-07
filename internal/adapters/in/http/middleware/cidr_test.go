package middleware

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryCIDRAllowlist(t *testing.T) {
	log := testLogger()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name        string
		allowedNets []*net.IPNet
		trustedNets []*net.IPNet
		remoteAddr  string
		xff         string
		wantStatus  int
	}{
		{
			name:        "no CIDRs configured passes through",
			allowedNets: nil,
			remoteAddr:  "203.0.113.50:1234",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "empty CIDRs passes through",
			allowedNets: []*net.IPNet{},
			remoteAddr:  "203.0.113.50:1234",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "allowed IP returns 200",
			allowedNets: ParseTrustedProxies([]string{"100.64.0.0/10"}),
			remoteAddr:  "100.100.1.1:1234",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "denied IP returns 403",
			allowedNets: ParseTrustedProxies([]string{"100.64.0.0/10"}),
			remoteAddr:  "203.0.113.50:1234",
			wantStatus:  http.StatusForbidden,
		},
		{
			name:        "localhost always allowed even when not in allowlist",
			allowedNets: ParseTrustedProxies([]string{"100.64.0.0/10"}),
			remoteAddr:  "127.0.0.1:1234",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "127.x.x.x always allowed",
			allowedNets: ParseTrustedProxies([]string{"100.64.0.0/10"}),
			remoteAddr:  "127.0.0.2:1234",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "IPv6 localhost always allowed",
			allowedNets: ParseTrustedProxies([]string{"100.64.0.0/10"}),
			remoteAddr:  "[::1]:1234",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "trusted proxy forwards allowed client IP",
			allowedNets: ParseTrustedProxies([]string{"100.64.0.0/10"}),
			trustedNets: ParseTrustedProxies([]string{"10.0.0.1"}),
			remoteAddr:  "10.0.0.1:1234",
			xff:         "100.100.1.1",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "trusted proxy forwards denied client IP",
			allowedNets: ParseTrustedProxies([]string{"100.64.0.0/10"}),
			trustedNets: ParseTrustedProxies([]string{"10.0.0.1"}),
			remoteAddr:  "10.0.0.1:1234",
			xff:         "203.0.113.50",
			wantStatus:  http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := RegistryCIDRAllowlist(tt.allowedNets, tt.trustedNets, log)(ok)

			req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			assert.Equal(t, tt.wantStatus, rr.Code)

			if tt.wantStatus == http.StatusForbidden {
				var errResp dto.ErrorResponse
				require.NoError(t, json.NewDecoder(rr.Body).Decode(&errResp))
				assert.Equal(t, "Forbidden", errResp.Error)
			}
		})
	}
}
