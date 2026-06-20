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

// DBInfo holds detected database information from an attachment container.
type DBInfo struct {
	Type        DBType
	Version     string
	Domain      string
	Name        string
	Host        string
	Port        int
	ContainerID string
	ImageName   string
	// Credentials contains sensitive values (passwords/tokens).
	// Never log or expose this map in API responses.
	Credentials map[string]string `json:"-"`
}

// ClearCredentials clears sensitive credential values from DBInfo.
func (d *DBInfo) ClearCredentials() {
	if d == nil || d.Credentials == nil {
		return
	}
	for k := range d.Credentials {
		d.Credentials[k] = ""
		delete(d.Credentials, k)
	}
	d.Credentials = nil
}

// BackupJob represents a scheduled or manual backup operation.
type BackupJob struct {
	ID          string
	Domain      string
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
	ID            string
	Domain        string
	ContainerName string
	ContainerID   string
	VolumeName    string
	MountPath     string
	Type          BackupType
	Status        BackupJobStatus
	StartedAt     time.Time
	CompletedAt   time.Time
	SizeBytes     int64
	ArtifactRef   string
	Error         string
	Metadata      map[string]string
}

// VolumeBackupTarget identifies one selected volume backup source.
type VolumeBackupTarget struct {
	Domain        string
	ContainerName string
	ContainerID   string
	VolumeName    string
	MountPath     string
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

// Backup labels for container metadata.
const (
	LabelBackupEnabled  = "gordon.backup"
	LabelBackupType     = "gordon.backup.type"
	LabelBackupVersion  = "gordon.backup.version"
	LabelBackupSchedule = "gordon.backup.schedule"
	LabelBackupSidecar  = "gordon.backup.sidecar"
)
