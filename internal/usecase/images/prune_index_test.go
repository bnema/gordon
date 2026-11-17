package images

import (
	"context"
	"encoding/json"
	"errors"
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

// legacyIndexFixture builds the production shape of issue #23: latest is
// an OCI index with amd64, arm64, and two attestation children, and the
// amd64 child manifest file is missing.
type legacyIndexFixture struct {
	manifests   *fakeManifestStorage
	blobs       *fakeBlobStorage
	missing     string
	indexBody   []byte
	kept        []string // blobs reachable through existing children
	orphan      string   // old blob reachable through nothing
	youngBlob   string   // recent blob reachable through nothing
	childByArch map[string]string
}

func newLegacyIndexFixture(t *testing.T, now time.Time) legacyIndexFixture {
	t.Helper()
	f := legacyIndexFixture{manifests: newFakeManifestStorage(), childByArch: map[string]string{}}
	f.manifests.repositories = []string{"app"}
	f.manifests.tagsByRepo["app"] = []string{"latest"}
	f.manifests.modTimes[manifestRefKey("app", "latest")] = now.Add(-30 * 24 * time.Hour)

	type platform struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	}
	type descriptor struct {
		MediaType   string            `json:"mediaType"`
		Digest      string            `json:"digest"`
		Platform    platform          `json:"platform"`
		Annotations map[string]string `json:"annotations,omitempty"`
	}
	entries := []struct {
		name string
		desc descriptor
	}{
		{"amd64", descriptor{Platform: platform{"linux", "amd64"}}},
		{"arm64", descriptor{Platform: platform{"linux", "arm64"}}},
		{"att-amd64", descriptor{Platform: platform{"unknown", "unknown"}, Annotations: map[string]string{"vnd.docker.reference.type": "attestation-manifest"}}},
		{"att-arm64", descriptor{Platform: platform{"unknown", "unknown"}, Annotations: map[string]string{"vnd.docker.reference.type": "attestation-manifest"}}},
	}
	descriptors := make([]descriptor, 0, len(entries))
	for _, entry := range entries {
		config := testDigest(entry.name + "-config")
		layer := testDigest(entry.name + "-layer")
		body := mustManifestJSON(t, config, layer)
		digest := manifestBodyDigest(body)
		f.childByArch[entry.name] = digest
		if entry.name == "amd64" {
			f.missing = digest
		} else {
			f.manifests.manifests[manifestRefKey("app", digest)] = body
			f.kept = append(f.kept, config, layer)
		}
		entry.desc.MediaType = "application/vnd.oci.image.manifest.v1+json"
		entry.desc.Digest = digest
		descriptors = append(descriptors, entry.desc)
	}
	body, err := json.Marshal(struct {
		SchemaVersion int          `json:"schemaVersion"`
		MediaType     string       `json:"mediaType"`
		Manifests     []descriptor `json:"manifests"`
	}{2, "application/vnd.oci.image.index.v1+json", descriptors})
	require.NoError(t, err)
	f.indexBody = body
	f.manifests.manifests[manifestRefKey("app", "latest")] = body

	f.orphan = testDigest("orphan")
	f.youngBlob = testDigest("young")
	modTimes := map[string]time.Time{f.orphan: now.Add(-72 * time.Hour), f.youngBlob: time.Now().Add(-time.Hour)}
	all := []string{f.orphan, f.youngBlob}
	for _, digest := range f.kept {
		modTimes[digest] = now.Add(-72 * time.Hour)
		all = append(all, digest)
	}
	f.blobs = &fakeBlobStorage{blobs: all, blobModTimes: modTimes}
	return f
}

func blobVerdicts(report domain.ImagePruneReport) map[string]domain.PruneVerdict {
	out := map[string]domain.PruneVerdict{}
	for _, candidate := range report.Plan.Candidates {
		if candidate.Kind == domain.PruneResourceOCIBlob {
			out[candidate.Ref] = candidate.Verdict
		}
	}
	return out
}

