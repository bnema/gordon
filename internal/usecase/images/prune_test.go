package images

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"bnema.dev/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// digestOf returns the sha256 digest of a manifest body, matching the
// identity the service derives during inventory.
func manifestBodyDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func testDigest(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// fakeProtectionStore returns one fixed snapshot.
type fakeProtectionStore struct {
	snapshot *domain.PruneProtectionSnapshot
	err      error
	calls    int
}

func (f *fakeProtectionStore) ProtectionSnapshot(context.Context) (*domain.PruneProtectionSnapshot, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.snapshot == nil {
		return &domain.PruneProtectionSnapshot{}, nil
	}
	return f.snapshot, nil
}

// fakePruneRuntime records every exact deletion request.
type fakePruneRuntime struct {
	inventory    *domain.RuntimeInventory
	inventoryErr error

	removedImages  []string
	removeImageErr error

	removedVolumes  []string
	removeVolumeErr error

	// onRemoveImage, when set, runs before a runtime image deletion so a
	// test can observe the state it executes under.
	onRemoveImage func()
}

func (f *fakePruneRuntime) InventoryRuntime(context.Context) (*domain.RuntimeInventory, error) {
	if f.inventoryErr != nil {
		return nil, f.inventoryErr
	}
	if f.inventory == nil {
		return &domain.RuntimeInventory{}, nil
	}
	return f.inventory, nil
}

func (f *fakePruneRuntime) RemoveImageExact(_ context.Context, ref domain.RuntimeImageRef) error {
	if f.onRemoveImage != nil {
		f.onRemoveImage()
	}
	if f.removeImageErr != nil {
		return f.removeImageErr
	}
	f.removedImages = append(f.removedImages, ref.ID)
	// A real adapter no longer reports a removed image, so the double
	// must not either: otherwise a second run would look idempotent
	// only because the double forgets nothing.
	if f.inventory != nil {
		kept := f.inventory.Images[:0]
		for _, image := range f.inventory.Images {
			if image.ID != ref.ID {
				kept = append(kept, image)
			}
		}
		f.inventory.Images = kept
	}
	return nil
}

func (f *fakePruneRuntime) RemoveVolumeExact(_ context.Context, ref domain.RuntimeVolumeRef) error {
	if f.removeVolumeErr != nil {
		return f.removeVolumeErr
	}
	f.removedVolumes = append(f.removedVolumes, ref.Name)
	return nil
}

// fakeBarrier records the exclusive lease lifecycle.
type fakeBarrier struct {
	mu       sync.Mutex
	acquires int
	releases int
	err      error
}

func (b *fakeBarrier) AcquireShared(context.Context) (out.GCLease, error) {
	return noopLease{}, nil
}

func (b *fakeBarrier) AcquireExclusive(context.Context) (out.GCLease, error) {
	if b.err != nil {
		return nil, b.err
	}
	b.mu.Lock()
	b.acquires++
	b.mu.Unlock()
	return &recordingLease{barrier: b}, nil
}

type recordingLease struct {
	barrier *fakeBarrier
	once    sync.Once
}

func (l *recordingLease) Release() {
	l.once.Do(func() {
		l.barrier.mu.Lock()
		l.barrier.releases++
		l.barrier.mu.Unlock()
	})
}

// noopLease is a lease used by the barrier under test.
type noopLease struct{}

func (noopLease) Release() {}

func newPruneService(t *testing.T, manifests *fakeManifestStorage, blobs *fakeBlobStorage, protection *fakeProtectionStore, runtime *fakePruneRuntime) (*Service, *fakeBarrier) {
	t.Helper()
	barrier := &fakeBarrier{}
	svc := NewService(&fakeRuntime{}, manifests, blobs, zerowrap.Default()).
		WithPrunePorts(protection, runtime, barrier)
	return svc, barrier
}

// TestPrune_SelectiveMixedBatch is the core acceptance case: app state
// exists, and a mixed batch of safe, protected, and unknown candidates
// produces exactly the safe deletions with no operation-wide refusal.
func TestPrune_SelectiveMixedBatch(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	oldTag := mustManifestJSON(t, testDigest("a"), testDigest("b"))
	latestTag := mustManifestJSON(t, testDigest("c"), testDigest("d"))

	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest", "v1", "v2", "v3", "v4", "v5"}
	for index, tag := range []string{"latest", "v1", "v2", "v3", "v4", "v5"} {
		manifests.modTimes[manifestRefKey("app", tag)] = now.Add(time.Duration(index-6) * time.Hour)
	}
	manifests.manifests[manifestRefKey("app", "latest")] = latestTag
	manifests.manifests[manifestRefKey("app", "v1")] = oldTag
	for _, tag := range []string{"v2", "v3", "v4", "v5"} {
		manifests.manifests[manifestRefKey("app", tag)] = mustManifestJSON(t, testDigest(tag), testDigest(tag+"-layer"))
	}

	blobs := &fakeBlobStorage{
		blobs:        []string{testDigest("b"), testDigest("d"), testDigest("orphan")},
		blobSizes:    map[string]int64{testDigest("orphan"): 4096},
		blobModTimes: map[string]time.Time{testDigest("orphan"): now.Add(-48 * time.Hour)},
	}

	activeImage := "sha256:active"
	danglingImage := "sha256:dangling"
	foreignImage := "sha256:foreign"
	protection := &fakeProtectionStore{snapshot: &domain.PruneProtectionSnapshot{
		Roots: []domain.ProtectionRoot{
			{Kind: domain.ProtectionActiveService, Ref: mustTagKey(t, "app", "v1"), Owner: "app@active.web"},
		},
		ImageClaims: []domain.ImageClaim{releasedImageClaim(danglingImage)},
	}}
	runtime := &fakePruneRuntime{inventory: &domain.RuntimeInventory{
		Images: []domain.RuntimeImage{
			{ID: activeImage, RepoTags: []string{"app:latest"}, Labels: map[string]string{domain.LabelApp: "app"}},
			{ID: danglingImage, Labels: map[string]string{domain.LabelApp: "app"}},
			{ID: foreignImage},
		},
		Containers: []domain.RuntimeContainerUse{
			{ContainerID: "c1", ImageID: activeImage, Running: true},
		},
	}}

	svc, barrier := newPruneService(t, manifests, blobs, protection, runtime)
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{
		KeepLast: 3, PruneDangling: true, PruneRegistry: true,
	})
	require.NoError(t, err)

	// latest + 3 = latest, v5, v4, v3 retained; v2 and v1 pruned
	// only if unprotected. v1 is protected by the active app state.
	assert.Equal(t, []manifestRef{{name: "app", reference: "v2"}}, manifests.deletedManifests)

	// The dangling app image is deleted; the foreign and in-use ones
	// are not.
	assert.Equal(t, []string{danglingImage}, runtime.removedImages)

	// The orphan blob is deleted; the blobs reachable from the retained
	// and protected manifests are not.
	assert.Equal(t, []string{testDigest("orphan")}, blobs.deletedBlobs)
	assert.Equal(t, int64(4096), report.Plan.ReclaimedBytes)
	assert.True(t, report.Plan.ReclaimedKnown)
	assert.True(t, report.Plan.Applied)
	assert.Empty(t, report.Plan.Failures)

	assert.GreaterOrEqual(t, report.Plan.CountByVerdict(domain.PruneVerdictProtected), 1)
	assert.GreaterOrEqual(t, report.Plan.CountByVerdict(domain.PruneVerdictEligible), 1)
	assert.Equal(t, 1, barrier.acquires)
	assert.Equal(t, 1, barrier.releases)
	assert.Equal(t, 1, protection.calls)
}

