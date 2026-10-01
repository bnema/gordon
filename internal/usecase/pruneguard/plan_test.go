package pruneguard

import (
	"strings"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

func digest(seed string) string {
	repeated := strings.Repeat(seed, 64)
	return "sha256:" + repeated[:64]
}

func tagKey(repository, tag string) string {
	return domain.RegistryTagRef{Repository: repository, Tag: tag}.Key()
}

func TestClassifyResource(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   Provenance
	}{
		{"nil", nil, ProvenanceUnmanaged},
		{"foreign", map[string]string{"com.example.role": "db"}, ProvenanceUnmanaged},
		{"managed only", map[string]string{domain.LabelManaged: "true"}, ProvenanceLegacyManaged},
		{"managed false", map[string]string{domain.LabelManaged: "false"}, ProvenanceUnmanaged},
		{"service", map[string]string{domain.LabelService: "srv"}, ProvenanceServiceManaged},
		{"service name", map[string]string{domain.LabelServiceName: "srv"}, ProvenanceServiceManaged},
		{
			"app wins over service",
			map[string]string{domain.LabelApp: "shop", domain.LabelService: "srv", domain.LabelManaged: "true"},
			ProvenanceAppOwned,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyResource(tc.labels); got != tc.want {
				t.Fatalf("ClassifyResource(%v) = %v, want %v", tc.labels, got, tc.want)
			}
		})
	}
}

func TestPlanRegistryTagsRetentionWindow(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tags := make([]RegistryTagCandidate, 0, 6)
	tags = append(tags, candidate("app", "latest", base.Add(6*time.Hour), digest("a")))
	for index := 0; index < 5; index++ {
		tags = append(tags, candidate("app", "v"+string(rune('1'+index)), base.Add(time.Duration(index)*time.Hour), digest(string(rune('b'+index)))))
	}

	verdicts := PlanRegistryTags(RegistryPlanInput{Tags: tags, Snapshot: emptySnapshot(), KeepLast: 3, Complete: true})
	byTag := indexVerdicts(t, verdicts)

	if byTag["latest"] != domain.PruneVerdictProtected {
		t.Fatalf("latest = %v, want protected", byTag["latest"])
	}
	for _, tag := range []string{"v5", "v4", "v3"} {
		if byTag[tag] != domain.PruneVerdictProtected {
			t.Fatalf("%s = %v, want protected (inside latest + 3)", tag, byTag[tag])
		}
	}
	for _, tag := range []string{"v1", "v2"} {
		if byTag[tag] != domain.PruneVerdictEligible {
			t.Fatalf("%s = %v, want eligible (outside latest + 3)", tag, byTag[tag])
		}
	}

	// Output order is deterministic regardless of input order.
	shuffled := []RegistryTagCandidate{tags[3], tags[0], tags[5], tags[1], tags[4], tags[2]}
	again := PlanRegistryTags(RegistryPlanInput{Tags: shuffled, Snapshot: emptySnapshot(), KeepLast: 3, Complete: true})
	for index := range verdicts {
		if verdicts[index].Ref != again[index].Ref {
			t.Fatalf("output order changed with input order at %d: %v vs %v", index, verdicts[index].Ref, again[index].Ref)
		}
	}
}

func TestPlanRegistryTagsLatestCountsSeparately(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tags := []RegistryTagCandidate{
		candidate("app", "latest", base, digest("a")),
		candidate("app", "v1", base.Add(time.Hour), digest("b")),
	}
	verdicts := PlanRegistryTags(RegistryPlanInput{Tags: tags, Snapshot: emptySnapshot(), KeepLast: 1, Complete: true})
	byTag := indexVerdicts(t, verdicts)
	if byTag["latest"] != domain.PruneVerdictProtected || byTag["v1"] != domain.PruneVerdictProtected {
		t.Fatalf("latest + 1 must retain both tags, got %v", byTag)
	}
}

func TestPlanRegistryTagsEqualTimestampsAreDeterministic(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tags := []RegistryTagCandidate{
		candidate("app", "b", base, digest("a")),
		candidate("app", "a", base, digest("b")),
	}
	verdicts := PlanRegistryTags(RegistryPlanInput{Tags: tags, Snapshot: emptySnapshot(), KeepLast: 1, Complete: true})
	byTag := indexVerdicts(t, verdicts)
	// Tag ordering descending resolves the tie: "b" is kept.
	if byTag["b"] != domain.PruneVerdictProtected || byTag["a"] != domain.PruneVerdictEligible {
		t.Fatalf("equal timestamps must resolve deterministically, got %v", byTag)
	}
}

