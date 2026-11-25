package backup

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// awaitRun waits for the detached run to finish, then returns it. It must
// run inside a synctest bubble, so the run's goroutine belongs to it.
func awaitRun[J any](t *testing.T, get func(context.Context, string) (J, error), id string, status func(J) domain.BackupJobStatus) J {
	t.Helper()
	synctest.Wait()
	job, err := get(context.Background(), id)
	require.NoError(t, err)
	require.NotEqual(t, domain.BackupStatusRunning, status(job), "the detached run must have finished")
	return job
}

// runBackupToEnd starts one database backup and waits for its outcome
// inside the caller's synctest bubble.
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

// runVolumeBackupToEnd starts one volume backup and waits for its outcome
// inside the caller's synctest bubble.
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
