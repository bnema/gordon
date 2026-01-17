package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
)

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
