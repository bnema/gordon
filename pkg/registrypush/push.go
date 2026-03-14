package registrypush

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

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

// UploadBlob uploads a single blob to the registry using chunked uploads.
// If the blob already exists, it is skipped.
func (p *Pusher) UploadBlob(ctx context.Context, baseURL, repo, digest string, size int64, content io.Reader, auth string) error {
	if err := p.checkBlobExists(ctx, baseURL, repo, digest, auth); err == nil {
		return nil
	} else if err != errBlobMissing {
		return err
	}

	uploadURL, err := p.startBlobUpload(ctx, baseURL, repo, auth)
	if err != nil {
		return err
	}

	chunks := chunkCount(size, p.chunkSize)
	if p.progress != nil {
		if _, err := fmt.Fprintf(p.progress, "Uploading blob %s (%d bytes, %d chunks)\n", digest, size, chunks); err != nil {
			return fmt.Errorf("failed to write upload progress: %w", err)
		}
	}

	uploadURL, err = p.uploadBlobChunks(ctx, uploadURL, content, auth)
	if err != nil {
		return err
	}

	return p.finalizeBlobUpload(ctx, uploadURL, digest, auth)
}

// defaultImageSource reads an image from the local Docker/Podman daemon.
func defaultImageSource(ctx context.Context, ref string) (v1.Image, error) {
	// Implementation in Task 3
	return nil, nil
}

var errBlobMissing = fmt.Errorf("blob missing")

func (p *Pusher) checkBlobExists(ctx context.Context, baseURL, repo, digest, auth string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, fmt.Sprintf("%s/v2/%s/blobs/%s", baseURL, repo, digest), nil)
	if err != nil {
		return fmt.Errorf("failed to create blob HEAD request: %w", err)
	}
	setAuthHeader(req, auth)

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("failed to check blob existence: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return errBlobMissing
	default:
		return fmt.Errorf("blob existence check returned status %d", resp.StatusCode)
	}
}

func (p *Pusher) startBlobUpload(ctx context.Context, baseURL, repo, auth string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/v2/%s/blobs/uploads/", baseURL, repo), nil)
	if err != nil {
		return "", fmt.Errorf("failed to create blob upload start request: %w", err)
	}
	setAuthHeader(req, auth)

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to start blob upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("blob upload start returned status %d", resp.StatusCode)
	}

	location, err := resolveLocation(baseURL, resp.Header.Get("Location"))
	if err != nil {
		return "", fmt.Errorf("failed to resolve blob upload location: %w", err)
	}

	return location, nil
}

func (p *Pusher) uploadBlobChunks(ctx context.Context, uploadURL string, content io.Reader, auth string) (string, error) {
	buffer := make([]byte, p.chunkSize)
	offset := int64(0)

	for {
		readBytes, err := io.ReadFull(io.LimitReader(content, p.chunkSize), buffer)
		if err == io.EOF {
			return uploadURL, nil
		}
		if err != nil && err != io.ErrUnexpectedEOF {
			return "", fmt.Errorf("failed to read blob chunk: %w", err)
		}

		nextURL, patchErr := p.uploadBlobChunk(ctx, uploadURL, buffer[:readBytes], offset, auth)
		if patchErr != nil {
			return "", patchErr
		}

		uploadURL = nextURL
		offset += int64(readBytes)

		if err == io.ErrUnexpectedEOF {
			return uploadURL, nil
		}
	}
}

func (p *Pusher) uploadBlobChunk(ctx context.Context, uploadURL string, chunk []byte, offset int64, auth string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, uploadURL, io.NopCloser(io.LimitReader(bytesReader(chunk), int64(len(chunk)))))
	if err != nil {
		return "", fmt.Errorf("failed to create blob chunk request: %w", err)
	}
	setAuthHeader(req, auth)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Range", fmt.Sprintf("%d-%d", offset, offset+int64(len(chunk))-1))
	req.ContentLength = int64(len(chunk))

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to upload blob chunk: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("blob chunk upload returned status %d", resp.StatusCode)
	}

	nextURL, err := resolveLocation(uploadURL, resp.Header.Get("Location"))
	if err != nil {
		return "", fmt.Errorf("failed to resolve next blob upload location: %w", err)
	}

	return nextURL, nil
}

func (p *Pusher) finalizeBlobUpload(ctx context.Context, uploadURL, digest, auth string) error {
	parsedURL, err := url.Parse(uploadURL)
	if err != nil {
		return fmt.Errorf("failed to parse blob upload location: %w", err)
	}

	query := parsedURL.Query()
	query.Set("digest", digest)
	parsedURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, parsedURL.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to create blob finalize request: %w", err)
	}
	setAuthHeader(req, auth)

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("failed to finalize blob upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("blob upload finalize returned status %d", resp.StatusCode)
	}

	return nil
}

func (p *Pusher) httpClient() *http.Client {
	return &http.Client{Transport: p.transport}
}

func setAuthHeader(req *http.Request, auth string) {
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
}

func resolveLocation(baseURL, location string) (string, error) {
	if location == "" {
		return "", fmt.Errorf("missing Location header")
	}

	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}

	ref, err := url.Parse(location)
	if err != nil {
		return "", err
	}

	return base.ResolveReference(ref).String(), nil
}

func chunkCount(size, chunkSize int64) int64 {
	if size == 0 || chunkSize <= 0 {
		return 0
	}
	return (size + chunkSize - 1) / chunkSize
}

func bytesReader(chunk []byte) io.Reader {
	return &sliceReader{chunk: chunk}
}

type sliceReader struct {
	chunk []byte
	off   int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.off >= len(r.chunk) {
		return 0, io.EOF
	}
	n := copy(p, r.chunk[r.off:])
	r.off += n
	return n, nil
}
