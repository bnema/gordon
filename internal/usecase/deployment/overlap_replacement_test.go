package deployment_test

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"sync"
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

// TestDeploy_NonOverlapEligibleServicesKeepSequentialOrder proves a service
// with a volume, a bind, or a device is never run beside its predecessor: the
// service is withdrawn, the superseded container is stopped and removed, and
// only then is the replacement created.
func TestDeploy_NonOverlapEligibleServicesKeepSequentialOrder(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "binds", "config")
	require.NoError(t, os.MkdirAll(source, 0o755))

	cases := []struct {
		name         string
		mutate       func(*domain.AppService)
		inhibits     bool
		cdiSupported bool
	}{
		{"volume", func(s *domain.AppService) { s.Volumes = []domain.AppVolume{{Name: "data", Path: "/data"}} }, true, false},
		{"bind", func(s *domain.AppService) {
			s.Binds = []domain.AppBind{{Name: "config", Path: "/etc/app.conf", ReadOnly: true}}
		}, true, false},
		{"device", func(s *domain.AppService) { s.Devices = []string{"test_gpu"} }, false, true},
		{"udp", func(s *domain.AppService) {
			s.UDP = []domain.AppUDPInterface{{Entrypoint: "game", Port: 28015, Publish: "0.0.0.0:28015"}}
		}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			state, runtime, images, secrets := mockDeps(t)
			rev := mockRevision()
			tc.mutate(&rev.Spec.Services[0])

			expectDeployPreflight(state, runtime, images, secrets, rev)
			runtime.EXPECT().VolumeExists(mock.Anything, mock.Anything).Return(false, nil).Maybe()
			runtime.EXPECT().CreateVolume(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
			if tc.cdiSupported {
				runtime.EXPECT().SupportsCDIDevices(mock.Anything).Return(nil)
			}
			expectNetworkProvision(runtime, "app-blog", 1)
			state.EXPECT().LoadActive(mock.Anything, "blog").Return(replaceableActive(), true, nil)
			state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
			if tc.inhibits {
				state.EXPECT().SaveRecoveryInhibition(mock.Anything, mock.Anything).Return(nil).Once()
			}

			var order []string
			expectRetireOld(state, runtime, &order, "c-old")
			expectSequentialCreate(state, runtime, &order, "c-new", 18080)
			state.EXPECT().SaveActive(mock.Anything, mock.Anything).RunAndReturn(
				func(context.Context, domain.AppActive) error {
					recordEvent(&order, "publish-active")
					return nil
				}).Once()
			state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil)
			state.EXPECT().ClearRecoveryInhibition(mock.Anything, "blog", "web", "c-old").Return(nil).Once()

			svc := deployment.NewService(deployment.Deps{
				State: state, Runtime: runtime, Images: images, Secrets: secrets,
				Traffic: &orderedTraffic{events: &order},
			}, zerowrap.Default()).
				WithBindPolicies(map[string]domain.AppBindPolicy{"config": {
					Name: "config", Source: source, Root: root, AllowedApps: []string{"blog"}, AllowedServices: []string{"web"},
				}}).
				WithDevicePolicies(map[string]domain.AppDevicePolicy{"test_gpu": {
					Name: "test_gpu", CDI: []string{"example.com/gpu=GPU-test-uuid"},
					AllowedApps: []string{"blog"}, AllowedServices: []string{"web"},
				}}).
				WithProbeDeps(deployment.NewTestProbeDeps(runtime,
					func(context.Context, string, string) (int, error) { return 200, nil },
					func(context.Context, string) error { return nil },
				))

			result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
			require.NoError(t, err)
			assert.Equal(t, "deployed", result.Services["web"].Result)
			assert.Equal(t,
				[]string{"withdraw", "stop-old", "remove-old", "create-new", "start-new", "publish-active", "traffic"},
				order, "the superseded container is gone before the replacement is created")
		})
	}
}

// TestDeploy_OverlapPublicationFailureKeepsOldContainer proves a failed ACTIVE
// publication of an overlap replacement never stops the superseded container,
// which still serves because ACTIVE and routing were not switched. The
// candidate stays journaled as the step's After, so reconciliation removes it,
// and the failure is reported with a leftover warning on the old container.
func TestDeploy_OverlapPublicationFailureKeepsOldContainer(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	expectDeployPreflight(state, runtime, images, secrets, rev)
	expectNetworkProvision(runtime, "app-blog", 1)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(replaceableActive(), true, nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	expectSequentialCreate(state, runtime, nil, "c-new", 18080)
	expectRetirementMarker(state, "c-old")
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(assert.AnError).Once()

	var events []string
	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
		Traffic: &orderedTraffic{events: &events},
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.Error(t, err)
	assert.Equal(t, "failed", result.Services["web"].Result)
	assert.Equal(t, "c-new", result.Services["web"].After)
	require.Len(t, result.Services["web"].CleanupWarnings, 1)
	assert.Equal(t, "c-old", result.Services["web"].CleanupWarnings[0].Leftover)
	assert.Empty(t, events, "the proxy graph was neither withdrawn nor rebuilt")
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "ReleaseBackendBinds", mock.Anything, mock.Anything, mock.Anything)
}

