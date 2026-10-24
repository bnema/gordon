package deployment_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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

const promoRegistry = "registry.example.com"

func setImageDigest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func promoImage(repo, c string) string { return promoRegistry + "/" + repo + "@" + setImageDigest(c) }

var ciPrincipal = domain.WorkloadPrincipal{Issuer: "https://ci.example.com", Subject: "repo:acme/blog"}

// setImageHarness is a deployment engine over a real store, with a permissive
// runtime that replaces containers and a manifest store holding the digests
// listed in manifests.
type setImageHarness struct {
	t         *testing.T
	ctx       context.Context
	store     *appstate.Store
	state     out.AppState
	runtime   *outmocks.MockContainerRuntime
	manifests *outmocks.MockManifestStorage
	barrier   *recordingBarrier
	svc       *deployment.Service
	known     map[string]bool
	seq       atomic.Int32
	// failCreate, when set, fails CreateContainer for the named service.
	failCreate string
}

func newPromoHarness(t *testing.T) *setImageHarness {
	t.Helper()
	h := &setImageHarness{
		t: t, ctx: context.Background(), store: newTestStore(t),
		runtime:   outmocks.NewMockContainerRuntime(t),
		manifests: outmocks.NewMockManifestStorage(t),
		barrier:   &recordingBarrier{},
		known:     map[string]bool{},
	}
	h.state = h.store
	h.manifests.EXPECT().GetManifest(mock.Anything, mock.Anything).RunAndReturn(func(repo, ref string) ([]byte, string, error) {
		h.barrier.requireHeld(t, "manifest check")
		if h.known[repo+"@"+ref] {
			return []byte("{}"), "application/vnd.oci.image.manifest.v1+json", nil
		}
		return nil, "", fmt.Errorf("%w: %s@%s", domain.ErrManifestNotFound, repo, ref)
	}).Maybe()
	h.expectRuntime()
	h.seed()
	h.build()
	return h
}

func (h *setImageHarness) build() {
	images := outmocks.NewMockImageResolver(h.t)
	images.EXPECT().ResolveDigest(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, ref string) (string, error) {
		_, digest, _ := strings.Cut(ref, "@")
		return digest, nil
	}).Maybe()
	h.svc = deployment.NewService(deployment.Deps{
		State: h.state, Runtime: h.runtime, Images: images, Secrets: outmocks.NewMockSecretProvider(h.t),
		Registry:  deployment.RegistryConfig{Domain: promoRegistry, PullAddress: "127.0.0.1:5000"},
		Manifests: h.manifests,
	}, zerowrap.Default()).WithGCBarrier(h.barrier).WithProbeDeps(deployment.NewTestProbeDeps(h.runtime,
		func(context.Context, string, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))
	policies, err := domain.NewSetImagePolicySet([]domain.SetImagePolicy{{
		Issuer: ciPrincipal.Issuer, Subjects: []string{ciPrincipal.Subject}, App: "blog", Service: "web", Repository: "web",
	}})
	require.NoError(h.t, err)
	h.svc.SetSetImageConfig(deployment.SetImageConfig{Policies: policies, RegistryHosts: []string{"ci.example.com"}})
}

