package domain

import (
	"strings"
	"testing"
)

func TestValidOCIDigest(t *testing.T) {
	tests := []struct {
		name   string
		digest string
		want   bool
	}{
		{"sha256", "sha256:" + strings.Repeat("ab", 32), true},
		{"sha512", "sha512:" + strings.Repeat("cd", 64), true},
		{"empty", "", false},
		{"no algorithm", strings.Repeat("ab", 32), false},
		{"empty encoded", "sha256:", false},
		{"short encoded", "sha256:abcd", false},
		{"uppercase algorithm", "SHA256:" + strings.Repeat("ab", 32), false},
		{"non hex", "sha256:" + strings.Repeat("zz", 32), false},
		{"space inside", "sha256:" + strings.Repeat("ab", 31) + "a ", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidOCIDigest(tc.digest); got != tc.want {
				t.Fatalf("ValidOCIDigest(%q) = %v, want %v", tc.digest, got, tc.want)
			}
		})
	}
}

func TestRegistryTagRefValidation(t *testing.T) {
	valid := RegistryTagRef{Repository: "app", Tag: "v1"}
	if !valid.Valid() || valid.String() != "app:v1" || valid.Key() == "" {
		t.Fatalf("valid registry tag ref not accepted: %+v", valid)
	}

	for _, ref := range []RegistryTagRef{
		{},
		{Repository: "app"},
		{Tag: "v1"},
		{Repository: "app", Tag: " "},
		{Repository: "a pp", Tag: "v1"},
	} {
		if ref.Valid() {
			t.Fatalf("empty or whitespace identity accepted: %+v", ref)
		}
		if ref.Key() != "" {
			t.Fatalf("invalid ref produced a map key: %+v", ref)
		}
	}

	if _, err := NewRegistryTagRef("", "v1"); err == nil {
		t.Fatal("expected constructor to reject an empty repository")
	}
}

func TestIdentityKeysAreUnambiguous(t *testing.T) {
	// A repository containing a colon must not collide with the
	// repository/tag separator used by the map key. The human display
	// form is documented as ambiguous and never parsed back.
	first := RegistryTagRef{Repository: "a:b", Tag: "c"}
	second := RegistryTagRef{Repository: "a", Tag: "b:c"}
	if first.Key() == second.Key() {
		t.Fatalf("distinct references collided on key: %q", first.Key())
	}
	if first.Key() == "" || second.Key() == "" {
		t.Fatalf("valid references must carry a canonical key: %q / %q", first.Key(), second.Key())
	}
}

func TestRuntimeImageRefRejectsPlaceholders(t *testing.T) {
	for _, id := range []string{"", " ", "<none>", "<none>:<none>"} {
		if ref := (RuntimeImageRef{ID: id}); ref.Valid() {
			t.Fatalf("placeholder image ID %q accepted", id)
		}
	}
	if !(RuntimeImageRef{ID: "sha256:abc"}).Valid() {
		t.Fatal("exact image ID rejected")
	}
}

func TestRuntimeVolumeRefValidation(t *testing.T) {
	if !(RuntimeVolumeRef{Name: "gordon-app-data"}).Valid() {
		t.Fatal("exact volume name rejected")
	}
	for _, name := range []string{"", " ", "vol ume", "vol\tume"} {
		if ref := (RuntimeVolumeRef{Name: name}); ref.Valid() {
			t.Fatalf("invalid volume name %q accepted", name)
		}
	}
}

func TestPruneReasonCoverage(t *testing.T) {
	reasons := PruneReasons()
	if len(reasons) == 0 {
		t.Fatal("no reason codes defined")
	}
	seen := make(map[PruneReason]struct{}, len(reasons))
	for _, reason := range reasons {
		if _, dup := seen[reason]; dup {
			t.Fatalf("reason %q listed twice in canonical order", reason)
		}
		seen[reason] = struct{}{}
		if !reason.Valid() {
			t.Fatalf("reason %q has no verdict category", reason)
		}
		if reason.Category() == "" {
			t.Fatalf("reason %q has an empty category", reason)
		}
	}
	if len(seen) != len(pruneReasonCategory) {
		t.Fatalf("canonical order covers %d reasons, category map has %d", len(seen), len(pruneReasonCategory))
	}
}

