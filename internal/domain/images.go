package domain

import "time"

const DefaultImagePruneKeepLast = 3

// ImagePruneOptions controls which prune subsystems are activated.
type ImagePruneOptions struct {
	// KeepLast is the number of most-recent non-latest tags to retain per repository.
	KeepLast int
	// PruneDangling enables runtime dangling image cleanup.
	PruneDangling bool
	// PruneRegistry enables registry tag retention and blob garbage collection.
	PruneRegistry bool
}

// DefaultImagePruneOptions returns options that prune both scopes with the default retention.
func DefaultImagePruneOptions() ImagePruneOptions {
	return ImagePruneOptions{
		KeepLast:      DefaultImagePruneKeepLast,
		PruneDangling: true,
		PruneRegistry: true,
	}
}

// ImagePruneConfig defines scheduled image cleanup behavior.
type ImagePruneConfig struct {
	Enabled  bool
	Schedule BackupSchedule
	// KeepLast is the number of most-recent non-latest tags to retain per repository.
	// The latest tag is retained separately when present.
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

// ImageInfo describes an image/tag visible from runtime and registry data.
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
