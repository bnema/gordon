package appstate_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// TestStore_CommitApplyRejectsSupersededIntent proves an apply that stages
// against an outdated desired revision cannot commit once a newer apply has
// published: concurrent applies can never overwrite each other silently.
func TestStore_CommitApplyRejectsSupersededIntent(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	require.NoError(t, store.StageApply(ctx, testIntent("blog", "rev-1")))
	require.NoError(t, store.CommitApply(ctx, "blog", "apply-test-rev-1"))
	require.NoError(t, store.MaterializeApply(ctx, "blog", "apply-test-rev-1"))

	// Staged while no desired state existed; by commit time rev-1 is live.
	require.NoError(t, store.StageApply(ctx, testIntent("blog", "rev-2")))
	require.ErrorIs(t, store.CommitApply(ctx, "blog", "apply-test-rev-2"), domain.ErrAppStateConflict)
}

// TestStore_RecoverDropsSupersededCommittedIntent proves recovery never
// overwrites newer desired state with a committed intent that lost the race.
func TestStore_RecoverDropsSupersededCommittedIntent(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	require.NoError(t, store.StageApply(ctx, testIntent("blog", "rev-1")))
	require.NoError(t, store.CommitApply(ctx, "blog", "apply-test-rev-1"))
	require.NoError(t, store.MaterializeApply(ctx, "blog", "apply-test-rev-1"))

	// A committed intent that never materialized.
	require.NoError(t, store.StageApply(ctx, testIntentAfter("blog", "rev-2", "rev-1")))
	require.NoError(t, store.CommitApply(ctx, "blog", "apply-test-rev-2"))

	// A newer apply wins and publishes rev-3.
	require.NoError(t, store.StageApply(ctx, testIntentAfter("blog", "rev-3", "rev-1")))
	require.NoError(t, store.CommitApply(ctx, "blog", "apply-test-rev-3"))
	require.NoError(t, store.MaterializeApply(ctx, "blog", "apply-test-rev-3"))

	require.NoError(t, store.Recover(ctx))

	desired, ok, err := store.LoadDesired(ctx, "blog")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "rev-3", desired.Revision, "the superseded intent must never overwrite newer state")
	_, err = store.LoadApplyIntent(ctx, "blog", "apply-test-rev-2")
	require.ErrorIs(t, err, domain.ErrAppIntentNotFound, "the stale intent is dropped")
}

// TestStore_CollectGarbagePreservesInFlightIntent proves a live apply's
// staged intent is never swept as an orphan.
func TestStore_CollectGarbagePreservesInFlightIntent(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	require.NoError(t, store.StageApply(ctx, testIntent("blog", "rev-inflight")))
	require.NoError(t, store.CollectGarbage(ctx, "blog", []string{"apply-test-rev-inflight"}))

	_, err := store.LoadApplyIntent(ctx, "blog", "apply-test-rev-inflight")
	require.NoError(t, err, "an in-flight intent must survive garbage collection")

	// Without the in-flight marker it is an orphan and is swept.
	require.NoError(t, store.CollectGarbage(ctx, "blog", nil))
	_, err = store.LoadApplyIntent(ctx, "blog", "apply-test-rev-inflight")
	require.ErrorIs(t, err, domain.ErrAppIntentNotFound)
}
