package registry

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bnema/gordon/internal/adapters/in/http/middleware"
	"github.com/bnema/gordon/internal/adapters/out/ratelimit"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
)

func TestRateLimitMiddleware_Disabled(t *testing.T) {
	// When limiters are nil, middleware should pass through
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := RateLimitMiddleware(nil, nil, nil, testLogger())
	wrappedHandler := middleware(handler)

	// Should pass through without rate limiting
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
		rec := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	}
}

func TestRateLimitMiddleware_GlobalLimitHit(t *testing.T) {
	globalLimiter := outmocks.NewMockRateLimiter(t)
	ipLimiter := outmocks.NewMockRateLimiter(t)

	// Global limiter returns false (rate limited)
	globalLimiter.EXPECT().Allow(context.Background(), "global").Return(false)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := RateLimitMiddleware(globalLimiter, ipLimiter, nil, testLogger())
	wrappedHandler := middleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	// Should return 429 when global limit hit
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Contains(t, rec.Body.String(), "TOOMANYREQUESTS")
}

func TestRateLimitMiddleware_PerIPLimitHit(t *testing.T) {
	globalLimiter := outmocks.NewMockRateLimiter(t)
	ipLimiter := outmocks.NewMockRateLimiter(t)

	// Global limiter allows, IP limiter denies
	globalLimiter.EXPECT().Allow(context.Background(), "global").Return(true)
	ipLimiter.EXPECT().Allow(context.Background(), "ip:192.168.1.100").Return(false)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := RateLimitMiddleware(globalLimiter, ipLimiter, nil, testLogger())
	wrappedHandler := middleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	// Should return 429 when per-IP limit hit
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
}

func TestRateLimitMiddleware_BothLimitsPass(t *testing.T) {
	globalLimiter := outmocks.NewMockRateLimiter(t)
	ipLimiter := outmocks.NewMockRateLimiter(t)

	// Both limiters allow
	globalLimiter.EXPECT().Allow(context.Background(), "global").Return(true)
	ipLimiter.EXPECT().Allow(context.Background(), "ip:192.168.1.100").Return(true)

	handlerCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	middleware := RateLimitMiddleware(globalLimiter, ipLimiter, nil, testLogger())
	wrappedHandler := middleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	// Should pass through to handler
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, handlerCalled, "handler should have been called")
}

func TestRateLimitMiddleware_TrustedProxy(t *testing.T) {
	globalLimiter := outmocks.NewMockRateLimiter(t)
	ipLimiter := outmocks.NewMockRateLimiter(t)

	// Request comes from trusted proxy (127.0.0.1) with XFF header
	// Should use the X-Forwarded-For IP (203.0.113.50) for rate limiting
	globalLimiter.EXPECT().Allow(context.Background(), "global").Return(true)
	ipLimiter.EXPECT().Allow(context.Background(), "ip:203.0.113.50").Return(true)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := RateLimitMiddleware(globalLimiter, ipLimiter, []string{"127.0.0.1"}, testLogger())
	wrappedHandler := middleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.50")
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRateLimitMiddleware_UntrustedProxy(t *testing.T) {
	globalLimiter := outmocks.NewMockRateLimiter(t)
	ipLimiter := outmocks.NewMockRateLimiter(t)

	// Request from untrusted IP with XFF header should use RemoteAddr IP
	globalLimiter.EXPECT().Allow(context.Background(), "global").Return(true)
	ipLimiter.EXPECT().Allow(context.Background(), "ip:192.168.1.100").Return(true)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := RateLimitMiddleware(globalLimiter, ipLimiter, []string{"127.0.0.1"}, testLogger())
	wrappedHandler := middleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.50") // Should be ignored
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRateLimitMiddleware_ResponseFormat(t *testing.T) {
	globalLimiter := outmocks.NewMockRateLimiter(t)
	ipLimiter := outmocks.NewMockRateLimiter(t)

	globalLimiter.EXPECT().Allow(context.Background(), "global").Return(false)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := RateLimitMiddleware(globalLimiter, ipLimiter, nil, testLogger())
	wrappedHandler := middleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	// Verify response format
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "registry/2.0", rec.Header().Get("Docker-Distribution-API-Version"))
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))
	assert.Contains(t, rec.Body.String(), "TOOMANYREQUESTS")
}

// Integration test using real MemoryStore implementation
func TestRateLimitMiddleware_Integration(t *testing.T) {
	log := testLogger()
	globalLimiter := ratelimit.NewMemoryStore(1000, 100, log) // High global limit
	ipLimiter := ratelimit.NewMemoryStore(2, 2, log)          // Low per-IP limit

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := RateLimitMiddleware(globalLimiter, ipLimiter, nil, log)
	wrappedHandler := middleware(handler)

	// First 2 requests from same IP should succeed (burst)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
		req.RemoteAddr = "192.168.1.100:12345"
		rec := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code, "request %d should succeed", i+1)
	}

	// 3rd request from same IP should be rate limited
	req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code, "3rd request should be rate limited")

	// Request from different IP should still succeed (independent limit)
	req2 := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req2.RemoteAddr = "192.168.1.101:12345"
	rec2 := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusOK, rec2.Code, "different IP should succeed")
}

func TestGetClientIP(t *testing.T) {
	// Parse trusted proxies for tests
	trustedNets := middleware.ParseTrustedProxies([]string{"127.0.0.1", "10.0.0.0/8"})
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

			ip := middleware.GetClientIP(req, tt.trustedNets)
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nets := middleware.ParseTrustedProxies(tt.proxies)
			got := middleware.IsTrustedProxy(tt.testIP, nets)
			assert.Equal(t, tt.want, got)
		})
	}
}
