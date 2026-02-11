package images

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	pkgruntime "github.com/bnema/gordon/pkg/runtime"
)

func TestService_ListImages_ReturnsAllImages(t *testing.T) {
	manifestStorage := newFakeManifestStorage()
	blobStorage := noopBlobStorage{}

	createdAt := time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)
	rt := &fakeRuntime{
		listDetails: []pkgruntime.ImageDetail{
			{
				ID:       "sha256:111",
				RepoTags: []string{"gordon/api:latest", "gordon/api:v1.2.3"},
				Size:     1234,
				Created:  createdAt,
			},
			{
				ID:       "sha256:222",
				RepoTags: []string{"<none>:<none>"},
				Size:     4321,
				Created:  createdAt.Add(-time.Hour),
			},
		},
	}

	svc := NewService(rt, manifestStorage, blobStorage, zerowrap.Default())

	images, err := svc.ListImages(context.Background())

	require.NoError(t, err)
	require.Len(t, images, 3)

	assert.Equal(t, domain.ImageInfo{
		Repository: "gordon/api",
		Tag:        "latest",
		Size:       1234,
		Created:    createdAt,
		ID:         "sha256:111",
		Dangling:   false,
	}, images[0])

	assert.Equal(t, domain.ImageInfo{
		Repository: "gordon/api",
		Tag:        "v1.2.3",
		Size:       1234,
		Created:    createdAt,
		ID:         "sha256:111",
		Dangling:   false,
	}, images[1])

	assert.Equal(t, domain.ImageInfo{
		Repository: "",
		Tag:        "",
		Size:       4321,
		Created:    createdAt.Add(-time.Hour),
		ID:         "sha256:222",
		Dangling:   true,
	}, images[2])
}

func TestService_ListImages_EmptyWhenNoImages(t *testing.T) {
	manifestStorage := newFakeManifestStorage()
	blobStorage := noopBlobStorage{}
	rt := &fakeRuntime{listDetails: []pkgruntime.ImageDetail{}}

	svc := NewService(rt, manifestStorage, blobStorage, zerowrap.Default())

	images, err := svc.ListImages(context.Background())

	require.NoError(t, err)
	assert.Empty(t, images)
}

func TestService_ListImages_SkipsPlaceholderTagsInMixedImage(t *testing.T) {
	manifestStorage := newFakeManifestStorage()
	blobStorage := noopBlobStorage{}
	createdAt := time.Date(2026, 2, 8, 13, 0, 0, 0, time.UTC)

	rt := &fakeRuntime{
		listDetails: []pkgruntime.ImageDetail{{
			ID:       "sha256:mixed",
			RepoTags: []string{"<none>:<none>", "gordon/web:latest"},
			Size:     100,
			Created:  createdAt,
		}},
	}

	svc := NewService(rt, manifestStorage, blobStorage, zerowrap.Default())

	images, err := svc.ListImages(context.Background())

	require.NoError(t, err)
	require.Len(t, images, 1)
	assert.Equal(t, "gordon/web", images[0].Repository)
	assert.Equal(t, "latest", images[0].Tag)
	assert.False(t, images[0].Dangling)
}

