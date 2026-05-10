package app

import (
	"context"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"

	"github.com/bnema/gordon/internal/domain"
)

type startupRecoveryFakeConfigService struct {
	calls                 *[]string
	routes                []domain.Route
	autoRouteEnabled      bool
	panicOnAutoRouteCheck bool
}

func (f *startupRecoveryFakeConfigService) GetRoutes(_ context.Context) []domain.Route {
	*f.calls = append(*f.calls, "routes")
	return append([]domain.Route(nil), f.routes...)
}

func (f *startupRecoveryFakeConfigService) IsAutoRouteEnabled(_ context.Context) bool {
	if f.panicOnAutoRouteCheck {
		panic("unexpected auto-route gate during startup recovery")
	}
	if f.calls != nil {
		*f.calls = append(*f.calls, "auto-route-enabled")
	}
	return f.autoRouteEnabled
}

type startupRecoveryFakeContainerService struct {
	calls           *[]string
	syncErr         error
	autoStartErr    error
	autoStartCtx    context.Context
	autoStartRoutes []domain.Route
	startMonitorCtx context.Context
}

func (f *startupRecoveryFakeContainerService) SyncContainers(_ context.Context) error {
	*f.calls = append(*f.calls, "sync")
	return f.syncErr
}

func (f *startupRecoveryFakeContainerService) AutoStart(ctx context.Context, routes []domain.Route) error {
	*f.calls = append(*f.calls, "autostart")
	f.autoStartCtx = ctx
	f.autoStartRoutes = append([]domain.Route(nil), routes...)
	return f.autoStartErr
}

func (f *startupRecoveryFakeContainerService) StartMonitor(ctx context.Context) {
	*f.calls = append(*f.calls, "monitor")
	f.startMonitorCtx = ctx
}

func TestSyncAndRecoverConfiguredRoutes_HappyPath(t *testing.T) {
	ctx := context.Background()
	routes := []domain.Route{{Domain: "app.example.com", Image: "reg.example.com/app:latest", HTTPS: true}}
	calls := make([]string, 0, 4)

	configSvc := &startupRecoveryFakeConfigService{
		calls:  &calls,
		routes: routes,
	}
	containerSvc := &startupRecoveryFakeContainerService{calls: &calls}

	syncAndRecoverConfiguredRoutes(ctx, configSvc, containerSvc, zerowrap.Default())

	assert.Equal(t, []string{"sync", "routes", "autostart", "monitor"}, calls)
	assert.Equal(t, routes, containerSvc.autoStartRoutes)
	assert.True(t, domain.IsInternalDeploy(containerSvc.autoStartCtx))
	assert.False(t, domain.IsInternalDeploy(ctx))
	assert.False(t, domain.IsInternalDeploy(containerSvc.startMonitorCtx))
}

func TestSyncAndRecoverConfiguredRoutes_SyncFailureStillRecoversAndStartsMonitor(t *testing.T) {
	ctx := context.Background()
	routes := []domain.Route{{Domain: "app.example.com", Image: "reg.example.com/app:latest", HTTPS: true}}
	calls := make([]string, 0, 4)

	configSvc := &startupRecoveryFakeConfigService{
		calls:  &calls,
		routes: routes,
	}
	containerSvc := &startupRecoveryFakeContainerService{
		calls:   &calls,
		syncErr: assert.AnError,
	}

	assert.NotPanics(t, func() {
		syncAndRecoverConfiguredRoutes(ctx, configSvc, containerSvc, zerowrap.Default())
	})

	assert.Equal(t, []string{"sync", "routes", "autostart", "monitor"}, calls)
	assert.Equal(t, routes, containerSvc.autoStartRoutes)
	assert.True(t, domain.IsInternalDeploy(containerSvc.autoStartCtx))
	assert.False(t, domain.IsInternalDeploy(containerSvc.startMonitorCtx))
}

func TestSyncAndRecoverConfiguredRoutes_AutoStartFailureStillStartsMonitor(t *testing.T) {
	ctx := context.Background()
	routes := []domain.Route{{Domain: "app.example.com", Image: "reg.example.com/app:latest", HTTPS: true}}
	calls := make([]string, 0, 4)

	configSvc := &startupRecoveryFakeConfigService{
		calls:  &calls,
		routes: routes,
	}
	containerSvc := &startupRecoveryFakeContainerService{
		calls:        &calls,
		autoStartErr: assert.AnError,
	}

	assert.NotPanics(t, func() {
		syncAndRecoverConfiguredRoutes(ctx, configSvc, containerSvc, zerowrap.Default())
	})

	assert.Equal(t, []string{"sync", "routes", "autostart", "monitor"}, calls)
	assert.Equal(t, routes, containerSvc.autoStartRoutes)
	assert.True(t, domain.IsInternalDeploy(containerSvc.autoStartCtx))
	assert.False(t, domain.IsInternalDeploy(containerSvc.startMonitorCtx))
}

func TestSyncAndRecoverConfiguredRoutes_ProceedsEvenWhenAutoRouteDisabled(t *testing.T) {
	ctx := context.Background()
	routes := []domain.Route{{Domain: "app.example.com", Image: "reg.example.com/app:latest", HTTPS: true}}
	calls := make([]string, 0, 4)

	configSvc := &startupRecoveryFakeConfigService{
		calls:                 &calls,
		routes:                routes,
		autoRouteEnabled:      false,
		panicOnAutoRouteCheck: true,
	}
	containerSvc := &startupRecoveryFakeContainerService{calls: &calls}

	assert.NotPanics(t, func() {
		syncAndRecoverConfiguredRoutes(ctx, configSvc, containerSvc, zerowrap.Default())
	})

	assert.Equal(t, []string{"sync", "routes", "autostart", "monitor"}, calls)
	assert.Equal(t, routes, containerSvc.autoStartRoutes)
	assert.True(t, domain.IsInternalDeploy(containerSvc.autoStartCtx))
	assert.False(t, domain.IsInternalDeploy(containerSvc.startMonitorCtx))
}
