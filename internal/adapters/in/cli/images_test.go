package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
)

type fakeImagesClient struct {
	listImagesResp []dto.Image
	listImagesErr  error
	pruneResp      *dto.ImagePruneResponse
	pruneErr       error

	listImagesCalls int
	pruneCalls      int
	lastPruneKeep   int
}

func (f *fakeImagesClient) ListImages(_ context.Context) ([]dto.Image, error) {
	f.listImagesCalls++
	if f.listImagesErr != nil {
		return nil, f.listImagesErr
	}
	return f.listImagesResp, nil
}

func (f *fakeImagesClient) PruneImages(_ context.Context, keepLast int) (*dto.ImagePruneResponse, error) {
	f.pruneCalls++
	f.lastPruneKeep = keepLast
	if f.pruneErr != nil {
		return nil, f.pruneErr
	}
	return f.pruneResp, nil
}

func TestRunImagesList_PrintsRowsAndSummary(t *testing.T) {
	createdAt := time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)
	client := &fakeImagesClient{
		listImagesResp: []dto.Image{
			{Repository: "registry.example.com/app", Tag: "latest", Size: 12_000_000, Created: createdAt, ID: "sha256:1111", Dangling: false},
			{Repository: "<none>", Tag: "<none>", Size: 512_000, Created: createdAt, ID: "sha256:2222", Dangling: true},
		},
	}

	var out bytes.Buffer
	err := runImagesList(context.Background(), client, &out)
	require.NoError(t, err)

	text := out.String()
	assert.Contains(t, text, "REPOSITORY")
	assert.Contains(t, text, "registry.example.com/app")
	assert.Contains(t, text, "latest")
	assert.Contains(t, text, "Total images: 2 (dangling: 1)")
	assert.Equal(t, 1, client.listImagesCalls)
}

func TestRunImagesList_EmptyOutput(t *testing.T) {
	client := &fakeImagesClient{listImagesResp: []dto.Image{}}

	var out bytes.Buffer
	err := runImagesList(context.Background(), client, &out)
	require.NoError(t, err)

	text := out.String()
	assert.Contains(t, text, "No images found")
	assert.Contains(t, text, "Total images: 0")
	assert.Equal(t, 1, client.listImagesCalls)
}

func TestRunImagesPrune_DryRunCallsListOnly(t *testing.T) {
	client := &fakeImagesClient{
		listImagesResp: []dto.Image{{Dangling: true}, {Dangling: false}, {Dangling: true}},
	}

	var out bytes.Buffer
	err := runImagesPrune(context.Background(), client, imagesPruneOptions{DryRun: true, KeepLast: 3}, &out)
	require.NoError(t, err)

	text := out.String()
	assert.Contains(t, text, "Dry run")
	assert.Contains(t, text, "would prune 2 dangling runtime images")
	assert.Contains(t, text, "would keep last 3 tags")
	assert.Equal(t, 1, client.listImagesCalls)
	assert.Equal(t, 0, client.pruneCalls)
}

func TestRunImagesPrune_DryRunKeepZeroSkipsRegistry(t *testing.T) {
	client := &fakeImagesClient{listImagesResp: []dto.Image{{Dangling: true}}}

	var out bytes.Buffer
	err := runImagesPrune(context.Background(), client, imagesPruneOptions{DryRun: true, KeepLast: 0}, &out)
	require.NoError(t, err)

	text := out.String()
	assert.Contains(t, text, "Registry cleanup skipped (--keep=0)")
	assert.Equal(t, 1, client.listImagesCalls)
	assert.Equal(t, 0, client.pruneCalls)
}

func TestRunImagesPrune_RuntimeOnlyForcesKeepZero(t *testing.T) {
	client := &fakeImagesClient{pruneResp: &dto.ImagePruneResponse{}}

	var out bytes.Buffer
	err := runImagesPrune(context.Background(), client, imagesPruneOptions{KeepLast: 9, RuntimeOnly: true}, &out)
	require.NoError(t, err)

	assert.Equal(t, 1, client.pruneCalls)
	assert.Equal(t, 0, client.lastPruneKeep)
	assert.Contains(t, out.String(), "Registry cleanup skipped")
}

func TestRunImagesPrune_UsesKeepFlagWhenNotRuntimeOnly(t *testing.T) {
	client := &fakeImagesClient{
		pruneResp: &dto.ImagePruneResponse{
			Runtime:  dto.RuntimePruneResult{DeletedCount: 2, SpaceReclaimed: 4096},
			Registry: dto.RegistryPruneResult{TagsRemoved: 3, BlobsRemoved: 1, SpaceReclaimed: 8192},
		},
	}

	var out bytes.Buffer
	err := runImagesPrune(context.Background(), client, imagesPruneOptions{KeepLast: 5}, &out)
	require.NoError(t, err)

	text := out.String()
	assert.Equal(t, 1, client.pruneCalls)
	assert.Equal(t, 5, client.lastPruneKeep)
	assert.Contains(t, text, "Runtime: deleted=2")
	assert.Contains(t, text, "Registry: tags_removed=3")
}

func TestRunImagesPrune_ReturnsRemoteErrors(t *testing.T) {
	client := &fakeImagesClient{pruneErr: errors.New("request failed")}

	err := runImagesPrune(context.Background(), client, imagesPruneOptions{KeepLast: 2}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to prune images")
}

func TestRunImagesPrune_RejectsNegativeKeep(t *testing.T) {
	err := runImagesPrune(context.Background(), &fakeImagesClient{}, imagesPruneOptions{KeepLast: -1}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--keep must be >= 0")
}

func TestRunImagesPrune_RejectsEmptyResponse(t *testing.T) {
	client := &fakeImagesClient{pruneResp: nil}
	err := runImagesPrune(context.Background(), client, imagesPruneOptions{KeepLast: 1}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty response")
}
