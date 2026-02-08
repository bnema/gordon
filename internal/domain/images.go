package domain

import "time"

// ImagePruneConfig defines scheduled image cleanup behavior.
type ImagePruneConfig struct {
	Enabled  bool
	Schedule BackupSchedule
	// KeepLast is the number of most-recent tags to retain per repository.
	KeepLast int
}

// RuntimePruneResult reports runtime image cleanup results.
type RuntimePruneResult struct {
	DeletedCount int
	// SpaceReclaimed is reported in bytes.
	SpaceReclaimed int64
}

// RegistryPruneResult reports registry cleanup results.
type RegistryPruneResult struct {
	TagsRemoved  int
	BlobsRemoved int
	// SpaceReclaimed is reported in bytes.
	SpaceReclaimed int64
}

// ImagePruneReport aggregates runtime and registry cleanup results.
type ImagePruneReport struct {
	Runtime  RuntimePruneResult
	Registry RegistryPruneResult
}

// ImageInfo describes a runtime image.
type ImageInfo struct {
	Repository string
	Tag        string
	// Size is reported in bytes.
	Size    int64
	Created time.Time
	// ID is the runtime image identifier (for example, image ID/digest).
	ID       string
	Dangling bool
}
