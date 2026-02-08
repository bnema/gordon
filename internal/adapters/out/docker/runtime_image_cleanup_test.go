package docker

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntime_PruneImages_DanglingOnlyFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1.41/images/prune", r.URL.Path)

		parsedFilters, err := filters.FromJSON(r.URL.Query().Get("filters"))
		require.NoError(t, err)
		assert.Equal(t, []string{"true"}, parsedFilters.Get("dangling"))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ImagesDeleted":[{"Deleted":"sha256:abc"},{"Untagged":"example:old"}],"SpaceReclaimed":1234}`))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	report, err := runtime.PruneImages(context.Background(), true)

	require.NoError(t, err)
	assert.Equal(t, []string{"sha256:abc", "example:old"}, report.DeletedIDs)
	assert.EqualValues(t, 1234, report.SpaceReclaimed)
}

func TestRuntime_PruneImages_FullUnusedFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1.41/images/prune", r.URL.Path)

		parsedFilters, err := filters.FromJSON(r.URL.Query().Get("filters"))
		require.NoError(t, err)
		assert.Equal(t, []string{"false"}, parsedFilters.Get("dangling"))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ImagesDeleted":[{"Deleted":"sha256:def"}],"SpaceReclaimed":5678}`))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	report, err := runtime.PruneImages(context.Background(), false)

	require.NoError(t, err)
	assert.Equal(t, []string{"sha256:def"}, report.DeletedIDs)
	assert.EqualValues(t, 5678, report.SpaceReclaimed)
}

func TestRuntime_PruneImages_ReturnsErrorOnNon2xxResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1.41/images/prune", r.URL.Path)

		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"internal error"}`))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	_, err := runtime.PruneImages(context.Background(), true)

	require.Error(t, err)
}

func TestRuntime_PruneImages_ReturnsErrorOnInvalidPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1.41/images/prune", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	_, err := runtime.PruneImages(context.Background(), true)

	require.Error(t, err)
}

func TestRuntime_PruneImages_ReturnsErrorOnSpaceReclaimedOverflow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1.41/images/prune", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ImagesDeleted":[],"SpaceReclaimed":9223372036854775808}`))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	_, err := runtime.PruneImages(context.Background(), true)

	require.Error(t, err)
	assert.ErrorContains(t, err, "space reclaimed")
	assert.ErrorContains(t, err, "int64")
}

func TestRuntime_ListImagesDetailed_MapsImageSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1.41/images/json", r.URL.Path)
		assert.Equal(t, "1", r.URL.Query().Get("all"))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"Id":"sha256:img1","RepoTags":["alpine:latest"],"Size":321,"Created":1700000000}]`))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	images, err := runtime.ListImagesDetailed(context.Background())

	require.NoError(t, err)
	require.Len(t, images, 1)
	assert.Equal(t, "sha256:img1", images[0].ID)
	assert.Equal(t, []string{"alpine:latest"}, images[0].RepoTags)
	assert.EqualValues(t, 321, images[0].Size)
	assert.Equal(t, time.Unix(1700000000, 0), images[0].Created)
}

func TestRuntime_ListImagesDetailed_ReturnsErrorOnNon2xxResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1.41/images/json", r.URL.Path)

		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"message":"upstream error"}`))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	_, err := runtime.ListImagesDetailed(context.Background())

	require.Error(t, err)
}

func TestRuntime_ListImagesDetailed_ReturnsErrorOnInvalidPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1.41/images/json", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"not":`))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	_, err := runtime.ListImagesDetailed(context.Background())

	require.Error(t, err)
}

func TestRuntime_PruneImages_UsesMaxInt64Boundary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1.41/images/prune", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ImagesDeleted":[],"SpaceReclaimed":9223372036854775807}`))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	report, err := runtime.PruneImages(context.Background(), true)

	require.NoError(t, err)
	assert.EqualValues(t, math.MaxInt64, report.SpaceReclaimed)
}

func newRuntimeForHTTPServer(t *testing.T, server *httptest.Server) *Runtime {
	t.Helper()

	host := strings.TrimPrefix(server.URL, "http://")
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+host), client.WithVersion("1.41"), client.WithHTTPClient(server.Client()))
	require.NoError(t, err)

	return NewRuntimeWithClient(cli)
}
