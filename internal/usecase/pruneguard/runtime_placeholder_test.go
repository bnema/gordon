package pruneguard

import (
	"reflect"
	"testing"

	"github.com/bnema/gordon/internal/domain"
)

func TestDomainIsPlaceholderRepoTag(t *testing.T) {
	tests := []struct {
		tag  string
		want bool
	}{
		{"", true},
		{"<none>", true},
		{"<none>:<none>", true},
		{"registry.example.com/shop:<none>", true},
		{"localhost:5000/shop:<none>", true},
		{"localhost/x:<none>", true},
		{"reg.example.com/shop@" + digest("a"), false},
		{"<none>@" + digest("a"), true},
		{"registry.example.com/shop:v1", false},
		{"localhost:5000/shop:latest", false},
		{"shop", false},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			if got := domain.IsPlaceholderRepoTag(tt.tag); got != tt.want {
				t.Fatalf("domain.IsPlaceholderRepoTag(%q) = %v, want %v", tt.tag, got, tt.want)
			}
		})
	}
}

// TestPlanRuntimeImagesSupersededPodmanImage covers a superseded image
// that Podman keeps as "<repo>:<none>": it is untagged, so its verdict is
// decided by container use and durable ownership claims.
func TestPlanRuntimeImagesSupersededPodmanImage(t *testing.T) {
	id := digest("b")
	repoDigest := "registry.example.com/shop@" + digest("c")
	appLabels := map[string]string{domain.LabelApp: "shop", domain.LabelAppRevision: "3"}
	image := domain.RuntimeImage{
		ID:          id,
		RepoTags:    []string{"registry.example.com/shop:<none>"},
		RepoDigests: []string{repoDigest},
		Labels:      appLabels,
	}

	tests := []struct {
		name       string
		claims     []domain.ImageClaim
		containers []domain.RuntimeContainerUse
		want       domain.PruneVerdict
		wantReason domain.PruneReason
	}{
		{
			name:       "released claim by digest is eligible",
			claims:     []domain.ImageClaim{{Reference: "registry.example.com/shop:v1", Digest: digest("c"), App: "shop", State: domain.VolumeClaimReleased}},
			want:       domain.PruneVerdictEligible,
			wantReason: domain.PruneReasonEligibleDanglingRuntimeImage,
		},
		{
			name:       "released claim by image ID is eligible",
			claims:     []domain.ImageClaim{{Reference: id, App: "shop", State: domain.VolumeClaimReleased}},
			want:       domain.PruneVerdictEligible,
			wantReason: domain.PruneReasonEligibleDanglingRuntimeImage,
		},
		{
			name:       "attached claim protects",
			claims:     []domain.ImageClaim{{Digest: digest("c"), App: "shop", State: domain.VolumeClaimAttached}},
			want:       domain.PruneVerdictProtected,
			wantReason: domain.PruneReasonProtectedOwnership,
		},
		{
			name:       "container use protects",
			claims:     []domain.ImageClaim{{Reference: id, App: "shop", State: domain.VolumeClaimReleased}},
			containers: []domain.RuntimeContainerUse{{ContainerID: "c1", ImageID: id}},
			want:       domain.PruneVerdictProtected,
			wantReason: domain.PruneReasonProtectedContainerUse,
		},
		{
			name:       "app labels without a claim stay unknown",
			want:       domain.PruneVerdictUnknown,
			wantReason: domain.PruneReasonUnknownProvenance,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdicts := PlanRuntimeImages(RuntimePlanInput{
				Images:     []domain.RuntimeImage{image},
				Containers: tt.containers,
				Snapshot:   &domain.PruneProtectionSnapshot{ImageClaims: tt.claims},
				Complete:   true,
			})
			got := verdicts[0]
			if got.Verdict != tt.want || !reflect.DeepEqual(got.Reasons, []domain.PruneReason{tt.wantReason}) {
				t.Fatalf("verdict = %v %v, want %v %v", got.Verdict, got.Reasons, tt.want, tt.wantReason)
			}
		})
	}
}

