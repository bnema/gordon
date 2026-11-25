package in

import (
	"context"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// DatabaseBackupService defines declarative app database backup use
// cases. App names are the backup identity: a domain is never accepted.
type DatabaseBackupService interface {
	// ListBackups lists stored backups of one app, or of every app when
	// app is empty.
	ListBackups(ctx context.Context, app string) ([]domain.BackupJob, error)
	// StartBackup starts one declared database backup detached from the
	// caller and returns the running job. service and database are
	// explicit selectors; an omitted selector succeeds only when exactly
	// one compatible target exists. A target that is already being backed
	// up returns its running job.
	StartBackup(ctx context.Context, app, service, database string) (domain.BackupJob, error)
	// BackupRun returns one tracked run by ID, or ErrBackupRunNotFound.
	BackupRun(ctx context.Context, id string) (domain.BackupJob, error)
	Restore(ctx context.Context, app, backupID string) error
	RestorePITR(ctx context.Context, app string, targetTime time.Time) error
	// Status reports stored backups plus declared targets without a
	// completed backup.
	Status(ctx context.Context) ([]domain.BackupJob, error)
}

// BackupService is the database backup service.
type BackupService = DatabaseBackupService

// VolumeBackupService defines declarative app volume archive backup use
// cases.
type VolumeBackupService interface {
	ListVolumeBackups(ctx context.Context, app string) ([]domain.VolumeBackupJob, error)
	// StartVolumeBackup starts one declared volume backup detached from
	// the caller and returns the running job. service and volume are
	// explicit selectors; an omitted selector succeeds only when exactly
	// one compatible target exists. A target that is already being backed
	// up returns its running job.
	StartVolumeBackup(ctx context.Context, app, service, volume string) (domain.VolumeBackupJob, error)
	// VolumeBackupRun returns one tracked run by ID, or
	// ErrBackupRunNotFound.
	VolumeBackupRun(ctx context.Context, id string) (domain.VolumeBackupJob, error)
	VolumeBackupStatus(ctx context.Context) ([]domain.VolumeBackupJob, error)
}
