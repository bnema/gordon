package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func TestHandler_Password_Success(t *testing.T) {
	authSvc := mocks.NewMockAuthService(t)

	authSvc.EXPECT().IsEnabled().Return(true)
	authSvc.EXPECT().GetAuthType().Return(domain.AuthTypePassword)
	authSvc.EXPECT().ValidatePassword(mock.Anything, "admin", "secret").Return(true)
	authSvc.EXPECT().GenerateToken(mock.Anything, "admin", []string{"push", "pull", "admin:*:*"}, 7*24*time.Hour).
		Return("jwt-token-here", nil)

	handler := NewHandler(authSvc, InternalAuth{}, testLogger())

	body := `{"username":"admin","password":"secret"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/password", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp PasswordResponse
	err := json.NewDecoder(rec.Body).Decode(&resp)
	assert.NoError(t, err)
	assert.Equal(t, "jwt-token-here", resp.Token)
	assert.Equal(t, 7*24*60*60, resp.ExpiresIn) // 7 days in seconds
	assert.NotEmpty(t, resp.IssuedAt)
}

func TestHandler_Password_InvalidCredentials(t *testing.T) {
	authSvc := mocks.NewMockAuthService(t)

	authSvc.EXPECT().IsEnabled().Return(true)
	authSvc.EXPECT().GetAuthType().Return(domain.AuthTypePassword)
	authSvc.EXPECT().ValidatePassword(mock.Anything, "admin", "wrongpassword").Return(false)

	handler := NewHandler(authSvc, InternalAuth{}, testLogger())

	body := `{"username":"admin","password":"wrongpassword"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/password", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid credentials")
}

