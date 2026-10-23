package deployment_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

// pruneBarrier models prune holding the exclusive GC lease: shared leases
// block on a channel until the exclusive holder releases.
type pruneBarrier struct{ released chan struct{} }

type nopLease struct{}

func (nopLease) Release() {}

func (b *pruneBarrier) AcquireShared(ctx context.Context) (out.GCLease, error) {
	select {
	case <-b.released:
		return nopLease{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *pruneBarrier) AcquireExclusive(context.Context) (out.GCLease, error) {
	return nil, errors.New("not used")
}

// TestStartPromotion_WaitsForPruneExclusiveLease proves a promotion racing
// prune never reads or writes state while prune holds the exclusive lease:
// it is durably blocked on the barrier, and it proceeds only after release.
// The bubble holds only mocks and channels (no bbolt, no sockets), so
// synctest.Wait observes the blocked goroutine deterministically.
func TestStartPromotion_WaitsForPruneExclusiveLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		state := outmocks.NewMockAppState(t)
		barrier := &pruneBarrier{released: make(chan struct{})}
		svc := deployment.NewService(deployment.Deps{
			State: state, Registry: deployment.RegistryConfig{Domain: promoRegistry},
		}, zerowrap.Default()).WithGCBarrier(barrier)

		recovered := false
		state.EXPECT().Recover(mock.Anything).RunAndReturn(func(context.Context) error {
			recovered = true
			return domain.ErrAppStateIO
		}).Once()

		var err error
		done := false
		go func() {
			_, err = svc.StartPromotion(context.Background(), deployment.PromotionInput{
				App: "blog", Service: "web", Image: "web@" + promoDigest("c"), Op: "k",
				Validate: func(domain.AppSpec) error { return nil },
			})
			done = true
		}()
		synctest.Wait()
		assert.False(t, recovered, "no state access while prune holds the exclusive lease")
		assert.False(t, done)

		close(barrier.released)
		synctest.Wait()
		require.True(t, done)
		assert.True(t, recovered)
		require.ErrorIs(t, err, domain.ErrAppStateIO)
	})
}
