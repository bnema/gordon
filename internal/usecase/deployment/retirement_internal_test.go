package deployment

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

// TestRetireContainer_ForceSkipsTheGracePeriod proves a never-published
// candidate is removed without waiting for a grace period.
func TestRetireContainer_ForceSkipsTheGracePeriod(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-candidate", true).Return(nil).Once()
	state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "c-candidate").Return(nil).Once()

	svc := NewService(Deps{State: state, Runtime: runtime}, zerowrap.Default())
	retired := svc.retireContainer(ctx, "blog", retireOptions{Service: "web", Force: true}, "c-candidate")
	assert.True(t, retired.Gone)
	assert.Empty(t, retired.Warnings)
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
}

// TestRetireContainer_UnconfirmedRemovalKeepsClaims proves a container the
// runtime would not remove keeps its backend claims and its recovery
// inhibition, and reports a bounded warning instead of a success.
func TestRetireContainer_UnconfirmedRemovalKeepsClaims(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().StopContainer(mock.Anything, "c-1", 5*time.Second).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-1", false).Return(assert.AnError).Once()

	svc := NewService(Deps{State: state, Runtime: runtime}, zerowrap.Default())
	retired := svc.retireContainer(ctx, "blog", retireOptions{
		Service: "web", Grace: 5 * time.Second, ClearInhibition: true,
	}, "c-1")

	assert.False(t, retired.Gone)
	require.Len(t, retired.Warnings, 1)
	assert.Equal(t, "c-1", retired.Warnings[0].Leftover)
	assert.Contains(t, retired.Warnings[0].Detail, "remove")
	state.AssertNotCalled(t, "ReleaseBackendBinds", mock.Anything, mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "ClearRecoveryInhibition", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestRetireContainer_ZeroGraceUsesTheAppDefault proves a zero grace is
// never an immediate kill on an app path.
func TestRetireContainer_ZeroGraceUsesTheAppDefault(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().StopContainer(mock.Anything, "c-1", domain.AppDefaultStopGrace).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-1", false).Return(nil).Once()
	state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "c-1").Return(nil).Once()

	svc := NewService(Deps{State: state, Runtime: runtime}, zerowrap.Default())
	retired := svc.retireContainer(ctx, "blog", retireOptions{Service: "web"}, "c-1")
	assert.True(t, retired.Gone)
	assert.Empty(t, retired.Warnings)
}