func TestPlanRegistryTagsProtectedRootsSurviveRetention(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	oldDigest := digest("a")
	tags := []RegistryTagCandidate{
		candidate("app", "latest", base.Add(10*time.Hour), digest("f")),
		candidate("app", "v9", base.Add(9*time.Hour), digest("e")),
		candidate("app", "v8", base.Add(8*time.Hour), digest("d")),
		candidate("app", "v7", base.Add(7*time.Hour), digest("c")),
		candidate("app", "v1", base.Add(time.Hour), oldDigest),
	}

	snapshot := &domain.PruneProtectionSnapshot{Roots: []domain.ProtectionRoot{
		{Kind: domain.ProtectionDesiredRevision, Ref: tagKey("app", "v1"), Owner: "app"},
	}}
	verdicts := PlanRegistryTags(RegistryPlanInput{Tags: tags, Snapshot: snapshot, KeepLast: 3, Complete: true})

	var v1 domain.RegistryTagVerdict
	for _, verdict := range verdicts {
		if verdict.Ref.Tag == "v1" {
			v1 = verdict
		}
	}
	if v1.Verdict != domain.PruneVerdictProtected {
		t.Fatalf("protected v1 = %v, want protected", v1.Verdict)
	}
	if len(v1.Reasons) != 1 || v1.Reasons[0] != domain.PruneReasonProtectedDesiredRevision {
		t.Fatalf("v1 reasons = %v, want the desired-revision root", v1.Reasons)
	}

	// The same protection reached through the digest must also hold.
	snapshot = &domain.PruneProtectionSnapshot{Roots: []domain.ProtectionRoot{
		{Kind: domain.ProtectionActiveService, Ref: oldDigest, Owner: "app/web"},
	}}
	verdicts = PlanRegistryTags(RegistryPlanInput{Tags: tags, Snapshot: snapshot, KeepLast: 3, Complete: true})
	for _, verdict := range verdicts {
		if verdict.Ref.Tag == "v1" && verdict.Verdict != domain.PruneVerdictProtected {
			t.Fatalf("digest-protected v1 = %v, want protected", verdict.Verdict)
		}
	}
}

func TestPlanRegistryTagsFailClosed(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tags := []RegistryTagCandidate{
		candidate("app", "latest", base.Add(10*time.Hour), digest("f")),
		candidate("app", "v9", base.Add(9*time.Hour), digest("e")),
		candidate("app", "v1", base.Add(time.Hour), digest("a")),
		candidate("app", "v2", base.Add(2*time.Hour), digest("b")),
	}
	tags[3].ManifestKnown = false

	verdicts := PlanRegistryTags(RegistryPlanInput{Tags: tags, Snapshot: emptySnapshot(), KeepLast: 1, Complete: true})
	byTag := indexVerdicts(t, verdicts)
	if byTag["v2"] != domain.PruneVerdictUnknown {
		t.Fatalf("unreadable manifest tag = %v, want unknown", byTag["v2"])
	}
	if byTag["v1"] != domain.PruneVerdictEligible {
		t.Fatalf("readable out-of-window tag = %v, want eligible", byTag["v1"])
	}

	verdicts = PlanRegistryTags(RegistryPlanInput{Tags: tags, KeepLast: 1, Complete: false})
	byTag = indexVerdicts(t, verdicts)
	if byTag["v1"] != domain.PruneVerdictUnknown {
		t.Fatalf("incomplete registry inventory = %v, want unknown", byTag["v1"])
	}
	// Protection still wins over incompleteness.
	snapshot := &domain.PruneProtectionSnapshot{Roots: []domain.ProtectionRoot{
		{Kind: domain.ProtectionPinned, Ref: tagKey("app", "v1")},
	}}
	verdicts = PlanRegistryTags(RegistryPlanInput{Tags: tags, Snapshot: snapshot, KeepLast: 1, Complete: false})
	byTag = indexVerdicts(t, verdicts)
	if byTag["v1"] != domain.PruneVerdictProtected {
		t.Fatalf("pinned tag under incomplete inventory = %v, want protected", byTag["v1"])
	}
}

