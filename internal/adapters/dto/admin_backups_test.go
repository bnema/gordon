package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBackupDTOsUseAppIdentity proves the wire shape identifies a backup
// by app, service, and database or volume: a domain is never part of it.
func TestBackupDTOsUseAppIdentity(t *testing.T) {
	job := BackupJob{ID: "job-1", App: "shop", Service: "api", Database: "orders", Schedule: "daily", Status: "completed"}
	volume := VolumeBackupJob{ID: "job-2", App: "shop", Service: "api", VolumeName: "data", RuntimeVolumeName: "gordon-shop--api--vol--data", Status: "completed"}

	for name, payload := range map[string]any{"database job": job, "volume job": volume} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(payload)
			require.NoError(t, err)
			var decoded map[string]any
			require.NoError(t, json.Unmarshal(raw, &decoded))
			for _, key := range []string{"id", "app", "service"} {
				assert.Contains(t, decoded, key)
			}
			assert.NotContains(t, decoded, "domain", "a domain is never a backup identity")
		})
	}

	runRequest, err := json.Marshal(BackupRunRequest{Service: "api", Database: "orders"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"service":"api","database":"orders"}`, string(runRequest))

	volumeRequest, err := json.Marshal(VolumeBackupRunRequest{Service: "api", Volume: "data"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"service":"api","volume":"data"}`, string(volumeRequest))
}
