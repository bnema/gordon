package deployment_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

// fakeState is a minimal in-memory AppState for preflight tests.
type fakeState struct {
	mu         sync.Mutex
	desired    map[string]domain.AppDesiredRevision
	revisions  map[string]domain.AppDesiredRevision
	active     map[string]domain.AppActive
	checkpoint domain.AppStoreCheckpoint
	ownership  map[string]domain.AppOwnership
	ops        map[string]domain.AppOperation
	savedOps   int
	recoverErr error
}

func newFakeState() *fakeState {
	return &fakeState{
		desired:   map[string]domain.AppDesiredRevision{},
		revisions: map[string]domain.AppDesiredRevision{},
		active:    map[string]domain.AppActive{},
		ownership: map[string]domain.AppOwnership{},
		ops:       map[string]domain.AppOperation{},
	}
}

var _ out.AppState = (*fakeState)(nil)

func (f *fakeState) Recover(context.Context) error { return f.recoverErr }
func (f *fakeState) LoadCheckpoint(context.Context) (domain.AppStoreCheckpoint, error) {
	return f.checkpoint, nil
}
func (f *fakeState) ListApps(context.Context) ([]string, error) { return nil, nil }
func (f *fakeState) LoadDesired(_ context.Context, app string) (domain.AppDesiredRevision, bool, error) {
	rev, ok := f.desired[app]
	return rev, ok, nil
}
func (f *fakeState) LoadRevision(_ context.Context, app, rev string) (domain.AppDesiredRevision, error) {
	r, ok := f.revisions[app+rev]
	if !ok {
		return domain.AppDesiredRevision{}, domain.ErrAppRevisionNotFound
	}
	return r, nil
}
func (f *fakeState) ListRevisions(context.Context, string) ([]string, error) { return nil, nil }
func (f *fakeState) LoadActive(_ context.Context, app string) (domain.AppActive, bool, error) {
	active, ok := f.active[app]
	return active, ok, nil
}
func (f *fakeState) LoadIntent(_ context.Context, app string) (domain.AppStopIntent, error) {
	return domain.AppStopIntent{App: app}, nil
}
func (f *fakeState) SaveIntent(context.Context, domain.AppStopIntent) error { return nil }
func (f *fakeState) LoadOwnership(_ context.Context, app string) (domain.AppOwnership, error) {
	return f.ownership[app], nil
}
func (f *fakeState) SaveOwnership(context.Context, domain.AppOwnership) error { return nil }
func (f *fakeState) StageApply(context.Context, domain.AppApplyIntent) error  { return nil }
func (f *fakeState) CommitApply(context.Context, string, string) error        { return nil }
func (f *fakeState) MaterializeApply(context.Context, string, string) error   { return nil }
func (f *fakeState) LoadApplyIntent(context.Context, string, string) (domain.AppApplyIntent, error) {
	return domain.AppApplyIntent{}, domain.ErrAppIntentNotFound
}
func (f *fakeState) ListIntents(context.Context, string) ([]string, error)  { return nil, nil }
func (f *fakeState) CollectGarbage(context.Context, string, []string) error { return nil }
func (f *fakeState) SaveOperation(_ context.Context, op domain.AppOperation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops[op.Op] = op
	f.savedOps++
	return nil
}
func (f *fakeState) LoadOperation(_ context.Context, _, opID string) (domain.AppOperation, error) {
	op, ok := f.ops[opID]
	if !ok {
		return domain.AppOperation{}, domain.ErrAppOperationNotFound
	}
	return op, nil
}
func (f *fakeState) SaveActive(context.Context, domain.AppActive) error { return nil }

// fakeImages resolves digests from a table.
type fakeImages struct {
	digests map[string]string
	err     error
	calls   int
}

func (f *fakeImages) ResolveDigest(_ context.Context, ref string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	if digest, ok := f.digests[ref]; ok {
		return digest, nil
	}
	return "", errors.New("unknown image")
}

// fakeSecrets serves values from a table.
type fakeSecrets struct {
	values map[string]string
}

func (f *fakeSecrets) Name() string      { return "fake" }
func (f *fakeSecrets) IsAvailable() bool { return true }
func (f *fakeSecrets) GetSecret(_ context.Context, key string) (string, error) {
	value, ok := f.values[key]
	if !ok {
		return "", errors.New("no such secret")
	}
	return value, nil
}