func TestPlanRegistryTagsKeepLastZeroSkips(t *testing.T) {
	tags := []RegistryTagCandidate{candidate("app", "v1", time.Now(), digest("a"))}
	for _, keepLast := range []int{0, -1} {
		if verdicts := PlanRegistryTags(RegistryPlanInput{Tags: tags, KeepLast: keepLast, Complete: true}); verdicts != nil {
			t.Fatalf("KeepLast=%d produced %d verdicts, want none", keepLast, len(verdicts))
		}
	}
}

func TestPlanRegistryTagsRejectsInvalidIdentity(t *testing.T) {
	verdicts := PlanRegistryTags(RegistryPlanInput{
		Tags:     []RegistryTagCandidate{{Ref: domain.RegistryTagRef{}, ManifestKnown: true}},
		Snapshot: emptySnapshot(),
		KeepLast: 1,
		Complete: true,
	})
	if len(verdicts) != 1 || verdicts[0].Verdict != domain.PruneVerdictUnknown {
		t.Fatalf("invalid tag identity = %+v, want unknown", verdicts)
	}
}

func TestPlanRuntimeImagesMixedBatch(t *testing.T) {
	activeID := "sha256:active"
	stoppedID := "sha256:stopped"
	danglingID := "sha256:dangling"
	foreignID := "sha256:foreign"
	legacyID := "sha256:legacy"
	serviceID := "sha256:service"
	taggedID := "sha256:tagged"

	images := []domain.RuntimeImage{
		{ID: activeID, Labels: appLabels("shop", "web")},
		{ID: stoppedID, Labels: appLabels("shop", "web")},
		{ID: danglingID, Labels: appLabels("shop", "web")},
		{ID: foreignID},
		{ID: legacyID, Labels: map[string]string{domain.LabelManaged: "true"}},
		{ID: serviceID, Labels: map[string]string{domain.LabelService: "edge"}},
		{ID: taggedID, RepoTags: []string{"app:latest"}, Labels: appLabels("shop", "web")},
	}
	containers := []domain.RuntimeContainerUse{
		{ContainerID: "c1", ImageID: activeID, Running: true},
		{ContainerID: "c2", ImageID: stoppedID},
	}

	verdicts := PlanRuntimeImages(RuntimePlanInput{Images: images, Containers: containers, Snapshot: &domain.PruneProtectionSnapshot{
		ImageClaims: []domain.ImageClaim{{Reference: danglingID, App: "shop", State: domain.VolumeClaimReleased}},
	}, Complete: true})
	byID := indexImageVerdicts(t, verdicts)

	expect := map[string]domain.PruneVerdict{
		activeID:   domain.PruneVerdictProtected,
		stoppedID:  domain.PruneVerdictProtected,
		danglingID: domain.PruneVerdictEligible,
		foreignID:  domain.PruneVerdictProtected,
		legacyID:   domain.PruneVerdictProtected,
		serviceID:  domain.PruneVerdictProtected,
		taggedID:   domain.PruneVerdictProtected,
	}
	for id, want := range expect {
		if byID[id] != want {
			t.Fatalf("%s = %v, want %v", id, byID[id], want)
		}
	}
	if byID[stoppedID] != domain.PruneVerdictProtected {
		t.Fatal("stopped container must protect its image")
	}
	if got := byID[foreignID]; got != domain.PruneVerdictProtected {
		t.Fatalf("unmanaged dangling image = %v, want protected", got)
	}
}

// TestPlanRuntimeImagesForgedLabelsNeverAuthorizeDeletion proves image
// labels are hints only: a dangling image that carries app ownership labels
// but has no durable ownership record stays unknown, never eligible.
func TestPlanRuntimeImagesForgedLabelsNeverAuthorizeDeletion(t *testing.T) {
	forged := "sha256:forged"
	verdicts := PlanRuntimeImages(RuntimePlanInput{
		Images: []domain.RuntimeImage{{
			ID:     forged,
			Labels: map[string]string{domain.LabelApp: "shop", domain.LabelAppID: "shop-uuid", domain.LabelManaged: "true"},
		}},
		Snapshot: emptySnapshot(),
		Complete: true,
	})

	require := verdicts[0]
	if require.Verdict != domain.PruneVerdictUnknown {
		t.Fatalf("forged app labels = %v, want unknown", require.Verdict)
	}
	if require.Reasons[0] != domain.PruneReasonUnknownProvenance {
		t.Fatalf("forged app label reason = %v", require.Reasons)
	}
}

