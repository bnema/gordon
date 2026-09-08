package pruneguard

// Tests for the §8 prune gates: operation-level disable while app state
// exists, per-volume provenance verdicts, and fail-closed image pruning
// over pinned/unknown/protected content.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestVolumesPruneAllowed(t *testing.T) {
	require.NoError(t, VolumesPruneAllowed(false))

	err := VolumesPruneAllowed(true)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrPruneDisabled)
}

func TestImagePruneSchedulerAllowed(t *testing.T) {
	require.NoError(t, ImagePruneSchedulerAllowed(false))

	err := ImagePruneSchedulerAllowed(true)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrPruneDisabled)
}

func TestClassifyVolume(t *testing.T) {
	assert.Equal(t, ProvenanceUnmanaged, ClassifyVolume(nil))
	assert.Equal(t, ProvenanceUnmanaged, ClassifyVolume(map[string]string{}))
	assert.Equal(t, ProvenanceUnmanaged, ClassifyVolume(map[string]string{domain.LabelManaged: "false"}))
	assert.Equal(t, ProvenanceLegacyManaged, ClassifyVolume(map[string]string{domain.LabelManaged: "true"}))
	assert.Equal(t, ProvenanceAppOwned, ClassifyVolume(map[string]string{
		domain.LabelManaged: "true",
		domain.LabelApp:     "blog",
	}))
}

func TestVolumePruneEligible(t *testing.T) {
	eligible, _ := VolumePruneEligible(ProvenanceLegacyManaged, true, false)
	assert.False(t, eligible, "in-use volumes are never eligible")

	eligible, _ = VolumePruneEligible(ProvenanceUnmanaged, false, false)
	assert.False(t, eligible, "unmanaged volumes are never eligible")

	eligible, reason := VolumePruneEligible(ProvenanceAppOwned, false, false)
	assert.False(t, eligible, "app-owned volumes are never eligible")
	assert.Contains(t, reason, "never deleted")

	eligible, _ = VolumePruneEligible(ProvenanceLegacyManaged, false, true)
	assert.False(t, eligible, "legacy managed volumes are never eligible while app state exists")

	eligible, _ = VolumePruneEligible(ProvenanceLegacyManaged, false, false)
	assert.True(t, eligible, "legacy behavior preserved only with no app state")

	// App-owned stays ineligible even with no app state: volumes are
	// never deleted, only retained as orphans.
	eligible, _ = VolumePruneEligible(ProvenanceAppOwned, false, true)
	assert.False(t, eligible)
}

func TestProtectedImageSet(t *testing.T) {
	protected := ProtectedImageSet(
		[]string{"sha256:active", "sha256:stopped"},
		[]string{"sha256:inflight"},
		[]string{"sha256:pinned", ""},
	)
	for _, digest := range []string{"sha256:active", "sha256:stopped", "sha256:inflight", "sha256:pinned"} {
		assert.Contains(t, protected, digest)
	}
	assert.NotContains(t, protected, "")
}

func TestPartitionImageCandidates(t *testing.T) {
	protected := ProtectedImageSet([]string{"sha256:active"})
	candidates := []ImageCandidate{
		{Digest: "sha256:stale", Tags: []string{"blog:old"}},
		{Digest: "sha256:active", Tags: []string{"blog:current"}},
		{Digest: "sha256:pinned-manifest", Pinned: true},
		{Digest: ""},
	}
	safe, blocked := PartitionImageCandidates(candidates, protected)
	require.Len(t, safe, 1)
	assert.Equal(t, "sha256:stale", safe[0].Digest)
	require.Len(t, blocked, 3, "protected, pinned, and unknown content all block")
}

func TestCheckManualImagePrune_FailsClosed(t *testing.T) {
	require.NoError(t, CheckManualImagePrune(
		[]ImageCandidate{{Digest: "sha256:stale"}},
		ProtectedImageSet([]string{"sha256:active"}),
	))

	for _, candidates := range [][]ImageCandidate{
		{{Digest: "sha256:active"}},
		{{Digest: "sha256:x", Pinned: true}},
		{{Digest: ""}},
		{{Digest: "sha256:stale"}, {Digest: "sha256:active"}},
	} {
		err := CheckManualImagePrune(candidates, ProtectedImageSet([]string{"sha256:active"}))
		require.Error(t, err, "candidates %v", candidates)
		assert.True(t, errors.Is(err, domain.ErrPruneDisabled), "manual prune fails closed, never prunes around")
	}
}
