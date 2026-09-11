package appstate_test

import (
	"context"
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

func newTestStore(t *testing.T) *appstate.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := appstate.NewStore(dir, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() {
		assert.NoError(t, store.Close())
	})
	return store
}

func testIntent(app, rev string) domain.AppApplyIntent {
	return testIntentAfter(app, rev, "")
}

// testIntentAfter builds an intent that expects a specific former desired
// revision, as the apply use case does.
func testIntentAfter(app, rev, supersedes string) domain.AppApplyIntent {
	intent := domain.AppApplyIntent{
		Intent:   "apply-test-" + rev,
		App:      app,
		Revision: rev,
		Spec: domain.AppSpec{
			Name: app,
			Services: []domain.AppService{
				{Name: "web", Image: "img:1", StopGrace: 10 * time.Second},
			},
		},
		Reservations: []domain.AppListenerReservation{
			{Proto: "http", Host: app + ".example.com", Service: "web", App: app},
		},
		CreatedAt: time.Now().UTC(),
	}
	intent.Supersedes = supersedes
	return intent
}

func backendClaim(app, service, container string, port int) domain.AppListenerReservation {
	return domain.AppListenerReservation{
		Proto: "tcp", IP: "127.0.0.1", Port: port,
		Service: service, App: app,
		Owner: domain.OwnerGordonBackend, ContainerID: container,
	}
}

// TestStore_BackendBindsRegisterRelease proves the gordon-backend claim
// lifecycle: registration records distinguishable loopback claims,
// same-app replacement claims coexist, release drops exactly the retired
// container's claims.
func TestStore_BackendBindsRegisterRelease(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	require.NoError(t, store.RegisterBackendBinds(ctx, []domain.AppListenerReservation{
		backendClaim("blog", "web", "c-old", 18080),
	}))
	// Replacement coexists (different container, different port).
	require.NoError(t, store.RegisterBackendBinds(ctx, []domain.AppListenerReservation{
		backendClaim("blog", "web", "c-new", 18081),
	}))

	checkpoint, err := store.LoadCheckpoint(ctx)
	require.NoError(t, err)
	backends := 0
	for _, res := range checkpoint.Reservations {
		if res.Owner == domain.OwnerGordonBackend {
			backends++
			assert.Equal(t, "127.0.0.1", res.IP)
		}
	}
	assert.Equal(t, 2, backends)

	// Release drops exactly the retired container's claim.
	require.NoError(t, store.ReleaseBackendBinds(ctx, "blog", "c-old"))
	checkpoint, err = store.LoadCheckpoint(ctx)
	require.NoError(t, err)
	backends = 0
	for _, res := range checkpoint.Reservations {
		if res.Owner == domain.OwnerGordonBackend {
			backends++
			assert.Equal(t, "c-new", res.ContainerID)
		}
	}
	assert.Equal(t, 1, backends)

	// Unknown release is ignored.
	require.NoError(t, store.ReleaseBackendBinds(ctx, "blog", "c-gone"))
}

// TestStore_BackendBindsConflictCrossApp proves fail-closed registration:
// another app's identical loopback claim conflicts; same-app claims never
// self-conflict.
func TestStore_BackendBindsConflictCrossApp(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	require.NoError(t, store.RegisterBackendBinds(ctx, []domain.AppListenerReservation{
		backendClaim("blog", "web", "c-blog", 18080),
	}))
	err := store.RegisterBackendBinds(ctx, []domain.AppListenerReservation{
		backendClaim("shop", "web", "c-shop", 18080),
	})
	require.ErrorIs(t, err, domain.ErrAppReservationConflict)

	// Same app re-registering (restart re-inspection) succeeds.
	require.NoError(t, store.RegisterBackendBinds(ctx, []domain.AppListenerReservation{
		backendClaim("blog", "web", "c-blog", 18080),
	}))
}

