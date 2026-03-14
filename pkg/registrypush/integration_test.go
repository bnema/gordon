package registrypush_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bnema/zerowrap"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	registryhttp "github.com/bnema/gordon/internal/adapters/in/http/registry"
	"github.com/bnema/gordon/internal/adapters/out/filesystem"
	"github.com/bnema/gordon/internal/domain"
	registrysvc "github.com/bnema/gordon/internal/usecase/registry"
	"github.com/bnema/gordon/pkg/registrypush"
)

func TestIntegration_PushSingleLayerImage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	img, err := random.Image(512, 1)
	require.NoError(t, err)

	harness := newRegistryHarness(t, registryhttp.DefaultMaxBlobChunkSize)
	defer harness.Close()

	err = harness.Push(ctx, img, registrypush.WithChunkSize(256))
	require.NoError(t, err)

	harness.RequireManifestAndBlobs(ctx, t, img)
}

func TestIntegration_PushMultiLayerChunked(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	img, err := random.Image(4096, 3)
	require.NoError(t, err)

	harness := newRegistryHarness(t, 1025)
	defer harness.Close()

	err = harness.Push(ctx, img, registrypush.WithChunkSize(1024))
	require.NoError(t, err)

	harness.RequireManifestAndBlobs(ctx, t, img)
}

func TestIntegration_PushSkipsExistingBlobs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	img, err := random.Image(1024, 2)
	require.NoError(t, err)

	harness := newRegistryHarness(t, registryhttp.DefaultMaxBlobChunkSize)
	defer harness.Close()

	err = harness.Push(ctx, img, registrypush.WithChunkSize(256))
	require.NoError(t, err)

	err = harness.Push(ctx, img, registrypush.WithChunkSize(256))
	require.NoError(t, err)
}

func TestIntegration_ServerRejectsOversizedChunk(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	img, err := random.Image(4096, 1)
	require.NoError(t, err)

	harness := newRegistryHarness(t, 512)
	defer harness.Close()

	err = harness.Push(ctx, img, registrypush.WithChunkSize(4096))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "413")
}

func TestIntegration_ManifestRoundtrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	img, err := random.Image(1024, 2)
	require.NoError(t, err)

	harness := newRegistryHarness(t, registryhttp.DefaultMaxBlobChunkSize)
	defer harness.Close()

	err = harness.Push(ctx, img, registrypush.WithChunkSize(512))
	require.NoError(t, err)

	expectedManifest, err := img.RawManifest()
	require.NoError(t, err)

	expectedMediaType, err := img.MediaType()
	require.NoError(t, err)

	resp := harness.GetManifest(t)
	body, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, expectedManifest, body)
	assert.Equal(t, string(expectedMediaType), resp.Header.Get("Content-Type"))
}

type registryHarness struct {
	server *httptest.Server
	ref    string
}

func newRegistryHarness(t *testing.T, maxBlobChunkSize int64) *registryHarness {
	t.Helper()

	log := zerowrap.Default()
	rootDir := t.TempDir()

	blobStore, err := filesystem.NewBlobStorage(rootDir, log)
	require.NoError(t, err)

	manifestStore, err := filesystem.NewManifestStorage(rootDir, log)
	require.NoError(t, err)

	svc := registrysvc.NewService(blobStore, manifestStore, noopEventPublisher{})
	handler := registryhttp.NewHandler(svc, log, maxBlobChunkSize)
	server := httptest.NewServer(handler)

	host := strings.TrimPrefix(server.URL, "http://")

	return &registryHarness{
		server: server,
		ref:    host + "/testapp:v1.0.0",
	}
}

func (h *registryHarness) Close() {
	h.server.Close()
}

func (h *registryHarness) Push(ctx context.Context, img v1.Image, opts ...registrypush.Option) error {
	allOpts := append([]registrypush.Option{
		registrypush.WithTransport(h.server.Client().Transport),
		registrypush.WithImageSource(func(context.Context, string) (v1.Image, error) {
			return img, nil
		}),
	}, opts...)

	pusher := registrypush.New(allOpts...)
	return pusher.Push(ctx, h.ref)
}

func (h *registryHarness) GetManifest(t *testing.T) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v2/testapp/manifests/v1.0.0", h.server.URL), nil)
	require.NoError(t, err)

	resp, err := h.server.Client().Do(req)
	require.NoError(t, err)

	return resp
}

func (h *registryHarness) RequireManifestAndBlobs(ctx context.Context, t *testing.T, img v1.Image) {
	t.Helper()

	manifestResp := h.GetManifest(t)
	require.Equal(t, http.StatusOK, manifestResp.StatusCode)
	require.NoError(t, manifestResp.Body.Close())

	layers, err := img.Layers()
	require.NoError(t, err)

	for _, layer := range layers {
		digest, digestErr := layer.Digest()
		require.NoError(t, digestErr)

		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v2/testapp/blobs/%s", h.server.URL, digest.String()), nil)
		require.NoError(t, reqErr)

		resp, respErr := h.server.Client().Do(req)
		require.NoError(t, respErr)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		require.NoError(t, resp.Body.Close())
	}
}

type noopEventPublisher struct{}

func (noopEventPublisher) Publish(domain.EventType, any) error {
	return nil
}
