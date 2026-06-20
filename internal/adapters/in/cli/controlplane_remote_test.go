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

var _ ControlPlane = (*remoteControlPlane)(nil)

func TestRemoteControlPlane_ImplementsInterface(t *testing.T) {
	client := remote.NewClient("https://gordon.example.com")
	if NewRemoteControlPlane(client) == nil {
		t.Fatal("expected non-nil remote control-plane")
	}
}

func TestRemoteControlPlane_RunVolumeBackupsPreservesPartialResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPartialContent)
		require.NoError(t, json.NewEncoder(w).Encode(dto.VolumeBackupRunResponse{
			Status:  "partial",
			Backups: []dto.VolumeBackupJob{{ID: "v1", Domain: "app.example.com", VolumeName: "gordon-app-data"}},
			Error:   "one volume failed",
		}))
	}))
	t.Cleanup(server.Close)

	cp := NewRemoteControlPlane(remote.NewClient(server.URL))
	result, err := cp.RunVolumeBackups(context.Background(), "app.example.com", "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "run volume backups")
	require.NotNil(t, result)
	assert.Equal(t, "partial", result.Status)
	require.Len(t, result.Backups, 1)
	assert.Equal(t, "v1", result.Backups[0].ID)
}

func TestRemoteControlPlane_VolumeBackupErrorsAreWrapped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)

	cp := NewRemoteControlPlane(remote.NewClient(server.URL))
	_, err := cp.VolumeBackupStatus(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "get volume backup status")
	var httpErr *remote.HTTPError
	require.True(t, errors.As(err, &httpErr))
	assert.Equal(t, http.StatusBadGateway, httpErr.StatusCode)
}
