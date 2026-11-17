package deployment

import (
	"context"
	"strings"
	"testing"

	"bnema.dev/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/out/appstate"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/pruneguard"
)

const testImageRepo = "reg.example.com/shop/web"

func testOwnedDigest(seed string) string {
	return "sha256:" + strings.Repeat(seed, 64)[:64]
}

func pinnedWeb(tag, digest string) pinnedService {
	return pinnedService{name: "web", spec: domain.AppService{Name: "web", Image: testImageRepo + ":" + tag}, digest: digest}
}

func ownedImage(tag, digest, state string) domain.AppOwnedImage {
	return domain.AppOwnedImage{Service: "web", Reference: testImageRepo + ":" + tag, Digest: digest, State: state}
}

func TestRecordImageOwnership(t *testing.T) {
	v1, v2 := testOwnedDigest("1"), testOwnedDigest("2")
	api := domain.AppOwnedImage{Service: "api", Reference: "reg.example.com/shop/api:1", Digest: testOwnedDigest("a"), State: domain.AppResourceAttached}

	tests := []struct {
		name     string
		existing []domain.AppOwnedImage
		pinned   pinnedService
		want     []domain.AppOwnedImage
	}{
		{
			name:   "first deploy attaches",
			pinned: pinnedWeb("v1", v1),
			want:   []domain.AppOwnedImage{ownedImage("v1", v1, domain.AppResourceAttached)},
		},
		{
			name:     "supersede releases the previous digest",
			existing: []domain.AppOwnedImage{api, ownedImage("v1", v1, domain.AppResourceAttached)},
			pinned:   pinnedWeb("v2", v2),
			want: []domain.AppOwnedImage{
				api,
				ownedImage("v1", v1, domain.AppResourceReleased),
				ownedImage("v2", v2, domain.AppResourceAttached),
			},
		},
		{
			name:     "same digest redeploy replaces in place",
			existing: []domain.AppOwnedImage{ownedImage("v1", v1, domain.AppResourceAttached)},
			pinned:   pinnedWeb("latest", v1),
			want:     []domain.AppOwnedImage{ownedImage("latest", v1, domain.AppResourceAttached)},
		},
		{
			name: "rollback drops the released entry of the reattached digest",
			existing: []domain.AppOwnedImage{
				ownedImage("v1", v1, domain.AppResourceReleased),
				ownedImage("v2", v2, domain.AppResourceAttached),
			},
			pinned: pinnedWeb("v1", v1),
			want: []domain.AppOwnedImage{
				ownedImage("v2", v2, domain.AppResourceReleased),
				ownedImage("v1", v1, domain.AppResourceAttached),
			},
		},
		{
			name:     "previous entry without digest is not released",
			existing: []domain.AppOwnedImage{ownedImage("v1", "", domain.AppResourceAttached)},
			pinned:   pinnedWeb("v2", v2),
			want:     []domain.AppOwnedImage{ownedImage("v2", v2, domain.AppResourceAttached)},
		},
		{
			name:     "new entry without digest releases nothing",
			existing: []domain.AppOwnedImage{ownedImage("v1", v1, domain.AppResourceAttached)},
			pinned:   pinnedWeb("v2", ""),
			want:     []domain.AppOwnedImage{ownedImage("v2", "", domain.AppResourceAttached)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, recordImageOwnership(tt.existing, tt.pinned))
		})
	}
}

func TestRecordImageOwnershipCapsReleasedEntries(t *testing.T) {
	var images []domain.AppOwnedImage
	seeds := "123456789"
	for _, seed := range seeds {
		images = recordImageOwnership(images, pinnedWeb("v"+string(seed), testOwnedDigest(string(seed))))
	}

	require.Len(t, images, maxReleasedImagesPerService+1)
	for i, image := range images[:maxReleasedImagesPerService] {
		seed := string(seeds[len(seeds)-1-maxReleasedImagesPerService+i])
		assert.Equal(t, testOwnedDigest(seed), image.Digest, "oldest released entries are dropped first")
		assert.Equal(t, domain.AppResourceReleased, image.State)
	}
	assert.Equal(t, ownedImage("v9", testOwnedDigest("9"), domain.AppResourceAttached), images[len(images)-1])
}

