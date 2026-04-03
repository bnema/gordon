package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/out/accesslog"
)

// mockAccessLogWriter records written entries and optionally returns an error.
type mockAccessLogWriter struct {
	entries []accesslog.Entry
	err     error
}

func (m *mockAccessLogWriter) Write(entry accesslog.Entry) error {
	m.entries = append(m.entries, entry)
	return m.err
}

func TestAccessLogger_EmitsEntryPerRequest(t *testing.T) {
	mock := &mockAccessLogWriter{}
	log := testLogger()

	handler := AccessLogger(mock, false, log, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/some/path?foo=bar", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	req.Header.Set("User-Agent", "test-agent")
	req.Header.Set("Referer", "https://example.com")
	req.RequestURI = "/some/path?foo=bar"
	req.Host = "example.com"
	req.Proto = "HTTP/1.1"
	rw := httptest.NewRecorder()

	handler.ServeHTTP(rw, req)

	require.Len(t, mock.entries, 1)
	e := mock.entries[0]
	assert.Equal(t, http.MethodGet, e.Method)
	assert.Equal(t, "/some/path", e.Path)
	assert.Equal(t, "foo=bar", e.Query)
	assert.Equal(t, "example.com", e.Host)
	assert.Equal(t, http.StatusOK, e.Status)
	assert.Equal(t, 5, e.BytesSent)
	assert.Equal(t, "test-agent", e.UserAgent)
	assert.Equal(t, "https://example.com", e.Referer)
	assert.Equal(t, "HTTP/1.1", e.Proto)
	assert.NotEmpty(t, e.RequestID)
	assert.Equal(t, "192.168.1.100", e.ClientIP)
}

func TestAccessLogger_RequestIDReused(t *testing.T) {
	mock := &mockAccessLogWriter{}
	log := testLogger()

	handler := AccessLogger(mock, false, log, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "fixed-id-123")
	rw := httptest.NewRecorder()

	handler.ServeHTTP(rw, req)

	require.Len(t, mock.entries, 1)
	assert.Equal(t, "fixed-id-123", mock.entries[0].RequestID)
}

func TestAccessLogger_ExcludeHealthCheckUA(t *testing.T) {
	mock := &mockAccessLogWriter{}
	log := testLogger()

	handler := AccessLogger(mock, true, log, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("User-Agent", "Gordon-HealthCheck/1.0")
	rw := httptest.NewRecorder()

	handler.ServeHTTP(rw, req)

	assert.Empty(t, mock.entries, "health-check request should not be logged")
}

func TestAccessLogger_ExcludeLoopbackIP(t *testing.T) {
	mock := &mockAccessLogWriter{}
	log := testLogger()

	handler := AccessLogger(mock, true, log, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rw := httptest.NewRecorder()

	handler.ServeHTTP(rw, req)

	assert.Empty(t, mock.entries, "loopback request should not be logged")
}

func TestAccessLogger_NoExcludeWhenDisabled(t *testing.T) {
	mock := &mockAccessLogWriter{}
	log := testLogger()

	handler := AccessLogger(mock, false, log, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("User-Agent", "Gordon-HealthCheck/1.0")
	rw := httptest.NewRecorder()

	handler.ServeHTTP(rw, req)

	assert.Len(t, mock.entries, 1, "health-check request should be logged when excludeHealthChecks=false")
}

func TestAccessLogger_WriteFailureDoesNotAffectResponse(t *testing.T) {
	mock := &mockAccessLogWriter{err: fmt.Errorf("disk full")}
	log := testLogger()

	handler := AccessLogger(mock, false, log, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rw := httptest.NewRecorder()

	// Should not panic or change the response.
	handler.ServeHTTP(rw, req)

	assert.Equal(t, http.StatusOK, rw.Code)
	assert.Equal(t, "ok", rw.Body.String())
}

func TestAccessLogger_PanicRecoveryChain_Captures500(t *testing.T) {
	mock := &mockAccessLogWriter{}
	log := testLogger()

	// Chain: AccessLogger (outer) → PanicRecovery → panicking handler
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})
	chain := AccessLogger(mock, false, log, nil)(PanicRecovery(log)(panicHandler))

	req := httptest.NewRequest(http.MethodGet, "/crash", nil)
	rw := httptest.NewRecorder()

	chain.ServeHTTP(rw, req)

	// PanicRecovery should have written 500.
	assert.Equal(t, http.StatusInternalServerError, rw.Code)

	// AccessLogger must have captured the 500.
	require.Len(t, mock.entries, 1)
	assert.Equal(t, http.StatusInternalServerError, mock.entries[0].Status)
}
