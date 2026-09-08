package appstate_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/out/appstate"
	"github.com/bnema/gordon/internal/domain"
)

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

var (
	storesMu  sync.RWMutex
	storeDirs = map[*appstate.Store]string{}
)

func newTestStore(t *testing.T) *appstate.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := appstate.NewStore(dir, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = dir })
	storesMu.Lock()
	storeDirs[store] = dir
	storesMu.Unlock()
	return store
}

// testDataDir returns the data dir backing a test store.
func testDataDir(t *testing.T, store *appstate.Store) string {
	t.Helper()
	storesMu.RLock()
	defer storesMu.RUnlock()
	dir, ok := storeDirs[store]
	require.True(t, ok, "unknown test store")
	return dir
}

func testIntent(app, rev string) domain.AppApplyIntent {
	return domain.AppApplyIntent{
		Intent:   "apply-test-" + rev,
		App:      app,
		Revision: rev,
		Spec: domain.AppSpec{
			Name: app,
			Services: []domain.AppService{
				{Name: "web", Image: "img:1", Replicas: 1, StopGrace: 10 * time.Second},
			},
		},
		Reservations: []domain.AppListenerReservation{
			{Proto: "http", Host: app + ".example.com", Service: "web", App: app},
		},
		CreatedAt: time.Now().UTC(),
	}
}

func TestStore_StageCommitMaterialize(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	require.NoError(t, store.StageApply(ctx, testIntent("blog", "rev-1")))
	// Not visible before commit.
	_, ok, err := store.LoadDesired(ctx, "blog")
	require.NoError(t, err)
	assert.False(t, ok)

	require.NoError(t, store.CommitApply(ctx, "blog", "apply-test-rev-1"))
	require.NoError(t, store.MaterializeApply(ctx, "blog", "apply-test-rev-1"))

	desired, ok, err := store.LoadDesired(ctx, "blog")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "rev-1", desired.Revision)
	assert.Equal(t, "pending", desired.Status)

	// Idempotent replay.
	require.NoError(t, store.MaterializeApply(ctx, "blog", "apply-test-rev-1"))

	// Checkpoint folded.
	checkpoint, err := store.LoadCheckpoint(ctx)
	require.NoError(t, err)
	require.Len(t, checkpoint.Reservations, 1)
	assert.Equal(t, "blog.example.com", checkpoint.Reservations[0].Host)

	// Commit of a committed intent fails.
	require.ErrorIs(t, store.CommitApply(ctx, "blog", "apply-test-rev-1"), domain.ErrAppStateConflict)
	// Materialize of unknown intent fails.
	require.ErrorIs(t, store.MaterializeApply(ctx, "blog", "apply-nope"), domain.ErrAppIntentNotFound)
}

func TestStore_RecoverCompletesCommitted(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	require.NoError(t, store.StageApply(ctx, testIntent("blog", "rev-1")))
	require.NoError(t, store.CommitApply(ctx, "blog", "apply-test-rev-1"))
	// Simulate crash: never materialized. Recover finishes it.
	require.NoError(t, store.Recover(ctx))

	desired, ok, err := store.LoadDesired(ctx, "blog")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "rev-1", desired.Revision)
}

func TestStore_ConcurrentSamePortCrossApp(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	// Two apps claiming the same host: store accepts both intents
	// (conflict detection lives in the use case); the checkpoint folds
	// per-app, and ListApps stays consistent under concurrency.
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range 4 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			app := []string{"a", "b", "c", "d"}[i]
			intent := testIntent(app, "rev-1")
			if err := store.StageApply(ctx, intent); err != nil {
				errs[i] = err
				return
			}
			if err := store.CommitApply(ctx, app, intent.Intent); err != nil {
				errs[i] = err
				return
			}
			errs[i] = store.MaterializeApply(ctx, app, intent.Intent)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		assert.NoError(t, err)
	}
	apps, err := store.ListApps(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c", "d"}, apps)
}

