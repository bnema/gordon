package docker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// TestRuntime_InspectContainerReportsNotFoundSentinel proves a missing
// container is distinguishable from a transient runtime failure with
// errors.Is, so recovery never treats an API outage as a dead workload.
func TestRuntime_InspectContainerReportsNotFoundSentinel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No such container: gone"}`))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	_, err := runtime.InspectContainer(context.Background(), "gone")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrContainerNotFound)
}

// TestRuntime_InspectContainerPreservesTransientErrors proves a non-404
// failure is not reported as a missing container.
func TestRuntime_InspectContainerPreservesTransientErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"server error"}`))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	_, err := runtime.InspectContainer(context.Background(), "c-1")
	require.Error(t, err)
	assert.NotErrorIs(t, err, domain.ErrContainerNotFound)
}

// TestRuntime_InspectContainerNormalizesStatusAndExecutionStart proves
// the state a recovery pass classifies on (running/exited/restarting/
// paused) and the current execution start used to scope log readiness.
func TestRuntime_InspectContainerNormalizesStatusAndExecutionStart(t *testing.T) {
	started := "2026-09-10T12:00:00.123456789Z"
	statuses := []string{"running", "exited", "restarting", "paused", "created"}
	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"Id":"c-1",
					"Created":"2026-09-10T11:59:00Z",
					"State":{"Status":"` + status + `","ExitCode":1,"StartedAt":"` + started + `"},
					"Config":{"Image":"img:1"}
				}`))
			}))
			defer server.Close()

			runtime := newRuntimeForHTTPServer(t, server)
			container, err := runtime.InspectContainer(context.Background(), "c-1")
			require.NoError(t, err)
			assert.Equal(t, status, container.Status)
			assert.Equal(t, 1, container.ExitCode, "a non-running container keeps its exit code")
			assert.Equal(t, time.Date(2026, 9, 10, 12, 0, 0, 123456789, time.UTC), container.StartedAt.UTC())
		})
	}
}

// TestRuntime_StartContainerReportsNotFoundSentinel proves the
// native-restart race can be told apart from a real start failure.
func TestRuntime_StartContainerReportsNotFoundSentinel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No such container: gone"}`))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	err := runtime.StartContainer(context.Background(), "gone")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrContainerNotFound)
}

// TestRuntime_GetContainerLogsSinceScopesTheRequest proves readiness
// reads one exact container and passes the execution boundary to the
// runtime, so markers from a previous execution cannot match.
func TestRuntime_GetContainerLogsSinceScopesTheRequest(t *testing.T) {
	// Sub-second boundary: a marker from the same second as a previous
	// execution must not pass the filter.
	since := time.Date(2026, 9, 10, 12, 0, 0, 123456789, time.UTC)
	var gotPath, gotSince, gotTail string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSince = r.URL.Query().Get("since")
		gotTail = r.URL.Query().Get("tail")
		w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
		_, _ = w.Write([]byte("2026-09-10T12:00:01Z ready\n"))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	stream, err := runtime.GetContainerLogsSince(context.Background(), "c-exact", since, false)
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()
	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Contains(t, string(body), "ready")

	assert.True(t, strings.HasSuffix(gotPath, "/containers/c-exact/logs"), "logs are read from the exact container ID")
	// The Docker API takes the boundary as unix seconds with nanoseconds.
	wireSince, parseErr := strconv.ParseFloat(gotSince, 64)
	require.NoError(t, parseErr)
	wantSince := float64(since.UnixNano()) / 1e9
	assert.InDelta(t, wantSince, wireSince, 1e-6, "the sub-second execution boundary reaches the runtime unchanged")
	assert.Equal(t, "10000", gotTail)
}

// TestRuntime_GetContainerLogsSinceReportsNotFoundSentinel proves a
// removed candidate fails readiness with an actionable error instead of
// hanging until the deadline.
func TestRuntime_GetContainerLogsSinceReportsNotFoundSentinel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No such container: gone"}`))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	_, err := runtime.GetContainerLogsSince(context.Background(), "gone", time.Now(), false)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrContainerNotFound)
	assert.False(t, errors.Is(err, context.DeadlineExceeded))
}
