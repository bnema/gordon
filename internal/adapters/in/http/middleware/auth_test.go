package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"

	"github.com/bnema/gordon/internal/domain"
)

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func TestRegistryAuthV2_LocalhostBypassRequiresInternalAuth(t *testing.T) {
	log := testLogger()
	called := false
	authSvc := stubAuthService{
		enabled:  true,
		authType: domain.AuthTypePassword,
		validatePassword: func(_ context.Context, _ string, _ string) bool {
			called = true
			return false
		},
	}
	internalAuth := InternalRegistryAuth{
		Username: "internal",
		Password: "secret",
	}

	handler := RegistryAuthV2(authSvc, internalAuth, log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/v2/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.SetBasicAuth("internal", "secret")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, called, "auth service should not be used for internal loopback auth")
}

func TestRegistryAuthV2_LocalhostWrongInternalAuthFallsBack(t *testing.T) {
	log := testLogger()
	called := false
	authSvc := stubAuthService{
		enabled:  true,
		authType: domain.AuthTypePassword,
		validatePassword: func(_ context.Context, _ string, _ string) bool {
			called = true
			return false
		},
	}
	internalAuth := InternalRegistryAuth{
		Username: "internal",
		Password: "secret",
	}

	handler := RegistryAuthV2(authSvc, internalAuth, log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/v2/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.SetBasicAuth("internal", "wrong")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, `Basic realm="Gordon Registry"`, rec.Header().Get("WWW-Authenticate"))
	assert.True(t, called, "auth service should be used when internal auth fails")
}

func TestRegistryAuthV2_NonLocalhostIgnoresInternalAuth(t *testing.T) {
	log := testLogger()
	called := false
	authSvc := stubAuthService{
		enabled:  true,
		authType: domain.AuthTypePassword,
		validatePassword: func(_ context.Context, _ string, _ string) bool {
			called = true
			return false
		},
	}
	internalAuth := InternalRegistryAuth{
		Username: "internal",
		Password: "secret",
	}

	handler := RegistryAuthV2(authSvc, internalAuth, log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/v2/", nil)
	req.RemoteAddr = "192.168.1.10:12345"
	req.SetBasicAuth("internal", "secret")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, `Basic realm="Gordon Registry"`, rec.Header().Get("WWW-Authenticate"))
	assert.True(t, called, "auth service should be used for non-localhost requests")
}

type stubAuthService struct {
	enabled          bool
	authType         domain.AuthType
	validatePassword func(ctx context.Context, username, password string) bool
	validateToken    func(ctx context.Context, tokenString string) (*domain.TokenClaims, error)
}

func (s stubAuthService) GetAuthType() domain.AuthType {
	return s.authType
}

func (s stubAuthService) IsEnabled() bool {
	return s.enabled
}

func (s stubAuthService) ValidatePassword(ctx context.Context, username, password string) bool {
	if s.validatePassword != nil {
		return s.validatePassword(ctx, username, password)
	}
	return false
}

func (s stubAuthService) ValidateToken(ctx context.Context, tokenString string) (*domain.TokenClaims, error) {
	if s.validateToken != nil {
		return s.validateToken(ctx, tokenString)
	}
	return nil, errors.New("not implemented")
}

func (s stubAuthService) GenerateToken(context.Context, string, []string, time.Duration) (string, error) {
	return "", errors.New("not implemented")
}

func (s stubAuthService) GenerateAccessToken(context.Context, string, []string, time.Duration) (string, error) {
	return "", errors.New("not implemented")
}

func (s stubAuthService) RevokeToken(context.Context, string) error {
	return errors.New("not implemented")
}

func (s stubAuthService) RevokeAllTokens(context.Context) (int, error) {
	return 0, errors.New("not implemented")
}

func (s stubAuthService) ListTokens(context.Context) ([]domain.Token, error) {
	return nil, errors.New("not implemented")
}

func (s stubAuthService) GeneratePasswordHash(string) (string, error) {
	return "", errors.New("not implemented")
}

func (s stubAuthService) GetAuthStatus(context.Context) (*domain.AuthStatus, error) {
	return nil, errors.New("not implemented")
}

func (s stubAuthService) ExtendToken(context.Context, string) (string, error) {
	return "", errors.New("not implemented")
}

// Tests for checkScopeAccess function

func TestCheckScopeAccess_ActionMapping(t *testing.T) {
	log := testLogger()

	tests := []struct {
		name           string
		method         string
		expectedAction string
	}{
		{"GET maps to pull", http.MethodGet, "pull"},
		{"HEAD maps to pull", http.MethodHead, "pull"},
		{"PUT maps to push", http.MethodPut, "push"},
		{"POST maps to push", http.MethodPost, "push"},
		{"PATCH maps to push", http.MethodPatch, "push"},
		{"DELETE maps to push", http.MethodDelete, "push"},
		{"OPTIONS defaults to pull", http.MethodOptions, "pull"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := &domain.TokenClaims{
				Scopes: []string{"repository:myrepo:" + tt.expectedAction},
			}

			req := httptest.NewRequest(tt.method, "/v2/myrepo/manifests/latest", nil)
			result := checkScopeAccess(req, claims, log)

			assert.True(t, result, "method %s should be allowed with %s scope", tt.method, tt.expectedAction)
		})
	}
}

