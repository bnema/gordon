package app

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	out "github.com/bnema/gordon/internal/boundaries/out"
)

type edgeAccessWriter struct {
	mu      sync.Mutex
	entries []out.AccessLogEntry
}

func (w *edgeAccessWriter) Write(entry out.AccessLogEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = append(w.entries, entry)
	return nil
}

func (w *edgeAccessWriter) entry(t *testing.T, index int) out.AccessLogEntry {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	require.Greater(t, len(w.entries), index)
	return w.entries[index]
}

func TestEdgeHandlerExternalTLSRequiresExplicitLoopbackTrust(t *testing.T) {
	log := zerowrap.New(zerowrap.Config{Level: "disabled"})

	tests := []struct {
		name         string
		trustedCIDRs []string
		remoteAddr   string
		wantStatus   int
	}{
		{
			name:         "localhost is denied when only a non-loopback proxy CIDR is configured",
			trustedCIDRs: []string{"10.0.0.0/8"},
			remoteAddr:   "127.0.0.1:1234",
			wantStatus:   http.StatusForbidden,
		},
		{
			name:         "localhost passes only when loopback is explicitly configured",
			trustedCIDRs: []string{"127.0.0.0/8"},
			remoteAddr:   "127.0.0.1:1234",
			wantStatus:   http.StatusServiceUnavailable,
		},
		{
			name:         "forwarded headers cannot bypass an untrusted direct peer",
			trustedCIDRs: []string{"10.0.0.0/8"},
			remoteAddr:   "192.0.2.5:1234",
			wantStatus:   http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultEdgeConfig()
			cfg.Edge.TLS.Mode = edgeTLSModeExternal
			cfg.Edge.TrustedProxyCIDRs = tt.trustedCIDRs
			handler := edgeHTTPHandlerWithMiddleware(http.NotFoundHandler(), nil, cfg, log, nil)
			req := httptest.NewRequest(http.MethodGet, "http://edge.test/healthz", nil)
			req.RemoteAddr = tt.remoteAddr
			req.Header.Set("X-Forwarded-For", "10.1.2.3")
			req.Header.Set("Forwarded", "for=10.1.2.3")
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			assert.Equal(t, tt.wantStatus, rr.Code)
		})
	}
}

func TestEdgeHandlerFinalMigrationHairpinAllowsOnlyMatchingDirectPeer(t *testing.T) {
	cfg := defaultEdgeConfig()
	cfg.Edge.TLS.Mode = edgeTLSModeExternal
	cfg.Edge.TrustedProxyCIDRs = []string{"127.0.0.1/32"}
	cfg.Edge.MigrationHairpinEnabled = true
	writer := &edgeAccessWriter{}
	log := zerowrap.New(zerowrap.Config{Level: "disabled"})
	handler := edgeHTTPHandlerWithMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), nil, cfg, log, writer)

	for _, tc := range []struct {
		name       string
		remoteIP   string
		localIP    string
		wantStatus int
	}{
		{name: "rootless kernel hairpin", remoteIP: "10.88.0.4", localIP: "10.88.0.4", wantStatus: http.StatusNoContent},
		{name: "same network peer", remoteIP: "10.88.0.5", localIP: "10.88.0.4", wantStatus: http.StatusForbidden},
		{name: "RFC1918 peer", remoteIP: "192.168.1.20", localIP: "10.88.0.4", wantStatus: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://edge.test/", nil)
			req.RemoteAddr = net.JoinHostPort(tc.remoteIP, "34567")
			req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP(tc.localIP), Port: 18081}))
			req.Header.Set("X-Forwarded-For", "198.51.100.99")
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			assert.Equal(t, tc.wantStatus, rr.Code)
		})
	}
	// A hairpin has direct-peer access only. It is not a trusted terminator, so
	// its client identity continues to be the TCP peer rather than spoofed XFF.
	assert.Equal(t, "10.88.0.4", writer.entry(t, 0).ClientIP)
}