// TestPlanRuntimeImagesAnyMatchingClaimProtects proves an attached claim
// protects an image even when a released claim for the same identity also
// exists, in either order: one live owner is enough to forbid deletion.
func TestPlanRuntimeImagesAnyMatchingClaimProtects(t *testing.T) {
	const identity = "sha256:shared"
	images := []domain.RuntimeImage{{ID: identity, Labels: map[string]string{domain.LabelApp: "shop"}}}
	orders := [][]domain.ImageClaim{
		{
			{Reference: identity, App: "shop", State: domain.VolumeClaimReleased},
			{Reference: identity, App: "shop", State: domain.VolumeClaimAttached},
		},
		{
			{Reference: identity, App: "shop", State: domain.VolumeClaimAttached},
			{Reference: identity, App: "shop", State: domain.VolumeClaimReleased},
		},
	}
	for index, claims := range orders {
		verdicts := PlanRuntimeImages(RuntimePlanInput{
			Images:   images,
			Snapshot: &domain.PruneProtectionSnapshot{ImageClaims: claims},
			Complete: true,
		})
		if verdicts[0].Verdict != domain.PruneVerdictProtected {
			t.Fatalf("order %d: image = %v, want protected while an attached claim exists", index, verdicts[0].Verdict)
		}
	}
}

// TestPlanRuntimeImagesReleasedClaimIsEligible proves a durable released
// claim with no container use and a complete inventory is eligible.
func TestPlanRuntimeImagesReleasedClaimIsEligible(t *testing.T) {
	released := "sha256:released"
	verdicts := PlanRuntimeImages(RuntimePlanInput{
		Images:   []domain.RuntimeImage{{ID: released, Labels: map[string]string{domain.LabelApp: "shop"}}},
		Snapshot: &domain.PruneProtectionSnapshot{ImageClaims: []domain.ImageClaim{{Reference: released, App: "shop", State: domain.VolumeClaimReleased}}},
		Complete: true,
	})
	if verdicts[0].Verdict != domain.PruneVerdictEligible {
		t.Fatalf("released image = %v, want eligible", verdicts[0].Verdict)
	}
}

// TestPlanRuntimeImagesAttachedClaimProtects proves an attached durable
// claim protects an otherwise dangling image.
func TestPlanRuntimeImagesAttachedClaimProtects(t *testing.T) {
	attached := "sha256:attached"
	verdicts := PlanRuntimeImages(RuntimePlanInput{
		Images:   []domain.RuntimeImage{{ID: attached, Labels: map[string]string{domain.LabelApp: "shop"}}},
		Snapshot: &domain.PruneProtectionSnapshot{ImageClaims: []domain.ImageClaim{{Reference: attached, App: "shop", State: domain.VolumeClaimAttached}}},
		Complete: true,
	})
	if verdicts[0].Verdict != domain.PruneVerdictProtected {
		t.Fatalf("attached image = %v, want protected", verdicts[0].Verdict)
	}
}

func TestPlanRuntimeImagesProtectionRootsAndUnknowns(t *testing.T) {
	pinnedID := "sha256:pinned"
	danglingID := "sha256:dangling"

	images := []domain.RuntimeImage{
		{ID: pinnedID, Labels: appLabels("shop", "web")},
		{ID: danglingID, Labels: appLabels("shop", "web")},
	}
	snapshot := &domain.PruneProtectionSnapshot{Roots: []domain.ProtectionRoot{
		{Kind: domain.ProtectionRecoveryInhibition, Ref: pinnedID, Owner: "shop"},
	}}

	verdicts := PlanRuntimeImages(RuntimePlanInput{Images: images, Snapshot: snapshot, Complete: true})
	byID := indexImageVerdicts(t, verdicts)
	if byID[pinnedID] != domain.PruneVerdictProtected {
		t.Fatalf("recovery-inhibited image = %v, want protected", byID[pinnedID])
	}
	if len(verdicts) != 2 || verdicts[1].Reasons[0] != domain.PruneReasonProtectedRecoveryInhibition {
		t.Fatalf("reasons = %v, want the recovery-inhibition root", verdicts[0].Reasons)
	}

	// An unresolvable container protects every candidate: its image may
	// be any of them.
	unknown := PlanRuntimeImages(RuntimePlanInput{
		Images:     images[1:],
		Containers: []domain.RuntimeContainerUse{{ContainerID: "c9", ImageUnknown: true}},
		Complete:   true,
	})
	if unknown[0].Verdict != domain.PruneVerdictUnknown {
		t.Fatalf("unknown container image = %v, want unknown", unknown[0].Verdict)
	}
	if unknown[0].Reasons[0] != domain.PruneReasonUnknownContainerUse {
		t.Fatalf("unknown container reason = %v", unknown[0].Reasons)
	}

	incomplete := PlanRuntimeImages(RuntimePlanInput{Images: images[1:], Complete: false})
	if incomplete[0].Verdict != domain.PruneVerdictUnknown {
		t.Fatalf("incomplete runtime inventory = %v, want unknown", incomplete[0].Verdict)
	}
}