// storeTraffic is a traffic publisher that behaves like the real one against
// a real app-state store: a withdrawal drops the service's recorded binds from
// ACTIVE (and can then report a failed graph apply), and a rebuild routes
// exactly the binds ACTIVE records at that moment. Tests assert on what is
// routed, not on call order.
type storeTraffic struct {
	mu           sync.Mutex
	store        *appstate.Store
	failWithdraw int
	failRebuild  int
	withdrawn    []string
	routed       []map[string]map[int]int
}

func (t *storeTraffic) HoldService(ctx context.Context, app, service string, _ time.Time) error {
	return t.WithdrawService(ctx, app, service)
}

func (t *storeTraffic) WithdrawService(ctx context.Context, app, service string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.withdrawn = append(t.withdrawn, app+"/"+service)
	active, ok, err := t.store.LoadActive(ctx, app)
	if err != nil || !ok {
		return err
	}
	if eff, found := active.Services[service]; found {
		eff.BackendBinds, eff.UDPBackendBinds = nil, nil
		active.Services[service] = eff
		if err := t.store.SaveActive(ctx, active); err != nil {
			return err
		}
	}
	if t.failWithdraw > 0 {
		t.failWithdraw--
		return assert.AnError
	}
	return nil
}

func (t *storeTraffic) WithdrawServiceState(ctx context.Context, app, service string) error {
	return t.WithdrawService(ctx, app, service)
}

func (t *storeTraffic) RebuildTraffic(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failRebuild > 0 {
		t.failRebuild--
		return assert.AnError
	}
	active, _, err := t.store.LoadActive(ctx, "blog")
	if err != nil {
		return err
	}
	routes := map[string]map[int]int{}
	for name, eff := range active.Services {
		routes[name] = maps.Clone(eff.BackendBinds)
	}
	t.routed = append(t.routed, routes)
	return nil
}

// lastRoute returns the binds the most recent graph routed for a service.
func (t *storeTraffic) lastRoute(service string) map[int]int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.routed) == 0 {
		return nil
	}
	return t.routed[len(t.routed)-1][service]
}

// allRoutes returns the binds of every graph routed for a service.
func (t *storeTraffic) allRoutes(service string) []map[int]int {
	t.mu.Lock()
	defer t.mu.Unlock()
	var routes []map[int]int
	for _, routed := range t.routed {
		routes = append(routes, routed[service])
	}
	return routes
}

// withdrawals returns the services withdrawn so far, in order.
func (t *storeTraffic) withdrawals() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.withdrawn...)
}

func (t *storeTraffic) rebuildCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.routed)
}

const overlapImage = "registry.example.com/blog/web:1.4.2"

// overlapWebSpec is a stateless public HTTP service.
func overlapWebSpec(grace time.Duration) domain.AppService {
	return domain.AppService{
		Name: "web", Image: overlapImage, Secrets: map[string]string{}, StopGrace: grace,
		HTTP: []domain.AppHTTPInterface{{Host: "blog.example.com", Port: 8080, TLS: domain.AppTLSAuto}},
		Readiness: domain.AppReadiness{
			Type: domain.AppReadinessHTTP, Path: "/healthz", Timeout: time.Second,
		},
	}
}

func overlapOldBinds() map[int]int { return map[int]int{8080: 17000} }

// seedServingApp seeds an app whose container c-old serves on 17000, with a
// materialized revision to deploy over it.
func seedServingApp(t *testing.T, ctx context.Context, store *appstate.Store, spec domain.AppService) {
	t.Helper()
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog"}))
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{App: "blog", ID: "app-blog"}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-old", EffectiveRevision: "rev-0", Spec: spec, BackendBinds: overlapOldBinds()},
	}}))
	seedDesired(t, ctx, store, testRevision("blog", spec))
}

// overlapDeployDeps wires the preflight and candidate creation of one overlap
// deploy that creates c-new on 18081 and expects readiness to pass.
func overlapDeployMocks(runtime *outmocks.MockContainerRuntime, images *outmocks.MockImageResolver) {
	images.EXPECT().ResolveDigest(mock.Anything, overlapImage).Return(restartTestDigest, nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, overlapImage).Return(nil, nil).Once()
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-new").Return(&domain.Container{ID: "c-new", Status: "running", StartedAt: executionStartedAt}, nil).Maybe()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18081, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
}

func overlapService(store *appstate.Store, runtime *outmocks.MockContainerRuntime, images *outmocks.MockImageResolver, secrets *outmocks.MockSecretProvider, traffic *storeTraffic) *deployment.Service {
	return deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets, Traffic: traffic,
		ImagePolicy: deployment.FixedImagePolicy(domain.ImageSourcePolicy{AllowedRegistries: []string{"registry.example.com"}}),
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))
}

// expectVerifiedRunning wires one recovery verification of a running
// container that publishes the given loopback port.
func expectVerifiedRunning(runtime *outmocks.MockContainerRuntime, id string, port int) {
	started := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	runtime.EXPECT().InspectContainer(mock.Anything, id).
		Return(&domain.Container{ID: id, Status: "running", StartedAt: started}, nil)
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, id).Return("", false, nil)
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, id, mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: port, Protocol: domain.NetworkProtocolTCP}}, nil)
}

