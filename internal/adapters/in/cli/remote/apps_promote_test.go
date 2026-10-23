package remote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
)

// These tests use a real httptest server (sockets), so they run outside
// synctest and observe the request synchronously.

func TestClientPromoteApp_PostsBodyWithKey(t *testing.T) {
	var gotKey string
	var gotBody dto.AppPromoteRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/admin/apps/blog/promote", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		gotKey = r.Header.Get("Idempotency-Key")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(dto.AppDeployResponse{Op: gotKey, App: "blog", Status: dto.AppStatusRunning})
	}))
	defer srv.Close()

	req := dto.AppPromoteRequest{Service: "web", Image: "web@sha256:abc", ExpectedRevision: "rev-1"}
	resp, key, err := NewClient(srv.URL).PromoteApp(context.Background(), "blog", req)
	require.NoError(t, err)
	assert.Equal(t, req, gotBody)
	assert.NotEmpty(t, key)
	assert.Equal(t, gotKey, key)
	assert.Equal(t, dto.AppStatusRunning, resp.Status)
}

func TestClientPromoteApp_ConflictIsNotRetried(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(dto.AppError{Error: "state-conflict", Message: "desired revision changed"})
	}))
	defer srv.Close()

	_, key, err := NewClient(srv.URL).PromoteApp(context.Background(), "blog", dto.AppPromoteRequest{Service: "web", Image: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "desired revision changed")
	assert.NotEmpty(t, key)
	assert.EqualValues(t, 1, atomic.LoadInt32(&calls), "a 409 is never replayed")
}
