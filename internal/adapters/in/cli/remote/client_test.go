package remote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withFastRetry(t *testing.T) {
	t.Helper()
	prevAttempts := retryMaxAttempts
	prevDelay := retryBaseDelay
	retryMaxAttempts = 3
	retryBaseDelay = 10 * time.Millisecond
	t.Cleanup(func() {
		retryMaxAttempts = prevAttempts
		retryBaseDelay = prevDelay
	})
}

func TestClientRestartRetriesOn5xx(t *testing.T) {
	withFastRetry(t)

	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/admin/restart/test.example.com", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		current := atomic.AddInt32(&attempts, 1)
		if current < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"temporary outage"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"restarted","domain":"test.example.com"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	result, err := client.Restart(context.Background(), "test.example.com", false)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "test.example.com", result.Domain)
	assert.EqualValues(t, 3, atomic.LoadInt32(&attempts))
}

func TestClientReloadRetriesOn5xx(t *testing.T) {
	withFastRetry(t)

	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/admin/reload", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		current := atomic.AddInt32(&attempts, 1)
		if current < 2 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`upstream down`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"reloaded"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	err := client.Reload(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 2, atomic.LoadInt32(&attempts))
}

func TestClientDeployReturnsErrorAfterRetryExhaustion(t *testing.T) {
	withFastRetry(t)

	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/admin/deploy/test.example.com", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"failed to deploy container"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	_, err := client.Deploy(context.Background(), "test.example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500 Internal Server Error: failed to deploy container")
	assert.EqualValues(t, retryMaxAttempts, atomic.LoadInt32(&attempts))
}

func TestClientWithInsecureTLS_AllowsSelfSignedCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/admin/status", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"routes":0,"registry_domain":"","registry_port":0,"server_port":0,"auto_route":false,"network_isolation":false,"container_status":{}}`))
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL).GetStatus(context.Background())
	require.Error(t, err)

	_, err = NewClient(srv.URL, WithInsecureTLS(true)).GetStatus(context.Background())
	require.NoError(t, err)
}