// TestPlanRuntimeImagesIncludeUnmanagedDangling proves the opt-in only
// releases unused, untagged images with no Gordon ownership record, no
// Gordon labels, and no child image, and that every other gate still holds.
func TestPlanRuntimeImagesIncludeUnmanagedDangling(t *testing.T) {
	id := digest("d")
	untagged := domain.RuntimeImage{ID: id, RepoTags: []string{"docker.io/library/foo:<none>"}}
	reasons := func(r domain.PruneReason) []domain.PruneReason { return []domain.PruneReason{r} }
	claim := func(state domain.VolumeClaimState, app string) *domain.PruneProtectionSnapshot {
		return &domain.PruneProtectionSnapshot{ImageClaims: []domain.ImageClaim{{Reference: id, App: app, State: state}}}
	}

	tests := []struct {
		name        string
		image       domain.RuntimeImage
		others      []domain.RuntimeImage
		containers  []domain.RuntimeContainerUse
		protectedID []string
		snapshot    *domain.PruneProtectionSnapshot
		optIn       bool
		incomplete  bool
		want        domain.PruneVerdict
		wantReasons []domain.PruneReason
	}{
		{name: "off keeps unmanaged protected", image: untagged,
			want: domain.PruneVerdictProtected, wantReasons: reasons(domain.PruneReasonProtectedUnmanaged)},
		{name: "opt-in makes unmanaged eligible", image: untagged, optIn: true,
			others: []domain.RuntimeImage{{ID: digest("9")}},
			want:   domain.PruneVerdictEligible, wantReasons: reasons(domain.PruneReasonEligibleUnmanagedDanglingImage)},
		{name: "parent of another image stays protected", image: untagged, optIn: true,
			others: []domain.RuntimeImage{{ID: digest("9"), ParentID: id}},
			want:   domain.PruneVerdictProtected, wantReasons: reasons(domain.PruneReasonProtectedUnmanaged)},
		{name: "stopped container use still protects", image: untagged, optIn: true,
			containers: []domain.RuntimeContainerUse{{ContainerID: "stopped", ImageID: id}},
			want:       domain.PruneVerdictProtected, wantReasons: reasons(domain.PruneReasonProtectedContainerUse)},
		{name: "recovery inhibited container still protects", image: untagged, optIn: true,
			containers:  []domain.RuntimeContainerUse{{ContainerID: "held", ImageID: id}},
			protectedID: []string{"held"},
			want:        domain.PruneVerdictProtected, wantReasons: reasons(domain.PruneReasonProtectedContainerUse)},
		{name: "unknown container use stays unknown", image: untagged, optIn: true,
			containers: []domain.RuntimeContainerUse{{ContainerID: "c", ImageUnknown: true}},
			want:       domain.PruneVerdictUnknown, wantReasons: reasons(domain.PruneReasonUnknownContainerUse)},
		{name: "matching root still protects", image: untagged, optIn: true,
			snapshot: &domain.PruneProtectionSnapshot{Roots: []domain.ProtectionRoot{{Kind: domain.ProtectionActiveService, Ref: id, Owner: "shop"}}},
			want:     domain.PruneVerdictProtected, wantReasons: reasons(domain.PruneReasonProtectedActiveService)},
		{name: "snapshot gap stays unknown", image: untagged, optIn: true,
			snapshot: &domain.PruneProtectionSnapshot{Gaps: []domain.InventoryGap{{
				Source: domain.InventorySourceAppState, Reason: domain.PruneReasonUnknownInventory, Detail: "app record unreadable",
			}}},
			want: domain.PruneVerdictUnknown, wantReasons: reasons(domain.PruneReasonUnknownInventory)},
		{name: "incomplete inventory stays unknown", image: untagged, optIn: true, incomplete: true,
			want: domain.PruneVerdictUnknown, wantReasons: reasons(domain.PruneReasonUnknownInventory)},
		{name: "attached claim still protects", image: untagged, optIn: true, snapshot: claim(domain.VolumeClaimAttached, "shop"),
			want: domain.PruneVerdictProtected, wantReasons: reasons(domain.PruneReasonProtectedOwnership)},
		{name: "retained claim still protects", image: untagged, optIn: true, snapshot: claim(domain.VolumeClaimRetained, "shop"),
			want: domain.PruneVerdictProtected, wantReasons: reasons(domain.PruneReasonProtectedOwnership)},
		{name: "invalid claim stays unknown", image: untagged, optIn: true, snapshot: claim(domain.VolumeClaimReleased, ""),
			want: domain.PruneVerdictUnknown, wantReasons: reasons(domain.PruneReasonUnknownProvenance)},
		{name: "app labels without claim stay unknown", optIn: true,
			image: domain.RuntimeImage{ID: id, Labels: map[string]string{domain.LabelApp: "shop"}},
			want:  domain.PruneVerdictUnknown, wantReasons: reasons(domain.PruneReasonUnknownProvenance)},
		{name: "managed labels still protected", optIn: true,
			image: domain.RuntimeImage{ID: id, Labels: map[string]string{domain.LabelManaged: "true"}},
			want:  domain.PruneVerdictProtected, wantReasons: reasons(domain.PruneReasonProtectedManagedOnly)},
		{name: "tagged image still protected", optIn: true,
			image: domain.RuntimeImage{ID: id, RepoTags: []string{"docker.io/library/foo:1"}},
			want:  domain.PruneVerdictProtected, wantReasons: reasons(domain.PruneReasonProtectedTagged)},
		{name: "mixed real and placeholder tags still protected", optIn: true,
			image: domain.RuntimeImage{ID: id, RepoTags: []string{"reg/shop:v2", "reg/shop:<none>"}},
			want:  domain.PruneVerdictProtected, wantReasons: reasons(domain.PruneReasonProtectedTagged)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := tt.snapshot
			if snapshot == nil {
				snapshot = &domain.PruneProtectionSnapshot{}
			}
			verdicts := PlanRuntimeImages(RuntimePlanInput{
				Images:                   append([]domain.RuntimeImage{tt.image}, tt.others...),
				Containers:               tt.containers,
				ProtectedContainerIDs:    tt.protectedID,
				Snapshot:                 snapshot,
				Complete:                 !tt.incomplete,
				IncludeUnmanagedDangling: tt.optIn,
			})
			var got domain.RuntimeImageVerdict
			for _, verdict := range verdicts {
				if verdict.Ref.ID == id {
					got = verdict
				}
			}
			if got.Verdict != tt.want || !reflect.DeepEqual(got.Reasons, tt.wantReasons) {
				t.Fatalf("verdict = %v %v, want %v %v", got.Verdict, got.Reasons, tt.want, tt.wantReasons)
			}
		})
	}
}

