package registry

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRateLimitMiddleware_Disabled(t *testing.T) {
	config := RateLimitConfig{
		Enabled: false,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := RateLimitMiddleware(config)
	wrappedHandler := middleware(handler)

	// Should pass through without rate limiting
	for i := 0; i < 1000; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
		rec := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	}
}

func TestRateLimitMiddleware_PerIPLimit(t *testing.T) {
	config := RateLimitConfig{
		Enabled:   true,
		GlobalRPS: 1000, // High global limit
		PerIPRPS:  1,    // Very low per-IP limit for testing
		Burst:     100,  // High burst to not interfere
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := RateLimitMiddleware(config)
	wrappedHandler := middleware(handler)

	// First request should succeed
	req1 := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req1.RemoteAddr = "192.168.1.100:12345"
	rec1 := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec1, req1)
	assert.Equal(t, http.StatusOK, rec1.Code)

	// Request from different IP should succeed (independent rate limiter)
	req2 := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req2.RemoteAddr = "192.168.1.101:12345"
	rec2 := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusOK, rec2.Code)
}

func TestRateLimitMiddleware_GlobalLimit(t *testing.T) {
	config := RateLimitConfig{
		Enabled:   true,
		GlobalRPS: 1, // Very low global limit for testing
		PerIPRPS:  100,
		Burst:     1,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := RateLimitMiddleware(config)
	wrappedHandler := middleware(handler)

	// First request should succeed
	req1 := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req1.RemoteAddr = "192.168.1.100:12345"
	rec1 := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec1, req1)
	assert.Equal(t, http.StatusOK, rec1.Code)

	// Second request should hit global limit even from different IP
	req2 := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req2.RemoteAddr = "192.168.1.101:12345"
	rec2 := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusTooManyRequests, rec2.Code)
}

func TestRateLimitMiddleware_ResponseFormat(t *testing.T) {
	config := RateLimitConfig{
		Enabled:   true,
		GlobalRPS: 1,
		PerIPRPS:  1,
		Burst:     1,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := RateLimitMiddleware(config)
	wrappedHandler := middleware(handler)

	// First request succeeds
	req1 := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req1.RemoteAddr = "192.168.1.100:12345"
	rec1 := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec1, req1)

	// Second request gets rate limited
	req2 := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req2.RemoteAddr = "192.168.1.100:12346"
	rec2 := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec2, req2)

	// Verify response format
	assert.Equal(t, http.StatusTooManyRequests, rec2.Code)
	assert.Equal(t, "application/json", rec2.Header().Get("Content-Type"))
	assert.Equal(t, "registry/2.0", rec2.Header().Get("Docker-Distribution-API-Version"))
	assert.Equal(t, "1", rec2.Header().Get("Retry-After"))
	assert.Contains(t, rec2.Body.String(), "TOOMANYREQUESTS")
}

func TestGetClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		xff        string
		xRealIP    string
		wantIP     string
	}{
		{
			name:       "from RemoteAddr with port",
			remoteAddr: "192.168.1.100:12345",
			wantIP:     "192.168.1.100",
		},
		{
			name:       "from RemoteAddr without port",
			remoteAddr: "192.168.1.100",
			wantIP:     "192.168.1.100",
		},
		{
			name:       "from X-Forwarded-For single IP",
			remoteAddr: "127.0.0.1:12345",
			xff:        "203.0.113.50",
			wantIP:     "203.0.113.50",
		},
		{
			name:       "from X-Forwarded-For multiple IPs",
			remoteAddr: "127.0.0.1:12345",
			xff:        "203.0.113.50, 10.0.0.1, 172.16.0.1",
			wantIP:     "203.0.113.50",
		},
		{
			name:       "from X-Real-IP",
			remoteAddr: "127.0.0.1:12345",
			xRealIP:    "203.0.113.50",
			wantIP:     "203.0.113.50",
		},
		{
			name:       "X-Forwarded-For takes precedence",
			remoteAddr: "127.0.0.1:12345",
			xff:        "203.0.113.50",
			xRealIP:    "203.0.113.60",
			wantIP:     "203.0.113.50",
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

			ip := getClientIP(req)
			assert.Equal(t, tt.wantIP, ip)
		})
	}
}

func TestDefaultRateLimitConfig(t *testing.T) {
	config := DefaultRateLimitConfig()

	require.True(t, config.Enabled)
	assert.Equal(t, float64(500), config.GlobalRPS)
	assert.Equal(t, float64(50), config.PerIPRPS)
	assert.Equal(t, 100, config.Burst)
}