func TestPlanRuntimeImagesProtectsUnknownIdentity(t *testing.T) {
	verdicts := PlanRuntimeImages(RuntimePlanInput{
		Images:   []domain.RuntimeImage{{ID: "<none>"}},
		Complete: true,
	})
	if len(verdicts) != 1 || verdicts[0].Verdict != domain.PruneVerdictUnknown {
		t.Fatalf("placeholder image identity = %+v, want unknown", verdicts)
	}
}

func TestPlanVolumesProtectsEverythingExceptReleased(t *testing.T) {
	released := "gordon-shop-web-data"
	attached := "gordon-shop-api-data"
	retained := "gordon-old-data"
	legacy := "gordon-legacy-data"
	serviceVolume := "gordon-edge-data"
	foreign := "pgdata"
	labelled := "gordon-mystery-data"
	inUse := "gordon-shop-cache"

	volumes := []*domain.VolumeInfo{
		{Name: released, Labels: appLabels("shop", "web")},
		{Name: attached, Labels: appLabels("shop", "api")},
		{Name: retained, Labels: appLabels("old", "web")},
		{Name: legacy, Labels: map[string]string{domain.LabelManaged: "true"}},
		{Name: serviceVolume, Labels: map[string]string{domain.LabelService: "edge"}},
		{Name: foreign},
		{Name: labelled, Labels: appLabels("ghost", "web")},
		{Name: inUse, Labels: appLabels("shop", "web"), InUse: true, Containers: []string{"c1"}},
	}

	snapshot := &domain.PruneProtectionSnapshot{VolumeClaims: []domain.VolumeClaim{
		{Name: released, App: "shop", AppID: "shop-uuid", Service: "web", State: domain.VolumeClaimReleased},
		{Name: attached, App: "shop", AppID: "shop-uuid", Service: "api", State: domain.VolumeClaimAttached},
		{Name: retained, App: "old", AppID: "old-uuid", Service: "web", State: domain.VolumeClaimRetained},
		{Name: inUse, App: "shop", AppID: "shop-uuid", Service: "web", State: domain.VolumeClaimAttached},
	}}

	verdicts := PlanVolumes(VolumePlanInput{Volumes: volumes, Snapshot: snapshot, Complete: true})
	byName := indexVolumeVerdicts(t, verdicts)

	expect := map[string]domain.PruneVerdict{
		released:      domain.PruneVerdictEligible,
		attached:      domain.PruneVerdictProtected,
		retained:      domain.PruneVerdictProtected,
		legacy:        domain.PruneVerdictProtected,
		serviceVolume: domain.PruneVerdictProtected,
		foreign:       domain.PruneVerdictProtected,
		labelled:      domain.PruneVerdictUnknown,
		inUse:         domain.PruneVerdictProtected,
	}
	for name, want := range expect {
		if byName[name] != want {
			t.Fatalf("%s = %v, want %v", name, byName[name], want)
		}
	}

	// A retained record is never reinterpreted as released, even when
	// the labels look released-compatible.
	if got := byName[retained]; got != domain.PruneVerdictProtected {
		t.Fatalf("retained volume = %v, want protected", got)
	}
}

