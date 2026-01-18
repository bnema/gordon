package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/bnema/gordon/internal/adapters/in/http/middleware"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func adminTestLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func TestAuthMiddleware_GlobalRateLimitHit(t *testing.T) {
	authSvc := inmocks.NewMockAuthService(t)
	globalLimiter := outmocks.NewMockRateLimiter(t)
	ipLimiter := outmocks.NewMockRateLimiter(t)

	// Global limiter returns false (rate limited)
	globalLimiter.EXPECT().Allow(context.Background(), "global").Return(false)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mw := AuthMiddleware(authSvc, globalLimiter, ipLimiter, nil, adminTestLogger())
	wrappedHandler := mw(handler)

	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Contains(t, rec.Body.String(), "rate limit exceeded")
}

func TestAuthMiddleware_PerIPRateLimitHit(t *testing.T) {
	authSvc := inmocks.NewMockAuthService(t)
	globalLimiter := outmocks.NewMockRateLimiter(t)
	ipLimiter := outmocks.NewMockRateLimiter(t)

	// Global limiter allows, per-IP limiter denies
	globalLimiter.EXPECT().Allow(context.Background(), "global").Return(true)
	ipLimiter.EXPECT().Allow(context.Background(), "ip:192.168.1.100").Return(false)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mw := AuthMiddleware(authSvc, globalLimiter, ipLimiter, nil, adminTestLogger())
	wrappedHandler := mw(handler)

	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Contains(t, rec.Body.String(), "rate limit exceeded")
}

func TestAuthMiddleware_TrustedProxy(t *testing.T) {
	authSvc := inmocks.NewMockAuthService(t)
	globalLimiter := outmocks.NewMockRateLimiter(t)
	ipLimiter := outmocks.NewMockRateLimiter(t)

	// Request from trusted proxy, should use X-Forwarded-For IP
	trustedNets := middleware.ParseTrustedProxies([]string{"127.0.0.1"})

	globalLimiter.EXPECT().Allow(context.Background(), "global").Return(true)
	ipLimiter.EXPECT().Allow(context.Background(), "ip:203.0.113.50").Return(true)
	authSvc.EXPECT().IsEnabled().Return(false)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mw := AuthMiddleware(authSvc, globalLimiter, ipLimiter, trustedNets, adminTestLogger())
	wrappedHandler := mw(handler)

	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.50")
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthMiddleware_NoRateLimiting(t *testing.T) {
	authSvc := inmocks.NewMockAuthService(t)

	// Auth disabled, no rate limiters
	authSvc.EXPECT().IsEnabled().Return(false)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mw := AuthMiddleware(authSvc, nil, nil, nil, adminTestLogger())
	wrappedHandler := mw(handler)

	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthMiddleware_AuthDisabled(t *testing.T) {
	authSvc := inmocks.NewMockAuthService(t)
	globalLimiter := outmocks.NewMockRateLimiter(t)
	ipLimiter := outmocks.NewMockRateLimiter(t)

	globalLimiter.EXPECT().Allow(context.Background(), "global").Return(true)
	ipLimiter.EXPECT().Allow(context.Background(), "ip:192.168.1.100").Return(true)
	authSvc.EXPECT().IsEnabled().Return(false)

	handlerCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	mw := AuthMiddleware(authSvc, globalLimiter, ipLimiter, nil, adminTestLogger())
	wrappedHandler := mw(handler)

	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, handlerCalled)
}

func TestAuthMiddleware_MissingAuthHeader(t *testing.T) {
	authSvc := inmocks.NewMockAuthService(t)
	globalLimiter := outmocks.NewMockRateLimiter(t)
	ipLimiter := outmocks.NewMockRateLimiter(t)

	globalLimiter.EXPECT().Allow(context.Background(), "global").Return(true)
	ipLimiter.EXPECT().Allow(context.Background(), "ip:192.168.1.100").Return(true)
	authSvc.EXPECT().IsEnabled().Return(true)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mw := AuthMiddleware(authSvc, globalLimiter, ipLimiter, nil, adminTestLogger())
	wrappedHandler := mw(handler)

	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "missing authorization header")
}

func TestAuthMiddleware_InvalidToken(t *testing.T) {
	authSvc := inmocks.NewMockAuthService(t)
	globalLimiter := outmocks.NewMockRateLimiter(t)
	ipLimiter := outmocks.NewMockRateLimiter(t)

	globalLimiter.EXPECT().Allow(context.Background(), "global").Return(true)
	ipLimiter.EXPECT().Allow(context.Background(), "ip:192.168.1.100").Return(true)
	authSvc.EXPECT().IsEnabled().Return(true)
	authSvc.EXPECT().ValidateToken(mock.Anything, "invalid-token").Return(nil, assert.AnError)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mw := AuthMiddleware(authSvc, globalLimiter, ipLimiter, nil, adminTestLogger())
	wrappedHandler := mw(handler)

	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	req.Header.Set("Authorization", "Bearer invalid-token")
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid token")
}