func TestEdgeHandlerMigrationProbeBypassesOnlyStrictDirectPeerCheck(t *testing.T) {
	t.Setenv("GORDON_MIGRATION_PROBE_TOKEN", "dedicated-probe-token")
	cfg := defaultEdgeConfig()
	cfg.Edge.TLS.Mode = edgeTLSModeExternal
	cfg.Edge.TrustedProxyCIDRs = []string{"10.0.0.0/8"}
	cfg.Edge.MigrationProbeEnabled = true
	cfg.Edge.MigrationProbeTokenEnv = "GORDON_MIGRATION_PROBE_TOKEN"
	log := zerowrap.New(zerowrap.Config{Level: "disabled"})
	handler := edgeHTTPHandlerWithMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), nil, cfg, log, nil)

	for _, probe := range []struct {
		name, token string
		status      int
	}{{"missing", "", http.StatusForbidden}, {"wrong", "wrong", http.StatusForbidden}, {"dedicated", "dedicated-probe-token", http.StatusNoContent}} {
		t.Run(probe.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://edge.test/", nil)
			req.RemoteAddr = "127.0.0.1:1234" // rootless loopback NAT peer, not a trusted terminator
			if probe.token != "" {
				req.Header.Set(migrationProbeHeader, probe.token)
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			assert.Equal(t, probe.status, rr.Code)
		})
	}

	// The same credential is inert after final activation.
	cfg.Edge.MigrationProbeEnabled = false
	finalHandler := edgeHTTPHandlerWithMiddleware(http.NotFoundHandler(), nil, cfg, log, nil)
	finalRequest := httptest.NewRequest(http.MethodGet, "http://edge.test/", nil)
	finalRequest.RemoteAddr = "127.0.0.1:1234"
	finalRequest.Header.Set(migrationProbeHeader, os.Getenv("GORDON_MIGRATION_PROBE_TOKEN"))
	finalResponse := httptest.NewRecorder()
	finalHandler.ServeHTTP(finalResponse, finalRequest)
	assert.Equal(t, http.StatusForbidden, finalResponse.Code)

	// A real configured terminator continues through normal edge handling after
	// activation; the migration credential contributes no authority.
	trustedFinalRequest := httptest.NewRequest(http.MethodGet, "http://edge.test/", nil)
	trustedFinalRequest.RemoteAddr = "10.1.2.3:1234"
	trustedFinalRequest.Header.Set(migrationProbeHeader, os.Getenv("GORDON_MIGRATION_PROBE_TOKEN"))
	trustedFinalResponse := httptest.NewRecorder()
	finalHandler.ServeHTTP(trustedFinalResponse, trustedFinalRequest)
	assert.Equal(t, http.StatusNotFound, trustedFinalResponse.Code)
}

func TestEdgeHandlerExternalTLSRestrictsPlaintextAndLogsTrustedForwardedAddress(t *testing.T) {
	cfg := defaultEdgeConfig()
	cfg.Edge.TLS.Mode = edgeTLSModeExternal
	cfg.Edge.TrustedProxyCIDRs = []string{"10.0.0.0/8"}
	writer := &edgeAccessWriter{}
	log := zerowrap.New(zerowrap.Config{Level: "disabled"})
	handler := edgeHTTPHandlerWithMiddleware(http.NotFoundHandler(), nil, cfg, log, writer)

	untrusted := httptest.NewRequest(http.MethodGet, "http://edge.test/healthz", nil)
	untrusted.RemoteAddr = "192.0.2.5:1234"
	untrusted.Header.Set("X-Forwarded-For", "198.51.100.10")
	untrustedResponse := httptest.NewRecorder()
	handler.ServeHTTP(untrustedResponse, untrusted)
	assert.Equal(t, http.StatusForbidden, untrustedResponse.Code)
	assert.Equal(t, "nosniff", untrustedResponse.Header().Get("X-Content-Type-Options"))
	assert.NotEmpty(t, untrustedResponse.Header().Get("X-Request-ID"))
	assert.Equal(t, "192.0.2.5", writer.entry(t, 0).ClientIP)

	trusted := httptest.NewRequest(http.MethodGet, "http://edge.test/healthz", nil)
	trusted.RemoteAddr = "10.1.2.3:1234"
	trusted.Header.Set("X-Forwarded-For", "198.51.100.10, 10.1.2.3")
	trustedResponse := httptest.NewRecorder()
	handler.ServeHTTP(trustedResponse, trusted)
	assert.Equal(t, http.StatusServiceUnavailable, trustedResponse.Code)
	assert.Equal(t, "nosniff", trustedResponse.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "198.51.100.10", writer.entry(t, 1).ClientIP)
}
