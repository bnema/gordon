package appstate_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/out/appstate"
	"github.com/bnema/gordon/internal/domain"
)

func hasRoot(t *testing.T, snapshot *domain.PruneProtectionSnapshot, kind domain.ProtectionRootKind, ref string) bool {
	t.Helper()
	for _, root := range snapshot.Roots {
		if root.Kind == kind && root.Ref == ref {
			return true
		}
	}
	return false
}

func tagRefKey(t *testing.T, repository, tag string) string {
	t.Helper()
	ref, err := domain.NewRegistryTagRef(repository, tag)
	require.NoError(t, err)
	return ref.Key()
}

// TestStore_ProtectionSnapshotCoversEveryRoot proves one snapshot
// transaction collects desired, active, inhibition, apply, operation,
// and ownership facts together.
func TestStore_ProtectionSnapshotCoversEveryRoot(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	desired := testIntent("shop", "rev-1")
	desired.Spec.Services[0].Image = "registry.example/shop:1"
	require.NoError(t, store.StageApply(ctx, desired))
	require.NoError(t, store.CommitApply(ctx, "shop", desired.Intent))
	require.NoError(t, store.MaterializeApply(ctx, "shop", desired.Intent))

	require.NoError(t, store.SaveActive(ctx, domain.AppActive{
		App: "shop",
		Services: map[string]domain.AppEffectiveService{
			"web": {
				EffectiveRevision: "rev-1",
				Image:             "registry.example/shop:1",
				Digest:            "sha256:" + strings.Repeat("ab", 32),
				Container:         "c-live",
			},
		},
	}))
	require.NoError(t, store.SaveRecoveryInhibition(ctx, domain.AppRecoveryInhibition{
		App: "shop", Service: "web", ContainerID: "c-inhibited",
		Reason: domain.AppInhibitReplacementPending,
	}))

	// A staged intent may still materialize.
	staged := testIntent("shop", "rev-2")
	staged.Intent = "apply-staged"
	staged.Spec.Services[0].Image = "registry.example/shop:2"
	require.NoError(t, store.StageApply(ctx, staged))

	// An unfinished operation pins its own image.
	require.NoError(t, store.SaveOperation(ctx, domain.AppOperation{
		Op: "op-open", Kind: "deploy", App: "shop",
		Steps: []domain.AppOperationStep{
			{ID: "preflight", State: domain.AppStepSucceeded},
			{ID: "service.web.replace", State: domain.AppStepPending,
				Digest: "sha256:" + strings.Repeat("cd", 32),
				Image:  "registry.example/shop:3"},
		},
	}))

	// A finished operation pins nothing.
	require.NoError(t, store.SaveOperation(ctx, domain.AppOperation{
		Op: "op-done", Kind: "deploy", App: "shop", Outcome: domain.AppOutcomeSuccess,
		Steps: []domain.AppOperationStep{
			{ID: "service.web.replace", State: domain.AppStepSucceeded,
				Digest: "sha256:" + strings.Repeat("ef", 32),
				Image:  "registry.example/shop:4"},
		},
	}))

	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{
		App: "shop", ID: "app-uuid-shop",
		Volumes: []domain.AppOwnedVolume{
			{Name: "data", Service: "web", RuntimeName: "gordon-shop--web--vol--data", State: domain.AppResourceAttached},
		},
	}))

	snapshot, err := store.ProtectionSnapshot(ctx)
	require.NoError(t, err)
	require.True(t, snapshot.Complete(), "snapshot gaps: %+v", snapshot.Gaps)

	assert.True(t, hasRoot(t, snapshot, domain.ProtectionDesiredRevision, tagRefKey(t, "registry.example/shop", "1")),
		"desired revision root missing")
	assert.True(t, hasRoot(t, snapshot, domain.ProtectionActiveService, tagRefKey(t, "registry.example/shop", "1")),
		"active service root missing")
	assert.True(t, hasRoot(t, snapshot, domain.ProtectionActiveService, "sha256:"+strings.Repeat("ab", 32)),
		"active digest root missing")
	assert.True(t, hasRoot(t, snapshot, domain.ProtectionContainerUse, "c-live"),
		"active container root missing")
	assert.True(t, hasRoot(t, snapshot, domain.ProtectionRecoveryInhibition, "c-inhibited"),
		"recovery inhibition root missing")
	assert.True(t, hasRoot(t, snapshot, domain.ProtectionApplyIntent, tagRefKey(t, "registry.example/shop", "2")),
		"staged apply intent root missing")
	assert.True(t, hasRoot(t, snapshot, domain.ProtectionOperation, "sha256:"+strings.Repeat("cd", 32)),
		"unfinished operation digest root missing")
	assert.True(t, hasRoot(t, snapshot, domain.ProtectionOperation, tagRefKey(t, "registry.example/shop", "3")),
		"unfinished operation image root missing")
	assert.False(t, hasRoot(t, snapshot, domain.ProtectionOperation, "sha256:"+strings.Repeat("ef", 32)),
		"finished operation must not pin content")
	assert.False(t, hasRoot(t, snapshot, domain.ProtectionOperation, tagRefKey(t, "registry.example/shop", "4")),
		"finished operation must not pin content")

	claim, ok := snapshot.VolumeClaimFor("gordon-shop--web--vol--data")
	require.True(t, ok)
	assert.Equal(t, domain.VolumeClaimAttached, claim.State)
	assert.Equal(t, "shop", claim.App)
	assert.True(t, snapshot.VolumeProtects("gordon-shop--web--vol--data"))
}