// TestDeploy_OverlapSwitchIsNotErasedByAPendingWithdrawal proves that a
// publication withdrawal left pending by an earlier failed recovery does not
// unroute the new generation: the deploy resolves it before publishing, so
// the graph the switch applies routes the candidate's binds. The traffic fake
// mutates ACTIVE like the real publisher, so a stray withdrawal after the
// switch would leave the service unrouted.
func TestDeploy_OverlapSwitchIsNotErasedByAPendingWithdrawal(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedServingApp(t, ctx, store, overlapWebSpec(0))
	traffic := &storeTraffic{store: store, failWithdraw: 1}
	svc := overlapService(store, runtime, images, secrets, traffic)

	// A recovery pass finds c-old down and cannot prove its withdrawal: the
	// service is marked as having a pending withdrawal.
	runtime.EXPECT().InspectContainer(mock.Anything, "c-old").
		Return(&domain.Container{ID: "c-old", Status: "exited"}, nil).Once()
	require.Error(t, svc.ReconcileRunning(ctx))

	overlapDeployMocks(runtime, images)
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", domain.AppDefaultStopGrace).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	assert.Equal(t, "deployed", result.Services["web"].Result)

	assert.Equal(t, map[int]int{8080: 18081}, traffic.lastRoute("web"), "the service routes the new generation")
	active, _, err := store.LoadActive(ctx, "blog")
	require.NoError(t, err)
	assert.Equal(t, "c-new", active.Services["web"].Container)
	assert.Equal(t, map[int]int{8080: 18081}, active.Services["web"].BackendBinds,
		"ACTIVE keeps only the new generation's binds")
}

// TestDeploy_OverlapRetirementSurvivesANewerOperation proves the cleanup of a
// superseded container is owned by the durable retirement marker, not by the
// operation journal: after the first retirement fails and a newer operation
// becomes the latest journal, the periodic reconciliation still retires it,
// with the predecessor's own stop grace, and only once its successor is
// verified and routed.
func TestDeploy_OverlapRetirementSurvivesANewerOperation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedServingApp(t, ctx, store, overlapWebSpec(45*time.Second))
	traffic := &storeTraffic{store: store}
	svc := overlapService(store, runtime, images, secrets, traffic)

	overlapDeployMocks(runtime, images)
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", 45*time.Second).Return(nil).Twice()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(assert.AnError).Once()

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err, "a retirement leftover does not fail a deploy whose new generation serves")
	require.Len(t, result.CleanupWarnings, 1)
	assert.Equal(t, "c-old", result.CleanupWarnings[0].Leftover)

	markers, err := store.LoadRecoveryInhibitions(ctx, "blog")
	require.NoError(t, err)
	require.Len(t, markers, 1, "the failed retirement leaves its durable marker")
	assert.Equal(t, "c-old", markers[0].ContainerID)
	assert.Equal(t, domain.AppInhibitRetirementPending, markers[0].Reason)
	assert.Equal(t, 45*time.Second, markers[0].StopGrace, "the predecessor's grace travels with the marker")

	// A newer operation becomes the latest journal and masks the deploy's.
	require.NoError(t, store.SaveOperation(ctx, domain.AppOperation{
		Op: "op-newer", Kind: "deploy", App: "blog", StartedAt: time.Now().Add(time.Minute).UTC(),
		Outcome: domain.AppOutcomeSuccess,
	}))

	expectVerifiedRunning(runtime, "c-new", 18082)
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	require.NoError(t, svc.ReconcileRunning(ctx))

	markers, err = store.LoadRecoveryInhibitions(ctx, "blog")
	require.NoError(t, err)
	assert.Empty(t, markers, "the marker is cleared once the container is confirmed gone")
	assert.Equal(t, map[int]int{8080: 18082}, traffic.lastRoute("web"), "the verified successor is routed")
	active, _, err := store.LoadActive(ctx, "blog")
	require.NoError(t, err)
	assert.Equal(t, "c-new", active.Services["web"].Container)
}

// TestReconcileRunning_RetiresSupersededOnlyAfterSuccessorIsVerified proves a
// superseded container is kept while its successor cannot be verified: the
// working generation is never stopped in favour of one that is not serving.
func TestReconcileRunning_RetiresSupersededOnlyAfterSuccessorIsVerified(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedServingApp(t, ctx, store, overlapWebSpec(0))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-new", EffectiveRevision: "rev-1", Spec: overlapWebSpec(0), BackendBinds: map[int]int{8080: 18081}},
	}}))
	require.NoError(t, store.SaveRecoveryInhibition(ctx, domain.AppRecoveryInhibition{
		App: "blog", Service: "web", ContainerID: "c-old", Reason: domain.AppInhibitRetirementPending,
	}))
	traffic := &storeTraffic{store: store}
	svc := overlapService(store, runtime, images, secrets, traffic)

	runtime.EXPECT().InspectContainer(mock.Anything, "c-new").Return(nil, assert.AnError).Once()
	require.Error(t, svc.ReconcileRunning(ctx))
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, "c-old", mock.Anything)
	markers, err := store.LoadRecoveryInhibitions(ctx, "blog")
	require.NoError(t, err)
	assert.Len(t, markers, 1, "the marker waits for a verified successor")

	// A record written before the grace was persisted falls back to the default.
	expectVerifiedRunning(runtime, "c-new", 18081)
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", domain.AppDefaultStopGrace).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	require.NoError(t, svc.ReconcileRunning(ctx))
	markers, err = store.LoadRecoveryInhibitions(ctx, "blog")
	require.NoError(t, err)
	assert.Empty(t, markers)
}

