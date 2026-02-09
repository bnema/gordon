package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntime_GetContainerHealthStatus_ConfiguredHealthcheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1.41/containers/test-container/json", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"Id":"test-container",
			"State":{"Status":"running","Health":{"Status":"healthy","FailingStreak":0,"Log":[]}},
			"Config":{"Healthcheck":{"Test":["CMD-SHELL","curl -f http://localhost/health || exit 1"]}}
		}`))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	status, hasHealthcheck, err := runtime.GetContainerHealthStatus(context.Background(), "test-container")

	require.NoError(t, err)
	assert.True(t, hasHealthcheck)
	assert.Equal(t, "healthy", status)
}

func TestRuntime_GetContainerHealthStatus_NoConfiguredHealthcheckWithStateHealthObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1.41/containers/test-container/json", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"Id":"test-container",
			"State":{"Status":"running","Health":{"Status":"","FailingStreak":0,"Log":null}},
			"Config":{"Healthcheck":null}
		}`))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	status, hasHealthcheck, err := runtime.GetContainerHealthStatus(context.Background(), "test-container")

	require.NoError(t, err)
	assert.False(t, hasHealthcheck)
	assert.Equal(t, "", status)
}

func TestRuntime_GetContainerHealthStatus_DisabledHealthcheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1.41/containers/test-container/json", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"Id":"test-container",
			"State":{"Status":"running","Health":{"Status":"starting","FailingStreak":0,"Log":[]}},
			"Config":{"Healthcheck":{"Test":["NONE"]}}
		}`))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	status, hasHealthcheck, err := runtime.GetContainerHealthStatus(context.Background(), "test-container")

	require.NoError(t, err)
	assert.False(t, hasHealthcheck)
	assert.Equal(t, "", status)
}
