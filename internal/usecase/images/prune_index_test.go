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
	manifests  *fakeManifestStorage
	blobs      *fakeBlobStorage
	missing    string
	indexBody  []byte
	kept       []string // blobs reachable through existing children
	orphan     string   // old blob reachable through nothing
	youngBlob  string   // recent blob reachable through nothing
	childByArc map[string]string
}

func newLegacyIndexFixture(t *testing.T, now time.Time) legacyIndexFixture {
	t.Helper()
	f := legacyIndexFixture{manifests: newFakeManifestStorage(), childByArc: map[string]string{}}
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
		f.childByArc[entry.name] = digest
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
		manifestRefKey("app", f.childByArc["arm64"]): errors.New("permission denied"),
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
	attestation := f.childByArc["att-arm64"]
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
