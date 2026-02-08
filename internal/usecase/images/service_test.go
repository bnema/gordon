package images

import (
	"context"
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
	manifestStorage := noopManifestStorage{}
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
	manifestStorage := noopManifestStorage{}
	blobStorage := noopBlobStorage{}
	rt := &fakeRuntime{listDetails: []pkgruntime.ImageDetail{}}

	svc := NewService(rt, manifestStorage, blobStorage, zerowrap.Default())

	images, err := svc.ListImages(context.Background())

	require.NoError(t, err)
	assert.Empty(t, images)
}

func TestService_ListImages_SkipsPlaceholderTagsInMixedImage(t *testing.T) {
	manifestStorage := noopManifestStorage{}
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
	manifestStorage := noopManifestStorage{}
	blobStorage := noopBlobStorage{}
	rt := &fakeRuntime{listErr: errors.New("runtime list failed")}

	svc := NewService(rt, manifestStorage, blobStorage, zerowrap.Default())

	_, err := svc.ListImages(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list images")
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

func (f *fakeRuntime) ListImagesDetailed(context.Context) ([]pkgruntime.ImageDetail, error) {
	return f.listDetails, f.listErr
}

func (f *fakeRuntime) PruneImages(_ context.Context, danglingOnly bool) (pkgruntime.PruneReport, error) {
	f.pruneCalled = true
	f.pruneDanglingOnly = danglingOnly
	return f.pruneReport, f.pruneErr
}

var _ pkgruntime.Runtime = (*fakeRuntime)(nil)