func (h *setImageHarness) expectRuntime() {
	r := h.runtime
	r.EXPECT().PullImageWithOptions(mock.Anything, mock.Anything).Return(nil).Maybe()
	r.EXPECT().VerifyImageDigest(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	r.EXPECT().InspectImageVolumes(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	r.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{}, nil).Maybe()
	r.EXPECT().CreateNetwork(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	r.EXPECT().CreateContainer(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, cfg *domain.ContainerConfig) (*domain.Container, error) {
		if h.failCreate != "" && cfg.Labels[domain.LabelAppService] == h.failCreate {
			return nil, fmt.Errorf("create %s refused", h.failCreate)
		}
		return &domain.Container{ID: fmt.Sprintf("c-new-%d", h.seq.Add(1)), Name: "web"}, nil
	}).Maybe()
	r.EXPECT().ConnectContainerToNetwork(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	r.EXPECT().StartContainer(mock.Anything, mock.Anything).Return(nil).Maybe()
	r.EXPECT().InspectContainer(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, id string) (*domain.Container, error) {
		return &domain.Container{ID: id, Status: "running", StartedAt: executionStartedAt}, nil
	}).Maybe()
	r.EXPECT().GetContainerBackendBinds(mock.Anything, mock.Anything, mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18081, Protocol: domain.NetworkProtocolTCP}}, nil).Maybe()
	r.EXPECT().StopContainer(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	r.EXPECT().RemoveContainer(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
}

// promoServices is a converged web + worker app on installation digests.
func promoServices() (domain.AppService, domain.AppService) {
	web, worker := twoServiceSpec()
	web.Image = promoImage("web", "a")
	worker.Image = promoImage("worker", "b")
	return web, worker
}

func (h *setImageHarness) seed() {
	web, worker := promoServices()
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	base := testRevision("blog", web, worker)
	base.Revision = "rev-0"
	require.NoError(h.t, h.store.SaveIntent(h.ctx, domain.AppStopIntent{App: "blog"}))
	require.NoError(h.t, h.store.SaveActive(h.ctx, domain.AppActive{
		App: "blog", Converged: true, ConvergedRevision: "rev-0",
		Services: map[string]domain.AppEffectiveService{
			"web": {Container: "c-web", EffectiveRevision: "rev-0", Image: web.Image, ActivatedBy: "op-created",
				ActivatedAt: at, Digest: setImageDigest("a"), Spec: base.Spec.Services[0], BackendBinds: map[int]int{8080: 18080}},
			"worker": {Container: "c-worker", EffectiveRevision: "rev-0", Image: worker.Image, ActivatedBy: "op-created",
				ActivatedAt: at, Digest: setImageDigest("b"), Spec: base.Spec.Services[1]},
		},
	}))
	require.NoError(h.t, h.store.SaveOwnership(h.ctx, domain.AppOwnership{App: "blog", ID: "app-blog"}))
	seedRevision(h.t, h.ctx, h.store, "intent-0", "", base)
}

func (h *setImageHarness) input(key, image string) deployment.SetImageInput {
	return deployment.SetImageInput{
		App: "blog", Service: "web", Image: image, Op: key, Principal: ciPrincipal,
		Validate: func(domain.AppSpec) error { return nil },
	}
}

func (h *setImageHarness) setImage(key, image string) (*deployment.StartDeployResult, error) {
	h.known["web@"+imageDigest(image)] = true
	return h.svc.StartSetImage(h.ctx, h.input(key, image))
}

func imageDigest(image string) string {
	_, digest, _ := strings.Cut(image, "@")
	return digest
}

func (h *setImageHarness) desired() domain.AppDesiredRevision {
	h.t.Helper()
	desired, ok, err := h.store.LoadDesired(h.ctx, "blog")
	require.NoError(h.t, err)
	require.True(h.t, ok)
	return desired
}

func (h *setImageHarness) active() domain.AppActive {
	h.t.Helper()
	active, _, err := h.store.LoadActive(h.ctx, "blog")
	require.NoError(h.t, err)
	return active
}

// recordingBarrier is a GC barrier that records whether the shared lease
// is held.
type recordingBarrier struct{ held atomic.Int32 }

type recordingLease struct {
	b    *recordingBarrier
	once sync.Once
}

func (l *recordingLease) Release() { l.once.Do(func() { l.b.held.Add(-1) }) }

func (b *recordingBarrier) AcquireShared(context.Context) (out.GCLease, error) {
	b.held.Add(1)
	return &recordingLease{b: b}, nil
}

func (b *recordingBarrier) AcquireExclusive(context.Context) (out.GCLease, error) {
	return nil, fmt.Errorf("exclusive lease is not used in these tests")
}

func (b *recordingBarrier) requireHeld(t *testing.T, what string) {
	t.Helper()
	require.Positive(t, b.held.Load(), "%s must run under the shared GC lease", what)
}

func TestStartSetImage_TwoServiceAppConvergesAndKeepsOtherProvenance(t *testing.T) {
	h := newPromoHarness(t)
	started, err := h.setImage("key-1", "web@"+setImageDigest("c"))
	require.NoError(t, err)
	require.True(t, started.Owned)
	assert.Equal(t, domain.AppOperationKindSetImage, started.Claim.Journal.Kind)
	assert.Equal(t, ciPrincipal.String(), started.Claim.Journal.Principal)
	assert.Equal(t, promoImage("web", "c"), started.Claim.Journal.Request.Image, "the image is canonicalized")
	assert.Equal(t, setImageDigest("c"), started.Claim.Journal.Steps[0].Digest, "step 0 protects the digest from prune")

	desired := h.desired()
	assert.Equal(t, "rev-0", desired.Supersedes)
	require.NotNil(t, desired.SetImage)
	assert.Equal(t, domain.AppSetImageProvenance{
		Service: "web", Image: promoImage("web", "c"), Digest: setImageDigest("c"),
		Principal: ciPrincipal.String(), Operation: "key-1", AppID: "app-blog",
	}, *desired.SetImage)
	assert.NotEmpty(t, desired.SourceSHA256)
	web, worker := promoServices()
	web.Image = promoImage("web", "c")
	assert.Equal(t, web.Image, desired.Spec.Services[0].Image)
	assert.Equal(t, worker.Image, desired.Spec.Services[1].Image)

	_, err = h.svc.ExecuteDeploy(h.ctx, started.Claim)
	require.NoError(t, err)
	op, err := h.store.LoadOperation(h.ctx, "blog", "key-1")
	require.NoError(t, err)
	assert.Equal(t, domain.AppOutcomeSuccess, op.Outcome)
	active := h.active()
	assert.True(t, active.Converged, "setting one service's image of a converged app keeps it converged")
	assert.Equal(t, desired.Revision, active.ConvergedRevision)
	assert.Equal(t, setImageDigest("c"), active.Services["web"].Digest)
	assert.Equal(t, desired.Revision, active.Services["web"].EffectiveRevision)
	assert.Equal(t, "rev-0", active.Services["worker"].EffectiveRevision, "the other service keeps its provenance")
	assert.Equal(t, "op-created", active.Services["worker"].ActivatedBy)

	// A second image change supersedes the first one.
	second, err := h.setImage("key-2", "ci.example.com/web@"+setImageDigest("d"))
	require.NoError(t, err)
	require.True(t, second.Owned)
	assert.Equal(t, desired.Revision, h.desired().Supersedes)
	_, err = h.svc.ExecuteDeploy(h.ctx, second.Claim)
	require.NoError(t, err)
	active = h.active()
	assert.True(t, active.Converged)
	assert.Equal(t, setImageDigest("d"), active.Services["web"].Digest)
	assert.Equal(t, "rev-0", active.Services["worker"].EffectiveRevision)
}

func TestStartSetImage_ReplayAfterLaterMutationReportsOriginal(t *testing.T) {
	h := newPromoHarness(t)
	started, err := h.setImage("key-1", "web@"+setImageDigest("c"))
	require.NoError(t, err)
	_, err = h.svc.ExecuteDeploy(h.ctx, started.Claim)
	require.NoError(t, err)
	changed := h.desired().Revision
	_, err = h.setImage("key-2", "web@"+setImageDigest("d"))
	require.NoError(t, err)
	before := h.desired().Revision

	replay, err := h.setImage("key-1", "registry.example.com/web@"+setImageDigest("c"))
	require.NoError(t, err, "a terminal success replays without error")
	assert.False(t, replay.Owned)
	assert.Equal(t, domain.AppOutcomeSuccess, replay.Claim.Journal.Outcome)
	assert.Equal(t, changed, replay.Claim.Journal.InputRevision)
	assert.Equal(t, before, h.desired().Revision, "a replay writes nothing")
}

func TestStartSetImage_KeyReuseForAnotherRequestConflicts(t *testing.T) {
	h := newPromoHarness(t)
	_, err := h.setImage("key-1", "web@"+setImageDigest("c"))
	require.NoError(t, err)
	revision := h.desired().Revision

	_, err = h.setImage("key-1", "web@"+setImageDigest("d"))
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	other := h.input("key-1", "web@"+setImageDigest("c"))
	other.Principal = domain.WorkloadPrincipal{Issuer: domain.GordonIssuer, Subject: "someone"}
	other.LocalOwner = true
	_, err = h.svc.StartSetImage(h.ctx, other)
	require.ErrorIs(t, err, domain.ErrAppStateConflict, "another principal never replays this key")
	assert.Equal(t, revision, h.desired().Revision)
}

func TestStartSetImage_RefusesBeforeAnyWrite(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*setImageHarness) deployment.SetImageInput
		want    error
	}{
		{name: "tag image", want: domain.ErrInvalidImageReference, prepare: func(h *setImageHarness) deployment.SetImageInput {
			return h.input("k", "registry.example.com/web:latest")
		}},
		{name: "malformed digest", want: domain.ErrInvalidImageReference, prepare: func(h *setImageHarness) deployment.SetImageInput {
			return h.input("k", "web@sha256:abc")
		}},
		{name: "cross host", want: domain.ErrInvalidImageReference, prepare: func(h *setImageHarness) deployment.SetImageInput {
			return h.input("k", "ghcr.io/web@"+setImageDigest("c"))
		}},
		{name: "missing manifest", want: domain.ErrManifestNotFound, prepare: func(h *setImageHarness) deployment.SetImageInput {
			return h.input("k", "web@"+setImageDigest("e"))
		}},
		{name: "policy denied", want: domain.ErrSetImageDenied, prepare: func(h *setImageHarness) deployment.SetImageInput {
			in := h.input("k", "web@"+setImageDigest("c"))
			in.Principal.Subject = "repo:acme/other"
			h.known["web@"+setImageDigest("c")] = true
			return in
		}},
		{name: "repository outside policy", want: domain.ErrSetImageDenied, prepare: func(h *setImageHarness) deployment.SetImageInput {
			h.known["worker@"+setImageDigest("c")] = true
			return h.input("k", "worker@"+setImageDigest("c"))
		}},
		{name: "unknown service", want: domain.ErrAppServiceNotFound, prepare: func(h *setImageHarness) deployment.SetImageInput {
			in := h.input("k", "web@"+setImageDigest("c"))
			in.Service, in.LocalOwner = "api", true
			h.known["web@"+setImageDigest("c")] = true
			return in
		}},
		{name: "stale expected revision", want: domain.ErrAppStateConflict, prepare: func(h *setImageHarness) deployment.SetImageInput {
			in := h.input("k", "web@"+setImageDigest("c"))
			in.ExpectedRevision = "rev-old"
			h.known["web@"+setImageDigest("c")] = true
			return in
		}},
		{name: "stopped app", want: domain.ErrAppStateConflict, prepare: func(h *setImageHarness) deployment.SetImageInput {
			require.NoError(t, h.store.SaveIntent(h.ctx, domain.AppStopIntent{App: "blog", Stopped: true}))
			h.known["web@"+setImageDigest("c")] = true
			return h.input("k", "web@"+setImageDigest("c"))
		}},
		{name: "divergent base", want: domain.ErrAppStateConflict, prepare: func(h *setImageHarness) deployment.SetImageInput {
			web, worker := promoServices()
			worker.Image = promoImage("worker", "f")
			next := testRevision("blog", web, worker)
			next.Revision = "rev-1"
			seedRevision(t, h.ctx, h.store, "intent-1", "rev-0", next)
			h.known["web@"+setImageDigest("c")] = true
			return h.input("k", "web@"+setImageDigest("c"))
		}},
		{name: "candidate fails validation", want: domain.ErrBindPolicy, prepare: func(h *setImageHarness) deployment.SetImageInput {
			in := h.input("k", "web@"+setImageDigest("c"))
			in.Validate = func(domain.AppSpec) error { return domain.ErrBindPolicy }
			h.known["web@"+setImageDigest("c")] = true
			return in
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newPromoHarness(t)
			input := tc.prepare(h)
			before := h.desired().Revision
			_, err := h.svc.StartSetImage(h.ctx, input)
			require.ErrorIs(t, err, tc.want)
			assert.Equal(t, before, h.desired().Revision)
			_, err = h.store.LoadOperation(h.ctx, "blog", "k")
			require.ErrorIs(t, err, domain.ErrAppOperationNotFound, "a refused image change journals nothing")
		})
	}
}

