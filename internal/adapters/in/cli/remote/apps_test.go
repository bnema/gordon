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

func TestClientDeployApp_ConflictDecodesJournal(t *testing.T) {
	journal := dto.AppDeployResponse{
		Op: "op-9", App: "blog", Revision: "rev-b", Outcome: "failed",
		Services: map[string]dto.AppServiceResultDTO{
			"web": {Result: "failed", EffectiveRevision: "rev-b", Error: "nope"},
		},
		Steps: []dto.AppStepDTO{{ID: "service.web.start", State: "failed", Error: "nope"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/admin/apps/blog/deploy", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(journal)
	}))
	defer srv.Close()

	resp, key, err := NewClient(srv.URL).DeployApp(context.Background(), "blog", dto.AppDeployRequest{})
	require.Error(t, err)
	require.NotNil(t, resp, "conflict must preserve the decoded journal")
	assert.Equal(t, "op-9", resp.Op)
	assert.Equal(t, "failed", resp.Services["web"].Result)
	assert.NotEmpty(t, key)
	var conflict *AppOpConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, http.StatusConflict, conflict.StatusCode)
	assert.Equal(t, "op-9", conflict.Response.Op)
	assert.Len(t, conflict.Response.Steps, 1)
}

func TestClientStopApp_ConflictDecodesJournal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/admin/apps/blog/stop", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(dto.AppDeployResponse{
			Op: "op-7", App: "blog", Outcome: "partial",
			Services: map[string]dto.AppServiceResultDTO{
				"web": {Result: "failed", EffectiveRevision: "rev-b", Error: "busy"},
			},
		})
	}))
	defer srv.Close()

	resp, key, err := NewClient(srv.URL).StopApp(context.Background(), "blog")
	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "partial", resp.Outcome)
	assert.NotEmpty(t, key)
	var conflict *AppOpConflictError
	require.ErrorAs(t, err, &conflict)
}

func TestClientDeployApp_ConflictWithoutJournalFallsBackToHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"state-conflict","message":"busy"}`))
	}))
	defer srv.Close()

	resp, _, err := NewClient(srv.URL).DeployApp(context.Background(), "blog", dto.AppDeployRequest{})
	require.Error(t, err)
	require.Nil(t, resp)
	var httpErr *HTTPError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusConflict, httpErr.StatusCode)
}