func TestService_ListImages_ReturnsErrorWhenRuntimeFails(t *testing.T) {
	manifestStorage := newFakeManifestStorage()
	blobStorage := noopBlobStorage{}
	rt := &fakeRuntime{listErr: errors.New("runtime list failed")}

	svc := NewService(rt, manifestStorage, blobStorage, zerowrap.Default())

	_, err := svc.ListImages(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list images")
}

func TestService_ListImages_IncludesRegistryTagsNotPresentInRuntime(t *testing.T) {
	manifestStorage := newFakeManifestStorage()
	manifestStorage.repositories = []string{"gordon/api"}
	manifestStorage.tagsByRepo["gordon/api"] = []string{"v1.2.3", "v1.2.2"}
	manifestStorage.modTimes[manifestRefKey("gordon/api", "v1.2.2")] = time.Date(2026, 2, 8, 9, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/api", "v1.2.3")] = time.Date(2026, 2, 8, 10, 0, 0, 0, time.UTC)

	createdAt := time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)
	rt := &fakeRuntime{
		listDetails: []pkgruntime.ImageDetail{
			{
				ID:       "sha256:runtime",
				RepoTags: []string{"reg.bnema.dev/gordon/api:latest"},
				Size:     4096,
				Created:  createdAt,
			},
		},
	}

	svc := NewService(rt, manifestStorage, noopBlobStorage{}, zerowrap.Default())

	images, err := svc.ListImages(context.Background())

	require.NoError(t, err)
	require.Len(t, images, 3)

	assert.Equal(t, domain.ImageInfo{
		Repository: "reg.bnema.dev/gordon/api",
		Tag:        "latest",
		Size:       4096,
		Created:    createdAt,
		ID:         "sha256:runtime",
		Dangling:   false,
	}, images[0])

	assert.Equal(t, domain.ImageInfo{
		Repository: "reg.bnema.dev/gordon/api",
		Tag:        "v1.2.3",
		Size:       0,
		Created:    time.Date(2026, 2, 8, 10, 0, 0, 0, time.UTC),
		ID:         "",
		Dangling:   false,
	}, images[1])

	assert.Equal(t, domain.ImageInfo{
		Repository: "reg.bnema.dev/gordon/api",
		Tag:        "v1.2.2",
		Size:       0,
		Created:    time.Date(2026, 2, 8, 9, 0, 0, 0, time.UTC),
		ID:         "",
		Dangling:   false,
	}, images[2])
}

func TestService_PruneRuntime_RemovesDanglingImages(t *testing.T) {
	manifestStorage := noopManifestStorage{}
	blobStorage := noopBlobStorage{}
	rt := &fakeRuntime{
		pruneReport: pkgruntime.PruneReport{
			DeletedIDs:     []string{"sha256:a", "sha256:b"},
			SpaceReclaimed: 2048,
		},
	}

	svc := NewService(rt, manifestStorage, blobStorage, zerowrap.Default())

	report, err := svc.PruneRuntime(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 2, report.Runtime.DeletedCount)
	assert.Equal(t, int64(2048), report.Runtime.SpaceReclaimed)
	assert.True(t, rt.pruneCalled)
	assert.True(t, rt.pruneDanglingOnly)
}

func TestService_PruneRuntime_NoDanglingImages(t *testing.T) {
	manifestStorage := noopManifestStorage{}
	blobStorage := noopBlobStorage{}
	rt := &fakeRuntime{
		pruneReport: pkgruntime.PruneReport{
			DeletedIDs:     nil,
			SpaceReclaimed: 0,
		},
	}

	svc := NewService(rt, manifestStorage, blobStorage, zerowrap.Default())

	report, err := svc.PruneRuntime(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 0, report.Runtime.DeletedCount)
	assert.Equal(t, int64(0), report.Runtime.SpaceReclaimed)
	assert.True(t, rt.pruneCalled)
	assert.True(t, rt.pruneDanglingOnly)
}

func TestService_PruneRuntime_ReturnsErrorWhenRuntimeFails(t *testing.T) {
	manifestStorage := noopManifestStorage{}
	blobStorage := noopBlobStorage{}
	rt := &fakeRuntime{pruneErr: errors.New("runtime prune failed")}

	svc := NewService(rt, manifestStorage, blobStorage, zerowrap.Default())

	_, err := svc.PruneRuntime(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to prune runtime images")
	assert.True(t, rt.pruneCalled)
	assert.True(t, rt.pruneDanglingOnly)
}

func TestService_PruneRegistry_KeepsLastNTags(t *testing.T) {
	manifestStorage := newFakeManifestStorage()
	manifestStorage.repositories = []string{"gordon/api"}
	manifestStorage.tagsByRepo["gordon/api"] = []string{"latest", "v3", "v2", "v1"}
	manifestStorage.modTimes[manifestRefKey("gordon/api", "latest")] = time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/api", "v3")] = time.Date(2026, 2, 8, 11, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/api", "v2")] = time.Date(2026, 2, 8, 10, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/api", "v1")] = time.Date(2026, 2, 8, 9, 0, 0, 0, time.UTC)
	manifestStorage.manifests[manifestRefKey("gordon/api", "latest")] = mustManifestJSON(t, "sha256:cfg-latest", "sha256:layer-latest")
	manifestStorage.manifests[manifestRefKey("gordon/api", "v3")] = mustManifestJSON(t, "sha256:cfg-v3", "sha256:layer-v3")
	manifestStorage.manifests[manifestRefKey("gordon/api", "v2")] = mustManifestJSON(t, "sha256:cfg-v2", "sha256:layer-v2")

	blobStorage := &fakeBlobStorage{}
	svc := NewService(&fakeRuntime{}, manifestStorage, blobStorage, zerowrap.Default())

	report, err := svc.PruneRegistry(context.Background(), 2)

	require.NoError(t, err)
	assert.Equal(t, 1, report.Registry.TagsRemoved)
	assert.ElementsMatch(t, []manifestRef{{name: "gordon/api", reference: "v1"}}, manifestStorage.deletedManifests)
}

func TestService_PruneRegistry_AlwaysKeepsLatestTag(t *testing.T) {
	manifestStorage := newFakeManifestStorage()
	manifestStorage.repositories = []string{"gordon/api"}
	manifestStorage.tagsByRepo["gordon/api"] = []string{"latest", "v3", "v2"}
	manifestStorage.modTimes[manifestRefKey("gordon/api", "latest")] = time.Date(2026, 2, 8, 8, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/api", "v3")] = time.Date(2026, 2, 8, 11, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/api", "v2")] = time.Date(2026, 2, 8, 10, 0, 0, 0, time.UTC)
	manifestStorage.manifests[manifestRefKey("gordon/api", "latest")] = mustManifestJSON(t, "sha256:cfg-latest", "sha256:layer-shared")
	manifestStorage.manifests[manifestRefKey("gordon/api", "v3")] = mustManifestJSON(t, "sha256:cfg-v3", "sha256:layer-shared")

	blobStorage := &fakeBlobStorage{}
	svc := NewService(&fakeRuntime{}, manifestStorage, blobStorage, zerowrap.Default())

	report, err := svc.PruneRegistry(context.Background(), 1)

	require.NoError(t, err)
	assert.Equal(t, 1, report.Registry.TagsRemoved)
	assert.Equal(t, []manifestRef{{name: "gordon/api", reference: "v2"}}, manifestStorage.deletedManifests)
}