func TestStartSetImage_LocalOwnerBypassesPolicy(t *testing.T) {
	h := newPromoHarness(t)
	h.known["worker@"+setImageDigest("c")] = true
	in := h.input("k", "worker@"+setImageDigest("c"))
	in.Principal = domain.WorkloadPrincipal{Issuer: domain.GordonIssuer, Subject: "local-owner"}
	in.LocalOwner = true
	started, err := h.svc.StartSetImage(h.ctx, in)
	require.NoError(t, err)
	assert.True(t, started.Owned)
	assert.Equal(t, "gordon:local-owner", started.Claim.Journal.Principal)
}

// casStore runs a concurrent mutation just before AcceptSetImage commits.
type casStore struct {
	out.AppState
	before func()
}

func (s *casStore) AcceptSetImage(ctx context.Context, rev domain.AppDesiredRevision, op domain.AppOperation) (*domain.AppOperation, bool, error) {
	s.before()
	return s.AppState.AcceptSetImage(ctx, rev, op)
}

func TestStartSetImage_CASLoserWritesNothing(t *testing.T) {
	tests := []struct {
		name  string
		race  func(*setImageHarness)
		check func(*testing.T, *setImageHarness)
	}{
		{name: "concurrent apply", race: func(h *setImageHarness) {
			web, worker := promoServices()
			next := testRevision("blog", web, worker)
			next.Revision = "rev-applied"
			next.Spec.Env = map[string]string{"MODE": "prod"}
			seedRevision(h.t, h.ctx, h.store, "intent-race", "rev-0", next)
		}, check: func(t *testing.T, h *setImageHarness) {
			assert.Equal(t, "rev-applied", h.desired().Revision)
		}},
		{name: "incarnation changed", race: func(h *setImageHarness) {
			require.NoError(h.t, h.store.SaveOwnership(h.ctx, domain.AppOwnership{App: "blog", ID: "app-blog-2"}))
		}, check: func(t *testing.T, h *setImageHarness) {
			assert.Equal(t, "rev-0", h.desired().Revision)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newPromoHarness(t)
			h.state = &casStore{AppState: h.store, before: func() { tc.race(h) }}
			h.build()
			_, err := h.setImage("k", "web@"+setImageDigest("c"))
			require.ErrorIs(t, err, domain.ErrAppStateConflict)
			tc.check(t, h)
			_, err = h.store.LoadOperation(h.ctx, "blog", "k")
			require.ErrorIs(t, err, domain.ErrAppOperationNotFound)
		})
	}
}