func TestPruneReasonWireValuesAreStable(t *testing.T) {
	// These strings appear in JSON and human output. If one changes,
	// this test must change deliberately.
	want := map[PruneReason]string{
		PruneReasonEligibleRetention:            "eligible-retention-window",
		PruneReasonEligibleUnreferencedBlob:     "eligible-unreferenced-blob",
		PruneReasonEligibleDanglingRuntimeImage: "eligible-dangling-runtime-image",
		PruneReasonEligibleReleasedVolume:       "eligible-released-volume",
		PruneReasonProtectedLatest:              "protected-latest",
		PruneReasonProtectedRetentionWindow:     "protected-retention-window",
		PruneReasonProtectedDesiredRevision:     "protected-desired-revision",
		PruneReasonProtectedActiveService:       "protected-active-service",
		PruneReasonProtectedRecoveryInhibition:  "protected-recovery-inhibition",
		PruneReasonProtectedApplyIntent:         "protected-apply-intent",
		PruneReasonProtectedOperation:           "protected-operation",
		PruneReasonProtectedOwnership:           "protected-ownership",
		PruneReasonProtectedContainerUse:        "protected-container-use",
		PruneReasonProtectedPendingUpload:       "protected-pending-upload",
		PruneReasonProtectedSharedContent:       "protected-shared-content",
		PruneReasonProtectedPinned:              "protected-pinned",
		PruneReasonProtectedServiceManaged:      "protected-service-managed",
		PruneReasonProtectedManagedOnly:         "protected-managed-without-ownership",
		PruneReasonProtectedTagged:              "protected-tagged-runtime-image",
		PruneReasonProtectedUnmanaged:           "protected-unmanaged",
		PruneReasonUnknownIdentity:              "unknown-identity",
		PruneReasonUnknownProvenance:            "unknown-provenance",
		PruneReasonUnknownInventory:             "unknown-incomplete-inventory",
		PruneReasonUnknownManifest:              "unknown-manifest-unreadable",
		PruneReasonUnknownMediaType:             "unknown-unsupported-media-type",
		PruneReasonUnknownContainerUse:          "unknown-container-use",
	}
	for reason, wire := range want {
		if string(reason) != wire {
			t.Fatalf("reason %q wire value changed to %q", wire, reason)
		}
	}
	if len(want) != len(pruneReasonCategory) {
		t.Fatalf("stability table covers %d reasons, %d defined", len(want), len(pruneReasonCategory))
	}
}

func TestCanonicalReasonsIsDeterministic(t *testing.T) {
	input := []PruneReason{
		PruneReasonUnknownInventory,
		PruneReasonProtectedActiveService,
		PruneReasonProtectedLatest,
		PruneReasonProtectedActiveService,
	}
	got := CanonicalReasons(input...)
	want := []PruneReason{
		PruneReasonProtectedLatest,
		PruneReasonProtectedActiveService,
		PruneReasonUnknownInventory,
	}
	if len(got) != len(want) {
		t.Fatalf("CanonicalReasons returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("CanonicalReasons returned %v, want %v", got, want)
		}
	}

	// A permuted input must produce the identical output.
	permuted := []PruneReason{input[2], input[0], input[1], input[3]}
	again := CanonicalReasons(permuted...)
	for i := range want {
		if again[i] != want[i] {
			t.Fatalf("permuted input produced %v, want %v", again, want)
		}
	}
}