// fakeRuntime records every call, proving preflight performs zero
// workload mutations: only InspectImageVolumes and VolumeExists fire.
type fakeRuntime struct {
	out.ContainerRuntime
	volumes map[string][]string
	exists  map[string]bool
	calls   []string
}

func (f *fakeRuntime) InspectImageVolumes(_ context.Context, imageRef string) ([]string, error) {
	f.calls = append(f.calls, "InspectImageVolumes:"+imageRef)
	return f.volumes[imageRef], nil
}
func (f *fakeRuntime) VolumeExists(_ context.Context, volumeName string) (bool, error) {
	f.calls = append(f.calls, "VolumeExists:"+volumeName)
	return f.exists[volumeName], nil
}

func testRevision(app string, services ...domain.AppService) domain.AppDesiredRevision {
	for i := range services {
		if services[i].Replicas == 0 {
			services[i].Replicas = 1
		}
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
		Name: "web", Image: "registry.example.com/blog/web:1.4.2",
		Readiness: domain.AppReadiness{Type: "http", Path: "/healthz"},
		HTTP:      []domain.AppHTTPInterface{{Host: "blog.example.com", Port: 8080, TLS: "auto"}},
		Secrets:   map[string]string{"DATABASE_URL": "database-url"},
	}
}

func newService(state *fakeState, runtime *fakeRuntime, images *fakeImages, secrets *fakeSecrets) *deployment.Service {
	return deployment.NewService(deployment.Deps{
		State:   state,
		Runtime: runtime,
		Images:  images,
		Secrets: secrets,
	}, zerowrap.Default())
}