// TestStartSetImage_ConcurrentSetImageRequestsSerialize proves two concurrent
// image changes of the same base serialize on the app lock: exactly one is
// accepted and the other is refused as busy while the winner is unfinished.
// It uses real bbolt (file I/O), so it runs outside synctest and joins the
// goroutines with a WaitGroup.
func TestStartSetImage_ConcurrentSetImageRequestsSerialize(t *testing.T) {
	h := newPromoHarness(t)
	h.known["web@"+setImageDigest("c")] = true
	h.known["web@"+setImageDigest("d")] = true
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, c := range []string{"c", "d"} {
		wg.Add(1)
		go func(i int, c string) {
			defer wg.Done()
			_, errs[i] = h.svc.StartSetImage(h.ctx, h.input("key-"+c, "web@"+setImageDigest(c)))
		}(i, c)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else {
			require.ErrorIs(t, err, domain.ErrAppBusy, "the loser sees the unfinished winner")
			require.ErrorIs(t, err, domain.ErrAppStateConflict, "busy is still a conflict")
		}
	}
	assert.Equal(t, 1, ok)
}

func TestStartSetImage_CrashAfterClaimReconcilesFailedAndReplays(t *testing.T) {
	h := newPromoHarness(t)
	started, err := h.setImage("key-1", "web@"+setImageDigest("c"))
	require.NoError(t, err)
	require.True(t, started.Owned)

	// A fresh engine over the same store is a restarted daemon: the claim
	// is no longer live and is never executed.
	h.build()
	replay, err := h.setImage("key-1", "web@"+setImageDigest("c"))
	require.NoError(t, err)
	require.False(t, replay.Owned, "the interrupted claim is never executed again")
	require.ErrorIs(t, replay.ReplayError(), domain.ErrAppStateConflict)
	assert.Equal(t, domain.AppOutcomeFailed, replay.Claim.Journal.Outcome, "the replay reports the reconciled failure")
	op, err := h.store.LoadOperation(h.ctx, "blog", "key-1")
	require.NoError(t, err)
	assert.Equal(t, domain.AppOutcomeFailed, op.Outcome, "the interrupted image change is finalized as failed")
	assert.Zero(t, h.seq.Load(), "no container was created")
}