func TestService_PruneRegistry_SkipsWhenFewerThanKeepLast(t *testing.T) {
	manifestStorage := newFakeManifestStorage()
	manifestStorage.repositories = []string{"gordon/api"}
	manifestStorage.tagsByRepo["gordon/api"] = []string{"latest", "v1"}
	manifestStorage.modTimes[manifestRefKey("gordon/api", "latest")] = time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/api", "v1")] = time.Date(2026, 2, 8, 11, 0, 0, 0, time.UTC)
	manifestStorage.manifests[manifestRefKey("gordon/api", "latest")] = mustManifestJSON(t, "sha256:cfg-latest", "sha256:layer-latest")
	manifestStorage.manifests[manifestRefKey("gordon/api", "v1")] = mustManifestJSON(t, "sha256:cfg-v1", "sha256:layer-v1")

	blobStorage := &fakeBlobStorage{}
	svc := NewService(&fakeRuntime{}, manifestStorage, blobStorage, zerowrap.Default())

	report, err := svc.PruneRegistry(context.Background(), 5)

	require.NoError(t, err)
	assert.Equal(t, 0, report.Registry.TagsRemoved)
	assert.Empty(t, manifestStorage.deletedManifests)
}

func TestService_PruneRegistry_KeepLastZeroSkipsRegistryCleanup(t *testing.T) {
	manifestStorage := newFakeManifestStorage()
	manifestStorage.repositories = []string{"gordon/api"}
	blobStorage := &fakeBlobStorage{blobs: []string{"sha256:orphan"}}
	svc := NewService(&fakeRuntime{}, manifestStorage, blobStorage, zerowrap.Default())

	report, err := svc.PruneRegistry(context.Background(), 0)

	require.NoError(t, err)
	assert.Equal(t, domain.RegistryPruneResult{}, report.Registry)
	assert.Equal(t, 0, manifestStorage.listRepositoriesCalls)
	assert.Empty(t, manifestStorage.deletedManifests)
	assert.Empty(t, blobStorage.deletedBlobs)
}

