package app

import (
	"context"
	"testing"

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
	backend, ok := index.LookupHost("blog.example.com")
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
		live, ok := index.LookupHost("blog.example.com")
		require.True(t, ok)
		assert.Equal(t, 32770, live.Port, "the live index must not change before the graph apply succeeds")
		candidateHosts := hosts.(*apptraffic.HostIndex)
		candidateBackend, ok := candidateHosts.LookupHost("blog.example.com")
		require.True(t, ok)
		assert.Equal(t, 32771, candidateBackend.Port)
		return nil
	}

	require.NoError(t, publisher.RebuildTraffic(ctx))
	assert.Equal(t, 1, applies)
	backend, ok = index.LookupHost("blog.example.com")
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
	backend, ok := index.LookupHost("blog.example.com")
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
	_, ok := index.LookupHost("blog.example.com")
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
	backend, ok := index.LookupHost("blog.example.com")
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
	backend, ok := index.LookupHost("blog.example.com")
	require.True(t, ok)
	assert.Equal(t, 32770, backend.Port)
}