func TestPrune_DryRunPlansWithoutDeleting(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest", "v1", "v2"}
	for index, tag := range []string{"latest", "v1", "v2"} {
		manifests.modTimes[manifestRefKey("app", tag)] = now.Add(time.Duration(index-3) * time.Hour)
		manifests.manifests[manifestRefKey("app", tag)] = mustManifestJSON(t, testDigest(tag), testDigest(tag+"-l"))
	}
	blobs := &fakeBlobStorage{blobs: []string{testDigest("x")}, blobModTimes: map[string]time.Time{testDigest("x"): now.Add(-72 * time.Hour)}}

	svc, _ := newPruneService(t, manifests, blobs, &fakeProtectionStore{}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{
		KeepLast: 1, PruneDangling: true, PruneRegistry: true, DryRun: true,
	})
	require.NoError(t, err)

	assert.False(t, report.Plan.Applied)
	assert.Empty(t, manifests.deletedManifests)
	assert.Empty(t, blobs.deletedBlobs)
	assert.NotEmpty(t, report.Plan.Candidates)

	// The dry-run plan must equal the executed plan.
	executed, err := svc.Prune(context.Background(), domain.ImagePruneOptions{
		KeepLast: 1, PruneDangling: true, PruneRegistry: true,
	})
	require.NoError(t, err)
	assert.Equal(t, report.Plan.Candidates, executed.Plan.Candidates)
	assert.True(t, executed.Plan.Applied)
}

