package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

func TestRemoteControlPlane_StartVolumeBackupAndPollRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/admin/backups/volumes/shop":
			w.WriteHeader(http.StatusAccepted)
			require.NoError(t, json.NewEncoder(w).Encode(dto.VolumeBackupRunResponse{
				Status:  "running",
				Backups: []dto.VolumeBackupJob{{ID: "v1", App: "shop", Service: "api", VolumeName: "data", Status: "running"}},
			}))
		case r.Method == http.MethodGet && r.URL.Path == "/admin/backups/volumes/shop/runs/v1":
			require.NoError(t, json.NewEncoder(w).Encode(dto.VolumeBackupJob{ID: "v1", App: "shop", Status: "completed"}))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	cp := remote.NewClient(server.URL)
	result, err := cp.StartVolumeBackup(context.Background(), "shop", "api", "data")
	require.NoError(t, err)
	assert.Equal(t, "running", result.Status)
	require.Len(t, result.Backups, 1)
	assert.Equal(t, "v1", result.Backups[0].ID)

	job, err := cp.GetVolumeBackupRun(context.Background(), "shop", "v1")
	require.NoError(t, err)
	assert.Equal(t, "completed", job.Status)
}

func TestRemoteControlPlane_VolumeBackupErrorsKeepHTTPStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)

	cp := remote.NewClient(server.URL)
	_, err := cp.VolumeBackupStatus(context.Background())

	require.Error(t, err)
	var httpErr *remote.HTTPError
	require.True(t, errors.As(err, &httpErr))
	assert.Equal(t, http.StatusBadGateway, httpErr.StatusCode)
}
