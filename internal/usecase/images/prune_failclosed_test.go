package images

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// TestPrune_SnapshotGapFailsClosed proves an incomplete protection
// snapshot never authorizes deletion: an unreadable app record could be
// hiding a claim on any candidate, so every candidate stays unknown.
func TestPrune_SnapshotGapFailsClosed(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest", "v1", "v2"}
	for index, tag := range []string{"latest", "v1", "v2"} {
		manifests.modTimes[manifestRefKey("app", tag)] = now.Add(time.Duration(index-3) * time.Hour)
		manifests.manifests[manifestRefKey("app", tag)] = mustManifestJSON(t, testDigest(tag), testDigest(tag+"-layer"))
	}

	orphan := testDigest("orphan")
	blobs := &fakeBlobStorage{
		blobs:        []string{orphan},
		blobModTimes: map[string]time.Time{orphan: now.Add(-72 * time.Hour)},
	}
	protection := &fakeProtectionStore{snapshot: &domain.PruneProtectionSnapshot{
		Gaps: []domain.InventoryGap{{
			Source: domain.InventorySourceAppState,
			Reason: domain.PruneReasonUnknownInventory,
			Detail: "app record unreadable",
		}},
	}}
	runtime := &fakePruneRuntime{inventory: &domain.RuntimeInventory{
		Images: []domain.RuntimeImage{{ID: "sha256:dangling", Labels: map[string]string{domain.LabelApp: "app"}}},
	}}

	svc, _ := newPruneService(t, manifests, blobs, protection, runtime)
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{
		KeepLast: 1, PruneDangling: true, PruneRegistry: true,
	})
	require.NoError(t, err)

	assert.Empty(t, manifests.deletedManifests, "an incomplete snapshot must not authorize tag deletion")
	assert.Empty(t, blobs.deletedBlobs, "an incomplete snapshot must not authorize blob deletion")
	assert.Empty(t, runtime.removedImages, "an incomplete snapshot must not authorize image deletion")
	assert.Zero(t, report.Plan.CountByVerdict(domain.PruneVerdictEligible))
	// Only the explicitly retained window tags stay protected; every
	// other candidate is unknown rather than eligible.
	assert.Equal(t, 2, report.Plan.CountByVerdict(domain.PruneVerdictProtected))
	assert.Equal(t, len(report.Plan.Candidates)-2, report.Plan.CountByVerdict(domain.PruneVerdictUnknown))
}

// TestPrune_MovedTagKeepsActiveDigestClosure proves an ACTIVE digest root
// keeps the full OCI closure of its manifest: moving the mutable tag away
// must not let prune delete the config or layers of the pinned image.
func TestPrune_MovedTagKeepsActiveDigestClosure(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	pinned := testDigest("active-manifest")
	configDigest := testDigest("active-config")
	layerDigest := testDigest("active-layer")

	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	// The tag now points elsewhere; the pinned manifest survives untagged.
	manifests.tagsByRepo["app"] = []string{"latest"}
	manifests.modTimes[manifestRefKey("app", "latest")] = now
	manifests.manifests[manifestRefKey("app", "latest")] = mustManifestJSON(t, testDigest("latest-config"))
	manifests.manifests[manifestRefKey("app", pinned)] = mustManifestJSON(t, configDigest, layerDigest)

	blobs := &fakeBlobStorage{
		blobs: []string{configDigest, layerDigest, testDigest("orphan")},
		blobModTimes: map[string]time.Time{
			configDigest:         now.Add(-72 * time.Hour),
			layerDigest:          now.Add(-72 * time.Hour),
			testDigest("orphan"): now.Add(-72 * time.Hour),
		},
	}
	protection := &fakeProtectionStore{snapshot: &domain.PruneProtectionSnapshot{
		Roots: []domain.ProtectionRoot{{
			Kind: domain.ProtectionActiveService, Ref: pinned, Repository: "app", Owner: "app@active.web",
		}},
	}}

	svc, _ := newPruneService(t, manifests, blobs, protection, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true})
	require.NoError(t, err)

	for _, protected := range []string{configDigest, layerDigest} {
		protectedVerdicts := report.Plan.CandidatesOfKind(domain.PruneResourceOCIBlob, domain.PruneVerdictProtected)
		found := false
		for _, verdict := range protectedVerdicts {
			if verdict.Ref == protected {
				found = true
			}
		}
		assert.True(t, found, "blob %s of the pinned manifest must stay protected", protected)
	}
	assert.NotContains(t, blobs.deletedBlobs, configDigest)
	assert.NotContains(t, blobs.deletedBlobs, layerDigest)
}