func TestService_PruneRegistry_GarbageCollectsUnreferencedBlobs(t *testing.T) {
	manifestStorage := newFakeManifestStorage()
	manifestStorage.repositories = []string{"gordon/api"}
	manifestStorage.tagsByRepo["gordon/api"] = []string{"latest"}
	manifestStorage.modTimes[manifestRefKey("gordon/api", "latest")] = time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)
	manifestStorage.manifests[manifestRefKey("gordon/api", "latest")] = mustManifestJSON(t, "sha256:cfg-live", "sha256:layer-live")

	blobStorage := &fakeBlobStorage{blobs: []string{"sha256:cfg-live", "sha256:layer-live", "sha256:orphan"}}
	svc := NewService(&fakeRuntime{}, manifestStorage, blobStorage, zerowrap.Default())

	report, err := svc.PruneRegistry(context.Background(), 1)

	require.NoError(t, err)
	assert.Equal(t, 1, report.Registry.BlobsRemoved)
	assert.Equal(t, []string{"sha256:orphan"}, blobStorage.deletedBlobs)
}

func TestService_PruneRegistry_PreservesSharedBlobs(t *testing.T) {
	manifestStorage := newFakeManifestStorage()
	manifestStorage.repositories = []string{"gordon/api"}
	manifestStorage.tagsByRepo["gordon/api"] = []string{"latest", "v2", "v1"}
	manifestStorage.modTimes[manifestRefKey("gordon/api", "latest")] = time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/api", "v2")] = time.Date(2026, 2, 8, 11, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/api", "v1")] = time.Date(2026, 2, 8, 10, 0, 0, 0, time.UTC)
	manifestStorage.manifests[manifestRefKey("gordon/api", "latest")] = mustManifestJSON(t, "sha256:cfg-latest", "sha256:layer-shared")
	manifestStorage.manifests[manifestRefKey("gordon/api", "v2")] = mustManifestJSON(t, "sha256:cfg-v2", "sha256:layer-shared")
	manifestStorage.manifests[manifestRefKey("gordon/api", "v1")] = mustManifestJSON(t, "sha256:cfg-v1", "sha256:layer-shared")

	blobStorage := &fakeBlobStorage{blobs: []string{"sha256:cfg-latest", "sha256:cfg-v2", "sha256:layer-shared", "sha256:cfg-v1"}}
	svc := NewService(&fakeRuntime{}, manifestStorage, blobStorage, zerowrap.Default())

	report, err := svc.PruneRegistry(context.Background(), 1)

	require.NoError(t, err)
	assert.Equal(t, 1, report.Registry.BlobsRemoved)
	assert.Equal(t, []string{"sha256:cfg-v1"}, blobStorage.deletedBlobs)
}

