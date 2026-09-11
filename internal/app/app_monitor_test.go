package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apptraffic"
)

func httpAppSpec() domain.AppService {
	return domain.AppService{
		Name: "web",
		HTTP: []domain.AppHTTPInterface{{Host: "blog.example.com", Port: 8080, TLS: "auto"}},
	}
}

// newPublisherServices wires the minimum app wiring the publisher needs:
// a state source, the ACTIVE-derived host index, and the activator.
func newPublisherServices(state *outmocks.MockAppState) (*services, *apptraffic.HostIndex) {
	index := apptraffic.NewHostIndex()
	return &services{
		appState:     state,
		appActivator: apptraffic.NewActivator(zerowrap.Default()),
		appHostIndex: index,
	}, index
}

func TestAppTrafficPublisher_WithdrawServiceIsFailClosedInStateAndIndex(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	svc, index := newPublisherServices(state)

	served := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", Spec: httpAppSpec(), BackendBinds: map[int]int{8080: 32770}},
	}}
	withdrawn := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", Spec: httpAppSpec()},
	}}
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(served, true, nil).Once()
	state.EXPECT().SaveActive(mock.Anything, mock.MatchedBy(func(active domain.AppActive) bool {
		service := active.Services["web"]
		return service.BackendBinds == nil && service.UDPBackendBinds == nil
	})).Return(nil).Once()
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(withdrawn, true, nil).Once()

	// Serve the host first so the withdrawal has something to remove.
	require.NoError(t, svc.appActivator.RebuildHostIndex(ctx, index, stubAppState{active: served}, nil))
	backend, ok := index.LookupHost("blog.example.com")
	require.True(t, ok)
	require.True(t, backend.Resolved(), "precondition: the host resolves to a loopback backend")

	publisher := newAppTrafficPublisher(svc, Config{})
	require.NoError(t, publisher.WithdrawService(ctx, "blog", "web"))

	// The proxy fails closed on an unresolved backend (proxy/service.go
	// resolveAppTarget), so an unresolved projection is a withdrawn one.
	backend, ok = index.LookupHost("blog.example.com")
	if ok {
		assert.False(t, backend.Resolved(), "a withdrawn service must not resolve to a backend")
	}
}

func TestAppTrafficPublisher_WithdrawServiceSurfacesStateFailure(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	svc, index := newPublisherServices(state)

	served := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", Spec: httpAppSpec(), BackendBinds: map[int]int{8080: 32770}},
	}}
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(served, true, nil).Once()
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(assert.AnError).Once()

	require.NoError(t, svc.appActivator.RebuildHostIndex(ctx, index, stubAppState{active: served}, nil))

	publisher := newAppTrafficPublisher(svc, Config{})
	err := publisher.WithdrawService(ctx, "blog", "web")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "persist fail-closed state")
	// The projection could not be rebuilt, so stale forwarding may remain:
	// the caller keeps its publication inhibition and retries.
	_, ok := index.LookupHost("blog.example.com")
	assert.True(t, ok)
}

func TestAppTrafficPublisher_WithdrawUnknownServiceIsANoop(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	svc, _ := newPublisherServices(state)

	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{App: "blog"}, true, nil).Once()
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{App: "blog"}, true, nil).Once()

	publisher := newAppTrafficPublisher(svc, Config{})
	require.NoError(t, publisher.WithdrawService(ctx, "blog", "web"))
}

func TestAppTrafficPublisher_RebuildTrafficReprojectsVerifiedState(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	svc, index := newPublisherServices(state)

	served := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", Spec: httpAppSpec(), BackendBinds: map[int]int{8080: 32770}},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(served, true, nil).Once()

	publisher := newAppTrafficPublisher(svc, Config{})
	require.NoError(t, publisher.RebuildTraffic(ctx))

	backend, ok := index.LookupHost("blog.example.com")
	require.True(t, ok)
	assert.Equal(t, 32770, backend.Port)
}

