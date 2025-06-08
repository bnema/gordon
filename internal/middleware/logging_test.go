package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewResponseWriter(t *testing.T) {
	originalWriter := httptest.NewRecorder()
	rw := NewResponseWriter(originalWriter)

	assert.NotNil(t, rw)
	assert.Equal(t, originalWriter, rw.ResponseWriter)
	assert.Equal(t, http.StatusOK, rw.StatusCode()) // Default status
	assert.Equal(t, 0, rw.BytesWritten())           // No bytes written yet
}

func TestResponseWriter_WriteHeader(t *testing.T) {
	originalWriter := httptest.NewRecorder()
	rw := NewResponseWriter(originalWriter)

	// Test setting status code
	rw.WriteHeader(http.StatusNotFound)
	assert.Equal(t, http.StatusNotFound, rw.StatusCode())

	// Note: In HTTP, calling WriteHeader multiple times is not typical behavior.
	// Our implementation captures the first call, which is the standard behavior.
}

func TestResponseWriter_Write(t *testing.T) {
	originalWriter := httptest.NewRecorder()
	rw := NewResponseWriter(originalWriter)

	// Test writing data
	data := []byte("Hello, World!")
	n, err := rw.Write(data)

	assert.NoError(t, err)
	assert.Equal(t, len(data), n)
	assert.Equal(t, len(data), rw.BytesWritten())

	// Test multiple writes
	moreData := []byte(" More data")
	n2, err2 := rw.Write(moreData)

	assert.NoError(t, err2)
	assert.Equal(t, len(moreData), n2)
	assert.Equal(t, len(data)+len(moreData), rw.BytesWritten())

	// Verify data was written to underlying writer
	result := originalWriter.Body.String()
	assert.Equal(t, "Hello, World! More data", result)
}

func TestResponseWriter_StatusCode(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{"OK", http.StatusOK},
		{"Not Found", http.StatusNotFound},
		{"Internal Server Error", http.StatusInternalServerError},
		{"Created", http.StatusCreated},
		{"Bad Request", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalWriter := httptest.NewRecorder()
			rw := NewResponseWriter(originalWriter)

			rw.WriteHeader(tt.statusCode)
			assert.Equal(t, tt.statusCode, rw.StatusCode())
		})
	}
}

func TestResponseWriter_BytesWritten(t *testing.T) {
	originalWriter := httptest.NewRecorder()
	rw := NewResponseWriter(originalWriter)

	// Initially zero
	assert.Equal(t, 0, rw.BytesWritten())

	// Write some data
	rw.Write([]byte("test"))
	assert.Equal(t, 4, rw.BytesWritten())

	// Write more data
	rw.Write([]byte("ing"))
	assert.Equal(t, 7, rw.BytesWritten())

	// Write empty data
	rw.Write([]byte(""))
	assert.Equal(t, 7, rw.BytesWritten()) // Should remain the same
}

func TestGetClientIP(t *testing.T) {
	tests := []struct {
		name       string
		setupReq   func(*http.Request)
		expectedIP string
	}{
		{
			name: "X-Forwarded-For single IP",
			setupReq: func(r *http.Request) {
				r.Header.Set("X-Forwarded-For", "192.168.1.100")
				r.RemoteAddr = "10.0.0.1:12345"
			},
			expectedIP: "192.168.1.100",
		},
		{
			name: "X-Forwarded-For multiple IPs",
			setupReq: func(r *http.Request) {
				r.Header.Set("X-Forwarded-For", "192.168.1.100, 10.0.0.1, 172.16.0.1")
				r.RemoteAddr = "10.0.0.1:12345"
			},
			expectedIP: "192.168.1.100",
		},
		{
			name: "X-Real-IP",
			setupReq: func(r *http.Request) {
				r.Header.Set("X-Real-IP", "203.0.113.195")
				r.RemoteAddr = "10.0.0.1:12345"
			},
			expectedIP: "203.0.113.195",
		},
		{
			name: "X-Forwarded",
			setupReq: func(r *http.Request) {
				r.Header.Set("X-Forwarded", "198.51.100.178")
				r.RemoteAddr = "10.0.0.1:12345"
			},
			expectedIP: "198.51.100.178",
		},
		{
			name: "RemoteAddr fallback",
			setupReq: func(r *http.Request) {
				r.RemoteAddr = "203.0.113.45:54321"
			},
			expectedIP: "203.0.113.45:54321",
		},
		{
			name: "X-Forwarded-For priority over X-Real-IP",
			setupReq: func(r *http.Request) {
				r.Header.Set("X-Forwarded-For", "1.2.3.4")
				r.Header.Set("X-Real-IP", "5.6.7.8")
				r.RemoteAddr = "10.0.0.1:12345"
			},
			expectedIP: "1.2.3.4",
		},
		{
			name: "X-Real-IP priority over X-Forwarded",
			setupReq: func(r *http.Request) {
				r.Header.Set("X-Real-IP", "1.2.3.4")
				r.Header.Set("X-Forwarded", "5.6.7.8")
				r.RemoteAddr = "10.0.0.1:12345"
			},
			expectedIP: "1.2.3.4",
		},
		{
			name: "Empty headers fallback to RemoteAddr",
			setupReq: func(r *http.Request) {
				r.Header.Set("X-Forwarded-For", "")
				r.Header.Set("X-Real-IP", "")
				r.RemoteAddr = "192.168.1.1:8080"
			},
			expectedIP: "192.168.1.1:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			tt.setupReq(req)

			ip := getClientIP(req)
			assert.Equal(t, tt.expectedIP, ip)
		})
	}
}

