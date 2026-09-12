package deployment

import (
	"context"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

type stubTraffic struct {
	withdrawn []string
	fail      bool
}

func (s *stubTraffic) WithdrawService(_ context.Context, app, service string) error {
	if s.fail {
		return assert.AnError
	}
	s.withdrawn = append(s.withdrawn, app+"/"+service)
	return nil
}

// WithdrawServiceState records the state-only withdrawal the canonical
// boundary performs; in-package tests treat it like WithdrawService
// without a graph application.
func (s *stubTraffic) WithdrawServiceState(_ context.Context, app, service string) error {
	if s.fail {
		return assert.AnError
	}
	s.withdrawn = append(s.withdrawn, app+"/"+service)
	return nil
}

func (s *stubTraffic) RebuildTraffic(context.Context) error { return nil }

// TestReconcileRemovedServices_WithdrawsStopsAndClears proves a service the
// new revision drops is withdrawn first, its exact container stopped and
// removed, its claims released, and its ACTIVE entry deleted while a
// healthy sibling is preserved.
func TestReconcileRemovedServices_WithdrawsStopsAndClears(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	traffic := &stubTraffic{}
	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-web", EffectiveRevision: "rev-1", Spec: domain.AppService{Name: "web"}},
		"legacy": {
			Container: "c-legacy", EffectiveRevision: "rev-1",
			Spec: domain.AppService{Name: "legacy", TCP: []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 9000, Publish: "0.0.0.0:9000"}}},
		},
	}}

	state.EXPECT().SaveRecoveryInhibition(mock.Anything, mock.MatchedBy(func(i domain.AppRecoveryInhibition) bool {
		return i.App == "blog" && i.Service == "legacy" && i.ContainerID == "c-legacy" && i.Reason == "removed"
	})).Return(nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "c-legacy", mock.Anything).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-legacy", false).Return(nil).Once()
	state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "c-legacy").Return(nil).Once()
	state.EXPECT().ClearRecoveryInhibition(mock.Anything, "blog", "legacy", "c-legacy").Return(nil).Once()
	var saved domain.AppActive
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, a domain.AppActive) error {
		saved = a
		return nil
	}).Once()

	svc := NewService(Deps{State: state, Runtime: runtime, Traffic: traffic}, zerowrap.Default())
	steps, removed, err := svc.reconcileRemovedServices(ctx, "blog", active, []pinnedService{
		{name: "web", spec: domain.AppService{Name: "web"}},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"legacy"}, removed)
	require.Len(t, steps, 1)
	assert.Equal(t, domain.AppStepSucceeded, steps[0].State)
	assert.Equal(t, []string{"blog/legacy"}, traffic.withdrawn)
	_, ok := saved.Services["legacy"]
	assert.False(t, ok, "removed service must be absent from ACTIVE")
	_, ok = saved.Services["web"]
	assert.True(t, ok, "healthy sibling must be preserved")
	runtime.AssertNotCalled(t, "RemoveVolume", mock.Anything, mock.Anything, mock.Anything)
}

// TestReconcileRemovedServices_WithdrawFailureAbortsBeforeStop proves a
// failed withdrawal stops the deploy before the container is touched, so
// ACTIVE is never cleared while the service may still be reachable.
func TestReconcileRemovedServices_WithdrawFailureAbortsBeforeStop(t *testing.T) {
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	traffic := &stubTraffic{fail: true}
	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"legacy": {Container: "c-legacy", EffectiveRevision: "rev-1", Spec: domain.AppService{Name: "legacy"}},
	}}

	svc := NewService(Deps{State: state, Runtime: runtime, Traffic: traffic}, zerowrap.Default())
	steps, _, err := svc.reconcileRemovedServices(context.Background(), "blog", active, nil)

	require.Error(t, err)
	require.Len(t, steps, 1)
	assert.Equal(t, domain.AppStepFailed, steps[0].State)
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "SaveActive", mock.Anything, mock.Anything)
}

// TestReconcileRemovedServices_NoopWhenAllActiveServicesRemain proves a
// deploy that keeps every active service performs no removal effects.
func TestReconcileRemovedServices_NoopWhenAllActiveServicesRemain(t *testing.T) {
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-web"},
	}}

	svc := NewService(Deps{State: state, Runtime: runtime, Traffic: &stubTraffic{}}, zerowrap.Default())
	steps, removed, err := svc.reconcileRemovedServices(context.Background(), "blog", active, []pinnedService{{name: "web"}})

	require.NoError(t, err)
	assert.Empty(t, steps)
	assert.Empty(t, removed)
}