func TestService_PruneRegistry_PreservesManifestListChildDigests(t *testing.T) {
	manifestStorage := newFakeManifestStorage()
	manifestStorage.repositories = []string{"gordon/api"}
	manifestStorage.tagsByRepo["gordon/api"] = []string{"latest"}
	manifestStorage.modTimes[manifestRefKey("gordon/api", "latest")] = time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)
	manifestStorage.manifests[manifestRefKey("gordon/api", "latest")] = mustManifestIndexJSON(t, "sha256:child-amd64", "sha256:child-arm64")
	manifestStorage.manifests[manifestRefKey("gordon/api", "sha256:child-amd64")] = mustManifestJSON(t, "sha256:cfg-amd64", "sha256:layer-amd64")
	manifestStorage.manifests[manifestRefKey("gordon/api", "sha256:child-arm64")] = mustManifestJSON(t, "sha256:cfg-arm64", "sha256:layer-arm64")

	blobStorage := &fakeBlobStorage{blobs: []string{
		"sha256:child-amd64",
		"sha256:child-arm64",
		"sha256:cfg-amd64",
		"sha256:layer-amd64",
		"sha256:cfg-arm64",
		"sha256:layer-arm64",
		"sha256:orphan",
	}}
	svc := NewService(&fakeRuntime{}, manifestStorage, blobStorage, zerowrap.Default())

	report, err := svc.PruneRegistry(context.Background(), 1)

	require.NoError(t, err)
	assert.Equal(t, 1, report.Registry.BlobsRemoved)
	assert.Equal(t, []string{"sha256:orphan"}, blobStorage.deletedBlobs)
}

func TestService_PruneRegistry_TieBreaksByTagName(t *testing.T) {
	manifestStorage := newFakeManifestStorage()
	manifestStorage.repositories = []string{"gordon/api"}
	manifestStorage.tagsByRepo["gordon/api"] = []string{"v1", "v2"}
	tie := time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/api", "v1")] = tie
	manifestStorage.modTimes[manifestRefKey("gordon/api", "v2")] = tie
	manifestStorage.manifests[manifestRefKey("gordon/api", "v2")] = mustManifestJSON(t, "sha256:cfg-v2", "sha256:layer-v2")

	blobStorage := &fakeBlobStorage{}
	svc := NewService(&fakeRuntime{}, manifestStorage, blobStorage, zerowrap.Default())

	report, err := svc.PruneRegistry(context.Background(), 1)

	require.NoError(t, err)
	assert.Equal(t, 1, report.Registry.TagsRemoved)
	assert.Equal(t, []manifestRef{{name: "gordon/api", reference: "v1"}}, manifestStorage.deletedManifests)
}

func TestService_PruneRegistry_ReturnsErrorWhenChildManifestMissing(t *testing.T) {
	manifestStorage := newFakeManifestStorage()
	manifestStorage.repositories = []string{"gordon/api"}
	manifestStorage.tagsByRepo["gordon/api"] = []string{"latest"}
	manifestStorage.modTimes[manifestRefKey("gordon/api", "latest")] = time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)
	manifestStorage.manifests[manifestRefKey("gordon/api", "latest")] = mustManifestIndexJSON(t, "sha256:child-missing")

	blobStorage := &fakeBlobStorage{blobs: []string{"sha256:child-missing", "sha256:orphan"}}
	svc := NewService(&fakeRuntime{}, manifestStorage, blobStorage, zerowrap.Default())

	_, err := svc.PruneRegistry(context.Background(), 1)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read manifest")
}