func TestStore_GarbageProtectsReferenced(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	require.NoError(t, store.StageApply(ctx, testIntent("blog", "rev-1")))
	require.NoError(t, store.CommitApply(ctx, "blog", "apply-test-rev-1"))
	require.NoError(t, store.MaterializeApply(ctx, "blog", "apply-test-rev-1"))
	require.NoError(t, store.StageApply(ctx, testIntent("blog", "rev-2")))
	require.NoError(t, store.CommitApply(ctx, "blog", "apply-test-rev-2"))
	require.NoError(t, store.MaterializeApply(ctx, "blog", "apply-test-rev-2"))

	// rev-1 is superseded but still listed; GC with no in-flight keeps
	// retention count (2 < 32) — both survive.
	require.NoError(t, store.CollectGarbage(ctx, "blog", nil))
	revs, err := store.ListRevisions(ctx, "blog")
	require.NoError(t, err)
	assert.Contains(t, revs, "rev-1")
	assert.Contains(t, revs, "rev-2")

	// Mark rev-2 active; GC must never evict it even with in-flight noise.
	active := domain.AppActive{
		App: "blog", ConvergedRevision: "rev-2", Converged: true,
		Services: map[string]domain.AppEffectiveService{
			"web": {EffectiveRevision: "rev-2", Image: "img:1"},
		},
	}
	require.NoError(t, store.SaveActive(ctx, active))
	require.NoError(t, store.CollectGarbage(ctx, "blog", []string{"rev-99"}))
	_, err = store.LoadRevision(ctx, "blog", "rev-2")
	require.NoError(t, err)
}

func TestStore_CorruptAndIncompatible(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	// Corrupt desired.json → scoped corrupt error, other apps unaffected.
	require.NoError(t, store.StageApply(ctx, testIntent("blog", "rev-1")))
	require.NoError(t, store.CommitApply(ctx, "blog", "apply-test-rev-1"))
	require.NoError(t, store.MaterializeApply(ctx, "blog", "apply-test-rev-1"))
	dataDir := testDataDir(t, store)
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "apps", "blog", "desired.json"), []byte("{nope"), 0o640))
	_, _, err := store.LoadDesired(ctx, "blog")
	require.ErrorIs(t, err, domain.ErrAppStateCorrupt)

	require.NoError(t, store.StageApply(ctx, testIntent("other", "rev-1")))
	require.NoError(t, store.CommitApply(ctx, "other", "apply-test-rev-1"))
	require.NoError(t, store.MaterializeApply(ctx, "other", "apply-test-rev-1"))
	_, ok, err := store.LoadDesired(ctx, "other")
	require.NoError(t, err)
	assert.True(t, ok)

	// Incompatible version → all mutations refused via checkpoint load.
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "apps", "store.json"), []byte(`{"version":99}`), 0o640))
	_, err = store.LoadCheckpoint(ctx)
	require.ErrorIs(t, err, domain.ErrAppStateIncompatible)
}

func TestStore_OperationJournal(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	op := domain.AppOperation{
		Op: "op-1", Kind: "deploy", App: "blog", InputRevision: "rev-1",
		StartedAt: time.Now().UTC(),
		Steps:     []domain.AppOperationStep{{ID: "preflight", State: "succeeded"}},
	}
	require.NoError(t, store.SaveOperation(ctx, op))
	loaded, err := store.LoadOperation(ctx, "blog", "op-1")
	require.NoError(t, err)
	assert.Equal(t, "rev-1", loaded.InputRevision)
	require.Len(t, loaded.Steps, 1)

	_, err = store.LoadOperation(ctx, "blog", "op-nope")
	require.ErrorIs(t, err, domain.ErrAppOperationNotFound)
}

func TestStore_IntentAndOwnership(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	intent, err := store.LoadIntent(ctx, "fresh")
	require.NoError(t, err)
	assert.False(t, intent.Stopped)

	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "fresh", Stopped: true, UpdatedBy: "op-1", UpdatedAt: time.Now().UTC()}))
	intent, err = store.LoadIntent(ctx, "fresh")
	require.NoError(t, err)
	assert.True(t, intent.Stopped)

	ownership, err := store.LoadOwnership(ctx, "fresh")
	require.NoError(t, err)
	assert.Equal(t, "fresh", ownership.App)
	ownership.Volumes = []domain.AppOwnedVolume{{Name: "d", Service: "db", RuntimeName: "gordon-fresh--db--vol--d", State: "attached"}}
	require.NoError(t, store.SaveOwnership(ctx, ownership))
	ownership, err = store.LoadOwnership(ctx, "fresh")
	require.NoError(t, err)
	require.Len(t, ownership.Volumes, 1)
}
