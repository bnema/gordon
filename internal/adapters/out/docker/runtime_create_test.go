package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestRuntime_CreateContainerAppliesRestartPolicy(t *testing.T) {
	var createBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1.41/containers/create":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&createBody))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"abc123","Warnings":null}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.41/containers/abc123/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"Id":"abc123",
				"Name":"/gordon-app.example.com",
				"Image":"sha256:image",
				"Created":"2026-05-05T00:00:00Z",
				"Config":{"Image":"nginx:latest","Labels":{}},
				"State":{"Status":"created","ExitCode":0},
				"NetworkSettings":{"Ports":{}}
			}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+host), client.WithVersion("1.41"), client.WithHTTPClient(server.Client()))
	require.NoError(t, err)

	runtime := NewRuntimeWithClient(cli)
	created, err := runtime.CreateContainer(context.Background(), &domain.ContainerConfig{
		Image:         "nginx:latest",
		Name:          "gordon-app.example.com",
		RestartPolicy: domain.RestartPolicyAlways,
	})
	require.NoError(t, err)
	assert.Equal(t, "abc123", created.ID)

	require.NotNil(t, createBody)
	hostConfig, ok := createBody["HostConfig"].(map[string]any)
	require.True(t, ok)
	restartPolicy, ok := hostConfig["RestartPolicy"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, domain.RestartPolicyAlways, restartPolicy["Name"])
}
