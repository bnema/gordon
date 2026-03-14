package registrypush

import (
	"context"
	"io"
	"net/http"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// DefaultChunkSize is 50MB - safely under Cloudflare's 100MB per-request limit
// and Gordon's default max_blob_chunk_size of 95MB.
const DefaultChunkSize = 50 * 1024 * 1024

// ImageSource provides a v1.Image for a given ref.
// The default reads from the local Docker/Podman daemon.
// Override with WithImageSource for testing.
type ImageSource func(ctx context.Context, ref string) (v1.Image, error)

// Pusher uploads container images to a registry using chunked blob uploads.
type Pusher struct {
	chunkSize   int64
	transport   http.RoundTripper
	imageSource ImageSource
	progress    io.Writer
}

// Option configures a Pusher.
type Option func(*Pusher)

// WithChunkSize sets the maximum size of each blob upload chunk.
func WithChunkSize(size int64) Option {
	return func(p *Pusher) { p.chunkSize = size }
}

// WithTransport sets a custom HTTP transport (for testing).
func WithTransport(t http.RoundTripper) Option {
	return func(p *Pusher) { p.transport = t }
}

// WithImageSource overrides the default daemon-based image reader.
func WithImageSource(src ImageSource) Option {
	return func(p *Pusher) { p.imageSource = src }
}

// WithProgress sets a writer for progress messages.
func WithProgress(w io.Writer) Option {
	return func(p *Pusher) { p.progress = w }
}

// New creates a Pusher with the given options.
func New(opts ...Option) *Pusher {
	p := &Pusher{chunkSize: DefaultChunkSize}
	for _, o := range opts {
		o(p)
	}
	if p.transport == nil {
		p.transport = http.DefaultTransport
	}
	if p.imageSource == nil {
		p.imageSource = defaultImageSource
	}
	return p
}

// Push reads a local image (by ref) from the Docker/Podman daemon and uploads
// it to the registry encoded in the ref, using chunked blob uploads.
//
// The ref must be fully qualified: registry.example.com/name:tag
func (p *Pusher) Push(ctx context.Context, ref string) error {
	// Implementation in subsequent tasks
	return nil
}

// defaultImageSource reads an image from the local Docker/Podman daemon.
func defaultImageSource(ctx context.Context, ref string) (v1.Image, error) {
	// Implementation in Task 3
	return nil, nil
}
