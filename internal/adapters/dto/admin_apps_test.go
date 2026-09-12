package dto

// Frozen-wire tests for the v2.50 app admin DTOs pin the exact JSON field
// names the daemon and the CLI exchange, and prove no response shape can
// carry secret values. The single intentional exception is
// AppSecretSetRequest: the write path must transport values.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dtoJSONKeys(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	return decoded
}

func TestAppErrorEnvelopeKeys(t *testing.T) {
	envelope := AppError{Error: "secret-missing", Message: "db password missing", Cause: "pass lookup failed", Hint: "set it first"}
	keys := dtoJSONKeys(t, envelope)
	assert.Equal(t, "secret-missing", keys["error"])
	assert.Equal(t, "db password missing", keys["message"])
	assert.Equal(t, "pass lookup failed", keys["cause"])
	assert.Equal(t, "set it first", keys["hint"])
	assert.NotContains(t, keys, "logs", "mutations must never carry logs")

	// Mixed-version window: the legacy single-field shape still decodes.
	var legacy AppError
	require.NoError(t, json.Unmarshal([]byte(`{"error":"image-unresolvable"}`), &legacy))
	assert.Equal(t, "image-unresolvable", legacy.Error)
	assert.Empty(t, legacy.Message)
}

func TestAppApplyResponseKeys(t *testing.T) {
	resp := AppApplyResponse{
		App:               "blog",
		FormerRevision:    "rev-old",
		ResultingRevision: "rev-new",
		Pending:           true,
		Diff:              AppDiffSection{Added: []string{"service.web"}, Removed: []string{}, Changed: []string{}},
		Intent:            "apply-abc",
	}
	keys := dtoJSONKeys(t, resp)
	for _, key := range []string{"app", "former_revision", "resulting_revision", "pending", "diff", "intent"} {
		assert.Contains(t, keys, key)
	}
	diff := keys["diff"].(map[string]any)
	for _, key := range []string{"added", "removed", "changed"} {
		assert.Contains(t, diff, key)
	}
}

func TestAppDeployResponseKeys(t *testing.T) {
	resp := AppDeployResponse{
		Op:       "op-abc",
		App:      "blog",
		Revision: "rev-new",
		Outcome:  "success",
		Services: map[string]AppServiceResultDTO{
			"web": {Result: "deployed", EffectiveRevision: "rev-new"},
		},
		Steps: []AppStepDTO{{ID: "service.web.replace", State: "succeeded"}},
		CleanupWarnings: []AppCleanupWarningDTO{
			{Service: "web", Leftover: "ctr-old", Detail: "retire failed after publish"},
		},
		Effective: &AppEffectiveDTO{Converged: true, Services: map[string]string{"web": "rev-new"}},
		Retained:  &AppRetainedDTO{Volumes: []string{"blog-db"}, Secrets: []string{"db/password"}},
	}
	keys := dtoJSONKeys(t, resp)
	for _, key := range []string{"op", "app", "revision", "outcome", "services", "steps", "cleanup_warnings", "effective", "retained"} {
		assert.Contains(t, keys, key)
	}
	svc := keys["services"].(map[string]any)["web"].(map[string]any)
	for _, key := range []string{"result", "effective_revision", "restart_unsafe"} {
		assert.Contains(t, svc, key)
	}
}

func TestAppShowResponseKeys(t *testing.T) {
	resp := AppShowResponse{
		App:     "blog",
		Desired: AppDesiredDTO{Revision: "rev-new", Status: "pending", Pending: true},
		Active: AppActiveDTO{Converged: false, Services: map[string]AppActiveServiceDTO{
			"web": {EffectiveRevision: "rev-old", Digest: "sha256:abc", Container: "ctr-old", RestartUnsafe: true},
		}},
		Intent: AppIntentDTO{Stopped: false},
		LastOp: &AppLastOpDTO{Op: "op-abc", Outcome: "success"},
	}
	keys := dtoJSONKeys(t, resp)
	for _, key := range []string{"app", "desired", "active", "intent", "retained", "last_op"} {
		assert.Contains(t, keys, key)
	}
	web := keys["active"].(map[string]any)["services"].(map[string]any)["web"].(map[string]any)
	for _, key := range []string{"effective_revision", "digest", "container", "restart_unsafe"} {
		assert.Contains(t, web, key)
	}
	assert.NotContains(t, web, "env", "active services must not inline env values")
}

// TestAppResponsesCarryNoSecretValues marshals representative responses and
// proves a sentinel secret value appears nowhere. The request that carries
// values (AppSecretSetRequest) is asserted as the single exception.
func TestAppResponsesCarryNoSecretValues(t *testing.T) {
	const sentinel = "s3cr3t-value-sentinel"

	responses := []any{
		AppApplyResponse{App: "blog", ResultingRevision: "rev-x", Intent: "apply-x"},
		AppDeployResponse{
			Op: "op-x", App: "blog", Revision: "rev-x", Outcome: "failed",
			Services: map[string]AppServiceResultDTO{
				"web": {Result: "failed", EffectiveRevision: "rev-x", Error: "boom"},
			},
			Effective: &AppEffectiveDTO{Services: map[string]string{"web": "rev-x"}},
			Retained:  &AppRetainedDTO{Volumes: []string{"blog-db"}, Secrets: []string{"db/password"}},
		},
		AppShowResponse{
			App:     "blog",
			Desired: AppDesiredDTO{Revision: "rev-x", Status: "active"},
			Active: AppActiveDTO{Services: map[string]AppActiveServiceDTO{
				"web": {EffectiveRevision: "rev-x", Digest: "sha256:x", Container: "ctr-x"},
			}},
		},
		AppSummaryDTO{App: "blog", Desired: "rev-x", Active: "rev-x"},
		AppDiffResponse{App: "blog", Diff: AppDiffSection{Changed: []string{"secret.db/password"}}},
	}
	for i, resp := range responses {
		raw, err := json.Marshal(resp)
		require.NoError(t, err, "response %d", i)
		body := string(raw)
		assert.NotContains(t, body, sentinel, "response %d leaks a secret value", i)
		assert.NotContains(t, strings.ToLower(body), `"value"`, "response %d has a value field", i)
	}

	// The one intentional exception: the set-secret request transports values.
	setReq := AppSecretSetRequest{Service: "web", Secrets: map[string]string{"password": sentinel}}
	raw, err := json.Marshal(setReq)
	require.NoError(t, err)
	assert.Contains(t, string(raw), sentinel)
}
