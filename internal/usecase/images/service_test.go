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

type noopBlobStorage struct {
	out.BlobStorage
}

type fakeRuntime struct {
	pkgruntime.Runtime

	listDetails []pkgruntime.ImageDetail
	listErr     error
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
	deleteErr             error
}

type fakeBlobStorage struct {
	out.BlobStorage

	blobs          []string
	blobSizes      map[string]int64
	blobModTimes   map[string]time.Time
	blobModTimeErr error
	deletedBlobs   []string
}

func (f *fakeRuntime) ListImagesDetailed(context.Context) ([]pkgruntime.ImageDetail, error) {
	return f.listDetails, f.listErr
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
	if f.deleteErr != nil {
		return f.deleteErr
	}
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

func (f *fakeBlobStorage) GetBlobModTime(digest string) (time.Time, error) {
	if f.blobModTimeErr != nil {
		return time.Time{}, f.blobModTimeErr
	}
	return f.blobModTimes[digest], nil
}

func (f *fakeBlobStorage) DeleteBlob(digest string) (int64, error) {
	f.deletedBlobs = append(f.deletedBlobs, digest)
	var size int64
	if f.blobSizes != nil {
		size = f.blobSizes[digest]
	}
	for i, existing := range f.blobs {
		if existing != digest {
			continue
		}
		f.blobs = append(f.blobs[:i], f.blobs[i+1:]...)
		break
	}
	return size, nil
}

func (f *fakeBlobStorage) CleanupStaleUploads(_ time.Duration) (int, int64, error) {
	return 0, 0, nil
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