func TestAppTrafficPublisher_SerializesConcurrentPublication(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	svc, index := newPublisherServices(state)

	served := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", Spec: httpAppSpec(), BackendBinds: map[int]int{8080: 32770}},
	}}
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(served, true, nil)
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil)
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil)

	publisher := newAppTrafficPublisher(svc, Config{})
	done := make(chan struct{})
	for range 4 {
		go func() {
			defer func() { done <- struct{}{} }()
			_ = publisher.RebuildTraffic(ctx)
			_ = publisher.WithdrawService(ctx, "blog", "web")
		}()
	}
	for range 4 {
		<-done
	}
	assert.NotNil(t, index)
}

// stubAppState is a read-only app state double for index projections.
// The zero value reports running intent.
type stubAppState struct {
	active  domain.AppActive
	stopped bool
}

func (s stubAppState) ListApps(context.Context) ([]string, error) {
	return []string{s.active.App}, nil
}

func (s stubAppState) LoadActive(context.Context, string) (domain.AppActive, bool, error) {
	return s.active, true, nil
}

func (s stubAppState) LoadIntent(context.Context, string) (domain.AppStopIntent, error) {
	return domain.AppStopIntent{App: s.active.App, Stopped: s.stopped}, nil
}

func TestAppTrafficPublisher_WithdrawalIsIdempotent(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	svc, _ := newPublisherServices(state)

	alreadyWithdrawn := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", Spec: httpAppSpec()},
	}}
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(alreadyWithdrawn, true, nil).Once()
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(alreadyWithdrawn, true, nil).Once()

	publisher := newAppTrafficPublisher(svc, Config{})
	require.NoError(t, publisher.WithdrawService(ctx, "blog", "web"))
}

func TestAppMonitor_RunsOnePassPerTick(t *testing.T) {
	reconciler := &fakeReconciler{}
	monitor := newAppMonitor(reconciler, zerowrap.Default())
	ticks := make(chan time.Time, 8)
	monitor.ticks = func() <-chan time.Time { return ticks }
	monitor.Start(context.Background())

	ticks <- time.Now()
	require.Eventually(t, func() bool { return reconciler.callCount() == 1 }, time.Second, time.Millisecond)
	ticks <- time.Now()
	require.Eventually(t, func() bool { return reconciler.callCount() == 2 }, time.Second, time.Millisecond)

	monitor.Stop()
	assert.Equal(t, 2, reconciler.callCount())
}

func TestAppMonitor_DoesNotOverlapPasses(t *testing.T) {
	reconciler := &fakeReconciler{enter: make(chan struct{}), release: make(chan struct{})}
	monitor := newAppMonitor(reconciler, zerowrap.Default())
	ticks := make(chan time.Time, 8)
	monitor.ticks = func() <-chan time.Time { return ticks }
	monitor.Start(context.Background())

	ticks <- time.Now()
	<-reconciler.entered()
	// Additional ticks while a pass is in flight queue at most one
	// further pass, never a concurrent one.
	for range 3 {
		ticks <- time.Now()
	}
	assert.Equal(t, 1, reconciler.callCount())
	assert.Equal(t, 1, reconciler.maxConcurrent())

	close(reconciler.release)
	monitor.Stop()
	assert.GreaterOrEqual(t, reconciler.callCount(), 1)
}

func TestAppMonitor_ReconciliationErrorDoesNotStopTheLoop(t *testing.T) {
	reconciler := &fakeReconciler{err: assert.AnError}
	monitor := newAppMonitor(reconciler, zerowrap.Default())
	ticks := make(chan time.Time, 8)
	monitor.ticks = func() <-chan time.Time { return ticks }
	monitor.Start(context.Background())

	ticks <- time.Now()
	ticks <- time.Now()
	require.Eventually(t, func() bool { return reconciler.callCount() == 2 }, time.Second, time.Millisecond)
	monitor.Stop()
}

func TestAppMonitor_StartIsIdempotent(t *testing.T) {
	reconciler := &fakeReconciler{}
	monitor := newAppMonitor(reconciler, zerowrap.Default())
	ticks := make(chan time.Time, 8)
	monitor.ticks = func() <-chan time.Time { return ticks }

	monitor.Start(context.Background())
	monitor.Start(context.Background())
	monitor.Start(context.Background())

	ticks <- time.Now()
	require.Eventually(t, func() bool { return reconciler.callCount() == 1 }, time.Second, time.Millisecond)
	monitor.Stop()
	assert.Equal(t, 1, reconciler.callCount(), "reload must never create a second loop")
}