func TestService_PruneRegistry_MultipleRepositories(t *testing.T) {
	manifestStorage := newFakeManifestStorage()
	manifestStorage.repositories = []string{"gordon/api", "gordon/web"}
	manifestStorage.tagsByRepo["gordon/api"] = []string{"latest", "v2", "v1"}
	manifestStorage.tagsByRepo["gordon/web"] = []string{"latest", "v3", "v2"}
	manifestStorage.modTimes[manifestRefKey("gordon/api", "latest")] = time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/api", "v2")] = time.Date(2026, 2, 8, 11, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/api", "v1")] = time.Date(2026, 2, 8, 10, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/web", "latest")] = time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/web", "v3")] = time.Date(2026, 2, 8, 11, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/web", "v2")] = time.Date(2026, 2, 8, 10, 0, 0, 0, time.UTC)
	manifestStorage.manifests[manifestRefKey("gordon/api", "latest")] = mustManifestJSON(t, "sha256:cfg-api-latest", "sha256:layer-api-latest")
	manifestStorage.manifests[manifestRefKey("gordon/api", "v2")] = mustManifestJSON(t, "sha256:cfg-api-v2", "sha256:layer-api-v2")
	manifestStorage.manifests[manifestRefKey("gordon/web", "latest")] = mustManifestJSON(t, "sha256:cfg-web-latest", "sha256:layer-web-latest")
	manifestStorage.manifests[manifestRefKey("gordon/web", "v3")] = mustManifestJSON(t, "sha256:cfg-web-v3", "sha256:layer-web-v3")

	blobStorage := &fakeBlobStorage{}
	svc := NewService(&fakeRuntime{}, manifestStorage, blobStorage, zerowrap.Default())

	report, err := svc.PruneRegistry(context.Background(), 1)

	require.NoError(t, err)
	assert.Equal(t, 2, report.Registry.TagsRemoved)
	assert.ElementsMatch(t, []manifestRef{
		{name: "gordon/api", reference: "v1"},
		{name: "gordon/web", reference: "v2"},
	}, manifestStorage.deletedManifests)
}

func TestService_PruneRegistry_EmptyRepository(t *testing.T) {
	manifestStorage := newFakeManifestStorage()
	manifestStorage.repositories = []string{"gordon/api"}
	manifestStorage.tagsByRepo["gordon/api"] = []string{}
	blobStorage := &fakeBlobStorage{}
	svc := NewService(&fakeRuntime{}, manifestStorage, blobStorage, zerowrap.Default())

	report, err := svc.PruneRegistry(context.Background(), 2)

	require.NoError(t, err)
	assert.Equal(t, domain.RegistryPruneResult{}, report.Registry)
	assert.Empty(t, manifestStorage.deletedManifests)
	assert.Empty(t, blobStorage.deletedBlobs)
}

func TestService_Prune_RunsBothRuntimeAndRegistry(t *testing.T) {
	manifestStorage := newFakeManifestStorage()
	manifestStorage.repositories = []string{"gordon/api"}
	manifestStorage.tagsByRepo["gordon/api"] = []string{"latest", "v2", "v1"}
	manifestStorage.modTimes[manifestRefKey("gordon/api", "latest")] = time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/api", "v2")] = time.Date(2026, 2, 8, 11, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/api", "v1")] = time.Date(2026, 2, 8, 10, 0, 0, 0, time.UTC)
	manifestStorage.manifests[manifestRefKey("gordon/api", "latest")] = mustManifestJSON(t, "sha256:cfg-live", "sha256:layer-live")
	manifestStorage.manifests[manifestRefKey("gordon/api", "v2")] = mustManifestJSON(t, "sha256:cfg-v2", "sha256:layer-v2")
	blobStorage := &fakeBlobStorage{blobs: []string{"sha256:cfg-live", "sha256:layer-live", "sha256:orphan"}}
	rt := &fakeRuntime{pruneReport: pkgruntime.PruneReport{DeletedIDs: []string{"sha256:img1"}, SpaceReclaimed: 1024}}

	svc := NewService(rt, manifestStorage, blobStorage, zerowrap.Default())

	report, err := svc.Prune(context.Background(), 1)

	require.NoError(t, err)
	assert.Equal(t, 1, report.Runtime.DeletedCount)
	assert.Equal(t, int64(1024), report.Runtime.SpaceReclaimed)
	assert.Equal(t, 1, report.Registry.TagsRemoved)
	assert.Equal(t, 1, report.Registry.BlobsRemoved)
	assert.Equal(t, []manifestRef{{name: "gordon/api", reference: "v1"}}, manifestStorage.deletedManifests)
	assert.Equal(t, []string{"sha256:orphan"}, blobStorage.deletedBlobs)
}

