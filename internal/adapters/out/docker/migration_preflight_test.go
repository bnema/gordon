package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeProbeRuntimeEnvironmentSanitizesDockerCompatibleFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_ping":
			w.WriteHeader(http.StatusOK)
		case "/v1.41/info":
			_, _ = w.Write([]byte(`{"Name":"podman","SecurityOptions":["name=rootless"],"DockerRootDir":"/tmp"}`))
		case "/v1.41/images/json", "/v1.41/networks":
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Fatalf("unexpected Docker-compatible request %s", r.URL.Path)
		}
	}))
	defer server.Close()

	runtime := newTestRuntime(t, server)
	report, err := runtime.ProbeRuntimeEnvironment(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "podman", report.Engine)
	assert.True(t, report.Rootless)
	assert.True(t, report.APIReachable)
	assert.True(t, report.ImageAvailable)
	assert.True(t, report.ImagePullable)
	assert.True(t, report.NetworkFeasible)
	assert.NotZero(t, report.DiskAvailable)
}