// TestStore_BackendBindsUDPProtocol proves UDP claims register alongside
// TCP claims on the same host port without conflict (distinct sockets),
// while a second UDP claim on that port from another app conflicts.
func TestStore_BackendBindsUDPProtocol(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	tcp := backendClaim("blog", "web", "c-blog", 19000)
	udp := backendClaim("blog", "web", "c-blog", 19000)
	udp.Proto = "udp"
	require.NoError(t, store.RegisterBackendBinds(ctx, []domain.AppListenerReservation{tcp, udp}))

	otherUDP := backendClaim("shop", "web", "c-shop", 19000)
	otherUDP.Proto = "udp"
	err := store.RegisterBackendBinds(ctx, []domain.AppListenerReservation{otherUDP})
	require.ErrorIs(t, err, domain.ErrAppReservationConflict)

	// Non-backend protocols are rejected.
	bad := backendClaim("blog", "web", "c-blog", 19001)
	bad.Proto = "sctp"
	require.ErrorIs(t, store.RegisterBackendBinds(ctx, []domain.AppListenerReservation{bad}), domain.ErrAppReservationConflict)
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
	require.NoError(t, store.StageApply(ctx, testIntentAfter("blog", "rev-2", "rev-1")))
	require.NoError(t, store.CommitApply(ctx, "blog", "apply-test-rev-2"))
	require.NoError(t, store.MaterializeApply(ctx, "blog", "apply-test-rev-2"))

	// rev-1 is superseded but still listed; GC with no in-flight keeps
	// retention count (2 < 8) — both survive.
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

	// Corrupt desired record → scoped corrupt error, other apps unaffected.
	require.NoError(t, store.StageApply(ctx, testIntent("blog", "rev-1")))
	require.NoError(t, store.CommitApply(ctx, "blog", "apply-test-rev-1"))
	require.NoError(t, store.MaterializeApply(ctx, "blog", "apply-test-rev-1"))
	require.NoError(t, appstate.CorruptDesiredForTest(store, "blog", func([]byte) []byte { return []byte("{nope") }))
	_, _, err := store.LoadDesired(ctx, "blog")
	require.ErrorIs(t, err, domain.ErrAppStateCorrupt)

	require.NoError(t, store.StageApply(ctx, testIntent("other", "rev-1")))
	require.NoError(t, store.CommitApply(ctx, "other", "apply-test-rev-1"))
	require.NoError(t, store.MaterializeApply(ctx, "other", "apply-test-rev-1"))
	_, ok, err := store.LoadDesired(ctx, "other")
	require.NoError(t, err)
	assert.True(t, ok)

	// Incompatible version → checkpoint load refuses the database.
	require.NoError(t, appstate.CorruptCheckpointForTest(store, func([]byte) []byte { return []byte(`{"version":99}`) }))
	_, err = store.LoadCheckpoint(ctx)
	require.ErrorIs(t, err, domain.ErrAppStateIncompatible)
}

func TestStore_GarbageSweepsStagedOrphan(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	// Staged but never committed = orphan. GC removes it; committed stays.
	require.NoError(t, store.StageApply(ctx, testIntent("blog", "rev-orphan")))
	require.NoError(t, store.StageApply(ctx, testIntent("blog", "rev-keep")))
	require.NoError(t, store.CommitApply(ctx, "blog", "apply-test-rev-keep"))
	require.NoError(t, store.CollectGarbage(ctx, "blog", nil))

	_, err := store.LoadApplyIntent(ctx, "blog", "apply-test-rev-orphan")
	require.ErrorIs(t, err, domain.ErrAppIntentNotFound)
	_, err = store.LoadApplyIntent(ctx, "blog", "apply-test-rev-keep")
	require.NoError(t, err)
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
	ownership.ID = "app-uuid-1"
	ownership.Volumes = []domain.AppOwnedVolume{{Name: "d", Service: "db", RuntimeName: "gordon-fresh--db--vol--d", State: "attached"}}
	require.NoError(t, store.SaveOwnership(ctx, ownership))
	ownership, err = store.LoadOwnership(ctx, "fresh")
	require.NoError(t, err)
	require.Len(t, ownership.Volumes, 1)
	assert.Equal(t, "app-uuid-1", ownership.ID)
}

func TestStore_RetentionEvictsUnreferencedBeyondDefault(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	// Apply 10 revisions; only desired stays protected. Default keeps 8
	// unreferenced + every protected reference.
	previous := ""
	for i := 1; i <= 10; i++ {
		rev := "rev-" + itoa2(i)
		require.NoError(t, store.StageApply(ctx, testIntentAfter("blog", rev, previous)))
		require.NoError(t, store.CommitApply(ctx, "blog", "apply-test-"+rev))
		require.NoError(t, store.MaterializeApply(ctx, "blog", "apply-test-"+rev))
		previous = rev
	}
	require.NoError(t, store.CollectGarbage(ctx, "blog", nil))
	revs, err := store.ListRevisions(ctx, "blog")
	require.NoError(t, err)
	// Desired (rev-10) protected + 8 newest unreferenced kept.
	assert.Len(t, revs, domain.AppDefaultRevisionRetention+1)
	assert.Contains(t, revs, "rev-10")
	assert.NotContains(t, revs, "rev-01")
}

func TestStore_ConfiguredRetentionOverridesDefault(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	store.WithRevisionRetention(2)
	assert.Equal(t, 2, store.Retention())

	previous := ""
	for i := 1; i <= 5; i++ {
		rev := "rev-" + itoa2(i)
		require.NoError(t, store.StageApply(ctx, testIntentAfter("blog", rev, previous)))
		require.NoError(t, store.CommitApply(ctx, "blog", "apply-test-"+rev))
		require.NoError(t, store.MaterializeApply(ctx, "blog", "apply-test-"+rev))
		previous = rev
	}
	require.NoError(t, store.CollectGarbage(ctx, "blog", nil))
	revs, err := store.ListRevisions(ctx, "blog")
	require.NoError(t, err)
	// Desired (rev-05) protected + 2 newest unreferenced kept.
	assert.Len(t, revs, 3)
	assert.Contains(t, revs, "rev-05")
	assert.NotContains(t, revs, "rev-01")

	store.WithRevisionRetention(0)
	assert.Equal(t, domain.AppDefaultRevisionRetention, store.Retention())
}

