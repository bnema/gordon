package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apptraffic"
)

// servedActive builds an ACTIVE record for one served host with a
// resolved loopback backend.
func servedActive(container string, port int) domain.AppActive {
	return domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: container, Spec: httpAppSpec(), BackendBinds: map[int]int{8080: port}},
	}}
}

// TestAppTrafficPublisher_PublishesIndexOnlyAfterGraphApply proves the
// prepare/commit contract: the graph is built and applied against a
// detached candidate while the live index still serves the previous
// generation, and only a successful apply publishes the candidate.
func TestAppTrafficPublisher_PublishesIndexOnlyAfterGraphApply(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	svc, index := newPublisherServices(state)

	first := servedActive("c-1", 32770)
	second := servedActive("c-2", 32771)

	// First generation: one successful publication.
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(first, true, nil).Once()

	publisher := newAppTrafficPublisher(svc, Config{})
	require.NoError(t, publisher.RebuildTraffic(ctx))
	backend, _, ok := index.LookupRoute("blog.example.com")
	require.True(t, ok)
	assert.Equal(t, 32770, backend.Port)

	// Second generation: the applier observes the candidate and the live
	// index side by side.
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(second, true, nil).Once()

	applies := 0
	publisher.applyGraph = func(_ context.Context, _ Config, hosts appHostRoutes) error {
		applies++
		require.NotNil(t, hosts, "the graph is built against the candidate view")
		require.Len(t, hosts.AppHosts(), 1)
		// The candidate carries the new backend while the live index
		// still carries the old one: nothing is published yet.
		live, _, ok := index.LookupRoute("blog.example.com")
		require.True(t, ok)
		assert.Equal(t, 32770, live.Port, "the live index must not change before the graph apply succeeds")
		candidateHosts := hosts.(*apptraffic.HostIndex)
		candidateBackend, _, ok := candidateHosts.LookupRoute("blog.example.com")
		require.True(t, ok)
		assert.Equal(t, 32771, candidateBackend.Port)
		return nil
	}

	require.NoError(t, publisher.RebuildTraffic(ctx))
	assert.Equal(t, 1, applies)
	backend, _, ok = index.LookupRoute("blog.example.com")
	require.True(t, ok)
	assert.Equal(t, 32771, backend.Port, "the accepted candidate is published exactly once")
}

// TestAppTrafficPublisher_GraphFailureKeepsPreviousGeneration proves a
// rejected graph leaves the live index exactly where it was.
func TestAppTrafficPublisher_GraphFailureKeepsPreviousGeneration(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	svc, index := newPublisherServices(state)

	first := servedActive("c-1", 32770)
	second := servedActive("c-2", 32771)

	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(first, true, nil).Once()

	publisher := newAppTrafficPublisher(svc, Config{})
	require.NoError(t, publisher.RebuildTraffic(ctx))

	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(second, true, nil).Once()

	publisher.applyGraph = func(context.Context, Config, appHostRoutes) error {
		return assert.AnError
	}

	require.ErrorIs(t, publisher.RebuildTraffic(ctx), assert.AnError)
	backend, _, ok := index.LookupRoute("blog.example.com")
	require.True(t, ok)
	assert.Equal(t, 32770, backend.Port, "a rejected graph leaves the previous generation published")
}

// TestAppTrafficPublisher_WithdrawalStaysFailClosedWhenGraphApplyFails
// proves a rejected graph cannot leave a withdrawn backend published: the
// durable binds are already gone, so the read model must drop them even
// though the error is reported for a retry.
func TestAppTrafficPublisher_WithdrawalStaysFailClosedWhenGraphApplyFails(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	svc, index := newPublisherServices(state)

	served := servedActive("c-1", 32770)
	withdrawn := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", Spec: httpAppSpec()},
	}}

	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(served, true, nil).Once()

	publisher := newAppTrafficPublisher(svc, Config{})
	require.NoError(t, publisher.RebuildTraffic(ctx))
	_, _, ok := index.LookupRoute("blog.example.com")
	require.True(t, ok)

	// The withdrawal clears the binds, then the graph apply is rejected.
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(served, true, nil).Once()
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(withdrawn, true, nil).Once()
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(withdrawn, true, nil).Once()

	publisher.applyGraph = func(context.Context, Config, appHostRoutes) error {
		return assert.AnError
	}

	require.Error(t, publisher.WithdrawService(ctx, "blog", "web"))
	backend, _, ok := index.LookupRoute("blog.example.com")
	if ok {
		assert.False(t, backend.Resolved(), "a withdrawn service must not keep a resolved backend")
	}
}