func TestService_Prune_RuntimeFailureDoesNotBlockRegistry(t *testing.T) {
	manifestStorage := newFakeManifestStorage()
	manifestStorage.repositories = []string{"gordon/api"}
	manifestStorage.tagsByRepo["gordon/api"] = []string{"latest", "v2", "v1"}
	manifestStorage.modTimes[manifestRefKey("gordon/api", "latest")] = time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/api", "v2")] = time.Date(2026, 2, 8, 11, 0, 0, 0, time.UTC)
	manifestStorage.modTimes[manifestRefKey("gordon/api", "v1")] = time.Date(2026, 2, 8, 10, 0, 0, 0, time.UTC)
	manifestStorage.manifests[manifestRefKey("gordon/api", "latest")] = mustManifestJSON(t, "sha256:cfg-live", "sha256:layer-live")
	manifestStorage.manifests[manifestRefKey("gordon/api", "v2")] = mustManifestJSON(t, "sha256:cfg-v2", "sha256:layer-v2")
	blobStorage := &fakeBlobStorage{blobs: []string{"sha256:cfg-live", "sha256:layer-live", "sha256:orphan"}}
	rt := &fakeRuntime{pruneErr: errors.New("runtime prune failed")}

	svc := NewService(rt, manifestStorage, blobStorage, zerowrap.Default())

	report, err := svc.Prune(context.Background(), 1)

	require.NoError(t, err)
	assert.Equal(t, 0, report.Runtime.DeletedCount)
	assert.Equal(t, int64(0), report.Runtime.SpaceReclaimed)
	assert.Equal(t, 1, report.Registry.TagsRemoved)
	assert.Equal(t, 1, report.Registry.BlobsRemoved)
	assert.Equal(t, []manifestRef{{name: "gordon/api", reference: "v1"}}, manifestStorage.deletedManifests)
	assert.Equal(t, []string{"sha256:orphan"}, blobStorage.deletedBlobs)
}

type fakeRuntime struct {
	pkgruntime.Runtime

	listDetails []pkgruntime.ImageDetail
	listErr     error

	pruneReport       pkgruntime.PruneReport
	pruneErr          error
	pruneCalled       bool
	pruneDanglingOnly bool
}

type noopManifestStorage struct {
	out.ManifestStorage
}

type noopBlobStorage struct {
	out.BlobStorage
}

type manifestRef struct {
	name      string
	reference string
}

type fakeManifestStorage struct {
	out.ManifestStorage

	repositories []string
	tagsByRepo   map[string][]string
	modTimes     map[string]time.Time
	manifests    map[string][]byte

	listRepositoriesCalls int
	deletedManifests      []manifestRef
}

type fakeBlobStorage struct {
	out.BlobStorage

	blobs        []string
	deletedBlobs []string
}

func (f *fakeRuntime) ListImagesDetailed(context.Context) ([]pkgruntime.ImageDetail, error) {
	return f.listDetails, f.listErr
}

func (f *fakeRuntime) PruneImages(_ context.Context, danglingOnly bool) (pkgruntime.PruneReport, error) {
	f.pruneCalled = true
	f.pruneDanglingOnly = danglingOnly
	return f.pruneReport, f.pruneErr
}

func newFakeManifestStorage() *fakeManifestStorage {
	return &fakeManifestStorage{
		tagsByRepo: make(map[string][]string),
		modTimes:   make(map[string]time.Time),
		manifests:  make(map[string][]byte),
	}
}

func manifestRefKey(name, reference string) string {
	return name + "@" + reference
}

func mustManifestJSON(t *testing.T, configDigest string, layerDigests ...string) []byte {
	t.Helper()

	type descriptor struct {
		Digest string `json:"digest"`
	}

	payload := struct {
		Config descriptor   `json:"config"`
		Layers []descriptor `json:"layers"`
	}{
		Config: descriptor{Digest: configDigest},
		Layers: make([]descriptor, 0, len(layerDigests)),
	}

	for _, digest := range layerDigests {
		payload.Layers = append(payload.Layers, descriptor{Digest: digest})
	}

	data, err := json.Marshal(payload)
	require.NoError(t, err)
	return data
}