// TestReconcileRunning_NeverRetiresAPendingContainerThatActiveNamesAgain
// proves a rollback to the superseded container is safe: when ACTIVE names the
// container again, its stale retirement marker is dropped and it is kept.
func TestReconcileRunning_NeverRetiresAPendingContainerThatActiveNamesAgain(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedServingApp(t, ctx, store, overlapWebSpec(0))
	require.NoError(t, store.SaveRecoveryInhibition(ctx, domain.AppRecoveryInhibition{
		App: "blog", Service: "web", ContainerID: "c-old", Reason: domain.AppInhibitRetirementPending,
	}))
	svc := overlapService(store, runtime, images, secrets, &storeTraffic{store: store})

	expectVerifiedRunning(runtime, "c-old", 17000)
	require.NoError(t, svc.ReconcileRunning(ctx))

	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
	markers, err := store.LoadRecoveryInhibitions(ctx, "blog")
	require.NoError(t, err)
	assert.Empty(t, markers)
	active, _, err := store.LoadActive(ctx, "blog")
	require.NoError(t, err)
	assert.Equal(t, "c-old", active.Services["web"].Container)
}

// TestBootRecovery_DoesNotRepublishUnverifiedBindsOfAPublishedCandidate proves
// interrupted-operation reconciliation never rebuilds traffic from the binds
// ACTIVE persisted for a published candidate: a restarted candidate's
// ephemeral port may belong to another workload. Only the verified start path
// routes, and the superseded container is retired afterwards through its
// marker.
func TestBootRecovery_DoesNotRepublishUnverifiedBindsOfAPublishedCandidate(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	spec := overlapWebSpec(0)
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog"}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-new", EffectiveRevision: "rev-1", Spec: spec, BackendBinds: map[int]int{8080: 18081}},
	}}))
	require.NoError(t, store.SaveOperation(ctx, domain.AppOperation{
		Op: "op-switch", Kind: "deploy", App: "blog", StartedAt: time.Now().UTC(),
		Steps: []domain.AppOperationStep{
			{ID: "preflight", State: domain.AppStepSucceeded},
			{ID: "service.web.replace", State: domain.AppStepPending, Service: "web", Before: "c-old", After: "c-new"},
		},
	}))
	require.NoError(t, store.SaveRecoveryInhibition(ctx, domain.AppRecoveryInhibition{
		App: "blog", Service: "web", ContainerID: "c-old", Reason: domain.AppInhibitRetirementPending,
	}))
	traffic := &storeTraffic{store: store}
	svc := overlapService(store, runtime, images, secrets, traffic)

	// The candidate restarted and now publishes a different port.
	runtime.EXPECT().IsContainerRunning(mock.Anything, "c-new").Return(true, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-new").Return(&domain.Container{ID: "c-new", Status: "running", StartedAt: executionStartedAt}, nil).Maybe()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18090, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	require.NoError(t, svc.ReconcileBoot(ctx))

	require.Positive(t, traffic.rebuildCount())
	for _, routed := range traffic.allRoutes("web") {
		assert.NotEqual(t, map[int]int{8080: 18081}, routed, "the unverified persisted port is never routed")
	}
	assert.Equal(t, map[int]int{8080: 18090}, traffic.lastRoute("web"), "the verified bind is routed")
	active, _, err := store.LoadActive(ctx, "blog")
	require.NoError(t, err)
	assert.Equal(t, map[int]int{8080: 18090}, active.Services["web"].BackendBinds)

	// Boot keeps the superseded container and its marker: the periodic pass
	// retires it once the successor is verified.
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, "c-old", mock.Anything)
	markers, err := store.LoadRecoveryInhibitions(ctx, "blog")
	require.NoError(t, err)
	require.Len(t, markers, 1)
	assert.Equal(t, "c-old", markers[0].ContainerID)
}

