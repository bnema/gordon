package out

import (
	"io"
)

// BlobStorage defines the contract for blob storage operations.
type BlobStorage interface {
	// GetBlob retrieves a blob by digest.
	GetBlob(digest string) (io.ReadCloser, error)

	// GetBlobPath returns the filesystem path to a blob.
	GetBlobPath(digest string) (string, error)

	// PutBlob stores a blob with the given digest.
	PutBlob(digest string, data io.Reader, size int64) error

	// DeleteBlob removes a blob by digest.
	DeleteBlob(digest string) error

	// BlobExists checks if a blob exists.
	BlobExists(digest string) bool

	// StartBlobUpload starts a new blob upload and returns the upload UUID.
	StartBlobUpload(name string) (string, error)

	// AppendBlobChunk appends data to an in-progress upload.
	AppendBlobChunk(name, uuid string, chunk []byte) (int64, error)

	// GetBlobUpload returns a writer for the upload.
	GetBlobUpload(uuid string) (io.WriteCloser, error)

	// FinishBlobUpload completes an upload and moves it to blob storage.
	FinishBlobUpload(uuid, digest string) error

	// CancelBlobUpload cancels an in-progress upload.
	CancelBlobUpload(uuid string) error
}

// ManifestStorage defines the contract for manifest storage operations.
type ManifestStorage interface {
	// GetManifest retrieves a manifest by name and reference.
	// Returns the manifest data and content type.
	GetManifest(name, reference string) ([]byte, string, error)

	// PutManifest stores a manifest.
	PutManifest(name, reference, contentType string, data []byte) error

	// DeleteManifest removes a manifest.
	DeleteManifest(name, reference string) error

	// ListTags returns all tags for a repository.
	ListTags(name string) ([]string, error)

	// ListRepositories returns all repository names.
	ListRepositories() ([]string, error)
}
