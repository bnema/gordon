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
	FilePath    string     `json:"file_path"`
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
