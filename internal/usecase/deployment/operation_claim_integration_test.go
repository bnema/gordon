package deployment_test

import (
	"context"
	"sync"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

// TestRemove_ConcurrentSameKeyExecutesOnce proves the guarantee the store
// claim exists for, end to end: two concurrent requests carrying the same
// idempotency key run the removal effects exactly once, and both callers
// observe the same journal.
func TestRemove_ConcurrentSameKeyExecutesOnce(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{App: "blog"}))
	require.NoError(t, store.SaveActive(ctx, active))

	// Removal effects must run exactly once for the key.
	runtime.EXPECT().StopContainer(mock.Anything, "c-1").Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-1", false).Return(nil).Once()

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime,
	}, zerowrap.Default())

	const racers = 4
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ops  []string
		errs []error
	)
	start := make(chan struct{})
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := svc.Remove(ctx, "blog", "key-1")
			mu.Lock()
			defer mu.Unlock()
			errs = append(errs, err)
			if result != nil {
				ops = append(ops, result.Op)
			}
		}()
	}
	close(start)
	wg.Wait()

	require.Len(t, errs, racers)
	for i, err := range errs {
		require.NoError(t, err, "racer %d", i)
	}
	require.Len(t, ops, racers)
	for i, op := range ops {
		assert.Equal(t, "key-1", op, "racer %d must observe the claimed key", i)
	}
	runtime.AssertNumberOfCalls(t, "StopContainer", 1)
	runtime.AssertNumberOfCalls(t, "RemoveContainer", 1)
}

// TestRemove_UnknownAppCreatesNoState proves a keyed mutation of an
// unknown name fails with ErrAppNotFound and leaves no durable app state
// behind, even with a real store.
func TestRemove_UnknownAppCreatesNoState(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime,
	}, zerowrap.Default())

	_, err := svc.Remove(ctx, "ghost", "key-1")
	require.ErrorIs(t, err, domain.ErrAppNotFound)

	apps, err := store.ListApps(ctx)
	require.NoError(t, err)
	assert.NotContains(t, apps, "ghost")
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything)
}
