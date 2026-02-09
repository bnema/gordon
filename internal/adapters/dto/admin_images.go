package dto

import "time"

// Image represents a runtime image in admin API responses.
type Image struct {
	Repository string    `json:"repository"`
	Tag        string    `json:"tag"`
	Size       int64     `json:"size"`
	Created    time.Time `json:"created"`
	ID         string    `json:"id"`
	Dangling   bool      `json:"dangling"`
}

// ImagesResponse is returned by image list endpoints.
type ImagesResponse struct {
	Images []Image `json:"images"`
}

// ImagePruneRequest triggers image pruning.
type ImagePruneRequest struct {
	KeepLast *int `json:"keep_last,omitempty"`
}

// RuntimePruneResult represents runtime prune results.
type RuntimePruneResult struct {
	DeletedCount   int   `json:"deleted_count"`
	SpaceReclaimed int64 `json:"space_reclaimed"`
}

// RegistryPruneResult represents registry prune results.
type RegistryPruneResult struct {
	TagsRemoved    int   `json:"tags_removed"`
	BlobsRemoved   int   `json:"blobs_removed"`
	SpaceReclaimed int64 `json:"space_reclaimed"`
}

// ImagePruneResponse is returned by image prune endpoints.
type ImagePruneResponse struct {
	Runtime  RuntimePruneResult  `json:"runtime"`
	Registry RegistryPruneResult `json:"registry"`
}
