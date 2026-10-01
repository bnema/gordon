package deployment_test

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
	"github.com/bnema/gordon/internal/usecase/deployment"
)

func testRevision(app string, services ...domain.AppService) domain.AppDesiredRevision {
	for i := range services {
		if services[i].StopGrace == 0 {
			services[i].StopGrace = 10 * time.Second
		}
		if services[i].Readiness.Timeout == 0 {
			services[i].Readiness.Timeout = 30 * time.Second
		}
	}
	return domain.AppDesiredRevision{
		Revision: "rev-1", App: app,
		Spec: domain.AppSpec{Name: app, Env: map[string]string{}, Services: services},
	}
}

func webService() domain.AppService {
	return domain.AppService{
		Name: "web", Image: "docker.io/example/web:1.4.2",
		Readiness: domain.AppReadiness{Type: "http", Path: "/healthz"},
		HTTP:      []domain.AppHTTPInterface{{Host: "blog.example.com", Port: 8080, TLS: "auto"}},
		Secrets:   map[string]string{"DATABASE_URL": "database-url"},
	}
}

func preflightService(
	t *testing.T,
	state *outmocks.MockAppState,
	runtime *outmocks.MockContainerRuntime,
	images *outmocks.MockImageResolver,
	secrets *outmocks.MockSecretProvider,
) *deployment.Service {
	t.Helper()
	// Every mutation claims a journal only when no other operation of the
	// app is unfinished. These tests start clean.
	state.EXPECT().LoadLatestOperation(mock.Anything, mock.Anything).
		Return(domain.AppOperation{}, false, nil).Maybe()
	return deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
		ImagePolicy: domain.ImageSourcePolicy{AllowedRegistries: []string{"registry.example.com"}},
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))
}

func TestComputeOutcome_TerminalResults(t *testing.T) {
	deployed := deployment.ServiceResult{Result: "deployed"}
	failed := deployment.ServiceResult{Result: "failed", Error: "boom"}
	assert.Equal(t, "success", deployment.ComputeOutcome(map[string]deployment.ServiceResult{"a": deployed}))
	assert.Equal(t, "failed", deployment.ComputeOutcome(map[string]deployment.ServiceResult{"a": failed}))
	assert.Equal(t, "partial", deployment.ComputeOutcome(map[string]deployment.ServiceResult{"a": deployed, "b": failed}))
	assert.Equal(t, "failed", deployment.ComputeOutcome(map[string]deployment.ServiceResult{}))
}

// TestReconcileBoot_ContinuesAfterAppFailure proves R1: a failing app
// never blocks verification of the following apps, and failures are
// aggregated instead of aborting at the first one.
func TestReconcileBoot_ContinuesAfterAppFailure(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	svcSvc := preflightService(t, state, runtime, images, secrets)

	badEff := domain.AppEffectiveService{
		Container:    "ctr-bad",
		Spec:         webService(),
		BackendBinds: map[int]int{8080: 32768},
	}
	badActive := domain.AppActive{App: "bad", Services: map[string]domain.AppEffectiveService{"web": badEff}}
	goodEff := domain.AppEffectiveService{
		Container: "ctr-good",
		Spec:      webService(),
	}
	goodActive := domain.AppActive{App: "good", Services: map[string]domain.AppEffectiveService{"web": goodEff}}

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"bad", "good"}, nil).Once()
	// bad app: Start fails bind verification, binds withdrawn.
	state.EXPECT().LoadIntent(mock.Anything, "bad").Return(domain.AppStopIntent{App: "bad"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "bad").Return(badActive, true, nil)
	state.EXPECT().LoadLatestOperation(mock.Anything, "bad").Return(domain.AppOperation{}, false, nil).Maybe()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().SaveIntent(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "bad").Return(nil, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "ctr-bad").Return(true, nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "ctr-bad", mock.Anything).Return(nil, assert.AnError).Once()
	// No traffic boundary is wired here, so the fail-closed bind
	// withdrawal is a no-op and deployment writes no ACTIVE binds itself.
	// good app: still verified after the bad one failed.
	state.EXPECT().LoadIntent(mock.Anything, "good").Return(domain.AppStopIntent{App: "good"}, nil).Once()
	// Start load + bind-refresh reload.
	state.EXPECT().LoadActive(mock.Anything, "good").Return(goodActive, true, nil)
	state.EXPECT().LoadLatestOperation(mock.Anything, "good").Return(domain.AppOperation{}, false, nil).Maybe()
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "good").Return(nil, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "ctr-good").Return(true, nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "ctr-good", mock.Anything).Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32777, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	// Bind refresh persists the new bind (differs from recorded nil).
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil).Once()

	err := svcSvc.ReconcileBoot(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"bad"`)
}
