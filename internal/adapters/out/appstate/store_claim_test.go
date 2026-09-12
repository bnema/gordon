package appstate_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/out/appstate"
	"github.com/bnema/gordon/internal/domain"
)

// knownApp gives the app a live identity so keyed mutations are allowed.
func knownApp(t *testing.T, store interface {
	SaveOwnership(context.Context, domain.AppOwnership) error
}, app string) {
	t.Helper()
	require.NoError(t, store.SaveOwnership(context.Background(), domain.AppOwnership{App: app}))
}

func deployClaim(app, key, revision string) domain.AppOperation {
	return domain.AppOperation{
		Op:        key,
		Kind:      "deploy",
		App:       app,
		StartedAt: time.Now().UTC(),
		Request:   domain.AppOperationRequestFor("deploy", app, revision, ""),
	}
}

// TestStore_ClaimOperationCreatesInFlightJournal proves an absent key is
// persisted as an in-flight claim so no effect can run twice.
func TestStore_ClaimOperationCreatesInFlightJournal(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	knownApp(t, store, "blog")

	candidate := deployClaim("blog", "key-1", "rev-1")
	existing, claimed, err := store.ClaimOperation(ctx, candidate)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, "key-1", existing.Op)
	assert.False(t, existing.Terminal(), "a fresh claim is not terminal")

	stored, err := store.LoadOperation(ctx, "blog", "key-1")
	require.NoError(t, err)
	assert.Equal(t, candidate.Request, stored.Request)
	assert.False(t, stored.Terminal())
}

// TestStore_ClaimOperationReplaysSameRequest proves a repeat of one
// request returns the stored journal instead of claiming again.
func TestStore_ClaimOperationReplaysSameRequest(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	knownApp(t, store, "blog")

	require.NoError(t, store.SaveOperation(ctx, domain.AppOperation{
		Op: "key-1", Kind: "deploy", App: "blog",
		Request:   domain.AppOperationRequestFor("deploy", "blog", "rev-1", ""),
		Outcome:   domain.AppOutcomeSuccess,
		StartedAt: time.Now().UTC(),
	}))

	existing, claimed, err := store.ClaimOperation(ctx, deployClaim("blog", "key-1", "rev-1"))
	require.NoError(t, err)
	assert.False(t, claimed)
	assert.Equal(t, domain.AppOutcomeSuccess, existing.Outcome)
}

// TestStore_ClaimOperationConflictsOnDifferentRequest proves one key can
// never answer two different requests.
func TestStore_ClaimOperationConflictsOnDifferentRequest(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	knownApp(t, store, "blog")

	_, claimed, err := store.ClaimOperation(ctx, deployClaim("blog", "key-1", "rev-1"))
	require.NoError(t, err)
	require.True(t, claimed)

	_, _, err = store.ClaimOperation(ctx, deployClaim("blog", "key-1", "rev-2"))
	require.ErrorIs(t, err, domain.ErrAppStateConflict)

	// A different kind with the same key is a different request too.
	stop := domain.AppOperation{
		Op: "key-1", Kind: "stop", App: "blog", StartedAt: time.Now().UTC(),
		Request: domain.AppOperationRequestFor("stop", "blog", "", ""),
	}
	_, _, err = store.ClaimOperation(ctx, stop)
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
}

// TestStore_ClaimOperationRefusesUnknownAppWithoutState proves a keyed
// mutation of an unknown name creates no durable state at all.
func TestStore_ClaimOperationRefusesUnknownAppWithoutState(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	_, _, err := store.ClaimOperation(ctx, deployClaim("ghost", "key-1", "rev-1"))
	require.ErrorIs(t, err, domain.ErrAppNotFound)

	apps, err := store.ListApps(ctx)
	require.NoError(t, err)
	assert.NotContains(t, apps, "ghost", "a refused claim must not create an app entry")

	_, err = store.LoadOperation(ctx, "ghost", "key-1")
	require.ErrorIs(t, err, domain.ErrAppOperationNotFound)
}

// TestStore_ClaimOperationIsAtomicUnderConcurrency proves two concurrent
// claims of one key produce exactly one winner.
func TestStore_ClaimOperationIsAtomicUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	knownApp(t, store, "blog")

	const racers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
	)
	start := make(chan struct{})
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, claimed, err := store.ClaimOperation(ctx, deployClaim("blog", "key-1", "rev-1"))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if claimed {
				winners++
			}
		}()
	}
	close(start)
	wg.Wait()
	assert.Equal(t, 1, winners, "exactly one claim may win")
}

// TestStore_ClaimOperationReplaysAfterReopen proves a claimed operation
// survives a daemon restart: the reopened store replays the stored journal
// instead of claiming the key again.
func TestStore_ClaimOperationReplaysAfterReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := appstate.NewStore(dir, testLogger())
	require.NoError(t, err)
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{App: "blog"}))

	_, claimed, err := store.ClaimOperation(ctx, deployClaim("blog", "key-1", "rev-1"))
	require.NoError(t, err)
	require.True(t, claimed)
	op := deployClaim("blog", "key-1", "rev-1")
	op.Outcome = domain.AppOutcomeSuccess
	require.NoError(t, store.SaveOperation(ctx, op))
	require.NoError(t, store.Close())

	reopened, err := appstate.NewStore(dir, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, reopened.Close()) })

	existing, claimed, err := reopened.ClaimOperation(ctx, deployClaim("blog", "key-1", "rev-1"))
	require.NoError(t, err)
	assert.False(t, claimed, "a reopened store replays the persisted claim")
	assert.Equal(t, domain.AppOutcomeSuccess, existing.Outcome)
}

// TestStore_ClaimOperationSurvivesRetire proves an operation journal
// stays reachable after removal while a new key against the freed name
// is refused.
func TestStore_ClaimOperationSurvivesRetire(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	knownApp(t, store, "blog")

	remove := domain.AppOperation{
		Op: "remove-1", Kind: "remove", App: "blog", StartedAt: time.Now().UTC(),
		Request: domain.AppOperationRequestFor("remove", "blog", "", ""),
	}
	_, claimed, err := store.ClaimOperation(ctx, remove)
	require.NoError(t, err)
	require.True(t, claimed)
	remove.Outcome = domain.AppOutcomeSuccess
	remove.Steps = []domain.AppOperationStep{{ID: "service.web.remove", State: domain.AppStepSucceeded}}
	require.NoError(t, store.SaveOperation(ctx, remove))
	require.NoError(t, store.RetireApp(ctx, "blog"))

	replayed, claimed, err := store.ClaimOperation(ctx, domain.AppOperation{
		Op: "remove-1", Kind: "remove", App: "blog",
		Request: domain.AppOperationRequestFor("remove", "blog", "", ""),
	})
	require.NoError(t, err)
	assert.False(t, claimed, "a repeated remove replays its stored result")
	assert.Equal(t, domain.AppOutcomeSuccess, replayed.Outcome)

	_, _, err = store.ClaimOperation(ctx, domain.AppOperation{
		Op: "remove-2", Kind: "remove", App: "blog",
		Request: domain.AppOperationRequestFor("remove", "blog", "", ""),
	})
	require.ErrorIs(t, err, domain.ErrAppNotFound, "a new key must not resurrect a retired name")
}