func TestPlanVolumesReleasedRequiresAgreeingLabels(t *testing.T) {
	name := "gordon-shop-web-data"
	claim := domain.VolumeClaim{Name: name, App: "shop", AppID: "shop-uuid", Service: "web", State: domain.VolumeClaimReleased}

	tests := []struct {
		name   string
		labels map[string]string
		want   domain.PruneVerdict
	}{
		{"labels agree by name", map[string]string{domain.LabelManaged: "true", domain.LabelApp: "shop", domain.LabelAppID: "shop-uuid", domain.LabelAppService: "web"}, domain.PruneVerdictEligible},
		{"labels agree by uuid", map[string]string{domain.LabelManaged: "true", domain.LabelApp: "shop-uuid", domain.LabelAppID: "shop-uuid"}, domain.PruneVerdictEligible},
		{"missing managed marker", map[string]string{domain.LabelApp: "shop", domain.LabelAppID: "shop-uuid"}, domain.PruneVerdictUnknown},
		{"missing app label", map[string]string{domain.LabelManaged: "true", domain.LabelAppID: "shop-uuid"}, domain.PruneVerdictUnknown},
		{"contradicting app", map[string]string{domain.LabelManaged: "true", domain.LabelApp: "other", domain.LabelAppID: "shop-uuid"}, domain.PruneVerdictUnknown},
		{"contradicting service", map[string]string{domain.LabelManaged: "true", domain.LabelApp: "shop", domain.LabelAppID: "shop-uuid", domain.LabelAppService: "api"}, domain.PruneVerdictUnknown},
		{"no labels", nil, domain.PruneVerdictUnknown},
		{"missing incarnation stamp", map[string]string{domain.LabelManaged: "true", domain.LabelApp: "shop", domain.LabelAppService: "web"}, domain.PruneVerdictUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			verdicts := PlanVolumes(VolumePlanInput{
				Volumes:  []*domain.VolumeInfo{{Name: name, Labels: tc.labels}},
				Snapshot: &domain.PruneProtectionSnapshot{VolumeClaims: []domain.VolumeClaim{claim}},
				Complete: true,
			})
			if verdicts[0].Verdict != tc.want {
				t.Fatalf("verdict = %v (%v), want %v", verdicts[0].Verdict, verdicts[0].Reasons, tc.want)
			}
		})
	}
}

func TestPlanVolumesFailClosedOnIncompleteOwnership(t *testing.T) {
	name := "gordon-shop-web-data"
	volume := &domain.VolumeInfo{Name: name, Labels: appLabels("shop", "web")}
	claim := domain.VolumeClaim{Name: name, App: "shop", AppID: "shop-uuid", Service: "web", State: domain.VolumeClaimReleased}

	snapshot := &domain.PruneProtectionSnapshot{
		VolumeClaims: []domain.VolumeClaim{claim},
		Gaps: []domain.InventoryGap{{
			Source: domain.InventorySourceOwnership,
			Reason: domain.PruneReasonUnknownInventory,
			Detail: "unreadable ownership record",
		}},
	}
	verdicts := PlanVolumes(VolumePlanInput{Volumes: []*domain.VolumeInfo{volume}, Snapshot: snapshot, Complete: true})
	if verdicts[0].Verdict != domain.PruneVerdictUnknown {
		t.Fatalf("released volume under incomplete ownership = %v, want unknown", verdicts[0].Verdict)
	}

	// A nil snapshot proves nothing: it is incomplete by definition.
	verdicts = PlanVolumes(VolumePlanInput{Volumes: []*domain.VolumeInfo{volume}, Complete: true})
	if verdicts[0].Verdict == domain.PruneVerdictEligible {
		t.Fatalf("nil snapshot made a volume eligible: %+v", verdicts[0])
	}

	verdicts = PlanVolumes(VolumePlanInput{
		Volumes:  []*domain.VolumeInfo{volume},
		Snapshot: &domain.PruneProtectionSnapshot{VolumeClaims: []domain.VolumeClaim{claim}},
		Complete: false,
	})
	if verdicts[0].Verdict != domain.PruneVerdictUnknown {
		t.Fatalf("incomplete runtime inventory = %v, want unknown", verdicts[0].Verdict)
	}
}

