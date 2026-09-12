package appstate_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// TestStore_RetireAppEndsIncarnation proves removing an app archives its
// ownership, clears its operational state, and detaches the name so a
// reapply allocates a new incarnation that cannot inherit the old
// secrets or volumes.
func TestStore_RetireAppEndsIncarnation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{
		App: "blog", ID: "app-old",
		Volumes: []domain.AppOwnedVolume{{
			Name: "data", Service: "web",
			RuntimeName: "gordon-blog--web--vol--data",
			State:       domain.AppResourceAttached,
		}},
		Secrets: []domain.AppOwnedSecret{{
			Service: "web", Env: "DATABASE_URL", Name: "database-url",
			Path: "gordon/apps/app-old/blog/web/database-url", State: domain.AppResourceAttached,
		}},
		Images: []domain.AppOwnedImage{{
			Service: "web", Reference: "registry.example.com/blog/web:1.4.2",
			Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			State:  domain.AppResourceAttached,
		}},
	}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{
		App: "blog",
		Services: map[string]domain.AppEffectiveService{
			"web": {Container: "c-old", EffectiveRevision: "rev-old"},
		},
	}))
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog", Stopped: true}))

	require.NoError(t, store.RetireApp(ctx, "blog"))

	ownership, err := store.LoadOwnership(ctx, "blog")
	require.NoError(t, err)
	assert.Empty(t, ownership.ID, "the name must not retain the old incarnation UUID")
	assert.Empty(t, ownership.Volumes)
	assert.Empty(t, ownership.Secrets)

	_, ok, err := store.LoadActive(ctx, "blog")
	require.NoError(t, err)
	assert.False(t, ok, "active state must be cleared")

	_, ok, err = store.LoadDesired(ctx, "blog")
	require.NoError(t, err)
	assert.False(t, ok, "desired state must be cleared")

	intent, err := store.LoadIntent(ctx, "blog")
	require.NoError(t, err)
	assert.False(t, intent.Stopped, "intent must be cleared")

	// Reapply: the first write after retirement allocates a new incarnation.
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{App: "blog"}))
	reused, err := store.LoadOwnership(ctx, "blog")
	require.NoError(t, err)
	require.NotEmpty(t, reused.ID)
	assert.NotEqual(t, "app-old", reused.ID, "a reused name must get a new incarnation")

	// The retired incarnation's resources remain protected (prune reads the
	// archived record), so they are retained rather than adopted or deleted.
	snapshot, err := store.ProtectionSnapshot(ctx)
	require.NoError(t, err)
	assert.True(t, containsVolumeClaim(snapshot.VolumeClaims, "gordon-blog--web--vol--data", domain.VolumeClaimRetained),
		"retained volumes must stay protected after retirement")
	claim, ok := snapshot.ImageClaimFor("registry.example.com/blog/web:1.4.2")
	if !ok {
		t.Fatalf("retired image claim missing from %+v", snapshot.ImageClaims)
	}
	assert.Equal(t, domain.VolumeClaimReleased, claim.State, "a removed app releases its pinned images")
}

func TestStore_RetireAppIsAtomicOnEmptyApp(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.RetireApp(context.Background(), "never-deployed"))

	ownership, err := store.LoadOwnership(context.Background(), "never-deployed")
	require.NoError(t, err)
	assert.Empty(t, ownership.ID)
}

// TestProtectionSnapshot_OperationDigestRootCarriesRepository proves an
// unfinished operation's digest root is repository-qualified so prune can
// walk that manifest's closure.
func TestProtectionSnapshot_OperationDigestRootCarriesRepository(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	const manifestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	require.NoError(t, store.SaveOperation(ctx, domain.AppOperation{
		Op: "op-open", Kind: "deploy", App: "blog",
		Steps: []domain.AppOperationStep{{
			ID: "service.web.replace", State: domain.AppStepPending,
			Digest: manifestDigest,
			Image:  "registry.example.com/blog/web:1.4.2",
		}},
	}))

	snapshot, err := store.ProtectionSnapshot(ctx)
	require.NoError(t, err)

	var digestRoot *domain.ProtectionRoot
	for i := range snapshot.Roots {
		if snapshot.Roots[i].Ref == manifestDigest {
			digestRoot = &snapshot.Roots[i]
		}
	}
	if digestRoot == nil {
		t.Fatalf("operation digest root missing from %+v", snapshot.Roots)
	}
	assert.Equal(t, "blog/web", digestRoot.Repository)
}

func containsVolumeClaim(claims []domain.VolumeClaim, name string, state domain.VolumeClaimState) bool {
	for _, claim := range claims {
		if claim.Name == name && claim.State == state {
			return true
		}
	}
	return false
}
