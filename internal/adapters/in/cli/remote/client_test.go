package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestClientFindAttachmentTargetsByImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/admin/attachments/by-image/postgres:16", r.URL.Path)
		require.Equal(t, "", r.URL.RawQuery)
		require.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"image":"postgres:16","targets":["app.example.com","workers"]}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	targets, err := client.FindAttachmentTargetsByImage(context.Background(), "postgres:16")
	require.NoError(t, err)
	assert.Equal(t, []string{"app.example.com", "workers"}, targets)
}

func TestClientFindAttachmentTargetsByImage_WithSlashContainingImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/admin/attachments/by-image/registry/org/image:tag", r.URL.Path)
		require.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"image":"registry/org/image:tag","targets":["workers"]}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	targets, err := client.FindAttachmentTargetsByImage(context.Background(), "registry/org/image:tag")
	require.NoError(t, err)
	assert.Equal(t, []string{"workers"}, targets)
}

func TestParseResponse_CapsErrorBodySize(t *testing.T) {
	largeBody := strings.Repeat("x", 10*1024*1024)
	resp := &http.Response{
		StatusCode: 500,
		Status:     "500 Internal Server Error",
		Body:       io.NopCloser(strings.NewReader(largeBody)),
		Header:     make(http.Header),
	}

	err := parseResponse(resp, nil)

	require.Error(t, err)
	assert.Less(t, len(err.Error()), 2*1024, "error body should be capped to ~1KB")
}

func TestStreamLogs_CapsErrorBodySize(t *testing.T) {
	largeBody := strings.Repeat("x", 10*1024*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(largeBody))
	}))
	defer srv.Close()

	c := &Client{
		baseURL:    srv.URL,
		httpClient: srv.Client(),
	}

	_, err := c.StreamProcessLogs(context.Background(), 100)

	require.Error(t, err)
	assert.Less(t, len(err.Error()), 2*1024, "streaming error body should be capped to ~1KB")
}

func TestRequestWithRetry_CapsErrorBodySize(t *testing.T) {
	withFastRetry(t)

	largeBody := strings.Repeat("x", 10*1024*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(largeBody))
	}))
	defer srv.Close()

	c := &Client{
		baseURL:    srv.URL,
		httpClient: srv.Client(),
	}

	_, err := c.requestWithRetry(context.Background(), http.MethodPost, "/test", nil)

	require.Error(t, err)
	assert.Less(t, len(err.Error()), 2*1024, "retry error body should be capped to ~1KB")
}

func TestExchangeRegistryToken_Success(t *testing.T) {
	const subject = "testuser"
	const gordonToken = "gordon-jwt-token"
	const shortToken = "short-lived-123"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/auth/token", r.URL.Path)
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "repository:*:push,pull", r.URL.Query().Get("scope"))
		require.Equal(t, "gordon-registry", r.URL.Query().Get("service"))

		u, p, ok := r.BasicAuth()
		require.True(t, ok, "expected Basic Auth")
		assert.Equal(t, subject, u)
		assert.Equal(t, gordonToken, p)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"short-lived-123","expires_in":300}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, WithToken(gordonToken))
	token, err := client.ExchangeRegistryToken(context.Background(), subject)
	require.NoError(t, err)
	assert.Equal(t, shortToken, token)
}

func TestExchangeRegistryToken_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid credentials"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, WithToken("bad-token"))
	_, err := client.ExchangeRegistryToken(context.Background(), "user")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

func TestExchangeRegistryToken_EmptyToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"","expires_in":300}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, WithToken("some-token"))
	_, err := client.ExchangeRegistryToken(context.Background(), "user")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestHTTPError_ErrorString(t *testing.T) {
	err := &HTTPError{StatusCode: 403, Status: "403 Forbidden", Body: "insufficient scope"}
	assert.Equal(t, "403 Forbidden: insufficient scope", err.Error())
}

func TestHTTPError_Unwrap(t *testing.T) {
	httpErr := &HTTPError{StatusCode: 403, Status: "403 Forbidden", Body: "insufficient scope"}
	wrapped := fmt.Errorf("deploy intent: %w", httpErr)

	var target *HTTPError
	assert.True(t, errors.As(wrapped, &target))
	assert.Equal(t, 403, target.StatusCode)
}

func TestParseErrorResponse_ReturnsHTTPError(t *testing.T) {
	resp := &http.Response{
		StatusCode: 403,
		Status:     "403 Forbidden",
		Body:       io.NopCloser(strings.NewReader("")),
	}
	err := parseErrorResponse(resp, []byte(`{"error":"insufficient scope"}`))

	var httpErr *HTTPError
	require.True(t, errors.As(err, &httpErr))
	assert.Equal(t, 403, httpErr.StatusCode)
	assert.Equal(t, "insufficient scope", httpErr.Body)
}

func TestParseErrorResponse_DeployFailureFields(t *testing.T) {
	resp := &http.Response{
		StatusCode: 500,
		Status:     "500 Internal Server Error",
		Body:       io.NopCloser(strings.NewReader("")),
	}
	err := parseErrorResponse(resp, []byte(`{"error":"container exited during startup","cause":"health check failed","hint":"check DATABASE_URL","logs":["booting app","connection refused"]}`))

	var httpErr *HTTPError
	require.True(t, errors.As(err, &httpErr))
	assert.Equal(t, 500, httpErr.StatusCode)
	assert.Equal(t, "container exited during startup", httpErr.Body)
	assert.Equal(t, "health check failed", httpErr.Cause)
	assert.Equal(t, "check DATABASE_URL", httpErr.Hint)
	assert.Equal(t, []string{"booting app", "connection refused"}, httpErr.Logs)
}

func TestParseErrorResponse_ErrorOnlyJSONNotStructured(t *testing.T) {
	resp := &http.Response{
		StatusCode: 500,
		Status:     "500 Internal Server Error",
		Body:       io.NopCloser(strings.NewReader("")),
	}
	err := parseErrorResponse(resp, []byte(`{"error":"failed to deploy container"}`))

	var httpErr *HTTPError
	require.True(t, errors.As(err, &httpErr))
	assert.Equal(t, "failed to deploy container", httpErr.Body)
	assert.False(t, httpErr.Structured)
	assert.Empty(t, httpErr.Cause)
	assert.Empty(t, httpErr.Hint)
	assert.Empty(t, httpErr.Logs)
}

func TestParseErrorResponse_NonJSON(t *testing.T) {
	resp := &http.Response{
		StatusCode: 500,
		Status:     "500 Internal Server Error",
		Body:       io.NopCloser(strings.NewReader("")),
	}
	err := parseErrorResponse(resp, []byte("plain text error"))

	var httpErr *HTTPError
	require.True(t, errors.As(err, &httpErr))
	assert.Equal(t, 500, httpErr.StatusCode)
	assert.Equal(t, "plain text error", httpErr.Body)
}

func TestExchangeRegistryToken_AccessTokenFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Token field empty, but access_token has a value
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fallback-token-456",
			"expires_in":   300,
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, WithToken("long-lived"))
	token, err := client.ExchangeRegistryToken(context.Background(), "ci-bot")
	require.NoError(t, err)
	assert.Equal(t, "fallback-token-456", token)
}
