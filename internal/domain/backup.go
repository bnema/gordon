package domain

import "time"

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
	BackupTypeLogical BackupType = "logical"
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
	Credentials map[string]string
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

// RetentionPolicy defines how many backups to keep per schedule tier.
type RetentionPolicy struct {
	Hourly  int
	Daily   int
	Weekly  int
	Monthly int
}

// BackupOverride allows per-domain backup configuration.
type BackupOverride struct {
	Schedules []BackupSchedule
	Retention *RetentionPolicy
}

// BackupConfig is the top-level backup configuration.
type BackupConfig struct {
	Enabled    bool
	StorageDir string
	Retention  RetentionPolicy
	Overrides  map[string]BackupOverride
}

// Backup labels for container metadata.
const (
	LabelBackupEnabled  = "gordon.backup"
	LabelBackupType     = "gordon.backup.type"
	LabelBackupVersion  = "gordon.backup.version"
	LabelBackupSchedule = "gordon.backup.schedule"
	LabelBackupSidecar  = "gordon.backup.sidecar"
)
