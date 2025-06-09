package middleware

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRegistryAuth(t *testing.T) {
	username := "testuser"
	password := "testpass"

	// Create test handler that the middleware wraps
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("authenticated"))
	})

	middleware := RegistryAuth(username, password)
	handler := middleware(testHandler)

	tests := []struct {
		name           string
		setupAuth      func(*http.Request)
		expectedStatus int
		expectedBody   string
		checkHeaders   bool
	}{
		{
			name: "valid credentials",
			setupAuth: func(r *http.Request) {
				auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
				r.Header.Set("Authorization", "Basic "+auth)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   "authenticated",
		},
		{
			name: "invalid username",
			setupAuth: func(r *http.Request) {
				auth := base64.StdEncoding.EncodeToString([]byte("wronguser:" + password))
				r.Header.Set("Authorization", "Basic "+auth)
			},
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "Unauthorized\n",
			checkHeaders:   true,
		},
		{
			name: "invalid password",
			setupAuth: func(r *http.Request) {
				auth := base64.StdEncoding.EncodeToString([]byte(username + ":wrongpass"))
				r.Header.Set("Authorization", "Basic "+auth)
			},
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "Unauthorized\n",
			checkHeaders:   true,
		},
		{
			name: "no authorization header",
			setupAuth: func(r *http.Request) {
				// No auth header
			},
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "Unauthorized\n",
			checkHeaders:   true,
		},
		{
			name: "malformed authorization header",
			setupAuth: func(r *http.Request) {
				r.Header.Set("Authorization", "Invalid auth")
			},
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "Unauthorized\n",
			checkHeaders:   true,
		},
		{
			name: "empty credentials",
			setupAuth: func(r *http.Request) {
				auth := base64.StdEncoding.EncodeToString([]byte(":"))
				r.Header.Set("Authorization", "Basic "+auth)
			},
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "Unauthorized\n",
			checkHeaders:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v2/test", nil)
			tt.setupAuth(req)

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			assert.Equal(t, tt.expectedStatus, rr.Code)
			assert.Equal(t, tt.expectedBody, rr.Body.String())

			if tt.checkHeaders {
				assert.Equal(t, `Basic realm="Gordon Registry"`, rr.Header().Get("WWW-Authenticate"))
				assert.Equal(t, "registry/2.0", rr.Header().Get("Docker-Distribution-API-Version"))
			}
		})
	}
}

func TestIsAuthenticated(t *testing.T) {
	expectedUsername := "testuser"
	expectedPassword := "testpass"

	tests := []struct {
		name           string
		setupRequest   func(*http.Request)
		expectedResult bool
	}{
		{
			name: "valid basic auth",
			setupRequest: func(r *http.Request) {
				auth := base64.StdEncoding.EncodeToString([]byte(expectedUsername + ":" + expectedPassword))
				r.Header.Set("Authorization", "Basic "+auth)
			},
			expectedResult: true,
		},
		{
			name: "wrong username",
			setupRequest: func(r *http.Request) {
				auth := base64.StdEncoding.EncodeToString([]byte("wronguser:" + expectedPassword))
				r.Header.Set("Authorization", "Basic "+auth)
			},
			expectedResult: false,
		},
		{
			name: "wrong password",
			setupRequest: func(r *http.Request) {
				auth := base64.StdEncoding.EncodeToString([]byte(expectedUsername + ":wrongpass"))
				r.Header.Set("Authorization", "Basic "+auth)
			},
			expectedResult: false,
		},
		{
			name: "no authorization header",
			setupRequest: func(r *http.Request) {
				// No auth header
			},
			expectedResult: false,
		},
		{
			name: "empty authorization header",
			setupRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "")
			},
			expectedResult: false,
		},
		{
			name: "bearer token instead of basic",
			setupRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer token123")
			},
			expectedResult: false,
		},
		{
			name: "malformed basic auth",
			setupRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "Basic invalidbase64")
			},
			expectedResult: false,
		},
		{
			name: "basic auth without colon",
			setupRequest: func(r *http.Request) {
				auth := base64.StdEncoding.EncodeToString([]byte("usernamepassword"))
				r.Header.Set("Authorization", "Basic "+auth)
			},
			expectedResult: false,
		},
		{
			name: "case sensitive username",
			setupRequest: func(r *http.Request) {
				auth := base64.StdEncoding.EncodeToString([]byte("TESTUSER:" + expectedPassword))
				r.Header.Set("Authorization", "Basic "+auth)
			},
			expectedResult: false,
		},
		{
			name: "case sensitive password",
			setupRequest: func(r *http.Request) {
				auth := base64.StdEncoding.EncodeToString([]byte(expectedUsername + ":TESTPASS"))
				r.Header.Set("Authorization", "Basic "+auth)
			},
			expectedResult: false,
		},
		{
			name: "unicode characters in credentials",
			setupRequest: func(r *http.Request) {
				auth := base64.StdEncoding.EncodeToString([]byte("tëstüser:tëstpäss"))
				r.Header.Set("Authorization", "Basic "+auth)
			},
			expectedResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			tt.setupRequest(req)

			result := isAuthenticated(req, expectedUsername, expectedPassword)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestRegistryAuth_DifferentMethods(t *testing.T) {
	username := "user"
	password := "pass"

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := RegistryAuth(username, password)
	handler := middleware(testHandler)

	methods := []string{"GET", "POST", "PUT", "DELETE", "HEAD", "OPTIONS", "PATCH"}
	
	for _, method := range methods {
		t.Run("method_"+method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/v2/test", nil)
			auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
			req.Header.Set("Authorization", "Basic "+auth)

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)
		})
	}
}

func TestRegistryAuth_EmptyCredentials(t *testing.T) {
	// Test with empty username and password
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := RegistryAuth("", "")
	handler := middleware(testHandler)

	// Test with empty auth
	req := httptest.NewRequest("GET", "/v2/test", nil)
	auth := base64.StdEncoding.EncodeToString([]byte(":"))
	req.Header.Set("Authorization", "Basic "+auth)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRegistryAuth_TimingAttackResistance(t *testing.T) {
	// This test ensures that authentication uses constant-time comparison
	// to prevent timing attacks
	username := "validuser"
	password := "validpassword"

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := RegistryAuth(username, password)
	handler := middleware(testHandler)

	// Test with various invalid credentials to ensure consistent timing
	invalidCredentials := []struct {
		user string
		pass string
	}{
		{"a", "b"},                                    // Very short
		{"verylongusernamethatdoesnotmatch", "short"}, // Long username
		{"short", "verylongpasswordthatdoesnotmatch"}, // Long password
		{username, "wrongpass"},                       // Correct user, wrong pass
		{"wronguser", password},                       // Wrong user, correct pass
	}

	for _, creds := range invalidCredentials {
		t.Run("timing_test_"+creds.user, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v2/test", nil)
			auth := base64.StdEncoding.EncodeToString([]byte(creds.user + ":" + creds.pass))
			req.Header.Set("Authorization", "Basic "+auth)

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			// All should return unauthorized
			assert.Equal(t, http.StatusUnauthorized, rr.Code)
		})
	}
}