// TestPrune_SubjectClosureProtectsManifestConfigAndLayers proves a retained
// artifact manifest keeps its subject manifest and that subject's blobs.
func TestPrune_SubjectClosureProtectsManifestConfigAndLayers(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	subject := testDigest("subject-manifest")
	configDigest := testDigest("subject-config")
	layerDigest := testDigest("subject-layer")

	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"attestation"}
	manifests.modTimes[manifestRefKey("app", "attestation")] = now
	manifests.manifests[manifestRefKey("app", "attestation")] = mustSubjectManifestJSON(t, subject)
	manifests.manifests[manifestRefKey("app", subject)] = mustManifestJSON(t, configDigest, layerDigest)

	blobs := &fakeBlobStorage{
		blobs: []string{subject, configDigest, layerDigest},
		blobModTimes: map[string]time.Time{
			subject: now.Add(-72 * time.Hour), configDigest: now.Add(-72 * time.Hour), layerDigest: now.Add(-72 * time.Hour),
		},
	}
	svc, _ := newPruneService(t, manifests, blobs, &fakeProtectionStore{snapshot: &domain.PruneProtectionSnapshot{}}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true})
	require.NoError(t, err)

	assert.Empty(t, blobs.deletedBlobs)
	for _, digest := range []string{subject, configDigest, layerDigest} {
		assert.Contains(t, candidateRefs(report.Plan.CandidatesOfKind(domain.PruneResourceOCIBlob, domain.PruneVerdictProtected)), digest)
	}
}

func TestPrune_MissingSubjectFailsClosed(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	orphan := testDigest("orphan")
	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"attestation"}
	manifests.modTimes[manifestRefKey("app", "attestation")] = now
	manifests.manifests[manifestRefKey("app", "attestation")] = mustSubjectManifestJSON(t, testDigest("missing-subject"))
	blobs := &fakeBlobStorage{blobs: []string{orphan}, blobModTimes: map[string]time.Time{orphan: now.Add(-72 * time.Hour)}}

	svc, _ := newPruneService(t, manifests, blobs, &fakeProtectionStore{snapshot: &domain.PruneProtectionSnapshot{}}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true})
	require.NoError(t, err)

	assert.Empty(t, blobs.deletedBlobs)
	assert.Contains(t, candidateRefs(report.Plan.CandidatesOfKind(domain.PruneResourceOCIBlob, domain.PruneVerdictUnknown)), orphan)
}

func TestPrune_SubjectCycleTerminatesAndProtectsClosure(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	a := testDigest("subject-a")
	b := testDigest("subject-b")
	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"attestation"}
	manifests.modTimes[manifestRefKey("app", "attestation")] = now
	manifests.manifests[manifestRefKey("app", "attestation")] = mustSubjectManifestJSON(t, a)
	manifests.manifests[manifestRefKey("app", a)] = mustSubjectManifestJSON(t, b)
	manifests.manifests[manifestRefKey("app", b)] = mustSubjectManifestJSON(t, a)
	blobs := &fakeBlobStorage{blobs: []string{a, b}, blobModTimes: map[string]time.Time{a: now.Add(-72 * time.Hour), b: now.Add(-72 * time.Hour)}}

	svc, _ := newPruneService(t, manifests, blobs, &fakeProtectionStore{snapshot: &domain.PruneProtectionSnapshot{}}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true})
	require.NoError(t, err)

	assert.Empty(t, blobs.deletedBlobs)
	protected := candidateRefs(report.Plan.CandidatesOfKind(domain.PruneResourceOCIBlob, domain.PruneVerdictProtected))
	assert.Contains(t, protected, a)
	assert.Contains(t, protected, b)
}