func TestRequestLogger(t *testing.T) {
	// Create a test handler
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test response"))
	})

	// Apply the middleware
	handler := RequestLogger(testHandler)

	// Create test request
	req := httptest.NewRequest("GET", "/test/path?param=value", nil)
	req.Header.Set("User-Agent", "test-agent")
	req.Header.Set("Referer", "https://example.com")
	req.Header.Set("X-Forwarded-For", "192.168.1.100")
	req.Host = "test.example.com"

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// Verify response
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "test response", rr.Body.String())

	// Note: In a real scenario, you'd want to capture the log output
	// For now, we just verify the middleware doesn't break the request flow
}

func TestContainerRequestLogger(t *testing.T) {
	containerID := "container123"
	domain := "app.example.com"

	// Create a test handler
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("container response"))
	})

	// Apply the middleware
	middleware := ContainerRequestLogger(containerID, domain)
	handler := middleware(testHandler)

	// Create test request
	req := httptest.NewRequest("POST", "/api/data", strings.NewReader("test data"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Real-IP", "10.0.0.5")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// Verify response
	assert.Equal(t, http.StatusCreated, rr.Code)
	assert.Equal(t, "container response", rr.Body.String())
}

func TestPanicRecovery(t *testing.T) {
	tests := []struct {
		name          string
		handler       http.HandlerFunc
		expectPanic   bool
		expectedCode  int
		expectedBody  string
	}{
		{
			name: "normal operation",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("success"))
			},
			expectPanic:  false,
			expectedCode: http.StatusOK,
			expectedBody: "success",
		},
		{
			name: "panic recovery",
			handler: func(w http.ResponseWriter, r *http.Request) {
				panic("test panic")
			},
			expectPanic:  true,
			expectedCode: http.StatusInternalServerError,
			expectedBody: "Internal Server Error\n",
		},
		{
			name: "nil panic",
			handler: func(w http.ResponseWriter, r *http.Request) {
				panic(nil)
			},
			expectPanic:  true,
			expectedCode: http.StatusInternalServerError,
			expectedBody: "Internal Server Error\n",
		},
		{
			name: "string panic",
			handler: func(w http.ResponseWriter, r *http.Request) {
				panic("string error")
			},
			expectPanic:  true,
			expectedCode: http.StatusInternalServerError,
			expectedBody: "Internal Server Error\n",
		},
		{
			name: "error panic",
			handler: func(w http.ResponseWriter, r *http.Request) {
				panic(assert.AnError)
			},
			expectPanic:  true,
			expectedCode: http.StatusInternalServerError,
			expectedBody: "Internal Server Error\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Apply panic recovery middleware
			handler := PanicRecovery(tt.handler)

			req := httptest.NewRequest("GET", "/test", nil)
			rr := httptest.NewRecorder()

			// This should not panic due to the middleware
			assert.NotPanics(t, func() {
				handler.ServeHTTP(rr, req)
			})

			assert.Equal(t, tt.expectedCode, rr.Code)
			assert.Equal(t, tt.expectedBody, rr.Body.String())
		})
	}
}

