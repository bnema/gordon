package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntime_ListNetworksIncludesEndpointNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1.41/networks":
			// Podman-compatible network list responses omit endpoint members.
			_, _ = w.Write([]byte(`[
				{"Id":"net1","Name":"gordon-net","Driver":"bridge","Labels":{"gordon.managed":"true"}}
			]`))
		case "/v1.41/networks/net1":
			_, _ = w.Write([]byte(`{
				"Id":"net1",
				"Name":"gordon-net",
				"Driver":"bridge",
				"Internal":true,
				"Labels":{"gordon.managed":"true"},
				"Containers":{"abc123":{"Name":"gordon-app.example.com","EndpointID":"ep1"}}
			}`))
		case "/v1.41/containers/gordon-app.example.com/json":
			_, _ = w.Write([]byte(`{
				"NetworkSettings":{"Networks":{"gordon-net":{"Aliases":["gordon-target-app-example-com"]}}}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runtime := newTestRuntime(t, server)
	networks, err := runtime.ListNetworks(context.Background())

	require.NoError(t, err)
	require.Len(t, networks, 1)
	assert.True(t, networks[0].Internal)
	assert.Equal(t, []string{"gordon-app.example.com", "gordon-target-app-example-com"}, networks[0].Containers)
	assert.NotContains(t, networks[0].Containers, "abc123", "engine IDs must not cross the runtime boundary")
}

func TestRuntime_GetContainerNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1.41/containers/abc123/json", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"NetworkSettings": {
				"Networks": {
					"bridge": {"IPAddress": "172.17.0.2"},
					"gordon-app": {"IPAddress": "172.18.0.2"}
				}
			}
		}`))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+host), client.WithVersion("1.41"), client.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("failed to create docker client: %v", err)
	}

	runtime := NewRuntimeWithClient(cli)
	network, err := runtime.GetContainerNetwork(context.Background(), "abc123")

	assert.NoError(t, err)
	assert.Equal(t, "gordon-app", network)
}

func TestRuntime_GetContainerNetwork_Fallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1.41/containers/abc123/json", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"NetworkSettings": {
				"Networks": {
					"bridge": {"IPAddress": "172.17.0.2"},
					"custom": {"IPAddress": "172.18.0.2"}
				}
			}
		}`))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+host), client.WithVersion("1.41"), client.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("failed to create docker client: %v", err)
	}

	runtime := NewRuntimeWithClient(cli)
	network, err := runtime.GetContainerNetwork(context.Background(), "abc123")

	assert.NoError(t, err)
	assert.Equal(t, "bridge", network)
}

func TestRuntime_GetContainerNetwork_EmptyNetworks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1.41/containers/abc123/json", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"NetworkSettings": {
				"Networks": {}
			}
		}`))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+host), client.WithVersion("1.41"), client.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("failed to create docker client: %v", err)
	}

	runtime := NewRuntimeWithClient(cli)
	network, err := runtime.GetContainerNetwork(context.Background(), "abc123")

	assert.NoError(t, err)
	assert.Equal(t, "bridge", network)
}

func TestRuntime_GetContainerNetwork_NilNetworkSettings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1.41/containers/abc123/json", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	runtime := newTestRuntime(t, server)
	network, err := runtime.GetContainerNetwork(context.Background(), "abc123")

	assert.NoError(t, err)
	assert.Equal(t, "bridge", network)
}

func newTestRuntime(t *testing.T, server *httptest.Server) *Runtime {
	t.Helper()
	host := strings.TrimPrefix(server.URL, "http://")
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+host), client.WithVersion("1.41"), client.WithHTTPClient(server.Client()))
	require.NoError(t, err)
	return NewRuntimeWithClient(cli)
}