func TestPrune_ZeroEligibleSucceeds(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest", "v1"}
	for index, tag := range []string{"latest", "v1"} {
		manifests.modTimes[manifestRefKey("app", tag)] = now.Add(time.Duration(index) * time.Hour)
		manifests.manifests[manifestRefKey("app", tag)] = mustManifestJSON(t, testDigest(tag))
	}
	blobs := &fakeBlobStorage{blobs: []string{}}
	runtime := &fakePruneRuntime{inventory: &domain.RuntimeInventory{}}

	svc, _ := newPruneService(t, manifests, blobs, &fakeProtectionStore{}, runtime)
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{
		KeepLast: 3, PruneDangling: true, PruneRegistry: true,
	})
	require.NoError(t, err, "a zero-deletion prune must succeed, not fail the operation")
	assert.Zero(t, report.Registry.TagsRemoved)
	assert.Zero(t, report.Registry.BlobsRemoved)
	assert.Zero(t, report.Runtime.DeletedCount)
	assert.Equal(t, 0, report.Plan.CountByVerdict(domain.PruneVerdictEligible))
}

func TestPrune_MissingChildManifestMakesUnreferencedBlobsUnknown(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	childDigest := testDigest("missing-child")
	indexBody := mustManifestIndexJSON(t, childDigest)

	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest", "v1", "v2"}
	manifests.modTimes[manifestRefKey("app", "latest")] = now
	manifests.modTimes[manifestRefKey("app", "v1")] = now.Add(-time.Hour)
	manifests.modTimes[manifestRefKey("app", "v2")] = now.Add(-2 * time.Hour)
	// latest is a multi-arch index whose child manifest is absent.
	manifests.manifests[manifestRefKey("app", "latest")] = indexBody
	manifests.manifests[manifestRefKey("app", "v1")] = mustManifestJSON(t, testDigest("c1"))
	manifests.manifests[manifestRefKey("app", "v2")] = mustManifestJSON(t, testDigest("c2"))

	orphan := testDigest("orphan")
	blobs := &fakeBlobStorage{
		blobs:        []string{orphan},
		blobModTimes: map[string]time.Time{orphan: now.Add(-72 * time.Hour)},
	}

	svc, _ := newPruneService(t, manifests, blobs, &fakeProtectionStore{}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{
		KeepLast: 1, PruneRegistry: true,
	})
	require.NoError(t, err)

	// The unreadable child makes the retained closure incomplete, so an
	// otherwise-unreferenced blob is unknown, not eligible.
	assert.Empty(t, blobs.deletedBlobs, "an incomplete closure must never authorize blob deletion")
	assert.Contains(t, manifestBodyDigest(indexBody), "sha256:")
	var unknown bool
	for _, candidate := range report.Plan.Candidates {
		if candidate.Kind == domain.PruneResourceOCIBlob && candidate.Verdict == domain.PruneVerdictUnknown {
			unknown = true
			assert.Equal(t, []domain.PruneReason{domain.PruneReasonUnknownManifest}, candidate.Reasons)
		}
	}
	assert.True(t, unknown, "the orphan blob must be reported unknown")
	require.NotEmpty(t, report.Plan.Gaps)
	var notFoundDetail bool
	for _, gap := range report.Plan.Gaps {
		if strings.HasPrefix(gap.Detail, "retained manifest not found for app@") {
			notFoundDetail = true
		}
	}
	assert.True(t, notFoundDetail, "the gap must name the repository, digest, and not-found cause: %+v", report.Plan.Gaps)

	// Tags outside the window are still deleted: the failure is scoped
	// to the content whose safety depended on the missing manifest.
	assert.Len(t, manifests.deletedManifests, 1)
	assert.Equal(t, "v2", manifests.deletedManifests[0].reference)
}

