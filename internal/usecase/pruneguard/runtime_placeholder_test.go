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
