package registrypush

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/docker/docker/client"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
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
	insecureTLS bool
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

// WithInsecureTLS disables TLS certificate verification for registry requests.
// This is ignored when a custom transport is provided via WithTransport.
func WithInsecureTLS(insecure bool) Option {
	return func(p *Pusher) { p.insecureTLS = insecure }
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
		if p.insecureTLS {
			p.transport = insecureTransport()
		} else {
			p.transport = http.DefaultTransport
		}
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
	parsedRef, err := name.ParseReference(ref)
	if err != nil {
		return fmt.Errorf("failed to parse image ref %s: %w", ref, err)
	}

	tag, ok := parsedRef.(name.Tag)
	if !ok {
		return fmt.Errorf("image ref %s must include a tag", ref)
	}

	authHeader, err := resolveAuthHeader(parsedRef)
	if err != nil {
		return err
	}

	img, err := p.imageSource(ctx, ref)
	if err != nil {
		return err
	}

	baseURL := registryBaseURL(parsedRef.Context().RegistryStr())
	repo := parsedRef.Context().RepositoryStr()

	if err := p.uploadImageLayers(ctx, img, baseURL, repo, authHeader); err != nil {
		return err
	}

	if err := p.uploadImageConfig(ctx, img, baseURL, repo, authHeader); err != nil {
		return err
	}

	return p.uploadManifest(ctx, baseURL, repo, tag.Identifier(), img, authHeader)
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
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}
	defer cli.Close()

	tag, err := name.NewTag(ref)
	if err != nil {
		return nil, fmt.Errorf("failed to parse image ref %s: %w", ref, err)
	}

	rc, err := cli.ImageSave(ctx, []string{ref})
	if err != nil {
		return nil, fmt.Errorf("failed to export image %s: %w", ref, err)
	}

	tmpFile, err := os.CreateTemp("", "gordon-push-*.tar")
	if err != nil {
		if closeErr := rc.Close(); closeErr != nil {
			return nil, fmt.Errorf("failed to create temp file for image export: %w (also failed to close image export stream: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("failed to create temp file for image export: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := io.Copy(tmpFile, rc); err != nil {
		if closeErr := rc.Close(); closeErr != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
			return nil, fmt.Errorf("failed to write image tar to temp file: %w (also failed to close image export stream: %v)", err, closeErr)
		}
		if closeErr := tmpFile.Close(); closeErr != nil {
			_ = os.Remove(tmpPath)
			return nil, fmt.Errorf("failed to write image tar to temp file: %w (also failed to close temp file: %v)", err, closeErr)
		}
		if removeErr := os.Remove(tmpPath); removeErr != nil {
			return nil, fmt.Errorf("failed to write image tar to temp file: %w (also failed to remove temp file %s: %v)", err, tmpPath, removeErr)
		}
		return nil, fmt.Errorf("failed to write image tar to temp file: %w", err)
	}
	if err := rc.Close(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("failed to close image export stream: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("failed to close temp file %s: %w", tmpPath, err)
	}

	img, err := tarball.ImageFromPath(tmpPath, &tag)
	if err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("failed to parse image tar for %s: %w", ref, err)
	}
	return img, nil
}

func (p *Pusher) uploadImageLayers(ctx context.Context, img v1.Image, baseURL, repo, auth string) error {
	layers, err := img.Layers()
	if err != nil {
		return fmt.Errorf("failed to read image layers: %w", err)
	}

	for _, layer := range layers {
		if err := p.uploadLayer(ctx, baseURL, repo, layer, auth); err != nil {
			return err
		}
	}

	return nil
}

func (p *Pusher) uploadLayer(ctx context.Context, baseURL, repo string, layer v1.Layer, auth string) error {
	digest, err := layer.Digest()
	if err != nil {
		return fmt.Errorf("failed to get layer digest: %w", err)
	}

	size, err := layer.Size()
	if err != nil {
		return fmt.Errorf("failed to get layer size for %s: %w", digest, err)
	}

	content, err := layer.Compressed()
	if err != nil {
		return fmt.Errorf("failed to read layer %s: %w", digest, err)
	}
	defer content.Close()

	if err := p.UploadBlob(ctx, baseURL, repo, digest.String(), size, content, auth); err != nil {
		return fmt.Errorf("failed to upload layer %s: %w", digest, err)
	}

	return nil
}

func (p *Pusher) uploadImageConfig(ctx context.Context, img v1.Image, baseURL, repo, auth string) error {
	config, err := img.RawConfigFile()
	if err != nil {
		return fmt.Errorf("failed to read image config: %w", err)
	}

	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(config))
	if err := p.UploadBlob(ctx, baseURL, repo, digest, int64(len(config)), bytesReader(config), auth); err != nil {
		return fmt.Errorf("failed to upload image config %s: %w", digest, err)
	}

	return nil
}

func (p *Pusher) uploadManifest(ctx context.Context, baseURL, repo, tag string, img v1.Image, auth string) error {
	manifest, err := img.RawManifest()
	if err != nil {
		return fmt.Errorf("failed to read image manifest: %w", err)
	}

	mediaType, err := img.MediaType()
	if err != nil {
		return fmt.Errorf("failed to read image media type: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("%s/v2/%s/manifests/%s", baseURL, repo, tag), bytesReader(manifest))
	if err != nil {
		return fmt.Errorf("failed to create manifest upload request: %w", err)
	}
	setAuthHeader(req, auth)
	req.Header.Set("Content-Type", string(mediaType))
	req.ContentLength = int64(len(manifest))

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("failed to upload manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("manifest upload returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

func resolveAuthHeader(ref name.Reference) (string, error) {
	authenticator, err := authn.DefaultKeychain.Resolve(ref.Context().Registry)
	if err != nil {
		return "", fmt.Errorf("failed to resolve registry credentials for %s: %w", ref.Context().RegistryStr(), err)
	}

	authConfig, err := authenticator.Authorization()
	if err != nil {
		return "", fmt.Errorf("failed to authorize for registry %s: %w", ref.Context().RegistryStr(), err)
	}

	if authConfig == nil {
		return "", nil
	}

	if authConfig.RegistryToken != "" {
		return "Bearer " + authConfig.RegistryToken, nil
	}

	if authConfig.Auth != "" {
		return "Basic " + authConfig.Auth, nil
	}

	if authConfig.Username != "" || authConfig.Password != "" {
		encoded := base64.StdEncoding.EncodeToString([]byte(authConfig.Username + ":" + authConfig.Password))
		return "Basic " + encoded, nil
	}

	return "", nil
}

func registryBaseURL(host string) string {
	if strings.HasPrefix(host, "localhost:") || strings.HasPrefix(host, "127.0.0.1:") || host == "localhost" || host == "127.0.0.1" {
		return "http://" + host
	}
	return "https://" + host
}

var errBlobMissing = errors.New("blob missing")

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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("blob existence check returned status %d: %s", resp.StatusCode, string(body))
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("blob upload start returned status %d: %s", resp.StatusCode, string(body))
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, uploadURL, bytesReader(chunk))
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("blob chunk upload returned status %d: %s", resp.StatusCode, string(body))
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("blob upload finalize returned status %d: %s", resp.StatusCode, string(body))
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

func insecureTransport() http.RoundTripper {
	var transport *http.Transport
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = t.Clone()
	} else {
		transport = &http.Transport{}
	}
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	//nolint:gosec // Explicit CLI opt-in via --insecure for self-signed/private cert deployments.
	transport.TLSClientConfig.InsecureSkipVerify = true
	return transport
}

func bytesReader(chunk []byte) io.Reader {
	return bytes.NewReader(chunk)
}