// TestDeploy_OverlapTrafficFailureIsRecoveredByThePeriodicPass proves the
// failed switch is recoverable and traceable: the graph apply is rejected after
// ACTIVE named the new container, the superseded container keeps running with
// its marker, the next deploy-side reconciliation keeps that marker, and the
// periodic pass retires the old container once the successor is routed.
func TestDeploy_OverlapTrafficFailureIsRecoveredByThePeriodicPass(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedServingApp(t, ctx, store, overlapWebSpec(0))
	traffic := &storeTraffic{store: store, failRebuild: 1}
	svc := overlapService(store, runtime, images, secrets, traffic)

	overlapDeployMocks(runtime, images)
	_, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.Error(t, err, "a rejected graph is a failed deploy")
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)

	active, _, err := store.LoadActive(ctx, "blog")
	require.NoError(t, err)
	assert.Equal(t, "c-new", active.Services["web"].Container, "ACTIVE names the new container")
	markers, err := store.LoadRecoveryInhibitions(ctx, "blog")
	require.NoError(t, err)
	require.Len(t, markers, 1)
	assert.Equal(t, "c-old", markers[0].ContainerID)

	// The next mutation reconciles the failed deploy: the published candidate
	// is kept, and so is the retirement marker.
	require.NoError(t, svc.AbandonDeploy(ctx, deployment.DeployClaim{App: "blog", Op: "op-none"}))
	markers, err = store.LoadRecoveryInhibitions(ctx, "blog")
	require.NoError(t, err)
	require.Len(t, markers, 1, "a published candidate's reconciliation keeps the retirement marker")

	expectVerifiedRunning(runtime, "c-new", 18081)
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", domain.AppDefaultStopGrace).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	require.NoError(t, svc.ReconcileRunning(ctx))
	assert.Equal(t, map[int]int{8080: 18081}, traffic.lastRoute("web"))
	markers, err = store.LoadRecoveryInhibitions(ctx, "blog")
	require.NoError(t, err)
	assert.Empty(t, markers)
}

// TestDeploy_PredecessorWithOwnedResourcesNeverOverlaps proves eligibility
// looks at the predecessor's effective spec too: a replacement that drops the
// UDP interface or the volume of the generation it supersedes still replaces
// it sequentially, because the old generation's UDP sessions or writes
// outlive the switch.
func TestDeploy_PredecessorWithOwnedResourcesNeverOverlaps(t *testing.T) {
	cases := map[string]func(*domain.AppService){
		"udp": func(s *domain.AppService) {
			s.UDP = []domain.AppUDPInterface{{Entrypoint: "game", Port: 28015, Publish: "0.0.0.0:28015"}}
		},
		"volume": func(s *domain.AppService) { s.Volumes = []domain.AppVolume{{Name: "data", Path: "/data"}} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			state, runtime, images, secrets := mockDeps(t)
			rev := mockRevision() // HTTP-only, no volume: eligible on its own
			previous := rev.Spec.Services[0]
			mutate(&previous)
			active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
				"web": {EffectiveRevision: "rev-0", Image: "img:0", Container: "c-old", Spec: previous},
			}}

			expectDeployPreflight(state, runtime, images, secrets, rev)
			expectNetworkProvision(runtime, "app-blog", 1)
			state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
			state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
			var order []string
			expectRetireOld(state, runtime, &order, "c-old")
			expectSequentialCreate(state, runtime, &order, "c-new", 18080)
			state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil).Once()
			state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil)
			state.EXPECT().ClearRecoveryInhibition(mock.Anything, "blog", "web", "c-old").Return(nil).Once()

			svc := deployment.NewService(deployment.Deps{
				State: state, Runtime: runtime, Images: images, Secrets: secrets,
				Traffic: &orderedTraffic{events: &order},
			}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
				func(context.Context, string, string) (int, error) { return 200, nil },
				func(context.Context, string) error { return nil },
			))
			_, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
			require.NoError(t, err)
			assert.Equal(t, []string{"withdraw", "stop-old", "remove-old", "create-new", "start-new", "traffic"}, order[:6],
				"the predecessor is gone before the replacement is created")
			state.AssertNotCalled(t, "SaveRecoveryInhibition", mock.Anything, mock.Anything)
		})
	}
}

// seedPendingRetirement seeds ACTIVE naming c-new and one retirement-pending
// marker per given superseded container.
func seedPendingRetirement(t *testing.T, ctx context.Context, store *appstate.Store, superseded ...string) {
	t.Helper()
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog"}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-new", EffectiveRevision: "rev-1", Spec: overlapWebSpec(0), BackendBinds: map[int]int{8080: 18081}},
	}}))
	for _, id := range superseded {
		require.NoError(t, store.SaveRecoveryInhibition(ctx, domain.AppRecoveryInhibition{
			App: "blog", Service: "web", ContainerID: id, Reason: domain.AppInhibitRetirementPending,
		}))
	}
}

func markerIDs(t *testing.T, ctx context.Context, store *appstate.Store) []string {
	t.Helper()
	markers, err := store.LoadRecoveryInhibitions(ctx, "blog")
	require.NoError(t, err)
	ids := make([]string, 0, len(markers))
	for _, marker := range markers {
		ids = append(ids, marker.ContainerID)
	}
	return ids
}