func TestStartSetImage_NoopRules(t *testing.T) {
	t.Run("desired and active already run the digest", func(t *testing.T) {
		h := newPromoHarness(t)
		started, err := h.setImage("k", "web@"+setImageDigest("a"))
		require.NoError(t, err)
		assert.False(t, started.Owned)
		assert.Equal(t, domain.AppOutcomeSuccess, started.Claim.Journal.Outcome)
		assert.NoError(t, started.ReplayError())
		assert.Equal(t, "rev-0", h.desired().Revision, "no desired write")
		op, err := h.store.LoadOperation(h.ctx, "blog", "k")
		require.NoError(t, err)
		assert.Equal(t, domain.AppOperationKindSetImage, op.Kind)
	})
	t.Run("desired has the digest but active does not", func(t *testing.T) {
		h := newPromoHarness(t)
		web, worker := promoServices()
		web.Image = promoImage("web", "c")
		next := testRevision("blog", web, worker)
		next.Revision = "rev-1"
		seedRevision(t, h.ctx, h.store, "intent-1", "rev-0", next)
		started, err := h.setImage("k", "web@"+setImageDigest("c"))
		require.NoError(t, err)
		require.True(t, started.Owned)
		assert.Equal(t, "rev-1", started.Claim.Revision, "the current desired revision is deployed")
		assert.Equal(t, "rev-1", h.desired().Revision, "no new revision")
		_, err = h.svc.ExecuteDeploy(h.ctx, started.Claim)
		require.NoError(t, err)
		assert.Equal(t, setImageDigest("c"), h.active().Services["web"].Digest)
		assert.True(t, h.active().Converged)
	})
}

func TestStartSetImage_ManifestCheckAndAcceptanceHoldGCLease(t *testing.T) {
	h := newPromoHarness(t)
	h.state = &casStore{AppState: h.store, before: func() { h.barrier.requireHeld(t, "acceptance") }}
	h.build()
	_, err := h.setImage("k", "web@"+setImageDigest("c"))
	require.NoError(t, err)
	assert.Zero(t, h.barrier.held.Load(), "the lease is released when the claim phase ends")
}
