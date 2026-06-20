package in

import (
	"context"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// DatabaseBackupService defines database backup orchestration use cases.
type DatabaseBackupService interface {
	ListBackups(ctx context.Context, domainName string) ([]domain.DatabaseBackupJob, error)
	RunBackup(ctx context.Context, domainName, dbName string) (*domain.DatabaseBackupResult, error)
	Restore(ctx context.Context, domainName, backupID string) error
	RestorePITR(ctx context.Context, domainName string, targetTime time.Time) error
	Status(ctx context.Context) ([]domain.DatabaseBackupJob, error)
	DetectDatabases(ctx context.Context, domainName string) ([]domain.DBInfo, error)
}

// BackupService is kept as a compatibility alias for the existing database backup feature.
type BackupService = DatabaseBackupService

// VolumeBackupService defines volume archive backup orchestration use cases.
type VolumeBackupService interface {
	ListVolumeBackups(ctx context.Context, domainName string) ([]domain.VolumeBackupJob, error)
	RunVolumeBackups(ctx context.Context, domainName string, volumeName string) ([]domain.VolumeBackupJob, error)
	VolumeBackupStatus(ctx context.Context) ([]domain.VolumeBackupJob, error)
}