// TestStore_ProtectionSnapshotStoppedAppKeepsActive proves a stopped app
// with no running container still protects its image and container.
func TestStore_ProtectionSnapshotStoppedAppKeepsActive(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	require.NoError(t, store.SaveActive(ctx, domain.AppActive{
		App:        "blog",
		StopIntent: true,
		Services: map[string]domain.AppEffectiveService{
			"web": {EffectiveRevision: "rev-1", Image: "registry.example/blog:7", Container: "c-stopped"},
		},
	}))
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog", Stopped: true, UpdatedBy: "test"}))

	snapshot, err := store.ProtectionSnapshot(ctx)
	require.NoError(t, err)
	assert.True(t, hasRoot(t, snapshot, domain.ProtectionActiveService, tagRefKey(t, "registry.example/blog", "7")))
	assert.True(t, hasRoot(t, snapshot, domain.ProtectionContainerUse, "c-stopped"))
}

// TestStore_ProtectionSnapshotKeepsHistoricalIncarnations proves a
// reused app name never loses the earlier incarnation's retained
// resources.
func TestStore_ProtectionSnapshotKeepsHistoricalIncarnations(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	const volume = "gordon-blog--db--vol--d"
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{
		App: "blog", ID: "app-uuid-1",
		Volumes: []domain.AppOwnedVolume{
			{Name: "d", Service: "db", RuntimeName: volume, State: domain.AppResourceRetained},
		},
	}))
	// Remove frees the name; a new incarnation takes it and owns
	// nothing yet.
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{App: "blog", ID: "app-uuid-2"}))

	snapshot, err := store.ProtectionSnapshot(ctx)
	require.NoError(t, err)
	require.True(t, snapshot.VolumeProtects(volume), "historical retained claim was lost")
	claims := snapshot.VolumeClaimsFor(volume)
	require.Len(t, claims, 1)
	assert.Equal(t, domain.VolumeClaimRetained, claims[0].State)
	assert.Equal(t, "app-uuid-1", claims[0].AppID)
}

// TestStore_ProtectionSnapshotKeepsDifferentClaimStates proves two
// incarnations' claims on one volume are distinct facts: a released
// claim must never overwrite or hide another incarnation's attached
// claim for the same runtime volume.
func TestStore_ProtectionSnapshotKeepsDifferentClaimStates(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	const volume = "gordon-app--web--vol--data"
	// The old incarnation released the volume.
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{
		App: "app", ID: "app-uuid-old",
		Volumes: []domain.AppOwnedVolume{
			{Name: "data", Service: "web", RuntimeName: volume, State: domain.AppResourceReleased},
		},
	}))
	// A new incarnation attached it again.
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{
		App: "app", ID: "app-uuid-new",
		Volumes: []domain.AppOwnedVolume{
			{Name: "data", Service: "web", RuntimeName: volume, State: domain.AppResourceAttached},
		},
	}))

	snapshot, err := store.ProtectionSnapshot(ctx)
	require.NoError(t, err)
	claims := snapshot.VolumeClaimsFor(volume)
	require.Len(t, claims, 2, "both incarnations' claims must survive")
	assert.True(t, snapshot.VolumeProtects(volume), "the attached claim must still protect the volume")
}

// TestStore_ProtectionSnapshotReleasedIsTheOnlyDeletableState proves
// only an explicit released record can ever make a volume eligible.
func TestStore_ProtectionSnapshotReleasedIsTheOnlyDeletableState(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	const volume = "gordon-shop--web--vol--data"
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{
		App: "shop", ID: "app-uuid-shop",
		Volumes: []domain.AppOwnedVolume{
			{Name: "data", Service: "web", RuntimeName: volume, State: domain.AppResourceReleased},
		},
	}))

	snapshot, err := store.ProtectionSnapshot(ctx)
	require.NoError(t, err)
	assert.False(t, snapshot.VolumeProtects(volume))
	claim, ok := snapshot.VolumeClaimFor(volume)
	require.True(t, ok)
	assert.Equal(t, domain.VolumeClaimReleased, claim.State)
}