func TestPrune_SharedBlobStaysProtectedThroughClosure(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	sharedLayer := testDigest("shared-layer")

	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest", "v1", "v2"}
	manifests.modTimes[manifestRefKey("app", "latest")] = now
	manifests.modTimes[manifestRefKey("app", "v1")] = now.Add(-time.Hour)
	manifests.modTimes[manifestRefKey("app", "v2")] = now.Add(-2 * time.Hour)
	manifests.manifests[manifestRefKey("app", "latest")] = mustManifestJSON(t, testDigest("cfg-latest"), sharedLayer)
	manifests.manifests[manifestRefKey("app", "v1")] = mustManifestJSON(t, testDigest("cfg-v1"), sharedLayer)
	manifests.manifests[manifestRefKey("app", "v2")] = mustManifestJSON(t, testDigest("cfg-v2"))

	blobs := &fakeBlobStorage{blobs: []string{sharedLayer}}
	svc, _ := newPruneService(t, manifests, blobs, &fakeProtectionStore{}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true})
	require.NoError(t, err)

	assert.Empty(t, blobs.deletedBlobs, "a layer shared with a retained tag must survive")
	protected := report.Plan.CandidatesOfKind(domain.PruneResourceOCIBlob, domain.PruneVerdictProtected)
	require.Len(t, protected, 1)
	assert.Equal(t, []domain.PruneReason{domain.PruneReasonProtectedSharedContent}, protected[0].Reasons)
}

func TestPrune_RegistryDeletionFailureIsIsolated(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest", "v1", "v2"}
	for index, tag := range []string{"latest", "v1", "v2"} {
		manifests.modTimes[manifestRefKey("app", tag)] = now.Add(time.Duration(index-3) * time.Hour)
		manifests.manifests[manifestRefKey("app", tag)] = mustManifestJSON(t, testDigest(tag))
	}
	manifests.deleteErr = errors.New("storage unavailable")

	blobs := &fakeBlobStorage{}
	svc, _ := newPruneService(t, manifests, blobs, &fakeProtectionStore{}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true})
	require.NoError(t, err, "one deletion failure must not fail the run")

	require.Len(t, report.Plan.Failures, 1)
	assert.Equal(t, domain.PruneResourceRegistryTag, report.Plan.Failures[0].Kind)
	assert.Zero(t, report.Registry.TagsRemoved)
	assert.Empty(t, report.Plan.Deleted)
}

func TestPrune_RuntimeDeletionFailureIsIsolated(t *testing.T) {
	image := "sha256:dangling"
	runtime := &fakePruneRuntime{
		inventory: &domain.RuntimeInventory{
			Images: []domain.RuntimeImage{{ID: image, Labels: map[string]string{domain.LabelApp: "app"}}},
		},
		removeImageErr: errors.New("image is in use"),
	}
	svc, _ := newPruneService(t, newFakeManifestStorage(), &fakeBlobStorage{}, releasedImageSnapshot(image), runtime)

	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{PruneDangling: true})
	require.NoError(t, err)
	require.Len(t, report.Plan.Failures, 1)
	assert.Equal(t, domain.PruneResourceRuntimeImage, report.Plan.Failures[0].Kind)
	assert.Zero(t, report.Runtime.DeletedCount)
}

func TestPrune_KeepLastZeroSkipsRegistry(t *testing.T) {
	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"v1"}
	svc, _ := newPruneService(t, manifests, &fakeBlobStorage{blobs: []string{testDigest("x")}}, &fakeProtectionStore{}, &fakePruneRuntime{})

	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 0, PruneRegistry: true})
	require.NoError(t, err)
	assert.Empty(t, manifests.deletedManifests)
	assert.Empty(t, report.Plan.Candidates)
}

