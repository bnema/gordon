package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/adapters/dto"
	in "github.com/bnema/gordon/internal/boundaries/in"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestLocalControlPlane_GetStatus(t *testing.T) {
	t.Parallel()

	configSvc := inmocks.NewMockConfigService(t)
	appSvc := inmocks.NewMockAppService(t)

	ctx := context.Background()
	configSvc.EXPECT().GetRegistryDomain().Return("registry.local")
	configSvc.EXPECT().GetRegistryPort().Return(5000)
	configSvc.EXPECT().GetServerPort().Return(80)
	configSvc.EXPECT().IsNetworkIsolationEnabled().Return(true)
	appSvc.EXPECT().List(mock.Anything).Return([]in.AppSummary{
		{App: "blog", Desired: "rev-1", Active: "rev-1", Converged: true},
	}, nil)

	cp := &localControlPlane{configSvc: configSvc, appSvc: appSvc}
	status, err := cp.GetStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, status.Apps)
	require.Equal(t, "registry.local", status.RegistryDomain)
	require.Equal(t, "active", status.ContainerStatus["blog"])
}

func TestLocalControlPlane_Backups(t *testing.T) {
	t.Parallel()

	backupSvc := inmocks.NewMockBackupService(t)
	ctx := context.Background()

	now := time.Now().UTC()
	jobs := []domain.BackupJob{{ID: "b1", Domain: "app.local", DBName: "postgres", Status: domain.BackupStatusCompleted, StartedAt: now}}
	backupSvc.EXPECT().ListBackups(mock.Anything, "app.local").Return(jobs, nil)
	backupSvc.EXPECT().Status(mock.Anything).Return(jobs, nil)
	backupSvc.EXPECT().RunBackup(mock.Anything, "app.local", "postgres").Return(&domain.BackupResult{Job: jobs[0]}, nil)
	backupSvc.EXPECT().DetectDatabases(mock.Anything, "app.local").Return([]domain.DBInfo{{Type: domain.DBTypePostgreSQL, Name: "postgres", Host: "postgres", Port: 5432}}, nil)

	cp := &localControlPlane{backupSvc: backupSvc}

	list, err := cp.ListBackups(ctx, "app.local")
	require.NoError(t, err)
	require.Len(t, list, 1)

	status, err := cp.BackupStatus(ctx)
	require.NoError(t, err)
	require.Len(t, status, 1)

	run, err := cp.RunBackup(ctx, "app.local", "postgres")
	require.NoError(t, err)
	require.NotNil(t, run.Backup)
	require.Equal(t, "b1", run.Backup.ID)

	dbs, err := cp.DetectDatabases(ctx, "app.local")
	require.NoError(t, err)
	require.Len(t, dbs, 1)
	require.Equal(t, "postgres", dbs[0].Name)
}

func TestLocalControlPlane_RunVolumeBackupsPreservesPartialJobs(t *testing.T) {
	t.Parallel()

	volumeBackupSvc := inmocks.NewMockVolumeBackupService(t)
	ctx := context.Background()
	runErr := errors.New("one volume failed")
	jobs := []domain.VolumeBackupJob{{ID: "v1", Domain: "app.local", VolumeName: "gordon-app-data", Status: domain.BackupStatusCompleted}}
	volumeBackupSvc.EXPECT().RunVolumeBackups(mock.Anything, "app.local", "").Return(jobs, runErr)

	cp := &localControlPlane{volumeBackupSvc: volumeBackupSvc}
	result, err := cp.RunVolumeBackups(ctx, "app.local", "")

	require.ErrorIs(t, err, runErr)
	require.NotNil(t, result)
	assert.Equal(t, "partial", result.Status)
	assert.Equal(t, runErr.Error(), result.Error)
	require.Len(t, result.Backups, 1)
	assert.Equal(t, "v1", result.Backups[0].ID)
}

func TestLocalControlPlane_RunVolumeBackupsMissingServiceUsesSentinel(t *testing.T) {
	t.Parallel()

	cp := &localControlPlane{}
	result, err := cp.RunVolumeBackups(context.Background(), "app.local", "")

	require.Error(t, err)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, domain.ErrVolumeBackupUnavailable)
}

func TestLocalControlPlane_ListVolumeBackupsWrapsServiceError(t *testing.T) {
	t.Parallel()

	volumeBackupSvc := inmocks.NewMockVolumeBackupService(t)
	wantErr := errors.New("store unavailable")
	volumeBackupSvc.EXPECT().ListVolumeBackups(mock.Anything, "app.local").Return(nil, wantErr)

	cp := &localControlPlane{volumeBackupSvc: volumeBackupSvc}
	jobs, err := cp.ListVolumeBackups(context.Background(), "app.local")

	require.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "list volume backups")
	assert.Nil(t, jobs)
}

func TestLocalControlPlane_PruneVolumesPreservesPlan(t *testing.T) {
	t.Parallel()

	volumeSvc := inmocks.NewMockVolumeService(t)
	report := &domain.VolumePruneReport{Plan: domain.PruneReport{
		Candidates: []domain.PruneCandidateReport{{
			Kind: domain.PruneResourceVolume, Ref: "released-volume", Verdict: domain.PruneVerdictEligible,
			Reasons: []domain.PruneReason{domain.PruneReasonEligibleReleasedVolume},
		}},
	}}
	volumeSvc.EXPECT().PruneVolumes(mock.Anything, true).Return(report, nil, nil)

	cp := &localControlPlane{volumeSvc: volumeSvc}
	result, err := cp.PruneVolumes(context.Background(), dto.VolumePruneRequest{DryRun: true})

	require.NoError(t, err)
	require.Len(t, result.Plan.Candidates, 1)
	assert.Equal(t, 1, result.Plan.Eligible)
	assert.Equal(t, "released-volume", result.Plan.Candidates[0].Ref)
	assert.False(t, result.Plan.Applied)
}

func TestLocalControlPlane_ListTags(t *testing.T) {
	t.Parallel()

	registrySvc := inmocks.NewMockRegistryService(t)
	registrySvc.EXPECT().ListTags(mock.Anything, "repo/app").Return([]string{"v1.0.0", "latest"}, nil)

	cp := &localControlPlane{registrySvc: registrySvc}
	tags, err := cp.ListTags(context.Background(), "repo/app")
	require.NoError(t, err)
	require.Equal(t, []string{"v1.0.0", "latest"}, tags)
}

func TestLocalControlPlane_GetTLSStatusWithoutService(t *testing.T) {
	t.Parallel()

	cp := &localControlPlane{}
	status, err := cp.GetTLSStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, &dto.TLSStatusResponse{
		ACMEEnabled:     false,
		SelectionReason: "public TLS service not configured",
	}, status)
}

func TestLocalControlPlane_GetContainerLogs(t *testing.T) {
	t.Parallel()

	logSvc := inmocks.NewMockLogService(t)
	logSvc.EXPECT().GetContainerLogs(mock.Anything, "app.local", 50).Return([]string{"line1", "line2"}, nil)

	cp := &localControlPlane{logSvc: logSvc}
	lines, err := cp.GetContainerLogs(context.Background(), "app.local", 50)
	require.NoError(t, err)
	require.Equal(t, []string{"line1", "line2"}, lines)
}