// TestReconcileRunning_DoesNotRetireWhenSuccessorIsNotVerifiedByThisPass is the
// regression for a successor that was verified by an earlier pass but is
// restarting in this one: it is withdrawn, not verified, and a final
// inspection that happens to see it running must not retire the predecessor.
func TestReconcileRunning_DoesNotRetireWhenSuccessorIsNotVerifiedByThisPass(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedPendingRetirement(t, ctx, store, "c-old")
	traffic := &storeTraffic{store: store}
	svc := overlapService(store, runtime, images, secrets, traffic)

	// Earlier pass: the successor is verified and routed, retirement fails.
	expectVerifiedRunning(runtime, "c-new", 18081)
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(assert.AnError).Once()
	require.Error(t, svc.ReconcileRunning(ctx))

	// This pass: the successor is restarting, so it is withdrawn and not
	// verified. Any later inspection would see it running.
	runtime.ExpectedCalls, runtime.Calls = nil, nil
	runtime.EXPECT().InspectContainer(mock.Anything, "c-new").
		Return(&domain.Container{ID: "c-new", Status: "restarting"}, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-new").
		Return(&domain.Container{ID: "c-new", Status: "running", StartedAt: time.Now()}, nil).Maybe()
	require.NoError(t, svc.ReconcileRunning(ctx))

	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, "c-old", mock.Anything)
	assert.Equal(t, []string{"c-old"}, markerIDs(t, ctx, store), "the predecessor stays until a pass verifies the successor")
}

// TestReconcileRunning_DoesNotRetireWhenSuccessorRestartedAfterVerification
// covers the other half: a successor verified at one start time and seen with
// another by the final inspection is a different execution, so unverified.
func TestReconcileRunning_DoesNotRetireWhenSuccessorRestartedAfterVerification(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedPendingRetirement(t, ctx, store, "c-old")
	svc := overlapService(store, runtime, images, secrets, &storeTraffic{store: store})

	first := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	restarted := first.Add(time.Minute)
	runtime.EXPECT().InspectContainer(mock.Anything, "c-new").Return(&domain.Container{ID: "c-new", Status: "running", StartedAt: first}, nil).Twice()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-new").Return(&domain.Container{ID: "c-new", Status: "running", StartedAt: restarted}, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-new").Return("", false, nil)
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18081, Protocol: domain.NetworkProtocolTCP}}, nil)
	require.NoError(t, svc.ReconcileRunning(ctx))

	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, "c-old", mock.Anything)
	assert.Equal(t, []string{"c-old"}, markerIDs(t, ctx, store))
}

// TestReconcileRunning_RetiresStackedPredecessors proves several superseded
// containers of one service (deploys that each left one behind) are all
// retired once the successor is verified.
func TestReconcileRunning_RetiresStackedPredecessors(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedPendingRetirement(t, ctx, store, "c-old-1", "c-old-2")
	svc := overlapService(store, runtime, images, secrets, &storeTraffic{store: store})

	expectVerifiedRunning(runtime, "c-new", 18081)
	for _, id := range []string{"c-old-1", "c-old-2"} {
		runtime.EXPECT().StopContainer(mock.Anything, id, domain.AppDefaultStopGrace).Return(nil).Once()
		runtime.EXPECT().RemoveContainer(mock.Anything, id, false).Return(nil).Once()
	}
	require.NoError(t, svc.ReconcileRunning(ctx))
	assert.Empty(t, markerIDs(t, ctx, store))
}

// releaseFailingState wraps the real store and fails the first backend claim
// releases, like a store write error would.
type releaseFailingState struct {
	*appstate.Store
	failures int
}

func (s *releaseFailingState) ReleaseBackendBinds(ctx context.Context, app, containerID string) error {
	if s.failures > 0 {
		s.failures--
		return assert.AnError
	}
	return s.Store.ReleaseBackendBinds(ctx, app, containerID)
}

// TestReconcileRunning_KeepsMarkerUntilBackendClaimsAreReleased proves a
// retirement whose claim release failed does not drop the durable marker: the
// leak is reported by the pass and retried (retiring a gone container is
// idempotent) until the claims are released.
func TestReconcileRunning_KeepsMarkerUntilBackendClaimsAreReleased(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedPendingRetirement(t, ctx, store, "c-old")
	state := &releaseFailingState{Store: store, failures: 1}
	traffic := &storeTraffic{store: store}
	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets, Traffic: traffic,
		ImagePolicy: deployment.FixedImagePolicy(domain.ImageSourcePolicy{AllowedRegistries: []string{"registry.example.com"}}),
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))

	expectVerifiedRunning(runtime, "c-new", 18081)
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(nil).Twice()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(domain.ErrContainerNotFound).Once()

	err := svc.ReconcileRunning(ctx)
	require.Error(t, err, "the leaked claims are reported, not discarded")
	assert.Contains(t, err.Error(), "release backend claims")
	assert.Equal(t, []string{"c-old"}, markerIDs(t, ctx, store), "the marker stays until the claims are released")

	require.NoError(t, svc.ReconcileRunning(ctx), "not-found counts as gone, so the retry settles it")
	assert.Empty(t, markerIDs(t, ctx, store))
}

// TestStop_RetiresPendingSupersededContainers proves a stopped app does not
// keep a superseded predecessor running, and that one which cannot be retired
// fails the stop and keeps its marker.
func TestStop_RetiresPendingSupersededContainers(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedPendingRetirement(t, ctx, store, "c-old")
	svc := overlapService(store, runtime, images, secrets, &storeTraffic{store: store})

	runtime.EXPECT().StopContainer(mock.Anything, "c-new", mock.Anything).Return(nil).Twice()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-new", false).Return(nil).Twice()
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(nil).Twice()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(assert.AnError).Once()

	result, err := svc.Stop(ctx, "blog", "")
	require.Error(t, err, "a predecessor that is still running fails the stop")
	assert.Equal(t, "failed", result.Services["web"].Result)
	assert.Equal(t, []string{"c-old"}, markerIDs(t, ctx, store), "its marker stays for the next attempt")

	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	_, err = svc.Stop(ctx, "blog", "")
	require.NoError(t, err)
	assert.Empty(t, markerIDs(t, ctx, store))
}