func TestPrune_RegistryOnlySkipsRuntime(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest", "v1", "v2"}
	for index, tag := range []string{"latest", "v1", "v2"} {
		manifests.modTimes[manifestRefKey("app", tag)] = now.Add(time.Duration(index-3) * time.Hour)
		manifests.manifests[manifestRefKey("app", tag)] = mustManifestJSON(t, testDigest(tag))
	}
	runtime := &fakePruneRuntime{
		inventory: &domain.RuntimeInventory{
			Images: []domain.RuntimeImage{{ID: "sha256:dangling", Labels: map[string]string{domain.LabelApp: "app"}}},
		},
	}
	svc, _ := newPruneService(t, manifests, &fakeBlobStorage{}, &fakeProtectionStore{}, runtime)
	_, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true})
	require.NoError(t, err)
	assert.Empty(t, runtime.removedImages, "registry-only prune must not touch runtime images")
}

func TestPrune_RequiresProtectionStore(t *testing.T) {
	svc := NewService(&fakeRuntime{}, newFakeManifestStorage(), &fakeBlobStorage{}, zerowrap.Default())
	_, err := svc.Prune(context.Background(), domain.DefaultImagePruneOptions())
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrPruneDisabled)
}

func TestPrune_ProtectionSnapshotFailureFailsClosed(t *testing.T) {
	protection := &fakeProtectionStore{err: errors.New("unreadable state")}
	svc, _ := newPruneService(t, newFakeManifestStorage(), &fakeBlobStorage{}, protection, &fakePruneRuntime{})
	_, err := svc.Prune(context.Background(), domain.DefaultImagePruneOptions())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "protection snapshot")
}

func TestPrune_RuntimeInventoryGapMakesCandidatesUnknown(t *testing.T) {
	runtime := &fakePruneRuntime{inventory: &domain.RuntimeInventory{
		Images: []domain.RuntimeImage{{ID: "sha256:dangling", Labels: map[string]string{domain.LabelApp: "app"}}},
		Gaps: []domain.InventoryGap{{
			Source: domain.InventorySourceRuntimeContainers,
			Reason: domain.PruneReasonUnknownContainerUse,
			Detail: "list containers failed",
		}},
	}}
	svc, _ := newPruneService(t, newFakeManifestStorage(), &fakeBlobStorage{}, &fakeProtectionStore{}, runtime)
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{PruneDangling: true})
	require.NoError(t, err)
	assert.Empty(t, runtime.removedImages)
	assert.Equal(t, 1, report.Plan.CountByVerdict(domain.PruneVerdictUnknown))
	require.Len(t, report.Plan.Gaps, 1)
}

// releasedImageClaim marks identity as durably released by an app. Runtime
// image deletion requires this positive ownership; labels alone never
// authorize it.
func releasedImageClaim(identity string) domain.ImageClaim {
	return domain.ImageClaim{
		Reference: identity, App: "app", AppID: "app-uuid",
		State: domain.VolumeClaimReleased,
	}
}

// releasedImageSnapshot is a complete snapshot containing one released
// runtime image claim.
func releasedImageSnapshot(identity string) *fakeProtectionStore {
	return &fakeProtectionStore{snapshot: &domain.PruneProtectionSnapshot{
		ImageClaims: []domain.ImageClaim{releasedImageClaim(identity)},
	}}
}

func mustTagKey(t *testing.T, repository, tag string) string {
	t.Helper()
	ref, err := domain.NewRegistryTagRef(repository, tag)
	require.NoError(t, err)
	return ref.Key()
}

func TestManifestReadFailureDetail(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "not found",
			err:  fmt.Errorf("%w: app/v1", domain.ErrManifestNotFound),
			want: "manifest not found for app:v1",
		},
		{
			name: "path error drops the host path",
			err:  fmt.Errorf("failed to read manifest: %w", &fs.PathError{Op: "open", Path: "/srv/gordon/registry/app/v1", Err: syscall.EACCES}),
			want: "manifest unreadable for app:v1: open: permission denied",
		},
		{
			name: "other read error",
			err:  errors.New("permission denied"),
			want: "manifest unreadable for app:v1: permission denied",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, manifestReadFailureDetail("manifest", "app:v1", tt.err))
		})
	}
}

