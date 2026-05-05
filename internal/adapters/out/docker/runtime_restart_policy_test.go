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

func TestRuntime_EnsureContainerRestartPolicy_SkipsWhenAlreadySet(t *testing.T) {
	var updateCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.41/containers/abc123/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"Id": "abc123",
				"HostConfig": {
					"RestartPolicy": {
						"Name": "always"
					}
				}
			}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1.41/containers/abc123/update":
			updateCalled = true
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	runtime := newRuntimeForRestartPolicyHTTPServer(t, server)
	err := runtime.EnsureContainerRestartPolicy(context.Background(), "abc123", domain.RestartPolicyAlways)
	require.NoError(t, err)
	assert.False(t, updateCalled, "update should not be called when restart policy is already set")
}

func TestRuntime_EnsureContainerRestartPolicy_UpdatesAndPreservesResources(t *testing.T) {
	var updateBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.41/containers/abc123/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"Id": "abc123",
				"HostConfig": {
					"RestartPolicy": {
						"Name": ""
					},
					"Memory": 268435456,
					"NanoCpus": 500000000,
					"PidsLimit": 128,
					"Ulimits": [
						{
							"Name": "nofile",
							"Soft": 65536,
							"Hard": 65536
						}
					]
				}
			}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1.41/containers/abc123/update":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&updateBody))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Warnings":null}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	runtime := newRuntimeForRestartPolicyHTTPServer(t, server)
	err := runtime.EnsureContainerRestartPolicy(context.Background(), "abc123", domain.RestartPolicyAlways)
	require.NoError(t, err)
	require.NotNil(t, updateBody)
	assert.Equal(t, domain.RestartPolicyAlways, updateBody["RestartPolicy"].(map[string]any)["Name"])
	assert.Equal(t, float64(268435456), updateBody["Memory"])
	assert.Equal(t, float64(500000000), updateBody["NanoCpus"])
	assert.Equal(t, float64(128), updateBody["PidsLimit"])
	ulimits, ok := updateBody["Ulimits"].([]any)
	require.True(t, ok, "Ulimits should be an array")
	require.Greater(t, len(ulimits), 0)
	firstUlimit, ok := ulimits[0].(map[string]any)
	require.True(t, ok, "first Ulimit should be a map")
	assert.Equal(t, "nofile", firstUlimit["Name"])
	assert.Equal(t, float64(65536), firstUlimit["Soft"])
	assert.Equal(t, float64(65536), firstUlimit["Hard"])
}

func TestRuntime_EnsureContainerRestartPolicy_ReturnsErrorWhenHostConfigMissing(t *testing.T) {
	var updateCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.41/containers/abc123/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"Id": "abc123"
			}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1.41/containers/abc123/update":
			updateCalled = true
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	runtime := newRuntimeForRestartPolicyHTTPServer(t, server)
	err := runtime.EnsureContainerRestartPolicy(context.Background(), "abc123", domain.RestartPolicyAlways)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing host config")
	assert.False(t, updateCalled, "update should not be called when host config is missing")
}

func newRuntimeForRestartPolicyHTTPServer(t *testing.T, server *httptest.Server) *Runtime {
	host := strings.TrimPrefix(server.URL, "http://")
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+host), client.WithVersion("1.41"), client.WithHTTPClient(server.Client()))
	require.NoError(t, err)
	return NewRuntimeWithClient(cli)
}