func TestPlanVolumesZeroDeletionIsSuccess(t *testing.T) {
	volumes := []*domain.VolumeInfo{
		{Name: "pgdata"},
		{Name: "gordon-shop-data", Labels: appLabels("shop", "web"), InUse: true},
	}
	verdicts := PlanVolumes(VolumePlanInput{Volumes: volumes, Snapshot: &domain.PruneProtectionSnapshot{}, Complete: true})
	if len(verdicts) != 2 {
		t.Fatalf("expected one verdict per volume, got %d", len(verdicts))
	}
	for _, verdict := range verdicts {
		if verdict.Verdict == domain.PruneVerdictEligible {
			t.Fatalf("volume %q unexpectedly eligible", verdict.Ref.Name)
		}
		if len(verdict.Reasons) == 0 {
			t.Fatalf("volume %q carries no reason", verdict.Ref.Name)
		}
	}
}

func TestPlanVolumesOrderIsDeterministic(t *testing.T) {
	volumes := []*domain.VolumeInfo{{Name: "c"}, {Name: "a"}, {Name: "b"}}
	first := PlanVolumes(VolumePlanInput{Volumes: volumes, Complete: true})
	second := PlanVolumes(VolumePlanInput{Volumes: []*domain.VolumeInfo{volumes[1], volumes[2], volumes[0]}, Complete: true})
	for index := range first {
		if first[index].Ref != second[index].Ref {
			t.Fatalf("order changed at %d: %v vs %v", index, first[index].Ref, second[index].Ref)
		}
	}
}

func TestPlansProduceValidPlanEntries(t *testing.T) {
	// Every verdict the planners emit must survive plan validation.
	plan := &domain.PrunePlan{}
	plan.Tags = PlanRegistryTags(RegistryPlanInput{
		Tags: []RegistryTagCandidate{
			candidate("app", "latest", time.Now(), digest("a")),
			candidate("app", "v1", time.Now().Add(-time.Hour), digest("b")),
		},
		Snapshot: emptySnapshot(),
		KeepLast: 1,
		Complete: true,
	})
	plan.Images = PlanRuntimeImages(RuntimePlanInput{
		Images: []domain.RuntimeImage{
			{ID: "sha256:one", Labels: appLabels("app", "web")},
			{ID: "sha256:two"},
		},
		Snapshot: emptySnapshot(),
		Complete: true,
	})
	plan.Volumes = PlanVolumes(VolumePlanInput{
		Volumes:  []*domain.VolumeInfo{{Name: "pgdata"}, {Name: "gordon-app-data", Labels: appLabels("app", "web")}},
		Snapshot: &domain.PruneProtectionSnapshot{},
		Complete: true,
	})
	if err := plan.Validate(); err != nil {
		t.Fatalf("planner output failed plan validation: %v", err)
	}
	if len(plan.EligibleTags()) != 0 {
		t.Fatalf("latest + 1 must retain every tag, got %v", plan.EligibleTags())
	}
}

func candidate(repository, tag string, modTime time.Time, manifestDigest string) RegistryTagCandidate {
	return RegistryTagCandidate{
		Ref:           domain.RegistryTagRef{Repository: repository, Tag: tag},
		Digest:        manifestDigest,
		ModTime:       modTime,
		ManifestKnown: true,
	}
}

// emptySnapshot is a complete snapshot that protects nothing.
func emptySnapshot() *domain.PruneProtectionSnapshot {
	return &domain.PruneProtectionSnapshot{}
}

func appLabels(app, service string) map[string]string {
	return map[string]string{
		domain.LabelManaged:    "true",
		domain.LabelApp:        app,
		domain.LabelAppID:      app + "-uuid",
		domain.LabelAppService: service,
	}
}

func indexVerdicts(t *testing.T, verdicts []domain.RegistryTagVerdict) map[string]domain.PruneVerdict {
	t.Helper()
	out := make(map[string]domain.PruneVerdict, len(verdicts))
	for _, verdict := range verdicts {
		out[verdict.Ref.Tag] = verdict.Verdict
	}
	return out
}

func indexImageVerdicts(t *testing.T, verdicts []domain.RuntimeImageVerdict) map[string]domain.PruneVerdict {
	t.Helper()
	out := make(map[string]domain.PruneVerdict, len(verdicts))
	for _, verdict := range verdicts {
		out[verdict.Ref.ID] = verdict.Verdict
	}
	return out
}

func indexVolumeVerdicts(t *testing.T, verdicts []domain.VolumeVerdict) map[string]domain.PruneVerdict {
	t.Helper()
	out := make(map[string]domain.PruneVerdict, len(verdicts))
	for _, verdict := range verdicts {
		out[verdict.Ref.Name] = verdict.Verdict
	}
	return out
}