func mustSubjectManifestJSON(t *testing.T, subject string) []byte {
	t.Helper()
	return []byte(`{"mediaType":"application/vnd.oci.image.manifest.v1+json","subject":{"digest":"` + subject + `"}}`)
}

func candidateRefs(candidates []domain.PruneCandidateReport) []string {
	refs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		refs = append(refs, candidate.Ref)
	}
	return refs
}

// TestPrune_ExternalRepositoryDigestRootDoesNotDisablePruning proves a
// durable digest root whose repository has no local content does not make
// the registry closure incomplete: external images never introduce local
// content, so unrelated blobs stay eligible.
func TestPrune_ExternalRepositoryDigestRootDoesNotDisablePruning(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	orphan := testDigest("orphan")

	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest"}
	manifests.modTimes[manifestRefKey("app", "latest")] = now
	manifests.manifests[manifestRefKey("app", "latest")] = mustManifestJSON(t, testDigest("latest-config"))

	blobs := &fakeBlobStorage{
		blobs:        []string{orphan},
		blobModTimes: map[string]time.Time{orphan: now.Add(-72 * time.Hour)},
	}
	protection := &fakeProtectionStore{snapshot: &domain.PruneProtectionSnapshot{
		Roots: []domain.ProtectionRoot{{
			Kind: domain.ProtectionActiveService, Ref: testDigest("external-manifest"),
			Repository: "docker.io/library/nginx", Owner: "app@active.web",
		}},
	}}

	svc, _ := newPruneService(t, manifests, blobs, protection, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true})
	require.NoError(t, err)

	assert.Equal(t, []string{orphan}, blobs.deletedBlobs, "an external root must not block local prune")
	assert.Zero(t, report.Plan.CountByVerdict(domain.PruneVerdictUnknown))
}

// TestPrune_UnresolvedDigestRootFailsClosed proves a durable digest root
// whose manifest cannot be read makes the registry closure incomplete, so
// unrelated blobs stay unknown instead of being deleted.
func TestPrune_UnresolvedDigestRootFailsClosed(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	missing := testDigest("missing-manifest")
	orphan := testDigest("orphan")

	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest"}
	manifests.modTimes[manifestRefKey("app", "latest")] = now
	manifests.manifests[manifestRefKey("app", "latest")] = mustManifestJSON(t, testDigest("latest-config"))

	blobs := &fakeBlobStorage{
		blobs:        []string{orphan},
		blobModTimes: map[string]time.Time{orphan: now.Add(-72 * time.Hour)},
	}
	protection := &fakeProtectionStore{snapshot: &domain.PruneProtectionSnapshot{
		Roots: []domain.ProtectionRoot{{
			Kind: domain.ProtectionActiveService, Ref: missing, Repository: "app", Owner: "app@active.web",
		}},
	}}

	svc, _ := newPruneService(t, manifests, blobs, protection, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true})
	require.NoError(t, err)

	assert.Empty(t, blobs.deletedBlobs, "an unresolved durable root must not authorize deletion")
	unknown := report.Plan.CandidatesOfKind(domain.PruneResourceOCIBlob, domain.PruneVerdictUnknown)
	require.Len(t, unknown, 1)
	assert.Equal(t, orphan, unknown[0].Ref)
}