// TestAppTrafficPublisher_ProjectionFailureAppliesNoGraph proves a
// failing projection never reaches the dataplane: the graph apply is not
// called at all and the live index is untouched.
func TestAppTrafficPublisher_ProjectionFailureAppliesNoGraph(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	svc, index := newPublisherServices(state)

	first := servedActive("c-1", 32770)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(first, true, nil).Once()

	publisher := newAppTrafficPublisher(svc, Config{})
	require.NoError(t, publisher.RebuildTraffic(ctx))

	state.EXPECT().ListApps(mock.Anything).Return(nil, assert.AnError).Once()
	applied := false
	publisher.applyGraph = func(context.Context, Config, appHostRoutes) error {
		applied = true
		return nil
	}

	require.Error(t, publisher.RebuildTraffic(ctx))
	assert.False(t, applied, "a failed projection must not reach the dataplane")
	backend, _, ok := index.LookupRoute("blog.example.com")
	require.True(t, ok)
	assert.Equal(t, 32770, backend.Port)
}

func TestSetImageCIDomainCollisionKeepsPublishedState(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	svc, index := newPublisherServices(state)
	active := servedActive("c-1", 32770)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Times(3)
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Times(3)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Times(3)
	publisher := newAppTrafficPublisher(svc, Config{})
	require.NoError(t, publisher.RebuildTraffic(ctx))
	var cfg Config
	cfg.Auth.Enabled = true
	cfg.Server.CIDomain = " BLOG.EXAMPLE.COM "
	snapshot, err := buildSetImageSnapshot(Config{})
	require.NoError(t, err)
	svc.setImageSnapshot.Store(&snapshot)
	before := svc.setImageSnapshot.Load()
	// Runtime reload rejects the colliding detached host view before publishing policies.
	svc.appTrafficPublisher = publisher
	plan, err := (&reloadRuntime{svc: svc}).prepare(ctx, cfg)
	require.NoError(t, err)
	require.ErrorContains(t, (&reloadRuntime{svc: svc}).applyServing(ctx, cfg, plan), "collides with an app host")
	require.Same(t, before, svc.setImageSnapshot.Load())
	_, _, ok := index.LookupRoute("blog.example.com")
	require.True(t, ok)
	require.Empty(t, publisher.cfg.Server.CIDomain)
	require.NoError(t, publisher.RebuildTraffic(ctx))
}

func TestAppTrafficPublisher_HoldLifecycle(t *testing.T) {
	for _, failApply := range []bool{false, true} {
		t.Run(fmt.Sprintf("apply_failure=%t", failApply), func(t *testing.T) {
			ctx := context.Background()
			state := outmocks.NewMockAppState(t)
			svc, index := newPublisherServices(state)
			publisher := newAppTrafficPublisher(svc, Config{})
			active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
				"web": {Container: "c-1", Spec: httpAppSpec(), BackendBinds: map[int]int{8080: 32770}, UDPBackendBinds: map[int]int{8080: 32771}},
			}}
			state.EXPECT().LoadActive(mock.Anything, "blog").RunAndReturn(func(context.Context, string) (domain.AppActive, bool, error) {
				return active, true, nil
			})
			state.EXPECT().SaveActive(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, updated domain.AppActive) error {
				active = updated
				return nil
			})
			state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil)
			state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil)
			publisher.applyGraph = func(context.Context, Config, appHostRoutes) error {
				if failApply {
					return assert.AnError
				}
				return nil
			}
			until := time.Now().Add(time.Minute)
			err := publisher.HoldService(ctx, "blog", "web", until)
			if failApply {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Empty(t, active.Services["web"].BackendBinds)
			assert.Empty(t, active.Services["web"].UDPBackendBinds)
			backend, heldUntil, ok := index.LookupRoute("blog.example.com")
			require.True(t, ok)
			assert.False(t, backend.Resolved())
			assert.Equal(t, until, heldUntil)
			_, held := svc.appHolds.Until("blog", "web")
			assert.True(t, held, "failed graph application must retain the hold")

			failApply = false
			require.NoError(t, publisher.WithdrawService(ctx, "blog", "web"))
			_, held = svc.appHolds.Until("blog", "web")
			assert.False(t, held)
			_, heldUntil, _ = index.LookupRoute("blog.example.com")
			assert.True(t, heldUntil.IsZero())

			require.NoError(t, publisher.HoldService(ctx, "blog", "web", until))
			require.NoError(t, publisher.WithdrawServiceState(ctx, "blog", "web"))
			_, held = svc.appHolds.Until("blog", "web")
			assert.False(t, held)

			require.NoError(t, publisher.HoldService(ctx, "blog", "web", until))
			ready := active.Services["web"]
			ready.BackendBinds = map[int]int{8080: 32772}
			active.Services["web"] = ready
			require.NoError(t, publisher.RebuildTraffic(ctx))
			_, held = svc.appHolds.Until("blog", "web")
			assert.False(t, held, "ready publication prunes the hold")
			backend, heldUntil, ok = index.LookupRoute("blog.example.com")
			require.True(t, ok)
			assert.True(t, backend.Resolved())
			assert.True(t, heldUntil.IsZero())
		})
	}
}