func TestPreflight_PassesAndPins(t *testing.T) {
	ctx := context.Background()
	state := newFakeState()
	rev := testRevision("blog", webService())
	state.desired["blog"] = rev
	state.revisions["blogrev-1"] = rev
	runtime := &fakeRuntime{exists: map[string]bool{}}
	images := &fakeImages{digests: map[string]string{"registry.example.com/blog/web:1.4.2": "sha256:abc"}}
	secrets := &fakeSecrets{values: map[string]string{"gordon/apps/blog/web/database-url": "s3cr3t"}}
	svc := newService(state, runtime, images, secrets)

	pinned, op, err := svc.Preflight(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	require.NotNil(t, pinned)
	require.NotNil(t, op)
	assert.Equal(t, "rev-1", op.InputRevision)
	require.Len(t, op.Steps, 2)
	assert.Equal(t, "succeeded", op.Steps[0].State)
	assert.Contains(t, op.Steps[1].Detail, "sha256:abc")
	// Only inspection calls fired — zero workload mutations.
	assert.ElementsMatch(t, []string{"InspectImageVolumes:registry.example.com/blog/web:1.4.2"}, runtime.calls)
	assert.Equal(t, 1, images.calls)
}

func TestPreflight_ImageFailureActivatesNothing(t *testing.T) {
	ctx := context.Background()
	state := newFakeState()
	rev := testRevision("blog", webService())
	state.desired["blog"] = rev
	runtime := &fakeRuntime{}
	images := &fakeImages{err: errors.New("registry down")}
	secrets := &fakeSecrets{}
	svc := newService(state, runtime, images, secrets)

	_, op, err := svc.Preflight(ctx, deployment.DeployInput{App: "blog"})
	require.ErrorIs(t, err, domain.ErrAppImageUnresolvable)
	require.NotNil(t, op)
	assert.Equal(t, "failed", op.Outcome)
	assert.Empty(t, runtime.calls)
}

func TestPreflight_SecretMissingFailsClosed(t *testing.T) {
	ctx := context.Background()
	state := newFakeState()
	rev := testRevision("blog", webService())
	state.desired["blog"] = rev
	runtime := &fakeRuntime{}
	images := &fakeImages{digests: map[string]string{"registry.example.com/blog/web:1.4.2": "sha256:abc"}}
	secrets := &fakeSecrets{values: map[string]string{}}
	svc := newService(state, runtime, images, secrets)

	_, _, err := svc.Preflight(ctx, deployment.DeployInput{App: "blog"})
	require.ErrorIs(t, err, domain.ErrAppSecretMissing)
}

func TestPreflight_UnmanagedImageVolumeRejected(t *testing.T) {
	ctx := context.Background()
	state := newFakeState()
	svcSpec := webService()
	rev := testRevision("blog", svcSpec)
	state.desired["blog"] = rev
	runtime := &fakeRuntime{volumes: map[string][]string{"registry.example.com/blog/web:1.4.2": {"/data"}}}
	images := &fakeImages{digests: map[string]string{"registry.example.com/blog/web:1.4.2": "sha256:abc"}}
	secrets := &fakeSecrets{values: map[string]string{"gordon/apps/blog/web/database-url": "x"}}
	svc := newService(state, runtime, images, secrets)

	_, _, err := svc.Preflight(ctx, deployment.DeployInput{App: "blog"})
	require.ErrorIs(t, err, domain.ErrAppUnmanagedImageVolume)
}

func TestPreflight_TargetedRefusesDivergence(t *testing.T) {
	ctx := context.Background()
	state := newFakeState()
	rev := testRevision("blog", webService())
	rev.Revision = "rev-2"
	state.desired["blog"] = rev
	state.revisions["blogrev-2"] = rev
	state.active["blog"] = domain.AppActive{App: "blog", ConvergedRevision: "rev-1", Converged: true}
	runtime := &fakeRuntime{}
	images := &fakeImages{}
	secrets := &fakeSecrets{}
	svc := newService(state, runtime, images, secrets)

	_, _, err := svc.Preflight(ctx, deployment.DeployInput{App: "blog", Revision: "rev-2", Service: "web"})
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
}

func TestPreflight_TargetedConvergedPasses(t *testing.T) {
	ctx := context.Background()
	state := newFakeState()
	rev := testRevision("blog", webService())
	state.desired["blog"] = rev
	state.revisions["blogrev-1"] = rev
	state.active["blog"] = domain.AppActive{App: "blog", ConvergedRevision: "rev-1", Converged: true}
	runtime := &fakeRuntime{}
	images := &fakeImages{digests: map[string]string{"registry.example.com/blog/web:1.4.2": "sha256:abc"}}
	secrets := &fakeSecrets{values: map[string]string{"gordon/apps/blog/web/database-url": "x"}}
	svc := newService(state, runtime, images, secrets)

	_, op, err := svc.Preflight(ctx, deployment.DeployInput{App: "blog", Service: "web"})
	require.NoError(t, err)
	require.NotNil(t, op)
}

func TestPreflight_MutableTagReresolved(t *testing.T) {
	ctx := context.Background()
	state := newFakeState()
	svcSpec := webService()
	svcSpec.Image = "registry.example.com/blog/web:latest"
	rev := testRevision("blog", svcSpec)
	state.desired["blog"] = rev
	runtime := &fakeRuntime{}
	images := &fakeImages{digests: map[string]string{"registry.example.com/blog/web:latest": "sha256:first"}}
	secrets := &fakeSecrets{values: map[string]string{"gordon/apps/blog/web/database-url": "x"}}
	svc := newService(state, runtime, images, secrets)

	_, op1, err := svc.Preflight(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	assert.Contains(t, op1.Steps[1].Detail, "sha256:first")

	// Same tag, new digest on explicit deploy — restart would reuse pinned.
	images.digests["registry.example.com/blog/web:latest"] = "sha256:second"
	_, op2, err := svc.Preflight(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	assert.Contains(t, op2.Steps[1].Detail, "sha256:second")
	assert.NotEqual(t, op1.Op, op2.Op)
}

func TestComputeOutcome_TerminalResults(t *testing.T) {
	deployed := deployment.ServiceResult{Result: "deployed"}
	failed := deployment.ServiceResult{Result: "failed", Error: "boom"}
	assert.Equal(t, "success", deployment.ComputeOutcome(map[string]deployment.ServiceResult{"a": deployed}))
	assert.Equal(t, "failed", deployment.ComputeOutcome(map[string]deployment.ServiceResult{"a": failed}))
	assert.Equal(t, "partial", deployment.ComputeOutcome(map[string]deployment.ServiceResult{"a": deployed, "b": failed}))
	assert.Equal(t, "failed", deployment.ComputeOutcome(map[string]deployment.ServiceResult{}))
}
