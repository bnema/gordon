package appstate_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

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