// TestRemove_FailsAndKeepsStateWhenAPendingPredecessorCannotBeRetired proves
// Remove does not end the incarnation (which deletes the markers) while a
// superseded container is still running.
func TestRemove_FailsAndKeepsStateWhenAPendingPredecessorCannotBeRetired(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedPendingRetirement(t, ctx, store, "c-old")
	svc := overlapService(store, runtime, images, secrets, &storeTraffic{store: store})

	runtime.EXPECT().StopContainer(mock.Anything, "c-new", mock.Anything).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-new", false).Return(nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(assert.AnError).Once()

	_, err := svc.Remove(ctx, "blog", "")
	require.Error(t, err)
	assert.Equal(t, []string{"c-old"}, markerIDs(t, ctx, store), "the marker survives so the container stays tracked")
	_, ok, loadErr := store.LoadActive(ctx, "blog")
	require.NoError(t, loadErr)
	assert.True(t, ok, "the incarnation is not ended")
}

// inhibitionsFailingState wraps the real store and fails the load of the
// recovery inhibitions, which is how Stop learns about pending predecessors.
type inhibitionsFailingState struct {
	*appstate.Store
}

func (s *inhibitionsFailingState) LoadRecoveryInhibitions(context.Context, string) ([]domain.AppRecoveryInhibition, error) {
	return nil, assert.AnError
}

// TestStop_PendingRetirementLoadFailureIsAJournaledFailure proves an app-level
// cleanup failure (the pending predecessors cannot even be listed) is not lost
// in the per-service results: the operation is journaled as failed, so Stop
// errors and a replay of the same key reads the same failure instead of a
// success.
func TestStop_PendingRetirementLoadFailureIsAJournaledFailure(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	seedPendingRetirement(t, ctx, store, "c-old")
	// The ACTIVE container is retired successfully before the load fails.
	runtime.EXPECT().StopContainer(mock.Anything, "c-new", mock.Anything).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-new", false).Return(nil).Once()
	svc := deployment.NewService(deployment.Deps{
		State: &inhibitionsFailingState{Store: store}, Runtime: runtime, Traffic: &storeTraffic{store: store},
	}, zerowrap.Default())

	result, err := svc.Stop(ctx, "blog", "key-stop")
	require.Error(t, err)
	assert.Equal(t, "deployed", result.Services["web"].Result, "the ACTIVE container really was stopped")
	require.NotEmpty(t, result.Warnings, "the app-level failure is surfaced on the result")

	op, loadErr := store.LoadOperation(ctx, "blog", "key-stop")
	require.NoError(t, loadErr)
	assert.Equal(t, domain.AppOutcomeFailed, op.Outcome, "the journal records the failure, not the per-service success")
	step, found := opStep(op, "pending.retire")
	require.True(t, found)
	assert.Equal(t, domain.AppStepFailed, step.State)
	assert.NotEmpty(t, step.Error)
	require.NotEmpty(t, op.Warnings)
	assert.Contains(t, op.Warnings[0].Detail, "pending retirements unknown")

	// Replaying the same key returns the same failure and runs nothing again.
	_, replayErr := svc.Stop(ctx, "blog", "key-stop")
	require.Error(t, replayErr)
	assert.ErrorIs(t, replayErr, domain.ErrAppStateConflict)
	assert.Contains(t, replayErr.Error(), domain.AppOutcomeFailed)
	runtime.AssertNumberOfCalls(t, "StopContainer", 1)
}

// executionStartedAt is the start time the fakes report for a freshly started
// container: deploy and restart observe it right after the start.
var executionStartedAt = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

// dropInspectExpectations removes the InspectContainer expectations so a test
// can report a different execution after a deploy.
func dropInspectExpectations(runtime *outmocks.MockContainerRuntime) {
	kept := runtime.ExpectedCalls[:0]
	for _, call := range runtime.ExpectedCalls {
		if call.Method != "InspectContainer" {
			kept = append(kept, call)
		}
	}
	runtime.ExpectedCalls = kept
}

// TestReconcileRunning_DoesNotWithdrawAFreshlyDeployedGeneration is the
// regression for a deployed generation being withdrawn by the first periodic
// pass: the deploy verified and routed that execution, so the pass must treat
// it as already seen. The traffic fake mutates ACTIVE like the real publisher,
// so a withdrawal would drop the service's routed binds.
func TestReconcileRunning_DoesNotWithdrawAFreshlyDeployedGeneration(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedServingApp(t, ctx, store, overlapWebSpec(0))
	traffic := &storeTraffic{store: store}
	svc := overlapService(store, runtime, images, secrets, traffic)

	overlapDeployMocks(runtime, images)
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	_, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	require.Equal(t, map[int]int{8080: 18081}, traffic.lastRoute("web"))

	// The first periodic pass sees the same execution the deploy verified.
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-new").Return("", false, nil)
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18081, Protocol: domain.NetworkProtocolTCP}}, nil)
	require.NoError(t, svc.ReconcileRunning(ctx))

	assert.Empty(t, traffic.withdrawals(), "the verified generation is never withdrawn")
	for i, routed := range traffic.allRoutes("web") {
		assert.Equal(t, map[int]int{8080: 18081}, routed, "graph %d kept routing the service", i)
	}
	active, _, err := store.LoadActive(ctx, "blog")
	require.NoError(t, err)
	assert.Equal(t, map[int]int{8080: 18081}, active.Services["web"].BackendBinds)
}

