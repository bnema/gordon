package edgesnapshot

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppliedStateTrackerFailsClosedAndRejectsRegression(t *testing.T) {
	tracker, err := NewAppliedStateTracker("gordon-edge-fixture")
	require.NoError(t, err)
	assert.Error(t, tracker.Applied(context.Background(), 7, 7))
	assert.ErrorIs(t, tracker.ReportAuthenticatedAppliedState(context.Background(), "other", AppliedState{ComponentID: "other", RouteGeneration: 7, TrafficGeneration: 7, Healthy: true}), ErrAppliedStateUnexpected)
	assert.ErrorIs(t, tracker.ReportAuthenticatedAppliedState(context.Background(), "gordon-edge-fixture", AppliedState{ComponentID: "gordon-edge-fixture", RouteGeneration: 7, TrafficGeneration: 6, Healthy: true}), ErrAppliedStateInvalid)
	require.NoError(t, tracker.ReportAuthenticatedAppliedState(context.Background(), "gordon-edge-fixture", AppliedState{ComponentID: "gordon-edge-fixture", RouteGeneration: 7, TrafficGeneration: 7, Healthy: true}))
	require.NoError(t, tracker.Applied(context.Background(), 7, 7))
	assert.Error(t, tracker.Applied(context.Background(), 8, 8))
	assert.ErrorIs(t, tracker.ReportAuthenticatedAppliedState(context.Background(), "gordon-edge-fixture", AppliedState{ComponentID: "gordon-edge-fixture", RouteGeneration: 6, TrafficGeneration: 6, Healthy: true}), ErrAppliedStateStale)
}

func TestAppliedStateTrackerHonorsCancellation(t *testing.T) {
	tracker, err := NewAppliedStateTracker("gordon-edge-fixture")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = tracker.ReportAuthenticatedAppliedState(ctx, "gordon-edge-fixture", AppliedState{ComponentID: "gordon-edge-fixture", RouteGeneration: 1, TrafficGeneration: 1, Healthy: true})
	assert.True(t, errors.Is(err, context.Canceled))
}