func itoa2(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func TestStore_ReopenPersistsAcrossClose(t *testing.T) {
	dir := t.TempDir()
	store, err := appstate.NewStore(dir, testLogger())
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, store.StageApply(ctx, testIntent("blog", "rev-1")))
	require.NoError(t, store.CommitApply(ctx, "blog", "apply-test-rev-1"))
	require.NoError(t, store.MaterializeApply(ctx, "blog", "apply-test-rev-1"))
	require.NoError(t, store.Close())

	reopened, err := appstate.NewStore(dir, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, reopened.Close()) })
	desired, ok, err := reopened.LoadDesired(ctx, "blog")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "rev-1", desired.Revision)
}

func TestStore_OwnershipUUIDSurvivesNameReuse(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	// First incarnation owns a retained volume under its UUID.
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{
		App: "blog",
		ID:  "app-uuid-1",
		Volumes: []domain.AppOwnedVolume{
			{Name: "d", Service: "db", RuntimeName: "gordon-blog--db--vol--d", State: "retained"},
		},
	}))

	// Simulate remove freeing the name, then a new app reusing it.
	// The old record stays under the old UUID; the new app gets a new
	// UUID and must not implicitly adopt the retained volume.
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{App: "blog", ID: "app-uuid-2"}))
	ownership, err := store.LoadOwnership(ctx, "blog")
	require.NoError(t, err)
	assert.Equal(t, "app-uuid-2", ownership.ID)
	assert.Empty(t, ownership.Volumes)
}

func TestStore_RecoveryInhibitionsAreDurableAndGenerationScoped(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := appstate.NewStore(dir, testLogger())
	require.NoError(t, err)

	require.NoError(t, store.SaveRecoveryInhibition(ctx, domain.AppRecoveryInhibition{
		App: "blog", Service: "web", ContainerID: "c-old",
		Reason: domain.AppInhibitReplacementPending, Operation: "op-1",
	}))
	require.NoError(t, store.SaveRecoveryInhibition(ctx, domain.AppRecoveryInhibition{
		App: "blog", Service: "worker", ContainerID: "c-worker",
		Reason: domain.AppInhibitReplacementPending, Operation: "op-2",
	}))
	require.NoError(t, store.SaveRecoveryInhibition(ctx, domain.AppRecoveryInhibition{
		App: "wiki", Service: "web", ContainerID: "c-wiki",
		Reason: domain.AppInhibitReplacementPending, Operation: "op-3",
	}))

	// Re-saving the same generation replaces it instead of duplicating.
	require.NoError(t, store.SaveRecoveryInhibition(ctx, domain.AppRecoveryInhibition{
		App: "blog", Service: "web", ContainerID: "c-old",
		Reason: "removed", Operation: "op-4",
	}))
	blog, err := store.LoadRecoveryInhibitions(ctx, "blog")
	require.NoError(t, err)
	require.Len(t, blog, 2)
	byService := map[string]domain.AppRecoveryInhibition{}
	for _, inhibition := range blog {
		byService[inhibition.Service] = inhibition
	}
	assert.Equal(t, "removed", byService["web"].Reason)
	assert.Equal(t, "op-4", byService["web"].Operation)
	assert.Equal(t, "c-worker", byService["worker"].ContainerID)

	wiki, err := store.LoadRecoveryInhibitions(ctx, "wiki")
	require.NoError(t, err)
	require.Len(t, wiki, 1)

	// Clearing one generation never touches another.
	require.NoError(t, store.ClearRecoveryInhibition(ctx, "blog", "web", "c-old"))
	blog, err = store.LoadRecoveryInhibitions(ctx, "blog")
	require.NoError(t, err)
	require.Len(t, blog, 1)
	assert.Equal(t, "worker", blog[0].Service)
	require.NoError(t, store.ClearRecoveryInhibition(ctx, "blog", "web", "c-old"),
		"clearing an absent record is not an error")

	unknown, err := store.LoadRecoveryInhibitions(ctx, "absent")
	require.NoError(t, err)
	assert.Empty(t, unknown)

	require.NoError(t, store.Close())

	// Reboot: the markers outlive the process.
	reopened, err := appstate.NewStore(dir, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, reopened.Close()) })
	blog, err = reopened.LoadRecoveryInhibitions(ctx, "blog")
	require.NoError(t, err)
	require.Len(t, blog, 1)
	assert.Equal(t, "c-worker", blog[0].ContainerID)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := reopened.LoadRecoveryInhibitions(cancelled, "blog"); err == nil {
		t.Fatal("a cancelled context must fail the read")
	}
	require.Error(t, reopened.SaveRecoveryInhibition(cancelled, domain.AppRecoveryInhibition{App: "blog"}))
	require.Error(t, reopened.ClearRecoveryInhibition(cancelled, "blog", "worker", "c-worker"))
}
