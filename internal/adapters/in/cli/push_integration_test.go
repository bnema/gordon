package cli

import (
	"context"
	"fmt"
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

func TestIntegration_TagAndPush_NativeRegistry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	harness := newCLITestRegistry(t, 95*1024*1024)
	defer harness.Close()

	img, err := random.Image(512, 1)
	require.NoError(t, err)

	originalPush := dockerPushFn
	originalTag := dockerTagFn
	originalImageExists := dockerImageExistsFn
	t.Cleanup(func() {
		dockerPushFn = originalPush
		dockerTagFn = originalTag
		dockerImageExistsFn = originalImageExists
	})

	dockerPushFn = func(ctx context.Context, ref string) error {
		pusher := registrypush.New(
			registrypush.WithChunkSize(256),
			registrypush.WithTransport(harness.server.Client().Transport),
			registrypush.WithImageSource(func(context.Context, string) (v1.Image, error) {
				return img, nil
			}),
		)
		return pusher.Push(ctx, ref)
	}
	dockerTagFn = func(context.Context, string, string) error {
		return nil
	}
	dockerImageExistsFn = func(context.Context, string) bool {
		return true
	}

	registry := strings.TrimPrefix(harness.server.URL, "http://")
	versionRef := registry + "/testapp:v1.0.0"
	latestRef := registry + "/testapp:latest"

	err = tagAndPush(ctx, registry, "testapp", "v1.0.0", versionRef, latestRef)
	require.NoError(t, err)

	harness.RequireManifest(t, "v1.0.0")
	harness.RequireManifest(t, "latest")
	harness.RequireBlobs(ctx, t, img)
}

type cliTestRegistry struct {
	server *httptest.Server
}

func newCLITestRegistry(t *testing.T, maxBlobChunkSize int64) *cliTestRegistry {
	t.Helper()

	log := zerowrap.Default()
	rootDir := t.TempDir()

	blobStore, err := filesystem.NewBlobStorage(rootDir, log)
	require.NoError(t, err)

	manifestStore, err := filesystem.NewManifestStorage(rootDir, log)
	require.NoError(t, err)

	svc := registrysvc.NewService(blobStore, manifestStore, cliNoopEventPublisher{})
	handler := registryhttp.NewHandler(svc, log, maxBlobChunkSize)

	return &cliTestRegistry{server: httptest.NewServer(handler)}
}

func (h *cliTestRegistry) Close() {
	h.server.Close()
}

func (h *cliTestRegistry) RequireManifest(t *testing.T, tag string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v2/testapp/manifests/%s", h.server.URL, tag), nil)
	require.NoError(t, err)

	resp, err := h.server.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func (h *cliTestRegistry) RequireBlobs(ctx context.Context, t *testing.T, img v1.Image) {
	t.Helper()

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

type cliNoopEventPublisher struct{}

func (cliNoopEventPublisher) Publish(domain.EventType, any) error {
	return nil
}