func TestCORS(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		expectedStatus int
		checkHeaders   bool
	}{
		{
			name:           "GET request",
			method:         "GET",
			expectedStatus: http.StatusOK,
			checkHeaders:   true,
		},
		{
			name:           "POST request",
			method:         "POST",
			expectedStatus: http.StatusOK,
			checkHeaders:   true,
		},
		{
			name:           "OPTIONS preflight",
			method:         "OPTIONS",
			expectedStatus: http.StatusOK,
			checkHeaders:   true,
		},
		{
			name:           "PUT request",
			method:         "PUT",
			expectedStatus: http.StatusOK,
			checkHeaders:   true,
		},
		{
			name:           "DELETE request",
			method:         "DELETE",
			expectedStatus: http.StatusOK,
			checkHeaders:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test handler that should only be called for non-OPTIONS requests
			called := false
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("test response"))
			})

			// Apply CORS middleware
			handler := CORS(testHandler)

			req := httptest.NewRequest(tt.method, "/test", nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			assert.Equal(t, tt.expectedStatus, rr.Code)

			if tt.checkHeaders {
				assert.Equal(t, "*", rr.Header().Get("Access-Control-Allow-Origin"))
				assert.Equal(t, "GET, POST, PUT, DELETE, OPTIONS", rr.Header().Get("Access-Control-Allow-Methods"))
				assert.Equal(t, "Content-Type, Authorization", rr.Header().Get("Access-Control-Allow-Headers"))
			}

			// For OPTIONS request, the handler should not be called
			if tt.method == "OPTIONS" {
				assert.False(t, called, "Handler should not be called for OPTIONS request")
				assert.Empty(t, rr.Body.String())
			} else {
				assert.True(t, called, "Handler should be called for non-OPTIONS request")
				assert.Equal(t, "test response", rr.Body.String())
			}
		})
	}
}

func TestChain(t *testing.T) {
	// Create test middlewares that add headers
	middleware1 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Middleware-1", "applied")
			next.ServeHTTP(w, r)
		})
	}

	middleware2 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Middleware-2", "applied")
			next.ServeHTTP(w, r)
		})
	}

	middleware3 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Middleware-3", "applied")
			next.ServeHTTP(w, r)
		})
	}

	// Create final handler
	finalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("final handler"))
	})

	tests := []struct {
		name        string
		middlewares []func(http.Handler) http.Handler
		expectHeaders map[string]string
	}{
		{
			name:        "no middlewares",
			middlewares: []func(http.Handler) http.Handler{},
			expectHeaders: map[string]string{},
		},
		{
			name:        "single middleware",
			middlewares: []func(http.Handler) http.Handler{middleware1},
			expectHeaders: map[string]string{
				"X-Middleware-1": "applied",
			},
		},
		{
			name:        "multiple middlewares",
			middlewares: []func(http.Handler) http.Handler{middleware1, middleware2, middleware3},
			expectHeaders: map[string]string{
				"X-Middleware-1": "applied",
				"X-Middleware-2": "applied",
				"X-Middleware-3": "applied",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Apply chain
			chain := Chain(tt.middlewares...)
			handler := chain(finalHandler)

			req := httptest.NewRequest("GET", "/test", nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)
			assert.Equal(t, "final handler", rr.Body.String())

			// Check expected headers
			for header, value := range tt.expectHeaders {
				assert.Equal(t, value, rr.Header().Get(header))
			}
		})
	}
}

func TestChain_ExecutionOrder(t *testing.T) {
	// Test that middlewares are executed in the correct order
	var executionOrder []string

	middleware1 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			executionOrder = append(executionOrder, "middleware1-before")
			next.ServeHTTP(w, r)
			executionOrder = append(executionOrder, "middleware1-after")
		})
	}

	middleware2 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			executionOrder = append(executionOrder, "middleware2-before")
			next.ServeHTTP(w, r)
			executionOrder = append(executionOrder, "middleware2-after")
		})
	}

	finalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		executionOrder = append(executionOrder, "final-handler")
		w.WriteHeader(http.StatusOK)
	})

	// Apply chain
	chain := Chain(middleware1, middleware2)
	handler := chain(finalHandler)

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	expectedOrder := []string{
		"middleware1-before",
		"middleware2-before",
		"final-handler",
		"middleware2-after",
		"middleware1-after",
	}

	assert.Equal(t, expectedOrder, executionOrder)
}

func TestMiddleware_Integration(t *testing.T) {
	// Test multiple middlewares working together
	finalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("integration test"))
	})

	// Chain multiple middlewares
	handler := Chain(
		PanicRecovery,
		CORS,
		RequestLogger,
	)(finalHandler)

	req := httptest.NewRequest("GET", "/integration", nil)
	req.Header.Set("X-Forwarded-For", "192.168.1.1")
	
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "integration test", rr.Body.String())
	
	// Check CORS headers are present
	assert.Equal(t, "*", rr.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "GET, POST, PUT, DELETE, OPTIONS", rr.Header().Get("Access-Control-Allow-Methods"))
}