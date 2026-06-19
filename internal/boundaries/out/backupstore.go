package out

import (
	"context"
	"io"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// DatabaseBackupStorage defines persistence for database backup artifacts and metadata.
type DatabaseBackupStorage interface {
	Store(ctx context.Context, domainName, dbName string, schedule domain.BackupSchedule, timestamp time.Time, data io.Reader) (string, error)
	Get(ctx context.Context, path string) (io.ReadCloser, error)
	List(ctx context.Context, domainName string, schedule *domain.BackupSchedule) ([]domain.DatabaseBackupJob, error)
	Delete(ctx context.Context, path string) error
	ApplyRetention(ctx context.Context, domainName string, policy domain.DatabaseBackupRetentionPolicy) (int, error)
}

// BackupStorage is kept as a compatibility alias for the existing database backup feature.
type BackupStorage = DatabaseBackupStorage

// VolumeArchiveExporter exports named container volumes as archive streams.
type VolumeArchiveExporter interface {
	ExportVolumeArchive(ctx context.Context, req domain.VolumeArchiveRequest) (*domain.VolumeArchiveResult, error)
}

// VolumeBackupStorage defines persistence for volume archive backup artifacts.
type VolumeBackupStorage interface {
	StoreVolumeArchive(ctx context.Context, job domain.VolumeBackupJob, data io.Reader) (string, error)
	GetVolumeArchive(ctx context.Context, artifactRef string) (io.ReadCloser, error)
	ListVolumeArchives(ctx context.Context, domainName string) ([]domain.VolumeBackupJob, error)
	DeleteVolumeArchive(ctx context.Context, artifactRef string) error
	ApplyVolumeRetention(ctx context.Context, domainName string, policy domain.VolumeBackupRetentionPolicy) (int, error)
}
