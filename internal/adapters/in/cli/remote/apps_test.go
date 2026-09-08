package remote

// Tests for the v2.50 app mutation transport (apps.go): every mutation
// carries a client-generated Idempotency-Key, retryable gateway statuses
// surface OutcomeUnknownError without replaying the mutation, and
// ambiguous outcomes recover via the by-key endpoint.

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

func TestClientApplyApp_SendsIdempotencyKey(t *testing.T) {
	var gotKey string
	var gotBody dto.AppApplyRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/admin/apps/apply", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		gotKey = r.Header.Get("Idempotency-Key")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(dto.AppApplyResponse{
			App: "blog", ResultingRevision: "rev-b", Pending: true, Intent: "apply-1",
		})
	}))
	defer srv.Close()

	resp, err := NewClient(srv.URL).ApplyApp(context.Background(), dto.AppApplyRequest{ManifestTOML: "x"})
	require.NoError(t, err)
	assert.Equal(t, "blog", resp.App)
	assert.NotEmpty(t, gotKey, "mutations must carry an Idempotency-Key")
	assert.Equal(t, "x", gotBody.ManifestTOML)
}

func TestClientDeployApp_GatewayErrorIsOutcomeUnknownWithoutReplay(t *testing.T) {
	var mutations int32
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/admin/apps/blog/deploy", r.URL.Path)
		atomic.AddInt32(&mutations, 1)
		gotKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"upstream unavailable"}`))
	}))
	defer srv.Close()

	resp, key, err := NewClient(srv.URL).DeployApp(context.Background(), "blog", dto.AppDeployRequest{})
	require.Nil(t, resp)
	require.Error(t, err)
	var unknown *OutcomeUnknownError
	require.ErrorAs(t, err, &unknown)
	assert.EqualValues(t, 1, atomic.LoadInt32(&mutations), "mutations must never replay on ambiguity")
	assert.NotEmpty(t, key)
	assert.Equal(t, gotKey, key, "recovery re-queries with the SAME key")
}

func TestClientOperationByKey_RecoveryPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		require.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(dto.AppDeployResponse{
			Op: "op-1", App: "blog", Revision: "rev-b", Outcome: "success",
		})
	}))
	defer srv.Close()

	resp, err := NewClient(srv.URL).OperationByKey(context.Background(), "blog", "key-abc")
	require.NoError(t, err)
	assert.Equal(t, "op-1", resp.Op)
	assert.Equal(t, "/admin/apps/blog/operations/by-key/key-abc", gotPath)
}

func TestClientLifecycle_MutationsCarryKeys(t *testing.T) {
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(dto.AppDeployResponse{Op: "op-x", Outcome: "success"})
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	ctx := context.Background()
	_, _, err := client.StopApp(ctx, "blog")
	require.NoError(t, err)
	_, _, err = client.StartApp(ctx, "blog")
	require.NoError(t, err)
	_, _, err = client.RemoveApp(ctx, "blog")
	require.NoError(t, err)
	require.Len(t, keys, 3)
	for _, key := range keys {
		assert.NotEmpty(t, key)
	}
	assert.NotEqual(t, keys[0], keys[1], "each mutation gets a fresh key")
}