// TestPrune_BlobClaimedByDigestRootSurvives proves a digest pinned
// directly by durable state (not reachable through any retained tag)
// is protected.
func TestPrune_BlobClaimedByDigestRootSurvives(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	pinned := testDigest("pinned-by-operation")

	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest", "v1"}
	for index, tag := range []string{"latest", "v1"} {
		manifests.modTimes[manifestRefKey("app", tag)] = now.Add(time.Duration(index) * time.Hour)
		manifests.manifests[manifestRefKey("app", tag)] = mustManifestJSON(t, testDigest(tag))
	}

	blobs := &fakeBlobStorage{
		blobs:        []string{pinned},
		blobModTimes: map[string]time.Time{pinned: now.Add(-72 * time.Hour)},
	}
	protection := &fakeProtectionStore{snapshot: &domain.PruneProtectionSnapshot{
		Roots: []domain.ProtectionRoot{
			{Kind: domain.ProtectionOperation, Ref: pinned, Owner: "app@op-1"},
		},
	}}

	svc, _ := newPruneService(t, manifests, blobs, protection, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true})
	require.NoError(t, err)

	assert.Empty(t, blobs.deletedBlobs, "a digest pinned by durable state must survive")
	protected := report.Plan.CandidatesOfKind(domain.PruneResourceOCIBlob, domain.PruneVerdictProtected)
	require.Len(t, protected, 1)
	assert.Equal(t, []domain.PruneReason{domain.PruneReasonProtectedOperation}, protected[0].Reasons)
}

// TestPrune_BlobWithoutModTimeStaysUnknown proves an unreadable blob age
// fails closed instead of falling through to eligibility: a blob that
// cannot be aged cannot be proven past the upload TTL.
func TestPrune_BlobWithoutModTimeStaysUnknown(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	unknownAge := testDigest("unknown-age")

	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest"}
	manifests.modTimes[manifestRefKey("app", "latest")] = now
	manifests.manifests[manifestRefKey("app", "latest")] = mustManifestJSON(t, testDigest("cfg"))

	blobs := &fakeBlobStorage{blobs: []string{unknownAge}, blobModTimeErr: errors.New("stat failed")}
	svc, _ := newPruneService(t, manifests, blobs, &fakeProtectionStore{}, &fakePruneRuntime{})

	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true})
	require.NoError(t, err)
	assert.Empty(t, blobs.deletedBlobs)
	unknown := report.Plan.CandidatesOfKind(domain.PruneResourceOCIBlob, domain.PruneVerdictUnknown)
	require.Len(t, unknown, 1)
	assert.Equal(t, []domain.PruneReason{domain.PruneReasonUnknownInventory}, unknown[0].Reasons)
}

// TestPrune_MalformedTagFailsClosed proves a tag whose identity cannot
// be established makes the registry plan incomplete rather than being
// skipped: skipping it would hide its content from the closure.
func TestPrune_MalformedTagFailsClosed(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	orphan := testDigest("orphan")

	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest", " "}
	manifests.modTimes[manifestRefKey("app", "latest")] = now
	manifests.manifests[manifestRefKey("app", "latest")] = mustManifestJSON(t, testDigest("cfg"))

	blobs := &fakeBlobStorage{
		blobs:        []string{orphan},
		blobModTimes: map[string]time.Time{orphan: now.Add(-72 * time.Hour)},
	}
	svc, _ := newPruneService(t, manifests, blobs, &fakeProtectionStore{}, &fakePruneRuntime{})

	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true})
	require.NoError(t, err)
	assert.Empty(t, blobs.deletedBlobs)
	assert.NotEmpty(t, report.Plan.Gaps)
	assert.Zero(t, report.Plan.CountByVerdict(domain.PruneVerdictEligible))
}