func TestCheckScopeAccess_RepoNameExtraction(t *testing.T) {
	log := testLogger()

	tests := []struct {
		name     string
		path     string
		wantRepo string
		scopes   []string
		want     bool
	}{
		{
			name:   "simple repo with manifests",
			path:   "/v2/myrepo/manifests/latest",
			scopes: []string{"repository:myrepo:pull"},
			want:   true,
		},
		{
			name:   "simple repo with blobs",
			path:   "/v2/myrepo/blobs/sha256:abc123",
			scopes: []string{"repository:myrepo:pull"},
			want:   true,
		},
		{
			name:   "nested repo with manifests",
			path:   "/v2/myorg/myapp/manifests/v1.0",
			scopes: []string{"repository:myorg/myapp:pull"},
			want:   true,
		},
		{
			name:   "deeply nested repo",
			path:   "/v2/myorg/team/myapp/manifests/latest",
			scopes: []string{"repository:myorg/team/myapp:pull"},
			want:   true,
		},
		{
			name:   "repo with tags list",
			path:   "/v2/myrepo/tags/list",
			scopes: []string{"repository:myrepo:pull"},
			want:   true,
		},
		{
			name:   "wrong repo name",
			path:   "/v2/myrepo/manifests/latest",
			scopes: []string{"repository:otherrepo:pull"},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := &domain.TokenClaims{Scopes: tt.scopes}
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			result := checkScopeAccess(req, claims, log)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestCheckScopeAccess_WildcardMatching(t *testing.T) {
	log := testLogger()

	tests := []struct {
		name   string
		path   string
		scopes []string
		want   bool
	}{
		{
			name:   "wildcard repo matches any",
			path:   "/v2/anyrepo/manifests/latest",
			scopes: []string{"repository:*:pull"},
			want:   true,
		},
		{
			name:   "org wildcard matches repo in org",
			path:   "/v2/myorg/myapp/manifests/latest",
			scopes: []string{"repository:myorg/*:pull"},
			want:   true,
		},
		{
			name:   "org wildcard does not match other org",
			path:   "/v2/otherorg/myapp/manifests/latest",
			scopes: []string{"repository:myorg/*:pull"},
			want:   false,
		},
		{
			name:   "wildcard action grants push",
			path:   "/v2/myrepo/manifests/latest",
			scopes: []string{"repository:myrepo:*"},
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := &domain.TokenClaims{Scopes: tt.scopes}
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			result := checkScopeAccess(req, claims, log)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestCheckScopeAccess_SpecialRoutes(t *testing.T) {
	log := testLogger()

	tests := []struct {
		name   string
		path   string
		scopes []string
		want   bool
	}{
		{
			name:   "v2 root is allowed",
			path:   "/v2/",
			scopes: []string{"repository:myrepo:pull"},
			want:   true,
		},
		{
			name:   "non-v2 path is allowed",
			path:   "/healthz",
			scopes: []string{"repository:myrepo:pull"},
			want:   true,
		},
		{
			name:   "catalog path is handled",
			path:   "/v2/_catalog",
			scopes: []string{"repository:myrepo:pull"},
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := &domain.TokenClaims{Scopes: tt.scopes}
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			result := checkScopeAccess(req, claims, log)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestCheckScopeAccess_DenialCases(t *testing.T) {
	log := testLogger()

	tests := []struct {
		name   string
		method string
		path   string
		scopes []string
		want   bool
	}{
		{
			name:   "pull scope denies push",
			method: http.MethodPut,
			path:   "/v2/myrepo/manifests/latest",
			scopes: []string{"repository:myrepo:pull"},
			want:   false,
		},
		{
			name:   "wrong repo denies access",
			method: http.MethodGet,
			path:   "/v2/myrepo/manifests/latest",
			scopes: []string{"repository:otherrepo:pull"},
			want:   false,
		},
		{
			name:   "empty scopes denies access",
			method: http.MethodGet,
			path:   "/v2/myrepo/manifests/latest",
			scopes: []string{},
			want:   false,
		},
		{
			name:   "invalid scope format is skipped",
			method: http.MethodGet,
			path:   "/v2/myrepo/manifests/latest",
			scopes: []string{"invalid-scope-format"},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := &domain.TokenClaims{Scopes: tt.scopes}
			req := httptest.NewRequest(tt.method, tt.path, nil)
			result := checkScopeAccess(req, claims, log)
			assert.Equal(t, tt.want, result)
		})
	}
}
