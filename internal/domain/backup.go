package domain

import (
	"io"
	"time"
)

// DBType identifies the database engine.
type DBType string

const (
	DBTypePostgreSQL DBType = "postgresql"
	DBTypeUnknown    DBType = "unknown"
)

// BackupSchedule defines when backups run.
type BackupSchedule string

const (
	ScheduleHourly  BackupSchedule = "hourly"
	ScheduleDaily   BackupSchedule = "daily"
	ScheduleWeekly  BackupSchedule = "weekly"
	ScheduleMonthly BackupSchedule = "monthly"
)

// BackupType distinguishes backup methods.
type BackupType string

const (
	BackupTypeLogical       BackupType = "logical"
	BackupTypeVolumeArchive BackupType = "volume_archive"
)

// VolumeBackupCompression identifies the archive compression format for volume backups.
type VolumeBackupCompression string

const (
	VolumeBackupCompressionGzip VolumeBackupCompression = "gzip"
	VolumeBackupCompressionZstd VolumeBackupCompression = "zstd"
)

// BackupJobStatus tracks backup job lifecycle state.
type BackupJobStatus string

const (
	BackupStatusPending   BackupJobStatus = "pending"
	BackupStatusRunning   BackupJobStatus = "running"
	BackupStatusCompleted BackupJobStatus = "completed"
	BackupStatusFailed    BackupJobStatus = "failed"
)

// DatabaseTarget is one declarative database backup target: the app name
// is the identity, the service and database select the exact declaration
// inside the app's ACTIVE record. Schedule is the declared backup
// schedule of that database.
type DatabaseTarget struct {
	App         string
	Service     string
	Database    string
	Schedule    BackupSchedule
	ContainerID string
}

// BackupJob represents a scheduled or manual backup operation.
type BackupJob struct {
	ID string
	// App is the canonical backup identity: an app name, never a domain.
	App         string
	Service     string
	DBName      string
	Schedule    BackupSchedule
	Type        BackupType
	Status      BackupJobStatus
	StartedAt   time.Time
	CompletedAt time.Time
	SizeBytes   int64
	FilePath    string
	Error       string
	Metadata    map[string]string
}

// BackupResult is returned after a backup operation completes.
type BackupResult struct {
	Job      BackupJob
	Duration time.Duration
}

// DatabaseBackupJob represents a logical database backup operation.
type DatabaseBackupJob = BackupJob

// DatabaseBackupResult is returned after a database backup operation completes.
type DatabaseBackupResult = BackupResult

// RetentionPolicy defines how many backups to keep per schedule tier.
type RetentionPolicy struct {
	Hourly  int
	Daily   int
	Weekly  int
	Monthly int
}

// DatabaseBackupRetentionPolicy defines how many database backups to keep per schedule tier.
type DatabaseBackupRetentionPolicy = RetentionPolicy

// VolumeBackupRetentionPolicy defines how many volume backup archives to keep.
type VolumeBackupRetentionPolicy struct {
	Keep int
}

// BackupOverride allows per-domain backup configuration.
type BackupOverride struct {
	Schedules []BackupSchedule
	Retention *RetentionPolicy
}

// BackupConfig is the database backup configuration.
type BackupConfig struct {
	Enabled    bool
	StorageDir string
	Retention  RetentionPolicy
	Overrides  map[string]BackupOverride
}

// DatabaseBackupConfig is the database backup configuration.
type DatabaseBackupConfig = BackupConfig

// VolumeBackupConfig is the volume backup configuration.
type VolumeBackupConfig struct {
	Enabled        bool
	Interval       time.Duration
	Compression    VolumeBackupCompression
	Retention      VolumeBackupRetentionPolicy
	Timeout        time.Duration
	MaxConcurrency int
	HelperImage    string
	VolumePrefix   string
	S3Bucket       string
	S3Region       string
	S3Prefix       string
	S3Endpoint     string
	S3PathStyle    bool
	S3SSEAlgorithm string
	S3SSEKMSKeyID  string
}

// VolumeBackupJob represents a filesystem archive backup of a named volume.
type VolumeBackupJob struct {
	ID string
	// App is the canonical backup identity: an app name, never a domain.
	App               string
	Service           string
	ContainerName     string
	ContainerID       string
	VolumeName        string
	RuntimeVolumeName string
	MountPath         string
	Type              BackupType
	Status            BackupJobStatus
	StartedAt         time.Time
	CompletedAt       time.Time
	SizeBytes         int64
	ArtifactRef       string
	Error             string
	Metadata          map[string]string
}

// VolumeBackupTarget identifies one declared volume backup source:
// app, service, and the declared volume name. RuntimeVolumeName is the
// runtime volume the archive is exported from, and MountPath is the
// declared mount path inside that volume.
type VolumeBackupTarget struct {
	App               string
	Service           string
	VolumeName        string
	RuntimeVolumeName string
	MountPath         string
}

// VolumeArchiveRequest describes a volume archive export request.
type VolumeArchiveRequest struct {
	VolumeName  string
	MountPath   string
	Compression VolumeBackupCompression
	HelperImage string
}

// VolumeArchiveMetadata contains metadata observed while exporting a volume archive.
type VolumeArchiveMetadata struct {
	SizeBytes int64
	Checksum  string
}

// VolumeArchiveResult contains a readable archive stream and export metadata.
type VolumeArchiveResult struct {
	Stream   io.ReadCloser
	Metadata VolumeArchiveMetadata
}
