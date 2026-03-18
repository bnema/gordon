package dto

import "time"

// Volume represents a volume in API responses.
type Volume struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	MountPoint string            `json:"mount_point"`
	Size       int64             `json:"size"`
	CreatedAt  time.Time         `json:"created_at"`
	InUse      bool              `json:"in_use"`
	Containers []string          `json:"containers,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// VolumePruneRequest defines parameters for volume pruning.
type VolumePruneRequest struct {
	DryRun bool `json:"dry_run"`
}

// VolumePruneResponse contains the results of a volume prune operation.
type VolumePruneResponse struct {
	VolumesRemoved int      `json:"volumes_removed"`
	SpaceReclaimed int64    `json:"space_reclaimed"`
	Volumes        []Volume `json:"volumes,omitempty"`
}
