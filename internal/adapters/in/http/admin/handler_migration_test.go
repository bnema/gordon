package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrationPlanAuthorizationAndSanitizedFailure(t *testing.T) {
	failure := map[string]any{"ready": false, "checks": []map[string]string{{"name": "rootless_podman", "status": "fail", "remediation": "install and start rootless Podman"}}}
	handler := newTestHandler(t, func(deps *HandlerDeps) {
		deps.MigrationPlan = func(context.Context) (any, error) { return failure, nil }
		deps.MigrationPlanFailed = func(any) bool { return true }
	})

	t.Run("forbidden", func(t *testing.T) {
		server := newScopedTestServer(t, handler, "admin:routes:read")
		resp, err := http.Get(server.URL + "/admin/migration/plan")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("unprocessable report is sanitized", func(t *testing.T) {
		server := newScopedTestServer(t, handler, "admin:config:read")
		resp, err := http.Get(server.URL + "/admin/migration/plan")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
		var report map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&report))
		assert.Equal(t, false, report["ready"])
		assert.NotContains(t, report["checks"], "socket")
	})
}
