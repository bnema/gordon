package domain

import "time"

// Manifest represents an OCI image manifest.
type Manifest struct {
	Name        string
	Reference   string
	ContentType string
	Data        []byte
	Digest      string
	Size        int64
	Annotations map[string]string
	CreatedAt   time.Time
}

// Blob represents a binary large object (layer or config) in the registry.
type Blob struct {
	Digest    string
	Size      int64
	CreatedAt time.Time
}

// Repository represents a container image repository.
type Repository struct {
	Name string
	Tags []string
}

// Upload represents an in-progress blob upload.
type Upload struct {
	UUID      string
	Name      string
	Size      int64
	StartedAt time.Time
}
