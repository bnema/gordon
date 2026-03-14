package container

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestSecretsChangedHandler_CanHandle(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	h := NewSecretsChangedHandler(testCtx(), containerSvc, configSvc, 200*time.Millisecond)
	defer h.Stop()

	assert.True(t, h.CanHandle(domain.EventSecretsChanged))
	assert.False(t, h.CanHandle(domain.EventConfigReload))
}

func TestSecretsChangedHandler_DebouncesBurstEvents(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	debounceDelay := 200 * time.Millisecond
	h := NewSecretsChangedHandler(testCtx(), containerSvc, configSvc, debounceDelay)
	defer h.Stop()

	route := &domain.Route{Domain: "app.example.com", Image: "app:latest"}
	configSvc.EXPECT().GetRoute(mock.Anything, route.Domain).Return(route, nil).Once()

	var deployCalls atomic.Int32
	containerSvc.EXPECT().Deploy(mock.Anything, *route).RunAndReturn(func(_ context.Context, deployedRoute domain.Route) (*domain.Container, error) {
		deployCalls.Add(1)
		return &domain.Container{ID: "container-1"}, nil
	}).Once()

	event := domain.Event{
		Type: domain.EventSecretsChanged,
		Data: domain.SecretsChangedPayload{
			Domain:    route.Domain,
			Operation: "set",
			Keys:      []string{"A", "B"},
		},
	}

	for range 5 {
		assert.NoError(t, h.Handle(context.Background(), event))
		time.Sleep(10 * time.Millisecond)
	}

	time.Sleep(debounceDelay + 100*time.Millisecond)
	assert.Equal(t, int32(1), deployCalls.Load())
}

func TestSecretsChangedHandler_IndependentDomains(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	debounceDelay := 100 * time.Millisecond
	h := NewSecretsChangedHandler(testCtx(), containerSvc, configSvc, debounceDelay)
	defer h.Stop()

	routeOne := &domain.Route{Domain: "app1.example.com", Image: "app1:latest"}
	routeTwo := &domain.Route{Domain: "app2.example.com", Image: "app2:latest"}

	configSvc.EXPECT().GetRoute(mock.Anything, routeOne.Domain).Return(routeOne, nil).Once()
	configSvc.EXPECT().GetRoute(mock.Anything, routeTwo.Domain).Return(routeTwo, nil).Once()

	containerSvc.EXPECT().Deploy(mock.Anything, *routeOne).Return(&domain.Container{ID: "container-1"}, nil).Once()
	containerSvc.EXPECT().Deploy(mock.Anything, *routeTwo).Return(&domain.Container{ID: "container-2"}, nil).Once()

	assert.NoError(t, h.Handle(context.Background(), domain.Event{
		Type: domain.EventSecretsChanged,
		Data: domain.SecretsChangedPayload{Domain: routeOne.Domain, Operation: "set", Keys: []string{"A"}},
	}))
	assert.NoError(t, h.Handle(context.Background(), domain.Event{
		Type: domain.EventSecretsChanged,
		Data: domain.SecretsChangedPayload{Domain: routeTwo.Domain, Operation: "delete", Keys: []string{"B"}},
	}))

	time.Sleep(debounceDelay + 100*time.Millisecond)
}

func TestSecretsChangedHandler_NoRouteFound_NoOp(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	debounceDelay := 100 * time.Millisecond
	h := NewSecretsChangedHandler(testCtx(), containerSvc, configSvc, debounceDelay)
	defer h.Stop()

	configSvc.EXPECT().GetRoute(mock.Anything, "missing.example.com").Return(nil, nil).Once()

	assert.NoError(t, h.Handle(context.Background(), domain.Event{
		Type: domain.EventSecretsChanged,
		Data: domain.SecretsChangedPayload{Domain: "missing.example.com", Operation: "set", Keys: []string{"A"}},
	}))

	time.Sleep(debounceDelay + 100*time.Millisecond)
}

func TestSecretsChangedHandler_InvalidPayload_NoOp(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	h := NewSecretsChangedHandler(testCtx(), containerSvc, configSvc, 100*time.Millisecond)
	defer h.Stop()

	err := h.Handle(context.Background(), domain.Event{
		Type: domain.EventSecretsChanged,
		Data: "wrong-payload",
	})

	assert.NoError(t, err)
}

func TestSecretsChangedHandler_FireDeploySkipsStaleGeneration(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	h := NewSecretsChangedHandler(testCtx(), containerSvc, configSvc, time.Second)
	defer h.Stop()

	const domainName = "app.example.com"

	// White-box: directly set internal state to simulate a stale generation.
	// This exercises the generation-based debounce logic that prevents
	// already-fired timers from triggering duplicate deploys.
	h.mu.Lock()
	h.generations[domainName] = 2
	h.timers[domainName] = time.NewTimer(time.Hour)
	h.mu.Unlock()

	staleDone := make(chan struct{})
	go func() {
		defer close(staleDone)
		h.fireDeploy(context.Background(), domainName, 1)
	}()

	select {
	case <-staleDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stale fireDeploy did not return")
	}

	h.mu.Lock()
	_, timerExists := h.timers[domainName]
	gen := h.generations[domainName]
	h.mu.Unlock()

	assert.True(t, timerExists)
	assert.Equal(t, uint64(2), gen)
}

func TestSecretsChangedHandler_FireDeployClearsCurrentGeneration(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	h := NewSecretsChangedHandler(testCtx(), containerSvc, configSvc, time.Second)
	defer h.Stop()

	route := &domain.Route{Domain: "app.example.com", Image: "app:latest"}
	configSvc.EXPECT().GetRoute(mock.Anything, route.Domain).Return(route, nil).Once()
	containerSvc.EXPECT().Deploy(mock.Anything, *route).Return(&domain.Container{ID: "container-1"}, nil).Once()

	h.mu.Lock()
	h.generations[route.Domain] = 1
	h.timers[route.Domain] = time.NewTimer(time.Hour)
	h.mu.Unlock()

	h.fireDeploy(context.Background(), route.Domain, 1)

	h.mu.Lock()
	_, timerExists := h.timers[route.Domain]
	_, generationExists := h.generations[route.Domain]
	h.mu.Unlock()

	assert.False(t, timerExists)
	assert.False(t, generationExists)
}

func TestSecretsChangedHandler_StopCancelsPendingDeploy(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	debounceDelay := 50 * time.Millisecond
	h := NewSecretsChangedHandler(testCtx(), containerSvc, configSvc, debounceDelay)

	assert.NoError(t, h.Handle(context.Background(), domain.Event{
		Type: domain.EventSecretsChanged,
		Data: domain.SecretsChangedPayload{Domain: "app.example.com", Operation: "set", Keys: []string{"A"}},
	}))

	h.Stop()
	time.Sleep(debounceDelay + 50*time.Millisecond)
}