func TestPrune_MissingIndexChildIsKnownAbsent(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	f := newLegacyIndexFixture(t, now)

	svc, _ := newPruneService(t, f.manifests, f.blobs, &fakeProtectionStore{}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 3, PruneRegistry: true, DryRun: true})
	require.NoError(t, err)

	assert.Empty(t, report.Plan.Gaps, "a child proven absent by a readable index is not a gap")
	require.Len(t, report.Plan.Warnings, 1)
	warning := report.Plan.Warnings[0]
	assert.Contains(t, warning, "app@latest ("+manifestBodyDigest(f.indexBody)+")")
	assert.Contains(t, warning, f.missing+" (linux/amd64)")

	verdicts := blobVerdicts(report)
	for _, digest := range f.kept {
		assert.Equal(t, domain.PruneVerdictProtected, verdicts[digest], "blob %s of an existing child", digest)
	}
	assert.Equal(t, domain.PruneVerdictEligible, verdicts[f.orphan], "an old unreferenced blob becomes eligible")
	assert.Equal(t, domain.PruneVerdictProtected, verdicts[f.youngBlob], "a blob younger than the upload TTL stays protected")
}

func TestPrune_MissingIndexChildExecutesOnlyOrphan(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	f := newLegacyIndexFixture(t, now)

	svc, _ := newPruneService(t, f.manifests, f.blobs, &fakeProtectionStore{}, &fakePruneRuntime{})
	_, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 3, PruneRegistry: true})
	require.NoError(t, err)

	assert.Equal(t, []string{f.orphan}, f.blobs.deletedBlobs)
	assert.Empty(t, f.manifests.deletedManifests)
}

func TestPrune_IndexChildReadErrorStaysGap(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	f := newLegacyIndexFixture(t, now)
	f.manifests.getErrs = map[string]error{
		manifestRefKey("app", f.childByArch["arm64"]): errors.New("permission denied"),
	}

	svc, _ := newPruneService(t, f.manifests, f.blobs, &fakeProtectionStore{}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 3, PruneRegistry: true, DryRun: true})
	require.NoError(t, err)

	require.Len(t, report.Plan.Gaps, 1)
	assert.Contains(t, report.Plan.Gaps[0].Detail, "unreadable")
	assert.Equal(t, domain.PruneVerdictUnknown, blobVerdicts(report)[f.orphan])
}

func TestPrune_MissingSubjectOfIndexChildStaysGap(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	f := newLegacyIndexFixture(t, now)
	// The arm64 attestation names the missing amd64 manifest as subject.
	attestation := f.childByArch["att-arm64"]
	f.manifests.manifests[manifestRefKey("app", attestation)] = mustSubjectManifestJSON(t, f.missing)

	svc, _ := newPruneService(t, f.manifests, f.blobs, &fakeProtectionStore{}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 3, PruneRegistry: true, DryRun: true})
	require.NoError(t, err)

	require.NotEmpty(t, report.Plan.Gaps, "a missing subject is never proven absent")
	assert.Contains(t, report.Plan.Gaps[0].Detail, "retained manifest not found for app@"+f.missing)
	assert.Equal(t, domain.PruneVerdictUnknown, blobVerdicts(report)[f.orphan])
}

// TestPrune_DigestTagBehindUnreadableClosureNodeStaysUnknown proves a
// digest-named tag is never deleted while the retained closure is
// incomplete: an unreadable node could be hiding it as a child.
func TestPrune_DigestTagBehindUnreadableClosureNodeStaysUnknown(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}

	nested := testDigest("nested-index")
	tags := []string{"latest", "v1"}
	manifests.manifests[manifestRefKey("app", "latest")] = mustManifestIndexJSON(t, nested)
	manifests.modTimes[manifestRefKey("app", "latest")] = now
	manifests.manifests[manifestRefKey("app", "v1")] = mustManifestJSON(t, testDigest("v1-config"))
	manifests.modTimes[manifestRefKey("app", "v1")] = now.Add(-time.Hour)
	manifests.getErrs = map[string]error{manifestRefKey("app", nested): errors.New("permission denied")}
	for index, seed := range []string{"c1", "c2", "c3", "c4"} {
		body := mustManifestJSON(t, testDigest(seed+"-config"))
		child := manifestBodyDigest(body)
		manifests.manifests[manifestRefKey("app", child)] = body
		manifests.modTimes[manifestRefKey("app", child)] = now.Add(time.Duration(-10-index) * 24 * time.Hour)
		tags = append(tags, child)
	}
	manifests.tagsByRepo["app"] = tags

	svc, _ := newPruneService(t, manifests, &fakeBlobStorage{}, &fakeProtectionStore{}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true})
	require.NoError(t, err)

	assert.Empty(t, manifests.deletedManifests, "an incomplete closure must not authorize digest tag deletion")
	assert.Equal(t, 4, len(report.Plan.CandidatesOfKind(domain.PruneResourceRegistryTag, domain.PruneVerdictUnknown)))
}

