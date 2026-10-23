package deployment_test

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/out/appstate"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

const workerDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// twoServiceSpec is a converged web + worker app. The worker has no
// interface, so a web-only deploy never touches it.
func twoServiceSpec() (domain.AppService, domain.AppService) {
	web := webService()
	web.Secrets = map[string]string{}
	web.Readiness = domain.AppReadiness{Type: domain.AppReadinessHTTP, Path: "/healthz", Timeout: time.Second}
	web.StopGrace = 10 * time.Second
	worker := domain.AppService{
		Name: "worker", Image: "docker.io/example/worker:1",
		Readiness: domain.AppReadiness{Type: domain.AppReadinessNone, Timeout: time.Second},
		StopGrace: 10 * time.Second,
	}
	return web, worker
}

// seedTwoServiceApp seeds ACTIVE at rev-0 for both services and a desired
// rev-1 whose only change is the web image.
func seedTwoServiceApp(t *testing.T, ctx context.Context, store *appstate.Store, webImage string) time.Time {
	t.Helper()
	web, worker := twoServiceSpec()
	activatedAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	base := testRevision("blog", web, worker)
	base.Revision = "rev-0"
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog"}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{
		App: "blog", Converged: true, ConvergedRevision: "rev-0",
		Services: map[string]domain.AppEffectiveService{
			"web": {
				Container: "c-old", EffectiveRevision: "rev-0", Image: web.Image, ActivatedBy: "op-created",
				ActivatedAt: activatedAt, Digest: restartTestDigest, Spec: web, BackendBinds: map[int]int{8080: 18080},
			},
			"worker": {
				Container: "c-worker", EffectiveRevision: "rev-0", Image: worker.Image, ActivatedBy: "op-created",
				ActivatedAt: activatedAt, Digest: workerDigest, Spec: worker,
			},
		},
	}))
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{App: "blog", ID: "app-blog"}))
	seedRevision(t, ctx, store, "intent-0", "", base)
	next := web
	next.Image = webImage
	desired := testRevision("blog", next, worker)
	seedRevision(t, ctx, store, "intent-1", "rev-0", desired)
	return activatedAt
}

// TestDeploy_TargetedDeployConvergesSemanticallyAndKeepsOtherProvenance
// proves a service-targeted deploy of a revision whose only change is that
// service leaves ACTIVE converged on the revision, while the untouched
// service keeps its effective revision and activation.
func TestDeploy_TargetedDeployConvergesSemanticallyAndKeepsOtherProvenance(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	activatedAt := seedTwoServiceApp(t, ctx, store, "docker.io/example/web:2.0")
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	images.EXPECT().ResolveDigest(mock.Anything, "docker.io/example/web:2.0").Return(
		"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, "docker.io/example/web:2.0").Return(nil, nil).Once()
	expectReplacement(runtime)
	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: outmocks.NewMockSecretProvider(t),
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog", Service: "web", Op: "op-web"})
	require.NoError(t, err)
	assert.Equal(t, "deployed", result.Services["web"].Result)

	active, ok, err := store.LoadActive(ctx, "blog")
	require.NoError(t, err)
	require.True(t, ok)
	assert.True(t, active.Converged, "every effective service equals desired")
	assert.Equal(t, "rev-1", active.ConvergedRevision)
	assert.Equal(t, "rev-1", active.Services["web"].EffectiveRevision)
	assert.Equal(t, "op-web", active.Services["web"].ActivatedBy)
	worker := active.Services["worker"]
	assert.Equal(t, "rev-0", worker.EffectiveRevision, "the untouched service keeps its provenance")
	assert.Equal(t, "op-created", worker.ActivatedBy)
	assert.True(t, activatedAt.Equal(worker.ActivatedAt))
}

// TestDeploy_TargetedDeployStaysUnconvergedWhenAnotherServiceDiffers proves
// semantic convergence still reports divergence: a targeted deploy of the
// web service leaves a worker whose desired image changed unconverged.
func TestDeploy_TargetedDeployStaysUnconvergedWhenAnotherServiceDiffers(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	seedTwoServiceApp(t, ctx, store, "docker.io/example/web:2.0")
	web, worker := twoServiceSpec()
	web.Image = "docker.io/example/web:2.0"
	worker.Image = "docker.io/example/worker:2"
	next := testRevision("blog", web, worker)
	next.Revision = "rev-2"
	seedRevision(t, ctx, store, "intent-2", "rev-1", next)

	active, _, err := store.LoadActive(ctx, "blog")
	require.NoError(t, err)
	converged, err := domain.AppSpecConverged(next.Spec, active, func(string) (map[string]string, error) { return map[string]string{}, nil })
	require.NoError(t, err)
	assert.False(t, converged)
}