func TestHandler_Password_AuthTypeToken(t *testing.T) {
	authSvc := mocks.NewMockAuthService(t)

	authSvc.EXPECT().IsEnabled().Return(true)
	authSvc.EXPECT().GetAuthType().Return(domain.AuthTypeToken)

	handler := NewHandler(authSvc, InternalAuth{}, testLogger())

	body := `{"username":"admin","password":"secret"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/password", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "password authentication not configured")
}

func TestHandler_Password_AuthDisabled(t *testing.T) {
	authSvc := mocks.NewMockAuthService(t)

	authSvc.EXPECT().IsEnabled().Return(false)

	handler := NewHandler(authSvc, InternalAuth{}, testLogger())

	body := `{"username":"admin","password":"secret"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/password", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "authentication is disabled")
}

func TestHandler_Password_MissingFields(t *testing.T) {
	authSvc := mocks.NewMockAuthService(t)

	authSvc.EXPECT().IsEnabled().Return(true)
	authSvc.EXPECT().GetAuthType().Return(domain.AuthTypePassword)

	handler := NewHandler(authSvc, InternalAuth{}, testLogger())

	tests := []struct {
		name string
		body string
	}{
		{"missing username", `{"password":"secret"}`},
		{"missing password", `{"username":"admin"}`},
		{"empty username", `{"username":"","password":"secret"}`},
		{"empty password", `{"username":"admin","password":""}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/auth/password", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func TestHandler_Password_MethodNotAllowed(t *testing.T) {
	handler := NewHandler(nil, InternalAuth{}, testLogger())

	methods := []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/auth/password", nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		})
	}
}

func TestHandler_Token_Success(t *testing.T) {
	authSvc := mocks.NewMockAuthService(t)

	authSvc.EXPECT().IsEnabled().Return(true)
	authSvc.EXPECT().GetAuthType().Return(domain.AuthTypePassword)
	authSvc.EXPECT().ValidatePassword(mock.Anything, "admin", "secret").Return(true)
	authSvc.EXPECT().GenerateAccessToken(mock.Anything, "admin", []string{"repository:myrepo:pull"}, 5*time.Minute).
		Return("access-token", nil)

	handler := NewHandler(authSvc, InternalAuth{}, testLogger())

	req := httptest.NewRequest(http.MethodGet, "/auth/token?scope=repository:myrepo:pull", nil)
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	err := json.NewDecoder(rec.Body).Decode(&resp)
	assert.NoError(t, err)
	assert.Equal(t, "access-token", resp["token"])
	assert.Equal(t, float64(300), resp["expires_in"])
}

func TestHandler_Token_Anonymous(t *testing.T) {
	authSvc := mocks.NewMockAuthService(t)

	authSvc.EXPECT().IsEnabled().Return(true)

	handler := NewHandler(authSvc, InternalAuth{}, testLogger())

	req := httptest.NewRequest(http.MethodGet, "/auth/token", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	err := json.NewDecoder(rec.Body).Decode(&resp)
	assert.NoError(t, err)
	assert.Equal(t, "", resp["token"])
	assert.Equal(t, float64(60), resp["expires_in"])
}

func TestHandler_Token_AuthDisabled(t *testing.T) {
	authSvc := mocks.NewMockAuthService(t)

	authSvc.EXPECT().IsEnabled().Return(false)

	handler := NewHandler(authSvc, InternalAuth{}, testLogger())

	req := httptest.NewRequest(http.MethodGet, "/auth/token", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	// Should return anonymous token when auth is disabled
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	err := json.NewDecoder(rec.Body).Decode(&resp)
	assert.NoError(t, err)
	assert.Equal(t, "", resp["token"])
}

func TestHandler_Token_InvalidCredentials(t *testing.T) {
	authSvc := mocks.NewMockAuthService(t)

	authSvc.EXPECT().IsEnabled().Return(true)
	authSvc.EXPECT().GetAuthType().Return(domain.AuthTypePassword)
	authSvc.EXPECT().ValidatePassword(mock.Anything, "admin", "wrongpassword").Return(false)

	handler := NewHandler(authSvc, InternalAuth{}, testLogger())

	req := httptest.NewRequest(http.MethodGet, "/auth/token", nil)
	req.SetBasicAuth("admin", "wrongpassword")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHandler_Token_InternalAuth(t *testing.T) {
	authSvc := mocks.NewMockAuthService(t)

	authSvc.EXPECT().IsEnabled().Return(true)
	authSvc.EXPECT().GenerateAccessToken(mock.Anything, "gordon-internal", []string{"repository:*:pull"}, 5*time.Minute).
		Return("internal-access-token", nil)

	internalAuth := InternalAuth{
		Username: "gordon-internal",
		Password: "internal-secret",
	}
	handler := NewHandler(authSvc, internalAuth, testLogger())

	req := httptest.NewRequest(http.MethodGet, "/auth/token", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.SetBasicAuth("gordon-internal", "internal-secret")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	err := json.NewDecoder(rec.Body).Decode(&resp)
	assert.NoError(t, err)
	assert.Equal(t, "internal-access-token", resp["token"])
}

func TestHandler_Token_TokenAuth(t *testing.T) {
	authSvc := mocks.NewMockAuthService(t)

	authSvc.EXPECT().IsEnabled().Return(true)
	authSvc.EXPECT().GetAuthType().Return(domain.AuthTypeToken)
	authSvc.EXPECT().ValidateToken(mock.Anything, "existing-jwt-token").Return(&domain.TokenClaims{
		Subject: "myuser",
	}, nil)
	authSvc.EXPECT().GenerateAccessToken(mock.Anything, "myuser", []string{"repository:*:pull"}, 5*time.Minute).
		Return("short-lived-token", nil)

	handler := NewHandler(authSvc, InternalAuth{}, testLogger())

	req := httptest.NewRequest(http.MethodGet, "/auth/token", nil)
	req.SetBasicAuth("myuser", "existing-jwt-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	err := json.NewDecoder(rec.Body).Decode(&resp)
	assert.NoError(t, err)
	assert.Equal(t, "short-lived-token", resp["token"])
}

func TestHandler_Token_MethodNotAllowed(t *testing.T) {
	handler := NewHandler(nil, InternalAuth{}, testLogger())

	methods := []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/auth/token", nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		})
	}
}

func TestHandler_NotFound(t *testing.T) {
	handler := NewHandler(nil, InternalAuth{}, testLogger())

	paths := []string{"/auth/unknown", "/auth/", "/auth"}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusNotFound, rec.Code)
		})
	}
}

func TestParseRequestedScopes_MultipleScopeParams(t *testing.T) {
	log := testLogger()
	h := &Handler{log: log}

	req := httptest.NewRequest(http.MethodGet, "/auth/token?scope=repository:repo1:pull&scope=repository:repo2:push", nil)

	scopes := h.parseRequestedScopes(req, log)

	assert.Len(t, scopes, 2)
	assert.Contains(t, scopes, "repository:repo1:pull")
	assert.Contains(t, scopes, "repository:repo2:push")
}

func TestParseRequestedScopes_InvalidScopeFiltered(t *testing.T) {
	log := testLogger()
	h := &Handler{log: log}

	tests := []struct {
		name       string
		query      string
		wantScopes []string
	}{
		{
			name:       "invalid format is skipped",
			query:      "scope=invalid-no-colons&scope=repository:valid:pull",
			wantScopes: []string{"repository:valid:pull"},
		},
		{
			name:       "missing actions is skipped",
			query:      "scope=repository:myrepo&scope=repository:valid:pull",
			wantScopes: []string{"repository:valid:pull"},
		},
		{
			name:       "all invalid returns default",
			query:      "scope=invalid&scope=also-invalid",
			wantScopes: []string{"repository:*:pull"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/auth/token?"+tt.query, nil)
			scopes := h.parseRequestedScopes(req, log)

			assert.Equal(t, tt.wantScopes, scopes)
		})
	}
}

func TestParseRequestedScopes_NoScopeReturnsDefault(t *testing.T) {
	log := testLogger()
	h := &Handler{log: log}

	req := httptest.NewRequest(http.MethodGet, "/auth/token", nil)
	scopes := h.parseRequestedScopes(req, log)

	assert.Equal(t, []string{"repository:*:pull"}, scopes)
}

func TestIsLocalhostRequest(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		want       bool
	}{
		{"127.0.0.1 is localhost", "127.0.0.1:12345", true},
		{"127.0.0.2 is localhost", "127.0.0.2:12345", true},
		{"::1 is localhost", "[::1]:12345", true},
		{"::1 without brackets", "::1:12345", true},
		{"192.168.1.1 is not localhost", "192.168.1.1:12345", false},
		{"10.0.0.1 is not localhost", "10.0.0.1:12345", false},
		{"public IP is not localhost", "8.8.8.8:12345", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/auth/token", nil)
			req.RemoteAddr = tt.remoteAddr

			result := isLocalhostRequest(req)

			assert.Equal(t, tt.want, result)
		})
	}
}

func TestHandler_isInternalAuth(t *testing.T) {
	tests := []struct {
		name         string
		username     string
		password     string
		internalAuth InternalAuth
		wantResult   bool
	}{
		{
			name:     "correct credentials",
			username: "gordon-internal",
			password: "internal-secret",
			internalAuth: InternalAuth{
				Username: "gordon-internal",
				Password: "internal-secret",
			},
			wantResult: true,
		},
		{
			name:     "incorrect username",
			username: "wrong-username",
			password: "internal-secret",
			internalAuth: InternalAuth{
				Username: "gordon-internal",
				Password: "internal-secret",
			},
			wantResult: false,
		},
		{
			name:     "incorrect password",
			username: "gordon-internal",
			password: "wrong-password",
			internalAuth: InternalAuth{
				Username: "gordon-internal",
				Password: "internal-secret",
			},
			wantResult: false,
		},
		{
			name:     "empty internal auth credentials",
			username: "gordon-internal",
			password: "internal-secret",
			internalAuth: InternalAuth{
				Username: "",
				Password: "",
			},
			wantResult: false,
		},
		{
			name:     "partially empty internal auth - empty password",
			username: "gordon-internal",
			password: "internal-secret",
			internalAuth: InternalAuth{
				Username: "gordon-internal",
				Password: "",
			},
			wantResult: false,
		},
		{
			name:     "partially empty internal auth - empty username",
			username: "gordon-internal",
			password: "internal-secret",
			internalAuth: InternalAuth{
				Username: "",
				Password: "internal-secret",
			},
			wantResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{
				internalAuth: tt.internalAuth,
			}

			result := h.isInternalAuth(tt.username, tt.password)

			assert.Equal(t, tt.wantResult, result)
		})
	}
}
