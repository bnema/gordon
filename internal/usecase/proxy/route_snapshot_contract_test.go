package proxy

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestRouteSnapshotContract_InvalidationForcesTargetResolution(t *testing.T) {
	provider := outmocks.NewMockRouteSnapshotProvider(t)
	provider.EXPECT().CurrentSnapshot(mock.Anything).Return(routeSnapshot(t, 1, readyEntry(t, "app.example.com", "203.0.113.10", 1)), nil).Once()
	svc := NewSnapshotService(provider, Config{})
	svc.targets["app.example.com"] = &domain.ProxyTarget{Host: "198.51.100.1", Port: 8080, Scheme: "http"}

	svc.InvalidateTarget(testContext(), "App.Example.com")
	target, err := svc.GetTarget(testContext(), "app.example.com")
	require.NoError(t, err)
	assert.Equal(t, "203.0.113.10", target.Host)
	assert.Equal(t, 8080, target.Port)
}

func TestRouteSnapshotContract_WaitForNoInFlight(t *testing.T) {
	t.Run("succeeds after tracked request releases", func(t *testing.T) {
		svc := NewService(nil, nil, nil, Config{})
		release := svc.TrackInFlight("route-old")
		result := make(chan bool, 1)
		waitEntered := make(chan struct{}, 1)
		svc.waitForNoInFlightWait = func() {
			select {
			case waitEntered <- struct{}{}:
			default:
			}
		}

		go func() {
			result <- svc.WaitForNoInFlight(context.Background(), "route-old", time.Second)
		}()
		awaitWaitPath(t, waitEntered)

		release()
		require.True(t, awaitWaiterResult(t, result))
	})

	t.Run("times out while tracked request remains", func(t *testing.T) {
		svc := NewService(nil, nil, nil, Config{})
		defer svc.TrackInFlight("route-old")()

		assert.False(t, svc.WaitForNoInFlight(context.Background(), "route-old", time.Nanosecond))
	})
}

func TestRouteSnapshotContract_RouteAndRegistryInFlightAreIndependent(t *testing.T) {
	svc := NewService(nil, nil, nil, Config{})
	defer svc.TrackInFlight("route-old")()
	svc.TrackRegistryRequest()
	defer svc.ReleaseRegistryRequest()

	assert.False(t, svc.WaitForNoInFlight(context.Background(), "route-old", time.Nanosecond))
	assert.Equal(t, int64(1), svc.RegistryInFlight())

	assert.True(t, svc.WaitForNoInFlight(context.Background(), "route-new", time.Nanosecond))
	assert.Equal(t, int64(1), svc.RegistryInFlight())
}

func awaitWaitPath(t *testing.T, waitEntered <-chan struct{}) {
	t.Helper()

	select {
	case <-waitEntered:
	case <-time.After(time.Second):
		t.Fatal("waiter did not enter the wait path")
	}
}

func awaitWaiterResult(t *testing.T, result <-chan bool) bool {
	t.Helper()

	select {
	case drained := <-result:
		return drained
	case <-time.After(time.Second):
		t.Fatal("waiter did not return after request release")
		return false
	}
}