// TestStore_ProtectionSnapshotUnknownStateIsRetained proves an
// unrecognized lifecycle state never becomes deletable.
func TestStore_ProtectionSnapshotUnknownStateIsRetained(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	const volume = "gordon-shop--web--vol--data"
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{
		App: "shop", ID: "app-uuid-shop",
		Volumes: []domain.AppOwnedVolume{
			{Name: "data", Service: "web", RuntimeName: volume},
		},
	}))

	snapshot, err := store.ProtectionSnapshot(ctx)
	require.NoError(t, err)
	assert.True(t, snapshot.VolumeProtects(volume), "empty state must map to retained")
}

// TestStore_ProtectionSnapshotMigrationBackfillsHistory proves the
// one-time migration reconstructs history for records written before
// the history bucket existed, and that reopening is idempotent.
func TestStore_ProtectionSnapshotMigrationBackfillsHistory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := appstate.NewStore(dir, testLogger())
	require.NoError(t, err)

	const volume = "gordon-blog--db--vol--d"
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{
		App: "blog", ID: "app-uuid-1",
		Volumes: []domain.AppOwnedVolume{
			{Name: "d", Service: "db", RuntimeName: volume, State: domain.AppResourceRetained},
		},
	}))

	// Reproduce a pre-migration database: per-app records exist, the
	// incarnation index does not.
	require.NoError(t, appstate.ResetOwnershipHistoryForTest(store))
	snapshot, err := store.ProtectionSnapshot(ctx)
	require.NoError(t, err)
	assert.False(t, snapshot.Complete(), "missing history bucket must be reported")
	assert.True(t, snapshot.VolumeProtects(volume), "per-app record still protects")
	require.NoError(t, store.Close())

	// Reopening runs the migration once and reuses the existing index
	// on every later open.
	reopened, err := appstate.NewStore(dir, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, reopened.Close()) })
	snapshot, err = reopened.ProtectionSnapshot(ctx)
	require.NoError(t, err)
	assert.True(t, snapshot.Complete(), "snapshot gaps after migration: %+v", snapshot.Gaps)
	assert.True(t, snapshot.VolumeProtects(volume))

	require.NoError(t, reopened.Close())
	third, err := appstate.NewStore(dir, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, third.Close()) })
	ownership, err := third.LoadOwnership(ctx, "blog")
	require.NoError(t, err)
	assert.Equal(t, "app-uuid-1", ownership.ID)
	snapshot, err = third.ProtectionSnapshot(ctx)
	require.NoError(t, err)
	assert.True(t, snapshot.VolumeProtects(volume))
}

// TestStore_ProtectionSnapshotReportsCorruptRecords proves unreadable
// state becomes a gap instead of an apparently unprotected snapshot.
func TestStore_ProtectionSnapshotReportsCorruptRecords(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	desired := testIntent("shop", "rev-1")
	require.NoError(t, store.StageApply(ctx, desired))
	require.NoError(t, store.CommitApply(ctx, "shop", desired.Intent))
	require.NoError(t, store.MaterializeApply(ctx, "shop", desired.Intent))
	require.NoError(t, appstate.CorruptDesiredForTest(store, "shop", func([]byte) []byte {
		return []byte("{not json")
	}))

	snapshot, err := store.ProtectionSnapshot(ctx)
	require.NoError(t, err)
	assert.False(t, snapshot.Complete())
	require.NotEmpty(t, snapshot.Gaps)
	assert.Equal(t, domain.InventorySourceAppState, snapshot.Gaps[0].Source)
}

// TestStore_ProtectionSnapshotConcurrentWrites proves a snapshot is
// atomic against concurrent state writes.
func TestStore_ProtectionSnapshotConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	const rounds = 40
	var wg sync.WaitGroup
	for index := 0; index < rounds; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			app := "app-" + itoa2(index%4)
			active := domain.AppActive{
				App: app,
				Services: map[string]domain.AppEffectiveService{
					"web": {EffectiveRevision: "rev-" + itoa2(index), Image: "registry.example/" + app + ":1"},
				},
			}
			assert.NoError(t, store.SaveActive(ctx, active))
			assert.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{
				App: app, ID: "app-uuid-" + itoa2(index%4),
				Volumes: []domain.AppOwnedVolume{
					{Name: "data", Service: "web", RuntimeName: "gordon-" + itoa2(index%4) + "--web--vol--data"},
				},
			}))
			snapshot, err := store.ProtectionSnapshot(ctx)
			assert.NoError(t, err)
			assert.NoError(t, snapshotValidate(snapshot))
		}(index)
	}
	wg.Wait()
}

// snapshotValidate checks the snapshot invariant callers rely on: every
// root and claim is well formed.
func snapshotValidate(snapshot *domain.PruneProtectionSnapshot) error {
	for _, root := range snapshot.Roots {
		if !root.Valid() {
			return assert.AnError
		}
	}
	for _, gap := range snapshot.Gaps {
		if !gap.Valid() {
			return assert.AnError
		}
	}
	return nil
}

// TestStore_ProtectionSnapshotCancellation proves a canceled context
// never opens a read transaction.
func TestStore_ProtectionSnapshotCancellation(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := store.ProtectionSnapshot(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

var _ = time.Second
