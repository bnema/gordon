package pruneguard

import (
	"testing"

	"github.com/bnema/gordon/internal/domain"
)

func TestIsPlaceholderRepoTag(t *testing.T) {
	tests := []struct {
		tag  string
		want bool
	}{
		{"", true},
		{"<none>", true},
		{"<none>:<none>", true},
		{"registry.example.com/shop:<none>", true},
		{"localhost:5000/shop:<none>", true},
		{"<none>@" + digest("a"), true},
		{"registry.example.com/shop:v1", false},
		{"localhost:5000/shop:latest", false},
		{"shop", false},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			if got := isPlaceholderRepoTag(tt.tag); got != tt.want {
				t.Fatalf("isPlaceholderRepoTag(%q) = %v, want %v", tt.tag, got, tt.want)
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
			if got.Verdict != tt.want || len(got.Reasons) == 0 || got.Reasons[0] != tt.wantReason {
				t.Fatalf("verdict = %v %v, want %v %v", got.Verdict, got.Reasons, tt.want, tt.wantReason)
			}
		})
	}
}

// TestPlanRuntimeImagesIncludeUnmanagedDangling proves the opt-in only
// releases unused, untagged images with no Gordon provenance, and that
// every other gate still holds.
func TestPlanRuntimeImagesIncludeUnmanagedDangling(t *testing.T) {
	id := digest("d")
	untagged := domain.RuntimeImage{ID: id, RepoTags: []string{"docker.io/library/foo:<none>"}}

	tests := []struct {
		name       string
		image      domain.RuntimeImage
		containers []domain.RuntimeContainerUse
		optIn      bool
		complete   bool
		want       domain.PruneVerdict
		wantReason domain.PruneReason
	}{
		{"off keeps unmanaged protected", untagged, nil, false, true, domain.PruneVerdictProtected, domain.PruneReasonProtectedUnmanaged},
		{"opt-in makes unmanaged eligible", untagged, nil, true, true, domain.PruneVerdictEligible, domain.PruneReasonEligibleUnmanagedDanglingImage},
		{
			"stopped container use still protects", untagged,
			[]domain.RuntimeContainerUse{{ContainerID: "stopped", ImageID: id}}, true, true,
			domain.PruneVerdictProtected, domain.PruneReasonProtectedContainerUse,
		},
		{
			"tagged image still protected",
			domain.RuntimeImage{ID: id, RepoTags: []string{"docker.io/library/foo:1"}}, nil, true, true,
			domain.PruneVerdictProtected, domain.PruneReasonProtectedTagged,
		},
		{
			"managed labels still protected",
			domain.RuntimeImage{ID: id, Labels: map[string]string{domain.LabelManaged: "true"}}, nil, true, true,
			domain.PruneVerdictProtected, domain.PruneReasonProtectedManagedOnly,
		},
		{"incomplete inventory stays unknown", untagged, nil, true, false, domain.PruneVerdictUnknown, domain.PruneReasonUnknownInventory},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdicts := PlanRuntimeImages(RuntimePlanInput{
				Images:                   []domain.RuntimeImage{tt.image},
				Containers:               tt.containers,
				Snapshot:                 &domain.PruneProtectionSnapshot{},
				Complete:                 tt.complete,
				IncludeUnmanagedDangling: tt.optIn,
			})
			got := verdicts[0]
			if got.Verdict != tt.want || len(got.Reasons) == 0 || got.Reasons[0] != tt.wantReason {
				t.Fatalf("verdict = %v %v, want %v %v", got.Verdict, got.Reasons, tt.want, tt.wantReason)
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