// deployOwnership records each pinned digest in turn through the durable
// store, as successive successful deploys do.
func deployOwnership(t *testing.T, store *appstate.Store, deploys ...pinnedService) {
	t.Helper()
	ctx := context.Background()
	for _, p := range deploys {
		ownership, err := store.LoadOwnership(ctx, "shop")
		require.NoError(t, err)
		ownership.App = "shop"
		ownership.Images = recordImageOwnership(ownership.Images, p)
		require.NoError(t, store.SaveOwnership(ctx, ownership))
	}
}

func planSupersededImage(t *testing.T, store *appstate.Store, digest string) domain.RuntimeImageVerdict {
	t.Helper()
	snapshot, err := store.ProtectionSnapshot(context.Background())
	require.NoError(t, err)
	verdicts := pruneguard.PlanRuntimeImages(pruneguard.RuntimePlanInput{
		Images: []domain.RuntimeImage{{
			ID:          testOwnedDigest("f"),
			RepoTags:    []string{testImageRepo + ":<none>"},
			RepoDigests: []string{testImageRepo + "@" + digest},
			Labels:      map[string]string{domain.LabelApp: "shop"},
		}},
		Snapshot: snapshot,
		Complete: true,
	})
	require.Len(t, verdicts, 1)
	return verdicts[0]
}

// TestSupersededImageIsPrunableAcrossSeam drives deploy ownership through
// the durable store into the runtime prune planner.
func TestSupersededImageIsPrunableAcrossSeam(t *testing.T) {
	v1, v2 := testOwnedDigest("1"), testOwnedDigest("2")

	t.Run("v1 to v2 makes superseded v1 eligible", func(t *testing.T) {
		store, err := appstate.NewStore(t.TempDir(), zerowrap.Default())
		require.NoError(t, err)
		deployOwnership(t, store, pinnedWeb("v1", v1), pinnedWeb("v2", v2))

		verdict := planSupersededImage(t, store, v1)
		assert.Equal(t, domain.PruneVerdictEligible, verdict.Verdict)
		assert.Equal(t, []domain.PruneReason{domain.PruneReasonEligibleDanglingRuntimeImage}, verdict.Reasons)
	})

	t.Run("rollback to v1 keeps v1 attached", func(t *testing.T) {
		store, err := appstate.NewStore(t.TempDir(), zerowrap.Default())
		require.NoError(t, err)
		deployOwnership(t, store, pinnedWeb("v1", v1), pinnedWeb("v2", v2), pinnedWeb("v1", v1))

		verdict := planSupersededImage(t, store, v1)
		assert.Equal(t, domain.PruneVerdictProtected, verdict.Verdict)
		assert.Equal(t, []domain.PruneReason{domain.PruneReasonProtectedOwnership}, verdict.Reasons)
		assert.Equal(t, domain.PruneVerdictEligible, planSupersededImage(t, store, v2).Verdict)
	})

	t.Run("released image named by an active root stays protected", func(t *testing.T) {
		store, err := appstate.NewStore(t.TempDir(), zerowrap.Default())
		require.NoError(t, err)
		deployOwnership(t, store, pinnedWeb("v1", v1), pinnedWeb("v2", v2))
		require.NoError(t, store.SaveActive(context.Background(), domain.AppActive{
			App: "shop",
			Services: map[string]domain.AppEffectiveService{
				"web": {Image: testImageRepo + ":v1", Digest: v1},
			},
		}))

		verdict := planSupersededImage(t, store, v1)
		assert.Equal(t, domain.PruneVerdictProtected, verdict.Verdict)
		assert.Equal(t, []domain.PruneReason{domain.PruneReasonProtectedActiveService}, verdict.Reasons)
	})

	t.Run("retire keeps released entries released without duplicates", func(t *testing.T) {
		store, err := appstate.NewStore(t.TempDir(), zerowrap.Default())
		require.NoError(t, err)
		deployOwnership(t, store, pinnedWeb("v1", v1), pinnedWeb("v2", v2))
		require.NoError(t, store.RetireApp(context.Background(), "shop"))

		snapshot, err := store.ProtectionSnapshot(context.Background())
		require.NoError(t, err)
		require.Len(t, snapshot.ImageClaims, 2)
		for _, claim := range snapshot.ImageClaims {
			assert.Equal(t, domain.VolumeClaimReleased, claim.State)
		}
		assert.Equal(t, domain.PruneVerdictEligible, planSupersededImage(t, store, v1).Verdict)
	})
}