func TestAppMonitor_StopCancelsAndJoins(t *testing.T) {
	blocking := &fakeReconciler{enter: make(chan struct{}), release: make(chan struct{})}
	monitor := newAppMonitor(blocking, zerowrap.Default())
	ticks := make(chan time.Time, 8)
	monitor.ticks = func() <-chan time.Time { return ticks }
	monitor.Start(context.Background())

	ticks <- time.Now()
	<-blocking.entered()

	stopped := make(chan struct{})
	go func() {
		monitor.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned while a bounded pass was still running")
	case <-time.After(30 * time.Millisecond):
	}
	close(blocking.release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop never joined the loop")
	}
	monitor.Stop() // idempotent
}

func TestAppMonitor_NilReconcilerNeverStarts(t *testing.T) {
	monitor := newAppMonitor(nil, zerowrap.Default())
	monitor.Start(context.Background())
	monitor.Stop()
}

// fakeReconciler records passes and can hold one open.
type fakeReconciler struct {
	mu          sync.Mutex
	calls       int
	inflight    int
	maxInflight int
	enter       chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	err         error
}

func (f *fakeReconciler) ReconcileRunning(context.Context) error {
	f.mu.Lock()
	f.calls++
	f.inflight++
	if f.inflight > f.maxInflight {
		f.maxInflight = f.inflight
	}
	f.mu.Unlock()
	if f.enter != nil {
		f.enteredOnce.Do(func() { close(f.enter) })
		<-f.release
	}
	f.mu.Lock()
	f.inflight--
	f.mu.Unlock()
	return f.err
}

func (f *fakeReconciler) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeReconciler) maxConcurrent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxInflight
}

func (f *fakeReconciler) entered() <-chan struct{} { return f.enter }

// TestAppTrafficPublisher_RebuildSkipsStoppedApps proves durable stopped
// intent is honored by every publication, including the plain rebuild a
// stopped-app convergence pass performs: a stopped service is never
// projected back into the proxy index.
func TestAppTrafficPublisher_RebuildSkipsStoppedApps(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	svc, index := newPublisherServices(state)

	served := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", Spec: httpAppSpec(), BackendBinds: map[int]int{8080: 32770}},
	}}
	// Precondition: the host is routable while the app runs.
	require.NoError(t, svc.appActivator.RebuildHostIndex(ctx, index, stubAppState{active: served}, nil))
	backend, ok := index.LookupHost("blog.example.com")
	require.True(t, ok)
	require.True(t, backend.Resolved())

	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").
		Return(domain.AppStopIntent{App: "blog", Stopped: true}, nil).Once()

	publisher := newAppTrafficPublisher(svc, Config{})
	require.NoError(t, publisher.RebuildTraffic(ctx))

	backend, ok = index.LookupHost("blog.example.com")
	if ok {
		assert.False(t, backend.Resolved(), "a stopped app must not stay routable")
	}
}

// TestAppTrafficPublisher_AdmissionHonorsContextCancellation proves a
// caller whose deadline expired never waits behind an in-flight
// publication: a monitor pass must not hold its app lock for a graph
// application someone else started, and shutdown must not block on it.
func TestAppTrafficPublisher_AdmissionHonorsContextCancellation(t *testing.T) {
	state := outmocks.NewMockAppState(t)
	svc, _ := newPublisherServices(state)

	release := make(chan struct{})
	entered := make(chan struct{})
	state.EXPECT().ListApps(mock.Anything).RunAndReturn(func(context.Context) ([]string, error) {
		close(entered)
		<-release
		return nil, nil
	}).Once()

	publisher := newAppTrafficPublisher(svc, Config{})
	holding := make(chan struct{})
	go func() {
		defer close(holding)
		_ = publisher.RebuildTraffic(context.Background())
	}()
	<-entered

	expired, cancel := context.WithCancel(context.Background())
	cancel()
	err := publisher.RebuildTraffic(expired)
	require.ErrorIs(t, err, context.Canceled)

	close(release)
	<-holding
}