// TestPrune_SecondExecutionIsIdempotent proves an executed plan is
// complete: re-running it finds nothing left to delete, and the second
// report carries no eligible candidate.
func TestPrune_SecondExecutionIsIdempotent(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest", "v1", "v2", "v3"}
	for index, tag := range []string{"latest", "v1", "v2", "v3"} {
		manifests.modTimes[manifestRefKey("app", tag)] = now.Add(time.Duration(index-4) * time.Hour)
		manifests.manifests[manifestRefKey("app", tag)] = mustManifestJSON(t, testDigest(tag), testDigest(tag+"-layer"))
	}

	orphan := testDigest("orphan")
	blobs := &fakeBlobStorage{
		blobs:        []string{orphan},
		blobModTimes: map[string]time.Time{orphan: now.Add(-72 * time.Hour)},
	}
	runtime := &fakePruneRuntime{inventory: &domain.RuntimeInventory{
		Images: []domain.RuntimeImage{{ID: "sha256:dangling", Labels: map[string]string{domain.LabelApp: "app"}}},
	}}

	svc, _ := newPruneService(t, manifests, blobs, releasedImageSnapshot("sha256:dangling"), runtime)
	opts := domain.ImagePruneOptions{KeepLast: 1, PruneDangling: true, PruneRegistry: true}

	first, err := svc.Prune(context.Background(), opts)
	require.NoError(t, err)
	require.NotEmpty(t, first.Plan.Deleted, "the first run must delete something to be worth repeating")
	firstDeleted := len(first.Plan.Deleted)
	firstTags := len(manifests.deletedManifests)
	firstImages := len(runtime.removedImages)
	firstBlobs := len(blobs.deletedBlobs)
	require.NotZero(t, firstTags+firstImages+firstBlobs)

	second, err := svc.Prune(context.Background(), opts)
	require.NoError(t, err)
	assert.Empty(t, second.Plan.Deleted, "a second run must delete nothing")
	assert.Zero(t, second.Plan.CountByVerdict(domain.PruneVerdictEligible), "nothing may remain eligible")
	assert.Zero(t, second.Plan.CountByVerdict(domain.PruneVerdictUnknown), "a completed plan leaves no unknowns behind")
	assert.Empty(t, second.Plan.Failures)
	assert.Equal(t, firstTags, len(manifests.deletedManifests))
	assert.Equal(t, firstImages, len(runtime.removedImages))
	assert.Equal(t, firstBlobs, len(blobs.deletedBlobs))
	assert.Len(t, first.Plan.Deleted, firstDeleted)
}

// TestPrune_RuntimeDeletionRunsOutsideRegistryLock proves the registry
// mutation lock is not held while runtime images are deleted, so a slow
// runtime prune cannot stall registry pushes.
func TestPrune_RuntimeDeletionRunsOutsideRegistryLock(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest", "v1"}
	manifests.modTimes[manifestRefKey("app", "latest")] = now
	manifests.modTimes[manifestRefKey("app", "v1")] = now.Add(-time.Hour)
	manifests.manifests[manifestRefKey("app", "latest")] = mustManifestJSON(t, testDigest("l"))
	manifests.manifests[manifestRefKey("app", "v1")] = mustManifestJSON(t, testDigest("one"))

	// The runtime port reports whether the registry lock was held when
	// the deletion ran.
	runtime := &fakePruneRuntime{
		inventory: &domain.RuntimeInventory{
			Images: []domain.RuntimeImage{{ID: "sha256:dangling", Labels: map[string]string{domain.LabelApp: "app"}}},
		},
	}
	svc, _ := newPruneService(t, manifests, &fakeBlobStorage{}, releasedImageSnapshot("sha256:dangling"), runtime)

	unlocked := false
	runtime.onRemoveImage = func() {
		unlocked = svc.mutationMu.TryLock()
		if unlocked {
			svc.mutationMu.Unlock()
		}
	}

	_, err := svc.Prune(context.Background(), domain.ImagePruneOptions{
		KeepLast: 1, PruneDangling: true, PruneRegistry: true,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sha256:dangling"}, runtime.removedImages)
	assert.True(t, unlocked, "runtime deletion must not run under the registry mutation lock")
}
