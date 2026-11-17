package images

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// TestPrune_DigestTagOfRetainedIndexChildIsNeverDeleted proves that legacy
// digest-named tags.json entries pointing at children of a retained index
// are never deleted by retention, even when they fall outside the window:
// deleting such a "tag" removes the child manifest file itself.
func TestPrune_DigestTagOfRetainedIndexChildIsNeverDeleted(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	children := make([]string, 0, 5)
	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	tags := []string{"latest"}
	blobDigests := make([]string, 0, 5)
	for index, seed := range []string{"amd64", "arm64", "armv7", "att-amd64", "att-arm64"} {
		config := testDigest(seed + "-config")
		body := mustManifestJSON(t, config)
		child := manifestBodyDigest(body)
		children = append(children, child)
		blobDigests = append(blobDigests, config)
		manifests.manifests[manifestRefKey("app", child)] = body
		manifests.modTimes[manifestRefKey("app", child)] = now.Add(time.Duration(-10-index) * 24 * time.Hour)
		tags = append(tags, child)
	}
	manifests.tagsByRepo["app"] = tags
	manifests.manifests[manifestRefKey("app", "latest")] = mustManifestIndexJSON(t, children...)
	manifests.modTimes[manifestRefKey("app", "latest")] = now.Add(-30 * 24 * time.Hour)

	blobModTimes := make(map[string]time.Time, len(blobDigests))
	for _, digest := range blobDigests {
		blobModTimes[digest] = now.Add(-72 * time.Hour)
	}
	blobs := &fakeBlobStorage{blobs: blobDigests, blobModTimes: blobModTimes}

	svc, _ := newPruneService(t, manifests, blobs, &fakeProtectionStore{}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 3, PruneRegistry: true})
	require.NoError(t, err)

	assert.Empty(t, manifests.deletedManifests, "a child manifest of a retained index must never be deleted")
	assert.Empty(t, blobs.deletedBlobs)
	assert.Empty(t, report.Plan.Gaps)
	for _, candidate := range report.Plan.Candidates {
		assert.Equal(t, domain.PruneVerdictProtected, candidate.Verdict, "candidate %s", candidate.Ref)
	}
}
