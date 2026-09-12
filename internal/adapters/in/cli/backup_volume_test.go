package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	climocks "github.com/bnema/gordon/internal/adapters/in/cli/mocks"
)

// backupRunPlane builds the control plane double the runner functions
// take directly: no resolver indirection is needed.
func backupRunPlane(t *testing.T) *climocks.MockControlPlane {
	t.Helper()
	return climocks.NewMockControlPlane(t)
}

func TestVolumeBackupStatus_JSONFlag(t *testing.T) {
	plane := backupRunPlane(t)

	plane.EXPECT().VolumeBackupStatus(mock.Anything).Return([]dto.VolumeBackupJob{{App: "shop", Service: "api", VolumeName: "data", Status: "running"}}, nil)

	var out bytes.Buffer
	require.NoError(t, runVolumeBackupStatus(context.Background(), plane, &out, true))
	require.JSONEq(t, `[{"id":"","app":"shop","service":"api","volume":"data","type":"","status":"running","size_bytes":0}]`, out.String())
}

func TestVolumeBackupRun_PrintsPartialJobsBeforeReturningError(t *testing.T) {
	plane := backupRunPlane(t)

	runErr := errors.New("one backup failed")
	plane.EXPECT().RunVolumeBackups(mock.Anything, "shop", "api", "data").Return(&dto.VolumeBackupRunResponse{
		Status:  "partial",
		Backups: []dto.VolumeBackupJob{{App: "shop", Service: "api", VolumeName: "data", Status: "completed"}},
		Error:   runErr.Error(),
	}, runErr)

	var out bytes.Buffer
	err := runVolumeBackupRun(context.Background(), plane, "shop", "api", "data", &out, true)
	require.Error(t, err)
	require.ErrorIs(t, err, runErr)
	require.JSONEq(t, `[{"id":"","app":"shop","service":"api","volume":"data","type":"","status":"completed","size_bytes":0}]`, out.String())
}
