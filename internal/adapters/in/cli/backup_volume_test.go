package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

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

func TestVolumeBackupRun_ReturnsAtOnceWithoutWait(t *testing.T) {
	plane := backupRunPlane(t)
	plane.EXPECT().StartVolumeBackup(mock.Anything, "shop", "api", "data").Return(&dto.VolumeBackupRunResponse{
		Status:  "running",
		Backups: []dto.VolumeBackupJob{{ID: "v1", App: "shop", Service: "api", VolumeName: "data", Status: "running"}},
	}, nil)

	var out bytes.Buffer
	require.NoError(t, runVolumeBackupRun(context.Background(), plane, "shop", "api", "data", false, &out, true))
	require.JSONEq(t, `{"status":"running","backups":[{"id":"v1","app":"shop","service":"api","volume":"data","type":"","status":"running","size_bytes":0}]}`, out.String())
}

func TestVolumeBackupRun_WaitPollsUntilCompleted(t *testing.T) {
	setFastBackupPoll(t)
	plane := backupRunPlane(t)
	plane.EXPECT().StartVolumeBackup(mock.Anything, "shop", "api", "data").Return(&dto.VolumeBackupRunResponse{
		Status:  "running",
		Backups: []dto.VolumeBackupJob{{ID: "v1", App: "shop", Status: "running"}},
	}, nil)
	plane.EXPECT().GetVolumeBackupRun(mock.Anything, "shop", "v1").Return(&dto.VolumeBackupJob{ID: "v1", App: "shop", Status: "running"}, nil).Once()
	plane.EXPECT().GetVolumeBackupRun(mock.Anything, "shop", "v1").Return(&dto.VolumeBackupJob{ID: "v1", App: "shop", Status: "completed", SizeBytes: 42}, nil).Once()

	var out bytes.Buffer
	require.NoError(t, runVolumeBackupRun(context.Background(), plane, "shop", "api", "data", true, &out, true))
	require.JSONEq(t, `{"status":"completed","backups":[{"id":"v1","app":"shop","volume":"","type":"","status":"completed","size_bytes":42}]}`, out.String())
}

func TestVolumeBackupRun_WaitFailsWhenTheBackupFails(t *testing.T) {
	setFastBackupPoll(t)
	plane := backupRunPlane(t)
	plane.EXPECT().StartVolumeBackup(mock.Anything, "shop", "", "").Return(&dto.VolumeBackupRunResponse{
		Status:  "running",
		Backups: []dto.VolumeBackupJob{{ID: "v1", App: "shop", Status: "running"}},
	}, nil)
	plane.EXPECT().GetVolumeBackupRun(mock.Anything, "shop", "v1").Return(&dto.VolumeBackupJob{ID: "v1", App: "shop", Status: "failed", Error: "upload failed"}, nil).Once()

	var out bytes.Buffer
	err := runVolumeBackupRun(context.Background(), plane, "shop", "", "", true, &out, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "upload failed")
	require.Contains(t, out.String(), "failed")
}

func TestVolumeBackupRun_StoppedWaitSaysTheBackupContinues(t *testing.T) {
	setFastBackupPoll(t)
	plane := backupRunPlane(t)
	ctx, cancel := context.WithCancel(context.Background())
	plane.EXPECT().StartVolumeBackup(mock.Anything, "shop", "", "").Return(&dto.VolumeBackupRunResponse{
		Status:  "running",
		Backups: []dto.VolumeBackupJob{{ID: "v1", App: "shop", Status: "running"}},
	}, nil)
	plane.EXPECT().GetVolumeBackupRun(mock.Anything, "shop", "v1").RunAndReturn(func(context.Context, string, string) (*dto.VolumeBackupJob, error) {
		cancel()
		return nil, context.Canceled
	}).Once()

	var out bytes.Buffer
	err := runVolumeBackupRun(ctx, plane, "shop", "", "", true, &out, false)
	require.ErrorIs(t, err, context.Canceled)
	require.Contains(t, err.Error(), "keeps running on the server")
}

func TestBackupRun_WaitPollsUntilCompleted(t *testing.T) {
	setFastBackupPoll(t)
	plane := backupRunPlane(t)
	plane.EXPECT().StartBackup(mock.Anything, "shop", "api", "orders").Return(&dto.BackupRunResponse{
		Status: "running",
		Backup: &dto.BackupJob{ID: "b1", App: "shop", Status: "running"},
	}, nil)
	plane.EXPECT().GetBackupRun(mock.Anything, "shop", "b1").Return(&dto.BackupJob{ID: "b1", App: "shop", Status: "completed"}, nil).Once()

	var out bytes.Buffer
	require.NoError(t, runBackupRun(context.Background(), plane, "shop", "api", "orders", true, &out, true))
	require.JSONEq(t, `{"status":"completed","backup":{"id":"b1","app":"shop","type":"","status":"completed","size_bytes":0}}`, out.String())
}

func TestBackupRun_StartErrorIsReported(t *testing.T) {
	plane := backupRunPlane(t)
	plane.EXPECT().StartBackup(mock.Anything, "shop", "", "").Return(nil, errors.New("boom"))

	var out bytes.Buffer
	err := runBackupRun(context.Background(), plane, "shop", "", "", false, &out, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to start backup")
}

// setFastBackupPoll makes --wait poll without delay for one test.
func setFastBackupPoll(t *testing.T) {
	t.Helper()
	prev := backupPollInterval
	backupPollInterval = time.Millisecond
	t.Cleanup(func() { backupPollInterval = prev })
}
