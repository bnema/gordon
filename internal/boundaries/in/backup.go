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
	// RunBackup runs one declared database backup. service and database
	// are explicit selectors; an omitted selector succeeds only when
	// exactly one compatible target exists.
	RunBackup(ctx context.Context, app, service, database string) (*domain.BackupResult, error)
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
	// RunVolumeBackups runs the declared volume backups of one app.
	// service and volume are explicit selectors; an omitted selector
	// succeeds only when exactly one compatible target exists.
	RunVolumeBackups(ctx context.Context, app, service, volume string) ([]domain.VolumeBackupJob, error)
	VolumeBackupStatus(ctx context.Context) ([]domain.VolumeBackupJob, error)
}