// TestPrune_StaleTagYieldsOneNotFoundGap proves a tag whose manifest is
// gone reports a not-found gap and no separate modtime gap, and that the
// gap keeps the tag and every stored blob from being deleted.
func TestPrune_StaleTagYieldsOneNotFoundGap(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest", "stale", "v2"}
	manifests.manifests[manifestRefKey("app", "latest")] = []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"` + testDigest("cfg") + `"},"layers":[]}`)
	manifests.manifests[manifestRefKey("app", "v2")] = manifests.manifests[manifestRefKey("app", "latest")]
	manifests.modTimes[manifestRefKey("app", "latest")] = now
	manifests.modTimes[manifestRefKey("app", "v2")] = now.Add(-time.Hour)
	manifests.modTimeErrs = map[string]error{manifestRefKey("app", "stale"): fmt.Errorf("%w: app/stale", domain.ErrManifestNotFound)}
	orphan := testDigest("orphan")
	blobs := &fakeBlobStorage{
		blobs:        []string{orphan},
		blobModTimes: map[string]time.Time{orphan: now.Add(-72 * time.Hour)},
	}

	svc, _ := newPruneService(t, manifests, blobs, &fakeProtectionStore{}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true})
	require.NoError(t, err)

	require.NotEmpty(t, report.Plan.Gaps)
	var notFound bool
	for _, gap := range report.Plan.Gaps {
		notFound = notFound || gap.Detail == "manifest not found for app:stale"
	}
	assert.True(t, notFound, "gaps: %+v", report.Plan.Gaps)
	for _, gap := range report.Plan.Gaps {
		assert.NotContains(t, gap.Detail, "modification time", "a missing manifest must not add a modtime gap")
	}
	assertNothingEligible(t, report)
	assert.Empty(t, manifests.deletedManifests)
	assert.Empty(t, blobs.deletedBlobs)
}

// TestPrune_ModTimeNotFoundWithReadableManifestFailsClosed proves a modtime
// failure is a gap when the manifest itself is readable.
func TestPrune_ModTimeNotFoundWithReadableManifestFailsClosed(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest", "v1", "v2"}
	manifests.manifests[manifestRefKey("app", "latest")] = []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"` + testDigest("cfg") + `"},"layers":[]}`)
	manifests.manifests[manifestRefKey("app", "v2")] = manifests.manifests[manifestRefKey("app", "latest")]
	manifests.modTimes[manifestRefKey("app", "latest")] = now
	manifests.modTimes[manifestRefKey("app", "v2")] = now.Add(-time.Hour)
	manifests.manifests[manifestRefKey("app", "v1")] = manifests.manifests[manifestRefKey("app", "latest")]
	manifests.modTimeErrs = map[string]error{manifestRefKey("app", "v1"): fmt.Errorf("%w: app/v1", domain.ErrManifestNotFound)}
	orphan := testDigest("orphan")
	blobs := &fakeBlobStorage{
		blobs:        []string{orphan},
		blobModTimes: map[string]time.Time{orphan: now.Add(-72 * time.Hour)},
	}

	svc, _ := newPruneService(t, manifests, blobs, &fakeProtectionStore{}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true})
	require.NoError(t, err)

	var modTimeGap bool
	for _, gap := range report.Plan.Gaps {
		if gap.Detail == "manifest modification time not found for app:v1" {
			modTimeGap = true
		}
	}
	assert.True(t, modTimeGap, "gaps: %+v", report.Plan.Gaps)
	assertNothingEligible(t, report)
	assert.Empty(t, manifests.deletedManifests)
	assert.Empty(t, blobs.deletedBlobs)
}

func assertNothingEligible(t *testing.T, report domain.ImagePruneReport) {
	t.Helper()
	var unknownTag bool
	for _, candidate := range report.Plan.Candidates {
		assert.NotEqual(t, domain.PruneVerdictEligible, candidate.Verdict, "%s %s", candidate.Kind, candidate.Ref)
		if candidate.Kind == domain.PruneResourceRegistryTag && candidate.Verdict == domain.PruneVerdictUnknown {
			unknownTag = true
		}
	}
	assert.True(t, unknownTag, "the affected tag must be unknown: %+v", report.Plan.Candidates)
}
