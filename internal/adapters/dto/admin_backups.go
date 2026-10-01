package dto

import "time"

// BackupJob represents backup metadata in admin API responses. App is the
// canonical backup identity: a domain is never used.
type BackupJob struct {
	ID          string     `json:"id"`
	App         string     `json:"app"`
	Service     string     `json:"service,omitempty"`
	Database    string     `json:"database,omitempty"`
	Schedule    string     `json:"schedule,omitempty"`
	Type        string     `json:"type"`
	Status      string     `json:"status"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	SizeBytes   int64      `json:"size_bytes"`
	Error       string     `json:"error,omitempty"`
}

// BackupsResponse is returned by backup listing endpoints.
type BackupsResponse struct {
	Backups []BackupJob `json:"backups"`
}

// BackupRunRequest triggers one declared database backup. Service and
// Database are explicit selectors; an omitted selector only succeeds when
// exactly one compatible target exists.
type BackupRunRequest struct {
	Service  string `json:"service"`
	Database string `json:"database,omitempty"`
}

// BackupRunResponse is returned after triggering a backup.
type BackupRunResponse struct {
	Status string     `json:"status"`
	Backup *BackupJob `json:"backup,omitempty"`
}

// VolumeBackupJob represents volume backup metadata in admin API
// responses. App, service, and the declared volume name are the identity.
type VolumeBackupJob struct {
	ID                string     `json:"id"`
	App               string     `json:"app"`
	Service           string     `json:"service,omitempty"`
	VolumeName        string     `json:"volume"`
	RuntimeVolumeName string     `json:"runtime_volume,omitempty"`
	MountPath         string     `json:"mount_path,omitempty"`
	Compression       string     `json:"compression,omitempty"`
	Type              string     `json:"type"`
	Status            string     `json:"status"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
	SizeBytes         int64      `json:"size_bytes"`
	ArtifactRef       string     `json:"artifact_ref,omitempty"`
	Error             string     `json:"error,omitempty"`
}

// VolumeBackupsResponse is returned by volume backup listing endpoints.
type VolumeBackupsResponse struct {
	Backups []VolumeBackupJob `json:"backups"`
}

// VolumeBackupRunRequest triggers one declared volume backup.
type VolumeBackupRunRequest struct {
	Service string `json:"service"`
	Volume  string `json:"volume,omitempty"`
}

// VolumeBackupRunResponse is returned after triggering a volume backup.
type VolumeBackupRunResponse struct {
	Status  string            `json:"status"`
	Backups []VolumeBackupJob `json:"backups,omitempty"`
	Error   string            `json:"error,omitempty"`
}
