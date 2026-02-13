package remote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
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
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream unavailable"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	_, err := client.Deploy(context.Background(), "test.example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "502 Bad Gateway: upstream unavailable")
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

func TestClientListImages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/admin/images", r.URL.Path)
		require.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"images":[{"repository":"registry.example.com/app","tag":"latest","size":1234,"created":"2026-02-08T12:00:00Z","id":"sha256:abc","dangling":false}]}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	images, err := client.ListImages(context.Background())
	require.NoError(t, err)
	require.Len(t, images, 1)
	assert.Equal(t, "registry.example.com/app", images[0].Repository)
	assert.Equal(t, "latest", images[0].Tag)
}

func TestClientPruneImages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/admin/images/prune", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)

		var req dto.ImagePruneRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.NotNil(t, req.KeepLast)
		assert.Equal(t, 4, *req.KeepLast)
		require.NotNil(t, req.PruneDangling)
		assert.True(t, *req.PruneDangling)
		require.NotNil(t, req.PruneRegistry)
		assert.True(t, *req.PruneRegistry)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runtime":{"deleted_count":2,"space_reclaimed":2048},"registry":{"tags_removed":3,"blobs_removed":1,"space_reclaimed":4096}}`))
	}))
	defer srv.Close()

	keepLast := 4
	pruneDangling := true
	pruneRegistry := true
	client := NewClient(srv.URL)
	resp, err := client.PruneImages(context.Background(), dto.ImagePruneRequest{
		KeepLast:      &keepLast,
		PruneDangling: &pruneDangling,
		PruneRegistry: &pruneRegistry,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 2, resp.Runtime.DeletedCount)
	assert.Equal(t, 3, resp.Registry.TagsRemoved)
}
