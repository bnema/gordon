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

func TestRuntime_CreateContainerPublishesExplicitUDPPort(t *testing.T) {
	createBody := createTestContainer(t, &domain.ContainerConfig{
		Image: "rust:latest",
		Name:  "gordon-rust",
		PortPublishes: []domain.ContainerPortPublish{
			{
				HostIP:        "127.0.0.1",
				HostPort:      38015,
				ContainerPort: 28015,
				Protocol:      domain.NetworkProtocolUDP,
			},
		},
	})

	exposedPorts, ok := createBody["ExposedPorts"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, exposedPorts, "28015/udp")

	hostConfig, ok := createBody["HostConfig"].(map[string]any)
	require.True(t, ok)
	portBindings, ok := hostConfig["PortBindings"].(map[string]any)
	require.True(t, ok)
	bindings, ok := portBindings["28015/udp"].([]any)
	require.True(t, ok)
	require.Len(t, bindings, 1)
	binding, ok := bindings[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "127.0.0.1", binding["HostIp"])
	assert.Equal(t, "38015", binding["HostPort"])
}

func TestRuntime_CreateContainerKeepsLegacyPortsRandomLoopbackTCP(t *testing.T) {
	createBody := createTestContainer(t, &domain.ContainerConfig{
		Image: "nginx:latest",
		Name:  "gordon-app.example.com",
		Ports: []int{8080},
	})

	exposedPorts, ok := createBody["ExposedPorts"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, exposedPorts, "8080/tcp")

	hostConfig, ok := createBody["HostConfig"].(map[string]any)
	require.True(t, ok)
	portBindings, ok := hostConfig["PortBindings"].(map[string]any)
	require.True(t, ok)
	bindings, ok := portBindings["8080/tcp"].([]any)
	require.True(t, ok)
	require.Len(t, bindings, 1)
	binding, ok := bindings[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "127.0.0.1", binding["HostIp"])
	assert.Equal(t, "0", binding["HostPort"])
}

func TestRuntime_CreateContainerAppliesRestartPolicy(t *testing.T) {
	createBody := createTestContainer(t, &domain.ContainerConfig{
		Image:         "nginx:latest",
		Name:          "gordon-app.example.com",
		RestartPolicy: domain.RestartPolicyAlways,
	})

	hostConfig, ok := createBody["HostConfig"].(map[string]any)
	require.True(t, ok)
	restartPolicy, ok := hostConfig["RestartPolicy"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, domain.RestartPolicyAlways, restartPolicy["Name"])
}

func createTestContainer(t *testing.T, config *domain.ContainerConfig) map[string]any {
	t.Helper()

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
	created, err := runtime.CreateContainer(context.Background(), config)
	require.NoError(t, err)
	assert.Equal(t, "abc123", created.ID)
	require.NotNil(t, createBody)

	return createBody
}