func TestAuthMiddleware_MissingAdminScope(t *testing.T) {
	authSvc := inmocks.NewMockAuthService(t)
	globalLimiter := outmocks.NewMockRateLimiter(t)
	ipLimiter := outmocks.NewMockRateLimiter(t)

	globalLimiter.EXPECT().Allow(context.Background(), "global").Return(true)
	ipLimiter.EXPECT().Allow(context.Background(), "ip:192.168.1.100").Return(true)
	authSvc.EXPECT().IsEnabled().Return(true)
	authSvc.EXPECT().ValidateToken(mock.Anything, "valid-token").Return(&domain.TokenClaims{
		Subject: "user1",
		Scopes:  []string{"push", "pull"}, // No admin scopes
	}, nil)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mw := AuthMiddleware(authSvc, globalLimiter, ipLimiter, nil, adminTestLogger())
	wrappedHandler := mw(handler)

	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	req.Header.Set("Authorization", "Bearer valid-token")
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "admin scope required")
}

func TestAuthMiddleware_Success(t *testing.T) {
	authSvc := inmocks.NewMockAuthService(t)
	globalLimiter := outmocks.NewMockRateLimiter(t)
	ipLimiter := outmocks.NewMockRateLimiter(t)

	globalLimiter.EXPECT().Allow(context.Background(), "global").Return(true)
	ipLimiter.EXPECT().Allow(context.Background(), "ip:192.168.1.100").Return(true)
	authSvc.EXPECT().IsEnabled().Return(true)
	authSvc.EXPECT().ValidateToken(mock.Anything, "valid-admin-token").Return(&domain.TokenClaims{
		Subject: "admin",
		Scopes:  []string{"admin:*:*"},
	}, nil)

	var capturedCtx context.Context
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedCtx = r.Context()
		w.WriteHeader(http.StatusOK)
	})

	mw := AuthMiddleware(authSvc, globalLimiter, ipLimiter, nil, adminTestLogger())
	wrappedHandler := mw(handler)

	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	req.Header.Set("Authorization", "Bearer valid-admin-token")
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify context has scopes and subject
	scopes := GetScopes(capturedCtx)
	assert.Equal(t, []string{"admin:*:*"}, scopes)
	assert.Equal(t, "admin", GetSubject(capturedCtx))
}

func TestAuthMiddleware_DirectToken(t *testing.T) {
	authSvc := inmocks.NewMockAuthService(t)
	globalLimiter := outmocks.NewMockRateLimiter(t)
	ipLimiter := outmocks.NewMockRateLimiter(t)

	globalLimiter.EXPECT().Allow(context.Background(), "global").Return(true)
	ipLimiter.EXPECT().Allow(context.Background(), "ip:192.168.1.100").Return(true)
	authSvc.EXPECT().IsEnabled().Return(true)
	// Token without "Bearer " prefix
	authSvc.EXPECT().ValidateToken(mock.Anything, "direct-token").Return(&domain.TokenClaims{
		Subject: "admin",
		Scopes:  []string{"admin:routes:read"},
	}, nil)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mw := AuthMiddleware(authSvc, globalLimiter, ipLimiter, nil, adminTestLogger())
	wrappedHandler := mw(handler)

	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	req.Header.Set("Authorization", "direct-token") // No "Bearer " prefix
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequireScope(t *testing.T) {
	tests := []struct {
		name       string
		scopes     []string
		resource   string
		action     string
		wantStatus int
	}{
		{
			name:       "wildcard scope grants access",
			scopes:     []string{"admin:*:*"},
			resource:   "routes",
			action:     "write",
			wantStatus: http.StatusOK,
		},
		{
			name:       "exact scope grants access",
			scopes:     []string{"admin:routes:read"},
			resource:   "routes",
			action:     "read",
			wantStatus: http.StatusOK,
		},
		{
			name:       "wrong resource denies access",
			scopes:     []string{"admin:secrets:read"},
			resource:   "routes",
			action:     "read",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "wrong action denies access",
			scopes:     []string{"admin:routes:read"},
			resource:   "routes",
			action:     "write",
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			mw := RequireScope(tt.resource, tt.action)
			wrappedHandler := mw(handler)

			req := httptest.NewRequest(http.MethodGet, "/admin/routes", nil)
			// Add scopes to context
			ctx := context.WithValue(req.Context(), ContextKeyScopes, tt.scopes)
			req = req.WithContext(ctx)

			rec := httptest.NewRecorder()
			wrappedHandler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

func TestHasAccess(t *testing.T) {
	ctx := context.WithValue(context.Background(), ContextKeyScopes, []string{"admin:routes:read", "admin:secrets:*"})

	assert.True(t, HasAccess(ctx, "routes", "read"))
	assert.False(t, HasAccess(ctx, "routes", "write"))
	assert.True(t, HasAccess(ctx, "secrets", "read"))
	assert.True(t, HasAccess(ctx, "secrets", "write"))
	assert.False(t, HasAccess(ctx, "config", "read"))
}