func mustManifestIndexJSON(t *testing.T, digests ...string) []byte {
	t.Helper()

	type descriptor struct {
		Digest string `json:"digest"`
	}

	payload := struct {
		Manifests []descriptor `json:"manifests"`
	}{
		Manifests: make([]descriptor, 0, len(digests)),
	}

	for _, digest := range digests {
		payload.Manifests = append(payload.Manifests, descriptor{Digest: digest})
	}

	data, err := json.Marshal(payload)
	require.NoError(t, err)
	return data
}

func (f *fakeManifestStorage) ListRepositories() ([]string, error) {
	f.listRepositoriesCalls++
	return append([]string(nil), f.repositories...), nil
}

func (f *fakeManifestStorage) ListTags(name string) ([]string, error) {
	return append([]string(nil), f.tagsByRepo[name]...), nil
}

func (f *fakeManifestStorage) GetManifestModTime(name, reference string) (time.Time, error) {
	modTime, ok := f.modTimes[manifestRefKey(name, reference)]
	if !ok {
		return time.Time{}, errors.New("manifest modtime not found")
	}
	return modTime, nil
}

func (f *fakeManifestStorage) DeleteManifest(name, reference string) error {
	f.deletedManifests = append(f.deletedManifests, manifestRef{name: name, reference: reference})
	tags := f.tagsByRepo[name]
	filtered := tags[:0]
	for _, tag := range tags {
		if tag != reference {
			filtered = append(filtered, tag)
		}
	}
	f.tagsByRepo[name] = filtered
	delete(f.manifests, manifestRefKey(name, reference))
	return nil
}

func (f *fakeManifestStorage) GetManifest(name, reference string) ([]byte, string, error) {
	manifest, ok := f.manifests[manifestRefKey(name, reference)]
	if !ok {
		return nil, "", errors.New("manifest not found")
	}
	return manifest, "application/vnd.oci.image.manifest.v1+json", nil
}

func (f *fakeBlobStorage) ListBlobs() ([]string, error) {
	return append([]string(nil), f.blobs...), nil
}

func (f *fakeBlobStorage) DeleteBlob(digest string) error {
	f.deletedBlobs = append(f.deletedBlobs, digest)
	for i, existing := range f.blobs {
		if existing != digest {
			continue
		}
		f.blobs = append(f.blobs[:i], f.blobs[i+1:]...)
		break
	}
	return nil
}

func TestSplitRepoTag(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantRepo string
		wantTag  string
	}{
		{name: "repository and tag", input: "alpine:latest", wantRepo: "alpine", wantTag: "latest"},
		{name: "sha-like tag stays a tag", input: "image:sha256vdfhnijkvfedhbni", wantRepo: "image", wantTag: "sha256vdfhnijkvfedhbni"},
		{name: "registry port and tag", input: "localhost:5000/repo:v1", wantRepo: "localhost:5000/repo", wantTag: "v1"},
		{name: "registry port without tag", input: "localhost:5000/repo", wantRepo: "localhost:5000/repo", wantTag: ""},
		{name: "digest reference not split as tag", input: "image@sha256:abcdef012345", wantRepo: "image@sha256:abcdef012345", wantTag: ""},
		{name: "missing tag", input: "alpine", wantRepo: "alpine", wantTag: ""},
		{name: "trailing colon", input: "alpine:", wantRepo: "alpine:", wantTag: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, tag := splitRepoTag(tt.input)
			assert.Equal(t, tt.wantRepo, repo)
			assert.Equal(t, tt.wantTag, tag)
		})
	}
}

var _ pkgruntime.Runtime = (*fakeRuntime)(nil)
