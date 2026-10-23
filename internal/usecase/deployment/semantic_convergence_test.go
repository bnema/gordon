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
	"github.com/bnema/gordon/internal/boundaries/out"
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

// TestDeploy_NetworkOnlyChangeConvergesOnlyWhenEveryMemberIsRepublished
// proves semantic convergence sees shared-network changes: a full deploy of a
// revision that only adds both services to a shared network leaves ACTIVE
// unconverged (and desired pending) when the second member fails, and
// converged when both members were republished under the revision.
func TestDeploy_NetworkOnlyChangeConvergesOnlyWhenEveryMemberIsRepublished(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fail      string
		converged bool
	}{
		{name: "second member fails", fail: "worker"},
		{name: "every member republished", converged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPromoHarness(t)
			h.failCreate = tc.fail
			web, worker := promoServices()
			next := testRevision("blog", web, worker)
			next.Revision = "rev-1"
			next.Spec.Networks = []domain.AppSharedNetwork{{Network: "shared", Services: []string{"web", "worker"}}}
			seedRevision(t, h.ctx, h.store, "intent-1", "rev-0", next)

			_, _ = h.svc.Deploy(h.ctx, deployment.DeployInput{App: "blog", Op: "op-net"})
			active := h.active()
			assert.Equal(t, tc.converged, active.Converged)
			converged, err := domain.AppSpecConverged(next.Spec, active, func(string) (map[string]string, error) { return map[string]string{}, nil })
			require.NoError(t, err)
			assert.Equal(t, tc.converged, converged, "desired is pending exactly when ACTIVE is unconverged")
		})
	}
}

// goneRevisionStore reports one revision as garbage-collected after its
// first load: the deploy's keep decision still sees it, and the convergence
// check that follows does not, as if retention collected it in between.
type goneRevisionStore struct {
	out.AppState
	gone  string
	loads int
}

func (s *goneRevisionStore) LoadRevision(ctx context.Context, app, revision string) (domain.AppDesiredRevision, error) {
	if revision == s.gone {
		s.loads++
		if s.loads > 1 {
			return domain.AppDesiredRevision{}, domain.ErrAppRevisionNotFound
		}
	}
	return s.AppState.LoadRevision(ctx, app, revision)
}

// TestDeploy_KeptServiceKeepsItsJoinedNetworks is the regression for a
// kept container keeping an older effective revision: rev-1 adds web and
// worker to a shared network and only web is published; rev-2 also changes
// the worker env. Web is kept unchanged with effective revision rev-1, whose
// memberships already match, so the rev-2 deploy converges and ACTIVE adopts
// the shared network. When rev-1 was garbage-collected, web's membership
// cannot be proven and convergence fails closed.
func TestDeploy_KeptServiceKeepsItsJoinedNetworks(t *testing.T) {
	for _, tc := range []struct {
		name      string
		gcRev1    bool
		converged bool
	}{
		{name: "kept member converges", converged: true},
		{name: "effective revision collected fails closed", gcRev1: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPromoHarness(t)
			shared := []domain.AppSharedNetwork{{Network: "shared", Services: []string{"web", "worker"}}}
			web, worker := promoServices()
			rev1 := testRevision("blog", web, worker)
			rev1.Revision = "rev-1"
			rev1.Spec.Networks = shared
			seedRevision(t, h.ctx, h.store, "intent-1", "rev-0", rev1)

			h.failCreate = "worker"
			_, _ = h.svc.Deploy(h.ctx, deployment.DeployInput{App: "blog", Op: "op-rev1"})
			active := h.active()
			require.False(t, active.Converged, "the partial rev-1 deploy is not converged")
			require.Equal(t, "rev-1", active.Services["web"].EffectiveRevision)
			require.Empty(t, active.Networks)

			worker.Env = map[string]string{"MODE": "prod"}
			rev2 := testRevision("blog", web, worker)
			rev2.Revision = "rev-2"
			rev2.Spec.Networks = shared
			seedRevision(t, h.ctx, h.store, "intent-2", "rev-1", rev2)
			h.failCreate = ""
			if tc.gcRev1 {
				h.state = &goneRevisionStore{AppState: h.store, gone: "rev-1"}
				h.build()
			}
			_, err := h.svc.Deploy(h.ctx, deployment.DeployInput{App: "blog", Op: "op-rev2"})
			require.NoError(t, err)

			active = h.active()
			assert.Equal(t, "rev-2", active.Services["worker"].EffectiveRevision)
			assert.Equal(t, "rev-1", active.Services["web"].EffectiveRevision, "web was kept unchanged")
			assert.Equal(t, tc.converged, active.Converged)
			if tc.converged {
				assert.Equal(t, shared, active.Networks)
			} else {
				assert.Empty(t, active.Networks, "unproven memberships are not adopted")
			}
		})
	}
}

// TestDeploy_TargetedDeployStaysUnconvergedWhenAnotherServiceDiffers proves
// a targeted deploy never converges an app whose desired revision also
// changes another service: the real targeted-deploy path refuses it with
// ErrAppStateConflict naming the other service's change, before it claims
// an operation, resolves an image, or touches the runtime. ACTIVE (both
// services, convergence, provenance) stays exactly as it was, and desired
// remains pending against it.
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
	before, ok, err := store.LoadActive(ctx, "blog")
	require.NoError(t, err)
	require.True(t, ok)

	// Strict mocks without expectations: any image resolution or runtime
	// call fails the test.
	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: outmocks.NewMockContainerRuntime(t), Images: outmocks.NewMockImageResolver(t),
		Secrets: outmocks.NewMockSecretProvider(t),
	}, zerowrap.Default())

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog", Service: "web", Op: "op-web"})
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "service-targeted deploy refused")
	assert.Contains(t, err.Error(), "rev-2")
	assert.Contains(t, err.Error(), "service/worker/image", "the refusal names the other service's change")

	after, ok, err := store.LoadActive(ctx, "blog")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, before, after, "ACTIVE is untouched: no service, convergence, or provenance change")
	assert.Equal(t, "c-worker", after.Services["worker"].Container)
	assert.Equal(t, "docker.io/example/worker:1", after.Services["worker"].Image)
	assert.Equal(t, "rev-0", after.ConvergedRevision)

	_, found, err := store.LoadLatestOperation(ctx, "blog")
	require.NoError(t, err)
	assert.False(t, found, "the refused deploy claimed no operation")

	converged, err := domain.AppSpecConverged(next.Spec, after, func(string) (map[string]string, error) { return map[string]string{}, nil })
	require.NoError(t, err)
	assert.False(t, converged, "desired rev-2 stays pending")
}
