package docker

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntime_InspectImageEnv_RedactsValuesInDebugLog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, "/images/")
		assert.Contains(t, r.URL.Path, "/json")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"Config": {
				"Env": ["API_KEY=super-secret-key", "DATABASE_URL=postgres://user:pass@example/db", "PORT=8080", "MALFORMED_SECRET_VALUE", "=empty-key-secret"]
			}
		}`))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+host), client.WithVersion("1.41"), client.WithHTTPClient(server.Client()))
	require.NoError(t, err)
	runtime := NewRuntimeWithClient(cli)

	var logs bytes.Buffer
	log := zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &logs})
	ctx := zerowrap.WithCtx(context.Background(), log)

	envVars, err := runtime.InspectImageEnv(ctx, "example/app:latest")
	require.NoError(t, err)
	assert.Equal(t, []string{"API_KEY=super-secret-key", "DATABASE_URL=postgres://user:pass@example/db", "PORT=8080", "MALFORMED_SECRET_VALUE", "=empty-key-secret"}, envVars)

	logOutput := logs.String()
	assert.Contains(t, logOutput, "env_keys")
	assert.Contains(t, logOutput, "API_KEY")
	assert.Contains(t, logOutput, "DATABASE_URL")
	assert.Contains(t, logOutput, "PORT")
	assert.Contains(t, logOutput, "[malformed]")
	assert.NotContains(t, logOutput, "super-secret-key")
	assert.NotContains(t, logOutput, "postgres://user:pass@example/db")
	assert.NotContains(t, logOutput, "MALFORMED_SECRET_VALUE")
	assert.NotContains(t, logOutput, "empty-key-secret")
	assert.NotContains(t, logOutput, "API_KEY=super-secret-key")
}

func TestWaitForVolumeArchiveContainerIgnoresNilErrorBeforeStatus(t *testing.T) {
	statusCh := make(chan container.WaitResponse, 1)
	errCh := make(chan error, 1)
	errCh <- nil
	statusCh <- container.WaitResponse{StatusCode: 7}

	statusCode, err := waitForVolumeArchiveContainer(statusCh, errCh)

	require.NoError(t, err)
	assert.Equal(t, int64(7), statusCode)
}

func TestWaitForVolumeArchiveContainerHandlesClosedErrorChannelBeforeStatus(t *testing.T) {
	statusCh := make(chan container.WaitResponse, 1)
	errCh := make(chan error)
	close(errCh)
	statusCh <- container.WaitResponse{StatusCode: 3}

	statusCode, err := waitForVolumeArchiveContainer(statusCh, errCh)

	require.NoError(t, err)
	assert.Equal(t, int64(3), statusCode)
}

func TestWaitForVolumeArchiveContainerReturnsErrorChannelError(t *testing.T) {
	statusCh := make(chan container.WaitResponse)
	errCh := make(chan error, 1)
	wantErr := errors.New("wait failed")
	errCh <- wantErr

	statusCode, err := waitForVolumeArchiveContainer(statusCh, errCh)

	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, int64(0), statusCode)
}

func TestWaitForVolumeArchiveContainerErrorsWhenStatusChannelCloses(t *testing.T) {
	statusCh := make(chan container.WaitResponse)
	errCh := make(chan error)
	close(statusCh)
	close(errCh)

	statusCode, err := waitForVolumeArchiveContainer(statusCh, errCh)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "wait channel closed")
	assert.Equal(t, int64(0), statusCode)
}
