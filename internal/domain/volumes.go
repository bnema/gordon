package domain

import "time"

// VolumeInfo describes a named volume and its usage status.
type VolumeInfo struct {
	Name       string
	Driver     string
	MountPoint string
	Size       int64
	CreatedAt  time.Time
	// InUse is true when at least one container mounts this volume.
	InUse bool
	// Containers lists the names of containers using this volume.
	Containers []string
	// Labels are key-value metadata attached to the volume.
	Labels map[string]string
}

// VolumePruneReport summarizes a volume prune operation.
type VolumePruneReport struct {
	VolumesRemoved int
	SpaceReclaimed int64
}
