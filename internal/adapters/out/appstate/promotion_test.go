package appstate_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// promotionAfter builds a promotion revision superseding base.
func promotionAfter(base domain.AppDesiredRevision, revision, appID, key string) (domain.AppDesiredRevision, domain.AppOperation) {
	rev := base
	rev.Revision, rev.Supersedes = revision, base.Revision
	rev.Promotion = &domain.AppPromotionProvenance{AppID: appID, Image: "registry/web@sha256:abc", Digest: "sha256:abc"}
	op := domain.AppOperation{App: base.App, Op: key, Kind: domain.AppOperationKindPromote, Outcome: domain.AppOutcomeSuccess,
		Request: domain.AppOperationRequest{Kind: domain.AppOperationKindPromote, App: base.App, Service: "web", Image: key}}
	return rev, op
}

// TestAcceptPromotion_CommittedApplyWins proves CommitApply stays the single
// commit point of an apply: a promotion that lands between CommitApply and
// MaterializeApply materializes the committed intent first, so its CAS fails
// and the apply is never dropped.
func TestAcceptPromotion_CommittedApplyWins(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	require.NoError(t, store.AcceptApply(ctx, testIntent("web", "r1")))
	owner, err := store.LoadOwnership(ctx, "web")
	require.NoError(t, err)
	base, _, err := store.LoadDesired(ctx, "web")
	require.NoError(t, err)

	intent := testIntentAfter("web", "r2", "r1")
	require.NoError(t, store.StageApply(ctx, intent))
	require.NoError(t, store.CommitApply(ctx, "web", intent.Intent))

	rev, op := promotionAfter(base, "r-promo", owner.ID, "key")
	_, _, err = store.AcceptPromotion(ctx, rev, op)
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	assert.Contains(t, err.Error(), `expected desired "r1" but found "r2"`, "the CAS lost against the materialized apply")

	require.NoError(t, store.MaterializeApply(ctx, "web", intent.Intent), "materialize stays idempotent")
	desired, _, err := store.LoadDesired(ctx, "web")
	require.NoError(t, err)
	assert.Equal(t, "r2", desired.Revision, "the committed apply is not lost")
	_, err = store.LoadRevision(ctx, "web", "r-promo")
	require.ErrorIs(t, err, domain.ErrAppRevisionNotFound)
	_, err = store.LoadOperation(ctx, "web", "key")
	require.ErrorIs(t, err, domain.ErrAppOperationNotFound)
}

// TestAcceptPromotion_RespectsRevisionRetention proves promotions sweep
// unreferenced revisions beyond retention in the accepting transaction and
// never sweep a protected one (desired, active, or an in-flight intent).
func TestAcceptPromotion_RespectsRevisionRetention(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t).WithRevisionRetention(2)
	require.NoError(t, store.AcceptApply(ctx, testIntent("web", "r00")))
	owner, err := store.LoadOwnership(ctx, "web")
	require.NoError(t, err)
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{App: "web", Services: map[string]domain.AppEffectiveService{
		"web": {EffectiveRevision: "r00"},
	}}))
	for i := 1; i <= 6; i++ {
		base, _, err := store.LoadDesired(ctx, "web")
		require.NoError(t, err)
		rev, op := promotionAfter(base, fmt.Sprintf("r%02d", i), owner.ID, fmt.Sprintf("key-%d", i))
		_, claimed, err := store.AcceptPromotion(ctx, rev, op)
		require.NoError(t, err)
		require.True(t, claimed)
	}
	kept := []string{}
	for i := 0; i <= 6; i++ {
		id := fmt.Sprintf("r%02d", i)
		if _, err := store.LoadRevision(ctx, "web", id); err == nil {
			kept = append(kept, id)
		}
	}
	assert.Equal(t, []string{"r00", "r04", "r05", "r06"}, kept,
		"active r00 and desired r06 are protected; two unreferenced revisions are retained")
}

// TestAcceptPromotion_IncarnationFallsBackToAppRecord proves the
// incarnation check reads the same identity LoadOwnership reports when the
// ownership record is absent.
func TestAcceptPromotion_IncarnationFallsBackToAppRecord(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	require.NoError(t, store.AcceptApply(ctx, testIntent("web", "r1")))
	owner, err := store.LoadOwnership(ctx, "web")
	require.NoError(t, err)
	require.NotEmpty(t, owner.ID, "the app record supplies the incarnation")
	base, _, err := store.LoadDesired(ctx, "web")
	require.NoError(t, err)

	rev, op := promotionAfter(base, "r2", owner.ID, "key")
	_, claimed, err := store.AcceptPromotion(ctx, rev, op)
	require.NoError(t, err)
	assert.True(t, claimed)
}

func TestAcceptPromotionAtomicity(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	require.NoError(t, store.AcceptApply(ctx, testIntent("web", "r1")))
	owner, err := store.LoadOwnership(ctx, "web")
	require.NoError(t, err)
	owner.App, owner.ID = "web", "incarnation"
	require.NoError(t, store.SaveOwnership(ctx, owner))
	base, ok, err := store.LoadDesired(ctx, "web")
	require.NoError(t, err)
	require.True(t, ok)
	rev := base
	rev.Revision, rev.Supersedes = "r2", "r1"
	rev.Promotion = &domain.AppPromotionProvenance{AppID: owner.ID, Image: "registry/web@sha256:abc", Digest: "sha256:abc"}
	op := domain.AppOperation{App: "web", Op: "key", Kind: domain.AppOperationKindPromote, Request: domain.AppOperationRequest{Kind: domain.AppOperationKindPromote, App: "web", Service: "web", Image: rev.Promotion.Image}}
	existing, claimed, err := store.AcceptPromotion(ctx, rev, op)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, rev.Promotion.Image, existing.Steps[0].Image)
	require.Equal(t, rev.Promotion.Digest, existing.Steps[0].Digest)
	desired, _, err := store.LoadDesired(ctx, "web")
	require.NoError(t, err)
	require.Equal(t, "r2", desired.Revision)
	// Replay precedes freshness checks, even after a later mutation.
	require.NoError(t, store.AcceptApply(ctx, testIntentAfter("web", "r3", "r2")))
	_, claimed, err = store.AcceptPromotion(ctx, rev, op)
	require.NoError(t, err)
	require.False(t, claimed)
	op.Request.Image = "different"
	_, _, err = store.AcceptPromotion(ctx, rev, op)
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	op.Op = "loser"
	_, _, err = store.AcceptPromotion(ctx, rev, op)
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	_, err = store.LoadOperation(ctx, "web", "loser")
	require.Error(t, err)
	rev.Revision, rev.Supersedes = "r4", "r3"
	rev.Promotion.AppID = "retired-incarnation"
	_, _, err = store.AcceptPromotion(ctx, rev, op)
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	_, err = store.LoadRevision(ctx, "web", "r4")
	require.ErrorIs(t, err, domain.ErrAppRevisionNotFound)
	desired, _, err = store.LoadDesired(ctx, "web")
	require.NoError(t, err)
	require.Equal(t, "r3", desired.Revision)
}
