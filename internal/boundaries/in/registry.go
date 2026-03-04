package in

import (
	"context"
	"io"

	"github.com/bnema/gordon/internal/domain"
)

// RegistryService defines the contract for container registry operations.
type RegistryService interface {
	// Manifest operations
	GetManifest(ctx context.Context, name, reference string) (*domain.Manifest, error)
	PutManifest(ctx context.Context, manifest *domain.Manifest) (digest string, err error)
	DeleteManifest(ctx context.Context, name, reference string) error

	// Blob operations
	GetBlob(ctx context.Context, digest string) (io.ReadCloser, error)
	GetBlobPath(ctx context.Context, digest string) (string, error)
	PutBlob(ctx context.Context, digest string, data io.Reader, size int64) error
	BlobExists(ctx context.Context, digest string) bool

	// Upload operations
	StartUpload(ctx context.Context, name string) (string, error)
	AppendBlobChunk(ctx context.Context, name, uuid string, data io.Reader) (int64, error)
	FinishUpload(ctx context.Context, uuid, digest string) error
	CancelUpload(ctx context.Context, uuid string) error

	// Tag operations
	ListTags(ctx context.Context, name string) ([]string, error)
	ListRepositories(ctx context.Context) ([]string, error)
}