// TestPrune_AbsentChildDoesNotProtectOtherRepositoryTag reproduces the
// cross-repository probe: an absent child of repo a must not make
// b:sha256:X protected while its blobs become eligible.
func TestPrune_AbsentChildDoesNotProtectOtherRepositoryTag(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cfg, layer := testDigest("b-config"), testDigest("b-layer")
	childBody := mustManifestJSON(t, cfg, layer)
	child := manifestBodyDigest(childBody)

	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"a", "b"}
	manifests.tagsByRepo["a"] = []string{"latest"}
	manifests.manifests[manifestRefKey("a", "latest")] = mustManifestIndexJSON(t, child)
	manifests.modTimes[manifestRefKey("a", "latest")] = now
	manifests.tagsByRepo["b"] = []string{"v1", child}
	manifests.manifests[manifestRefKey("b", "v1")] = mustManifestJSON(t, testDigest("v1-config"))
	manifests.modTimes[manifestRefKey("b", "v1")] = now
	manifests.manifests[manifestRefKey("b", child)] = childBody
	manifests.modTimes[manifestRefKey("b", child)] = now.Add(-30 * 24 * time.Hour)

	blobs := &fakeBlobStorage{
		blobs:        []string{cfg, layer},
		blobModTimes: map[string]time.Time{cfg: now.Add(-72 * time.Hour), layer: now.Add(-72 * time.Hour)},
	}
	svc, _ := newPruneService(t, manifests, blobs, &fakeProtectionStore{}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true, DryRun: true})
	require.NoError(t, err)

	verdicts := map[string]domain.PruneVerdict{}
	for _, candidate := range report.Plan.Candidates {
		verdicts[candidate.Ref] = candidate.Verdict
	}
	// The tag and its content share one fate: both go, never tag kept
	// and content deleted.
	assert.Equal(t, domain.PruneVerdictEligible, verdicts["b:"+child])
	assert.Equal(t, domain.PruneVerdictEligible, verdicts[cfg])
	assert.Equal(t, domain.PruneVerdictEligible, verdicts[layer])
	require.Len(t, report.Plan.Warnings, 1)
}

// TestPrune_ProtectedDigestTagIsTraversedToFixpoint proves a tag
// protected by closure membership keeps its own content: a legacy digest
// file whose body differs from the tag that named the digest is walked.
func TestPrune_ProtectedDigestTagIsTraversedToFixpoint(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	latestBody := mustManifestJSON(t, testDigest("latest-config"))
	mirror := manifestBodyDigest(latestBody)
	extra := testDigest("mirror-only-layer")

	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest", "v1", mirror}
	manifests.manifests[manifestRefKey("app", "latest")] = latestBody
	manifests.modTimes[manifestRefKey("app", "latest")] = now
	manifests.manifests[manifestRefKey("app", "v1")] = mustManifestJSON(t, testDigest("v1-config"))
	manifests.modTimes[manifestRefKey("app", "v1")] = now
	manifests.manifests[manifestRefKey("app", mirror)] = mustManifestJSON(t, testDigest("latest-config"), extra)
	manifests.modTimes[manifestRefKey("app", mirror)] = now.Add(-30 * 24 * time.Hour)

	blobs := &fakeBlobStorage{blobs: []string{extra}, blobModTimes: map[string]time.Time{extra: now.Add(-72 * time.Hour)}}
	svc, _ := newPruneService(t, manifests, blobs, &fakeProtectionStore{}, &fakePruneRuntime{})
	_, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true})
	require.NoError(t, err)

	assert.Empty(t, manifests.deletedManifests)
	assert.Empty(t, blobs.deletedBlobs, "content of a protected digest tag must survive")
}

