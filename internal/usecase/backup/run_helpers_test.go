package backup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// awaitRun polls get until the run with id is no longer running.
func awaitRun[J any](t *testing.T, get func(context.Context, string) (J, error), id string, status func(J) domain.BackupJobStatus) J {
	t.Helper()
	var job J
	require.Eventually(t, func() bool {
		var err error
		job, err = get(context.Background(), id)
		require.NoError(t, err)
		return status(job) != domain.BackupStatusRunning
	}, 5*time.Second, time.Millisecond)
	return job
}

// runBackupToEnd starts one database backup and waits for its outcome.
func runBackupToEnd(t *testing.T, svc *Service, app, service, database string) (domain.BackupJob, error) {
	t.Helper()
	job, err := svc.StartBackup(context.Background(), app, service, database)
	if err != nil {
		return job, err
	}
	job = awaitRun(t, svc.BackupRun, job.ID, func(j domain.BackupJob) domain.BackupJobStatus { return j.Status })
	if job.Status == domain.BackupStatusFailed {
		return job, errors.New(job.Error)
	}
	return job, nil
}

// runVolumeBackupToEnd starts one volume backup and waits for its outcome.
func runVolumeBackupToEnd(t *testing.T, svc *VolumeService, app, service, volume string) (domain.VolumeBackupJob, error) {
	t.Helper()
	job, err := svc.StartVolumeBackup(context.Background(), app, service, volume)
	if err != nil {
		return job, err
	}
	job = awaitRun(t, svc.VolumeBackupRun, job.ID, func(j domain.VolumeBackupJob) domain.BackupJobStatus { return j.Status })
	if job.Status == domain.BackupStatusFailed {
		return job, errors.New(job.Error)
	}
	return job, nil
}
