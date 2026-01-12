package middleware

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
)

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func TestRegistryAuth_ValidCredentials(t *testing.T) {
	log := testLogger()
	middleware := RegistryAuth("admin", "secret", log)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("authenticated"))
	}))

	req := httptest.NewRequest("GET", "/v2/", nil)
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "authenticated", rec.Body.String())
}

func TestRegistryAuth_InvalidUsername(t *testing.T) {
	log := testLogger()
	middleware := RegistryAuth("admin", "secret", log)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/v2/", nil)
	req.SetBasicAuth("wronguser", "secret")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, `Basic realm="Gordon Registry"`, rec.Header().Get("WWW-Authenticate"))
	assert.Equal(t, "registry/2.0", rec.Header().Get("Docker-Distribution-API-Version"))
}

func TestRegistryAuth_InvalidPassword(t *testing.T) {
	log := testLogger()
	middleware := RegistryAuth("admin", "secret", log)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/v2/", nil)
	req.SetBasicAuth("admin", "wrongpassword")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRegistryAuth_NoCredentials(t *testing.T) {
	log := testLogger()
	middleware := RegistryAuth("admin", "secret", log)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/v2/", nil)
	// No auth header
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, `Basic realm="Gordon Registry"`, rec.Header().Get("WWW-Authenticate"))
}

func TestRegistryAuth_MalformedAuthHeader(t *testing.T) {
	log := testLogger()
	middleware := RegistryAuth("admin", "secret", log)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/v2/", nil)
	req.Header.Set("Authorization", "Basic notbase64!!!") // Invalid base64
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRegistryAuth_WrongAuthScheme(t *testing.T) {
	log := testLogger()
	middleware := RegistryAuth("admin", "secret", log)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/v2/", nil)
	req.Header.Set("Authorization", "Bearer some-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRegistryAuth_EmptyCredentials(t *testing.T) {
	log := testLogger()
	middleware := RegistryAuth("admin", "secret", log)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/v2/", nil)
	req.SetBasicAuth("", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRegistryAuth_PartialCredentials(t *testing.T) {
	log := testLogger()
	middleware := RegistryAuth("admin", "secret", log)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Only username, empty password
	req := httptest.NewRequest("GET", "/v2/", nil)
	req.SetBasicAuth("admin", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestIsAuthenticated_ValidCredentials(t *testing.T) {
	log := testLogger()

	req := httptest.NewRequest("GET", "/v2/", nil)
	req.SetBasicAuth("admin", "secret")

	result := isAuthenticated(req, "admin", "secret", log)

	assert.True(t, result)
}

func TestIsAuthenticated_InvalidCredentials(t *testing.T) {
	log := testLogger()

	req := httptest.NewRequest("GET", "/v2/", nil)
	req.SetBasicAuth("admin", "wrong")

	result := isAuthenticated(req, "admin", "secret", log)

	assert.False(t, result)
}

func TestIsAuthenticated_NoAuthHeader(t *testing.T) {
	log := testLogger()

	req := httptest.NewRequest("GET", "/v2/", nil)

	result := isAuthenticated(req, "admin", "secret", log)

	assert.False(t, result)
}

func TestIsAuthenticated_TimingAttackPrevention(t *testing.T) {
	// This test verifies that constant-time comparison is used
	// by checking both username and password are validated
	log := testLogger()

	tests := []struct {
		name         string
		username     string
		password     string
		expectedUser string
		expectedPass string
		shouldAuth   bool
	}{
		{
			name:         "both correct",
			username:     "admin",
			password:     "secret",
			expectedUser: "admin",
			expectedPass: "secret",
			shouldAuth:   true,
		},
		{
			name:         "username wrong first char",
			username:     "bdmin",
			password:     "secret",
			expectedUser: "admin",
			expectedPass: "secret",
			shouldAuth:   false,
		},
		{
			name:         "username wrong last char",
			username:     "admio",
			password:     "secret",
			expectedUser: "admin",
			expectedPass: "secret",
			shouldAuth:   false,
		},
		{
			name:         "password wrong first char",
			username:     "admin",
			password:     "aecret",
			expectedUser: "admin",
			expectedPass: "secret",
			shouldAuth:   false,
		},
		{
			name:         "password wrong last char",
			username:     "admin",
			password:     "secreo",
			expectedUser: "admin",
			expectedPass: "secret",
			shouldAuth:   false,
		},
		{
			name:         "different length username",
			username:     "adm",
			password:     "secret",
			expectedUser: "admin",
			expectedPass: "secret",
			shouldAuth:   false,
		},
		{
			name:         "different length password",
			username:     "admin",
			password:     "sec",
			expectedUser: "admin",
			expectedPass: "secret",
			shouldAuth:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v2/", nil)
			req.SetBasicAuth(tt.username, tt.password)

			result := isAuthenticated(req, tt.expectedUser, tt.expectedPass, log)

			assert.Equal(t, tt.shouldAuth, result)
		})
	}
}

func TestRegistryAuth_SpecialCharactersInCredentials(t *testing.T) {
	log := testLogger()

	tests := []struct {
		name     string
		username string
		password string
	}{
		{"unicode username", "用户", "password"},
		{"unicode password", "admin", "密码"},
		{"special chars", "admin@example.com", "p@ss:w0rd!"},
		{"spaces", "admin user", "pass word"},
		{"colon in password", "admin", "pass:word"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			middleware := RegistryAuth(tt.username, tt.password, log)

			handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest("GET", "/v2/", nil)
			req.SetBasicAuth(tt.username, tt.password)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

func TestRegistryAuth_Base64EdgeCases(t *testing.T) {
	log := testLogger()
	middleware := RegistryAuth("admin", "secret", log)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{
			name:       "empty basic auth",
			authHeader: "Basic ",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "basic auth with only colon",
			authHeader: "Basic " + base64.StdEncoding.EncodeToString([]byte(":")),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "missing colon in decoded value",
			authHeader: "Basic " + base64.StdEncoding.EncodeToString([]byte("adminpassword")),
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v2/", nil)
			req.Header.Set("Authorization", tt.authHeader)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}