// TestPlanRuntimeImagesBareDigestRootProtectsReleasedImage proves a root
// that names content by bare digest (an active service or an unfinished
// operation) protects a runtime image known only by its repo digest, even
// when a released claim would otherwise make it eligible.
func TestPlanRuntimeImagesBareDigestRootProtectsReleasedImage(t *testing.T) {
	contentDigest := digest("e")
	image := domain.RuntimeImage{
		ID:          digest("f"),
		RepoTags:    []string{"reg.example.com/shop:<none>"},
		RepoDigests: []string{"reg.example.com/shop@" + contentDigest},
	}
	for _, kind := range []domain.ProtectionRootKind{domain.ProtectionActiveService, domain.ProtectionOperation} {
		t.Run(string(kind), func(t *testing.T) {
			verdicts := PlanRuntimeImages(RuntimePlanInput{
				Images: []domain.RuntimeImage{image},
				Snapshot: &domain.PruneProtectionSnapshot{
					Roots:       []domain.ProtectionRoot{{Kind: kind, Ref: contentDigest, Owner: "shop"}},
					ImageClaims: []domain.ImageClaim{{Digest: contentDigest, App: "shop", State: domain.VolumeClaimReleased}},
				},
				Complete: true,
			})
			if verdicts[0].Verdict != domain.PruneVerdictProtected {
				t.Fatalf("verdict = %v %v, want protected", verdicts[0].Verdict, verdicts[0].Reasons)
			}
		})
	}
}