func TestPrune_DockerManifestListAbsentChild(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cfg := testDigest("arm-config")
	present := mustManifestJSON(t, cfg)
	presentDigest := manifestBodyDigest(present)
	missing := testDigest("amd-manifest")
	list := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.list.v2+json","manifests":[` +
		`{"digest":"` + missing + `","platform":{"os":"linux","architecture":"amd64"}},` +
		`{"digest":"` + presentDigest + `","platform":{"os":"linux","architecture":"arm","variant":"v7"}}]}`)

	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest"}
	manifests.manifests[manifestRefKey("app", "latest")] = list
	manifests.modTimes[manifestRefKey("app", "latest")] = now
	manifests.manifests[manifestRefKey("app", presentDigest)] = present

	orphan := testDigest("orphan")
	blobs := &fakeBlobStorage{blobs: []string{cfg, orphan}, blobModTimes: map[string]time.Time{cfg: now.Add(-72 * time.Hour), orphan: now.Add(-72 * time.Hour)}}
	svc, _ := newPruneService(t, manifests, blobs, &fakeProtectionStore{}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true, DryRun: true})
	require.NoError(t, err)

	assert.Empty(t, report.Plan.Gaps)
	require.Len(t, report.Plan.Warnings, 1)
	assert.Contains(t, report.Plan.Warnings[0], missing+" (linux/amd64)")
	verdicts := blobVerdicts(report)
	assert.Equal(t, domain.PruneVerdictProtected, verdicts[cfg])
	assert.Equal(t, domain.PruneVerdictEligible, verdicts[orphan])
}

func TestPrune_NestedIndexAbsentGrandchild(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cfg := testDigest("present-config")
	present := mustManifestJSON(t, cfg)
	presentDigest := manifestBodyDigest(present)
	missing := testDigest("missing-grandchild")
	nested := mustManifestIndexJSON(t, presentDigest, missing)
	nestedDigest := manifestBodyDigest(nested)

	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest"}
	manifests.manifests[manifestRefKey("app", "latest")] = mustManifestIndexJSON(t, nestedDigest)
	manifests.modTimes[manifestRefKey("app", "latest")] = now
	manifests.manifests[manifestRefKey("app", nestedDigest)] = nested
	manifests.manifests[manifestRefKey("app", presentDigest)] = present

	blobs := &fakeBlobStorage{blobs: []string{cfg}, blobModTimes: map[string]time.Time{cfg: now.Add(-72 * time.Hour)}}
	svc, _ := newPruneService(t, manifests, blobs, &fakeProtectionStore{}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true, DryRun: true})
	require.NoError(t, err)

	assert.Empty(t, report.Plan.Gaps)
	require.Len(t, report.Plan.Warnings, 1)
	assert.Contains(t, report.Plan.Warnings[0], "index app@"+nestedDigest+" lists missing child manifest "+missing)
	assert.Equal(t, domain.PruneVerdictProtected, blobVerdicts(report)[cfg])
}

func TestPrune_EveryIndexListingAnAbsentChildIsReported(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	missing := testDigest("shared-missing")
	first := mustManifestIndexJSON(t, missing)
	second := []byte(`{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"` + missing + `"}]}`)

	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest", "v1"}
	manifests.manifests[manifestRefKey("app", "latest")] = first
	manifests.modTimes[manifestRefKey("app", "latest")] = now
	manifests.manifests[manifestRefKey("app", "v1")] = second
	manifests.modTimes[manifestRefKey("app", "v1")] = now

	svc, _ := newPruneService(t, manifests, &fakeBlobStorage{}, &fakeProtectionStore{}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true, DryRun: true})
	require.NoError(t, err)

	require.Len(t, report.Plan.Warnings, 2)
	assert.Contains(t, report.Plan.Warnings[0], manifestBodyDigest(first))
	assert.Contains(t, report.Plan.Warnings[1], manifestBodyDigest(second))
}

