package middleware

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/http/httphelper"
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
			allowedNets: httphelper.ParseTrustedProxies([]string{"100.64.0.0/10"}),
			remoteAddr:  "100.100.1.1:1234",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "denied IP returns 403",
			allowedNets: httphelper.ParseTrustedProxies([]string{"100.64.0.0/10"}),
			remoteAddr:  "203.0.113.50:1234",
			wantStatus:  http.StatusForbidden,
		},
		{
			name:        "localhost always allowed even when not in allowlist",
			allowedNets: httphelper.ParseTrustedProxies([]string{"100.64.0.0/10"}),
			remoteAddr:  "127.0.0.1:1234",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "127.x.x.x always allowed",
			allowedNets: httphelper.ParseTrustedProxies([]string{"100.64.0.0/10"}),
			remoteAddr:  "127.0.0.2:1234",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "IPv6 localhost always allowed",
			allowedNets: httphelper.ParseTrustedProxies([]string{"100.64.0.0/10"}),
			remoteAddr:  "[::1]:1234",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "trusted proxy forwards allowed client IP",
			allowedNets: httphelper.ParseTrustedProxies([]string{"100.64.0.0/10"}),
			trustedNets: httphelper.ParseTrustedProxies([]string{"10.0.0.1"}),
			remoteAddr:  "10.0.0.1:1234",
			xff:         "100.100.1.1",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "trusted proxy forwards denied client IP",
			allowedNets: httphelper.ParseTrustedProxies([]string{"100.64.0.0/10"}),
			trustedNets: httphelper.ParseTrustedProxies([]string{"10.0.0.1"}),
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

func TestHTTPSRedirect_NoPortHost_OmitsTLSPort(t *testing.T) {
	log := testLogger()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := HTTPSRedirect(nil, 8088, 8443, true, log, func(host string) bool { return host == "o2.bnema.dev" })(ok)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Host = "o2.bnema.dev"
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusPermanentRedirect, resp.StatusCode)
	assert.Equal(t, "https://o2.bnema.dev/", resp.Header.Get("Location"))
}

func TestHTTPSRedirect_HTTPListenerPort_MapsToTLSPort(t *testing.T) {
	log := testLogger()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := HTTPSRedirect(nil, 8088, 8443, true, log, func(host string) bool { return host == "o2.bnema.dev" })(ok)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Host = "o2.bnema.dev:8088"
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusPermanentRedirect, resp.StatusCode)
	assert.Equal(t, "https://o2.bnema.dev:8443/", resp.Header.Get("Location"))
}

func TestHTTPSRedirect_UnknownExplicitPort_IsPreserved(t *testing.T) {
	log := testLogger()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := HTTPSRedirect(nil, 8088, 8443, true, log, func(host string) bool { return host == "o2.bnema.dev" })(ok)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Host = "o2.bnema.dev:9999"
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusPermanentRedirect, resp.StatusCode)
	assert.Equal(t, "https://o2.bnema.dev:9999/", resp.Header.Get("Location"))
}

func TestHTTPSRedirect_TrailingDotHostRedirectsToCanonicalHost(t *testing.T) {
	log := testLogger()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := HTTPSRedirect(nil, 8088, 8443, true, log, func(host string) bool { return host == "o2.bnema.dev" })(ok)

	srv := httptest.NewServer(handler)
	defer srv.Close()
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/path", nil)
	require.NoError(t, err)
	req.Host = "O2.Bnema.Dev.:8088"
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusPermanentRedirect, resp.StatusCode)
	assert.Equal(t, "https://o2.bnema.dev:8443/path", resp.Header.Get("Location"))
}

func TestHTTPSRedirect_RejectsInvalidHost(t *testing.T) {
	log := testLogger()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := HTTPSRedirect(nil, 8088, 8443, true, log, func(host string) bool { return host == "o2.bnema.dev" })(ok)

	tests := []struct {
		name string
		host string
	}{
		{name: "invalid port", host: "o2.bnema.dev:abcd"},

		{name: "localhost", host: "localhost"},
		{name: "ipv6", host: "[::1]:8088"},
		{name: "unconfigured public host", host: "evil.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
			require.NoError(t, err)
			req.Host = tt.host
			resp, err := srv.Client().Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
			assert.Empty(t, resp.Header.Get("Location"))
		})
	}
}

func TestProxyCIDRAllowlist(t *testing.T) {
	log := testLogger()
	redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://"+r.Host+r.RequestURI, http.StatusPermanentRedirect)
	})

	tests := []struct {
		name        string
		allowedNets []*net.IPNet
		remoteAddr  string
		wantStatus  int
	}{
		{
			name:        "no CIDRs configured passes through",
			allowedNets: nil,
			remoteAddr:  "203.0.113.50:1234",
			wantStatus:  http.StatusPermanentRedirect,
		},
		{
			name:        "allowed IP gets redirect",
			allowedNets: httphelper.ParseTrustedProxies([]string{"173.245.48.0/20"}),
			remoteAddr:  "173.245.48.1:1234",
			wantStatus:  http.StatusPermanentRedirect,
		},
		{
			name:        "blocked IP gets 403",
			allowedNets: httphelper.ParseTrustedProxies([]string{"173.245.48.0/20"}),
			remoteAddr:  "203.0.113.50:1234",
			wantStatus:  http.StatusForbidden,
		},
		{
			name:        "localhost always allowed",
			allowedNets: httphelper.ParseTrustedProxies([]string{"173.245.48.0/20"}),
			remoteAddr:  "127.0.0.1:1234",
			wantStatus:  http.StatusPermanentRedirect,
		},
		{
			name:        "ignores XFF (uses RemoteAddr only)",
			allowedNets: httphelper.ParseTrustedProxies([]string{"173.245.48.0/20"}),
			remoteAddr:  "203.0.113.50:1234",
			wantStatus:  http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := ProxyCIDRAllowlist(tt.allowedNets, log)(redirect)

			req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
			req.RemoteAddr = tt.remoteAddr
			// Even with XFF from an "allowed" range, the proxy middleware
			// must check RemoteAddr, not forwarded headers.
			req.Header.Set("X-Forwarded-For", "173.245.48.1")
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
