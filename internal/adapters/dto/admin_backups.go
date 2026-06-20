package dto

import "time"

// BackupJob represents backup metadata in admin API responses.
type BackupJob struct {
	ID          string     `json:"id"`
	Domain      string     `json:"domain"`
	DBName      string     `json:"db_name"`
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

// BackupRunRequest triggers a backup run.
type BackupRunRequest struct {
	DB string `json:"db,omitempty"`
}

// BackupRunResponse is returned after triggering a backup.
type BackupRunResponse struct {
	Status string     `json:"status"`
	Backup *BackupJob `json:"backup,omitempty"`
}

// DatabaseInfo represents a detected database attachment.
type DatabaseInfo struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	ContainerID string `json:"container_id"`
	ImageName   string `json:"image_name"`
}

// BackupDetectResponse is returned by detect endpoints.
type BackupDetectResponse struct {
	Databases []DatabaseInfo `json:"databases"`
}

// VolumeBackupJob represents volume backup metadata in admin API responses.
type VolumeBackupJob struct {
	ID            string     `json:"id"`
	Domain        string     `json:"domain"`
	ContainerName string     `json:"container_name,omitempty"`
	ContainerID   string     `json:"container_id,omitempty"`
	VolumeName    string     `json:"volume_name"`
	MountPath     string     `json:"mount_path,omitempty"`
	Compression   string     `json:"compression,omitempty"`
	Type          string     `json:"type"`
	Status        string     `json:"status"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	SizeBytes     int64      `json:"size_bytes"`
	ArtifactRef   string     `json:"artifact_ref,omitempty"`
	Error         string     `json:"error,omitempty"`
}

// VolumeBackupsResponse is returned by volume backup listing endpoints.
type VolumeBackupsResponse struct {
	Backups []VolumeBackupJob `json:"backups"`
}

// VolumeBackupRunRequest triggers volume backups.
type VolumeBackupRunRequest struct {
	Volume string `json:"volume,omitempty"`
}

// VolumeBackupRunResponse is returned after triggering volume backups.
type VolumeBackupRunResponse struct {
	Status  string            `json:"status"`
	Backups []VolumeBackupJob `json:"backups,omitempty"`
	Error   string            `json:"error,omitempty"`
}