// TestReconcileRunning_TreatsARestartedExecutionAfterDeployAsFresh proves the
// recorded execution is the one observed before readiness: if the container
// restarted natively afterwards, its start time differs and the pass withdraws
// and verifies it again.
func TestReconcileRunning_TreatsARestartedExecutionAfterDeployAsFresh(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedServingApp(t, ctx, store, overlapWebSpec(0))
	traffic := &storeTraffic{store: store}
	svc := overlapService(store, runtime, images, secrets, traffic)

	overlapDeployMocks(runtime, images)
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	_, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)

	dropInspectExpectations(runtime)
	runtime.EXPECT().InspectContainer(mock.Anything, "c-new").Return(
		&domain.Container{ID: "c-new", Status: "running", StartedAt: executionStartedAt.Add(time.Minute)}, nil)
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-new").Return("", false, nil)
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18090, Protocol: domain.NetworkProtocolTCP}}, nil)
	require.NoError(t, svc.ReconcileRunning(ctx))

	assert.Equal(t, []string{"blog/web"}, traffic.withdrawals(), "a different execution is withdrawn before it is verified")
	assert.Equal(t, map[int]int{8080: 18090}, traffic.lastRoute("web"), "and routed again on its verified binds")
}

// TestReconcileRunning_DoesNotWithdrawARestartedGeneration covers the restart
// path: an in-place restart verified and routed the new execution, so the
// first periodic pass keeps it.
func TestReconcileRunning_DoesNotWithdrawARestartedGeneration(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedServingApp(t, ctx, store, overlapWebSpec(0))
	traffic := &storeTraffic{store: store}
	svc := overlapService(store, runtime, images, secrets, traffic)

	restartedAt := executionStartedAt.Add(time.Hour)
	runtime.EXPECT().RestartContainer(mock.Anything, "c-old", mock.Anything).Return(nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-old").Return(
		&domain.Container{ID: "c-old", Status: "running", StartedAt: restartedAt}, nil)
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-old", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 17002, Protocol: domain.NetworkProtocolTCP}}, nil)
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-old").Return("", false, nil)
	_, err := svc.Restart(ctx, "blog", "web", "")
	require.NoError(t, err)
	withdrawnByRestart := len(traffic.withdrawals())

	require.NoError(t, svc.ReconcileRunning(ctx))
	assert.Len(t, traffic.withdrawals(), withdrawnByRestart, "the periodic pass withdraws nothing more")
	assert.Equal(t, map[int]int{8080: 17002}, traffic.lastRoute("web"))
}

// TestReconcileRunning_RestoresARebuiltGenerationWhoseTrafficWasRejected is the
// regression for a missing-container rebuild whose graph apply was rejected:
// the rebuilt execution must not be recorded as verified, or the next periodic
// pass sees an already verified generation whose binds match ACTIVE, skips the
// publication, and leaves the service unrouted indefinitely.
func TestReconcileRunning_RestoresARebuiltGenerationWhoseTrafficWasRejected(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedReplaceableApp(t, ctx, store)
	traffic := &storeTraffic{store: store, failRebuild: 1}
	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets, Traffic: traffic,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))

	// The recorded container is gone: restart rebuilds it from the pinned
	// ACTIVE digest, and the graph apply that follows is rejected.
	runtime.EXPECT().RestartContainer(mock.Anything, "c-old", mock.Anything).Return(domain.ErrContainerNotFound).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(domain.ErrContainerNotFound).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(domain.ErrContainerNotFound).Once()
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-rebuilt", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-rebuilt").Return(nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-rebuilt").Return(
		&domain.Container{ID: "c-rebuilt", Status: "running", StartedAt: executionStartedAt}, nil)
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-rebuilt", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18082, Protocol: domain.NetworkProtocolTCP}}, nil)
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-rebuilt").Return("", false, nil)

	_, err := svc.Restart(ctx, "blog", "web", "op-restart")
	require.Error(t, err, "a rejected graph is a failed restart")
	require.Zero(t, traffic.rebuildCount(), "no graph was accepted")
	active, _, loadErr := store.LoadActive(ctx, "blog")
	require.NoError(t, loadErr)
	require.Equal(t, "c-rebuilt", active.Services["web"].Container)

	// The next periodic pass must verify and route the generation itself.
	require.NoError(t, svc.ReconcileRunning(ctx))
	assert.Equal(t, map[int]int{8080: 18082}, traffic.lastRoute("web"), "the rebuilt generation is routed again")
}