func TestValidateVerdictReasons(t *testing.T) {
	if err := validateVerdictReasons(PruneVerdictEligible, []PruneReason{PruneReasonEligibleRetention}); err != nil {
		t.Fatalf("well-formed eligible verdict rejected: %v", err)
	}
	cases := []struct {
		name    string
		verdict PruneVerdict
		reasons []PruneReason
	}{
		{"undefined verdict", PruneVerdict("maybe"), []PruneReason{PruneReasonEligibleRetention}},
		{"no reasons", PruneVerdictEligible, nil},
		{"undefined reason", PruneVerdictEligible, []PruneReason{"made-up"}},
		{"reason contradicts verdict", PruneVerdictEligible, []PruneReason{PruneReasonProtectedLatest}},
		{"unknown reason under protected", PruneVerdictProtected, []PruneReason{PruneReasonUnknownIdentity}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateVerdictReasons(tc.verdict, tc.reasons); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestPrunePlanValidateRejectsMalformedEntries(t *testing.T) {
	plan := &PrunePlan{
		Tags: []RegistryTagVerdict{{
			Ref:     RegistryTagRef{Repository: "app", Tag: "v1"},
			Verdict: PruneVerdictEligible,
			Reasons: []PruneReason{PruneReasonEligibleRetention},
		}},
		Volumes: []VolumeVerdict{{
			Ref:     RuntimeVolumeRef{Name: "gordon-app-data"},
			Verdict: PruneVerdictProtected,
			Reasons: []PruneReason{PruneReasonProtectedOwnership},
		}},
		Gaps: []InventoryGap{{
			Source: InventorySourceRegistry,
			Reason: PruneReasonUnknownInventory,
			Detail: "list tags failed",
		}},
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("well-formed plan rejected: %v", err)
	}

	bad := *plan
	bad.Tags = []RegistryTagVerdict{{Verdict: PruneVerdictEligible, Reasons: []PruneReason{PruneReasonEligibleRetention}}}
	if err := bad.Validate(); err == nil {
		t.Fatal("plan with an invalid tag identity passed validation")
	}

	badGap := *plan
	badGap.Gaps = []InventoryGap{{Source: InventorySourceRegistry, Reason: PruneReasonProtectedLatest}}
	if err := badGap.Validate(); err == nil {
		t.Fatal("plan with a non-unknown gap reason passed validation")
	}
}

func TestPrunePlanEligibleAccessors(t *testing.T) {
	plan := &PrunePlan{
		Tags: []RegistryTagVerdict{
			{Ref: RegistryTagRef{Repository: "app", Tag: "v1"}, Verdict: PruneVerdictEligible, Reasons: []PruneReason{PruneReasonEligibleRetention}},
			{Ref: RegistryTagRef{Repository: "app", Tag: "v2"}, Verdict: PruneVerdictProtected, Reasons: []PruneReason{PruneReasonProtectedActiveService}},
		},
		Images: []RuntimeImageVerdict{
			{Ref: RuntimeImageRef{ID: "sha256:dead"}, Verdict: PruneVerdictEligible, Reasons: []PruneReason{PruneReasonEligibleDanglingRuntimeImage}},
			{Ref: RuntimeImageRef{ID: "sha256:beef"}, Verdict: PruneVerdictUnknown, Reasons: []PruneReason{PruneReasonUnknownContainerUse}},
		},
		Volumes: []VolumeVerdict{
			{Ref: RuntimeVolumeRef{Name: "gordon-a-data"}, Verdict: PruneVerdictEligible, Reasons: []PruneReason{PruneReasonEligibleReleasedVolume}},
		},
	}

	eligibleTags := plan.EligibleTags()
	if len(eligibleTags) != 1 || eligibleTags[0].Tag != "v1" {
		t.Fatalf("EligibleTags() = %v, want only app:v1", eligibleTags)
	}
	eligibleImages := plan.EligibleImages()
	if len(eligibleImages) != 1 || eligibleImages[0].ID != "sha256:dead" {
		t.Fatalf("EligibleImages() = %v, want only sha256:dead", eligibleImages)
	}
	eligibleVolumes := plan.EligibleVolumes()
	if len(eligibleVolumes) != 1 || eligibleVolumes[0].Name != "gordon-a-data" {
		t.Fatalf("EligibleVolumes() = %v, want only gordon-a-data", eligibleVolumes)
	}
	if got := plan.CountByVerdict(PruneVerdictEligible); got != 3 {
		t.Fatalf("CountByVerdict(eligible) = %d, want 3", got)
	}
	if got := plan.CountByVerdict(PruneVerdictUnknown); got != 1 {
		t.Fatalf("CountByVerdict(unknown) = %d, want 1", got)
	}
}

func TestPruneProtectionSnapshotLookups(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	snapshot := &PruneProtectionSnapshot{
		Roots: []ProtectionRoot{
			{Kind: ProtectionLatest, Ref: "app\x00latest"},
			{Kind: ProtectionActiveService, Ref: digest, Owner: "app/web"},
			{Kind: ProtectionContainerUse, Ref: "sha256:imageid", Owner: "container-1"},
			{Kind: ProtectionOwnership, Ref: "gordon-app-data", Owner: "app"},
		},
		VolumeClaims: []VolumeClaim{
			{Name: "gordon-app-data", App: "app", Service: "web", State: VolumeClaimAttached},
			{Name: "gordon-app-data", App: "app", Service: "web", State: VolumeClaimRetained},
		},
	}

	if !snapshot.ProtectsDigest(digest) {
		t.Fatal("digest root not matched")
	}
	if snapshot.ProtectsDigest("") {
		t.Fatal("empty digest matched a root")
	}
	if snapshot.ProtectsDigest("not-a-digest") {
		t.Fatal("invalid digest matched a root")
	}
	if !snapshot.ProtectsRuntimeImage("sha256:imageid", nil) {
		t.Fatal("runtime image ID root not matched")
	}
	if !snapshot.ProtectsRuntimeImage("", []string{digest}) {
		t.Fatal("repo digest root not matched")
	}
	if snapshot.Protects(ProtectionLatest, "other\x00latest") {
		t.Fatal("non-matching tag root matched")
	}

	claim, ok := snapshot.VolumeClaimFor("gordon-app-data")
	if !ok || claim.State != VolumeClaimRetained {
		t.Fatalf("VolumeClaimFor returned %+v/%v, want the last recorded claim", claim, ok)
	}
	if claim.State.Protects() != true {
		t.Fatal("retained claim must protect")
	}
	if VolumeClaimReleased.Protects() {
		t.Fatal("released claim must not protect")
	}
	if _, ok := snapshot.VolumeClaimFor("absent"); ok {
		t.Fatal("absent volume reported a claim")
	}
	if snapshot.Complete() != true {
		t.Fatal("snapshot without gaps must be complete")
	}

	gapped := &PruneProtectionSnapshot{Gaps: []InventoryGap{{
		Source: InventorySourceAppState,
		Reason: PruneReasonUnknownInventory,
		Detail: "unreadable app record",
	}}}
	if gapped.Complete() {
		t.Fatal("snapshot with a valid gap reported complete")
	}
	if (*PruneProtectionSnapshot)(nil).Complete() {
		t.Fatal("nil snapshot must not report complete")
	}
}

func TestVolumeClaimValidation(t *testing.T) {
	if !(VolumeClaim{Name: "vol", App: "app", State: VolumeClaimAttached}).Valid() {
		t.Fatal("complete claim rejected")
	}
	for _, claim := range []VolumeClaim{
		{App: "app", State: VolumeClaimAttached},
		{Name: "vol", State: VolumeClaimAttached},
		{Name: "vol", App: "app"},
		{Name: "vol", App: "app", State: "gone"},
	} {
		if claim.Valid() {
			t.Fatalf("incomplete claim accepted: %+v", claim)
		}
	}
}

func TestRuntimeInventoryCompleteness(t *testing.T) {
	if !(&RuntimeInventory{}).Complete() {
		t.Fatal("empty inventory must be complete")
	}
	if (*RuntimeInventory)(nil).Complete() {
		t.Fatal("nil inventory must not be complete")
	}
	if (&RuntimeInventory{Gaps: []InventoryGap{{
		Source: InventorySourceRuntimeImages,
		Reason: PruneReasonUnknownInventory,
		Detail: "list images failed",
	}}}).Complete() {
		t.Fatal("inventory with a gap reported complete")
	}
}