// TestPrune_NamedTagMembershipIsCaseSensitive proves only digests compare
// case-insensitively: Latest is a different tag from the retained latest.
func TestPrune_NamedTagMembershipIsCaseSensitive(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest", "Latest", "v1"}
	manifests.manifests[manifestRefKey("app", "latest")] = mustManifestJSON(t, testDigest("latest-config"))
	manifests.modTimes[manifestRefKey("app", "latest")] = now
	manifests.manifests[manifestRefKey("app", "v1")] = mustManifestJSON(t, testDigest("v1-config"))
	manifests.modTimes[manifestRefKey("app", "v1")] = now
	manifests.manifests[manifestRefKey("app", "Latest")] = mustManifestJSON(t, testDigest("old-config"))
	manifests.modTimes[manifestRefKey("app", "Latest")] = now.Add(-30 * 24 * time.Hour)

	svc, _ := newPruneService(t, manifests, &fakeBlobStorage{}, &fakeProtectionStore{}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true, DryRun: true})
	require.NoError(t, err)

	for _, candidate := range report.Plan.Candidates {
		if candidate.Ref == "app:Latest" {
			assert.Equal(t, domain.PruneVerdictEligible, candidate.Verdict)
			return
		}
	}
	t.Fatal("app:Latest candidate missing")
}

// TestPrune_FixpointGapMakesDigestTagsAndBlobsUnknown proves a gap found
// while walking a newly protected digest tag still fails closed: other
// old digest tags and unreferenced blobs become unknown.
func TestPrune_FixpointGapMakesDigestTagsAndBlobsUnknown(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	latestBody := mustManifestJSON(t, testDigest("latest-config"))
	mirror := manifestBodyDigest(latestBody)
	otherBody := mustManifestJSON(t, testDigest("other-config"))
	other := manifestBodyDigest(otherBody)
	old := now.Add(-30 * 24 * time.Hour)

	manifests := newFakeManifestStorage()
	manifests.repositories = []string{"app"}
	manifests.tagsByRepo["app"] = []string{"latest", "v1", mirror, other}
	manifests.manifests[manifestRefKey("app", "latest")] = latestBody
	manifests.modTimes[manifestRefKey("app", "latest")] = now
	manifests.manifests[manifestRefKey("app", "v1")] = mustManifestJSON(t, testDigest("v1-config"))
	manifests.modTimes[manifestRefKey("app", "v1")] = now
	// The legacy mirror file names a subject that is not stored.
	manifests.manifests[manifestRefKey("app", mirror)] = mustSubjectManifestJSON(t, testDigest("missing-subject"))
	manifests.modTimes[manifestRefKey("app", mirror)] = old
	manifests.manifests[manifestRefKey("app", other)] = otherBody
	manifests.modTimes[manifestRefKey("app", other)] = old

	orphan := testDigest("orphan")
	blobs := &fakeBlobStorage{blobs: []string{orphan}, blobModTimes: map[string]time.Time{orphan: now.Add(-72 * time.Hour)}}
	svc, _ := newPruneService(t, manifests, blobs, &fakeProtectionStore{}, &fakePruneRuntime{})
	report, err := svc.Prune(context.Background(), domain.ImagePruneOptions{KeepLast: 1, PruneRegistry: true})
	require.NoError(t, err)

	assert.Empty(t, manifests.deletedManifests)
	assert.Empty(t, blobs.deletedBlobs)
	require.NotEmpty(t, report.Plan.Gaps)
	verdicts := map[string]domain.PruneVerdict{}
	for _, candidate := range report.Plan.Candidates {
		verdicts[candidate.Ref] = candidate.Verdict
	}
	assert.Equal(t, domain.PruneVerdictProtected, verdicts["app:"+mirror])
	assert.Equal(t, domain.PruneVerdictUnknown, verdicts["app:"+other])
	assert.Equal(t, domain.PruneVerdictUnknown, verdicts[orphan])
}
