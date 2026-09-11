package apps_test

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apps"
)

func testSpec(name string) domain.AppSpec {
	return domain.AppSpec{
		Name: name,
		Services: []domain.AppService{
			{
				Name: "web", Image: "img:1", StopGrace: 10 * time.Second,
				Readiness: domain.AppReadiness{Type: "none", Timeout: 30 * time.Second},
				HTTP:      []domain.AppHTTPInterface{{Host: name + ".example.com", Port: 8080, TLS: "auto"}},
			},
		},
	}
}

// applySuccess wires a full accept path on the store mock.
func applySuccess(store *outmocks.MockAppState, spec domain.AppSpec) {
	store.EXPECT().Recover(mock.Anything).Return(nil).Once()
	store.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil).Once()
	store.EXPECT().LoadDesired(mock.Anything, spec.Name).Return(domain.AppDesiredRevision{}, false, nil).Once()
	store.EXPECT().LoadActive(mock.Anything, spec.Name).Return(domain.AppActive{}, false, nil).Once()
	store.EXPECT().StageApply(mock.Anything, mock.Anything).Return(nil).Once()
	store.EXPECT().CommitApply(mock.Anything, spec.Name, mock.Anything).Return(nil).Once()
	store.EXPECT().MaterializeApply(mock.Anything, spec.Name, mock.Anything).Return(nil).Once()
	store.EXPECT().CollectGarbage(mock.Anything, spec.Name, mock.Anything).Return(nil).Once()
}

// recordingBarrier records shared-lease acquisition for apply tests.
type recordingBarrier struct {
	shared   int
	released int
	err      error
}

func (b *recordingBarrier) AcquireShared(context.Context) (out.GCLease, error) {
	if b.err != nil {
		return nil, b.err
	}
	b.shared++
	return &recordingLease{barrier: b}, nil
}

func (b *recordingBarrier) AcquireExclusive(context.Context) (out.GCLease, error) {
	return nil, assert.AnError
}

type recordingLease struct{ barrier *recordingBarrier }

func (l *recordingLease) Release() { l.barrier.released++ }

func TestApply_RejectsImageOutsideRegistryAllowlistBeforeStoreAccess(t *testing.T) {
	store := outmocks.NewMockAppState(t)
	spec := testSpec("blog")
	spec.Services[0].Image = "registry.example.com/team/app:1"
	svc := apps.NewService(store, zerowrap.Default()).WithImagePolicy(domain.ImageSourcePolicy{})

	_, _, err := svc.Apply(context.Background(), spec, []byte("manifest"), false)

	assert.ErrorIs(t, err, domain.ErrAppImageNotAllowed)
}

// TestApply_HoldsSharedGCLease proves apply holds the shared GC lease for
// its whole mutation, so prune cannot snapshot a half-published apply.
func TestApply_HoldsSharedGCLease(t *testing.T) {
	ctx := context.Background()
	store := outmocks.NewMockAppState(t)
	spec := testSpec("blog")
	applySuccess(store, spec)
	barrier := &recordingBarrier{}
	svc := apps.NewService(store, zerowrap.Default()).WithGCBarrier(barrier)

	_, _, err := svc.Apply(ctx, spec, []byte("manifest"), false)

	require.NoError(t, err)
	assert.Equal(t, 1, barrier.shared, "apply must take the shared GC lease")
	assert.Equal(t, 1, barrier.released, "apply must release the shared GC lease")
}

// TestApply_FailsClosedWhenGCBarrierRefuses proves an apply never publishes
// desired state while it cannot hold the shared lease (e.g. prune holds the
// exclusive lease).
func TestApply_FailsClosedWhenGCBarrierRefuses(t *testing.T) {
	ctx := context.Background()
	barrier := &recordingBarrier{err: assert.AnError}
	svc := apps.NewService(outmocks.NewMockAppState(t), zerowrap.Default()).WithGCBarrier(barrier)

	_, _, err := svc.Apply(ctx, testSpec("blog"), []byte("manifest"), false)

	require.Error(t, err)
	assert.Equal(t, 0, barrier.shared)
}

func TestApply_AcceptsAndPersists(t *testing.T) {
	ctx := context.Background()
	store := outmocks.NewMockAppState(t)
	svc := apps.NewService(store, zerowrap.Default())
	spec := testSpec("blog")

	applySuccess(store, spec)
	result, dry, err := svc.Apply(ctx, spec, []byte("manifest"), false)
	require.NoError(t, err)
	assert.Nil(t, dry)
	require.NotNil(t, result)
	assert.True(t, result.Pending)
	assert.False(t, result.Noop)
	assert.NotEmpty(t, result.ResultingRevision)
	assert.NotEmpty(t, result.IntentID)
}

func TestApply_NoopIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := outmocks.NewMockAppState(t)
	svc := apps.NewService(store, zerowrap.Default())
	spec := testSpec("blog")

	// First apply persists.
	applySuccess(store, spec)
	first, _, err := svc.Apply(ctx, spec, []byte("manifest"), false)
	require.NoError(t, err)

	// Second apply with identical content is a no-op: no writes.
	desired := domain.AppDesiredRevision{Revision: first.ResultingRevision, App: "blog", Spec: spec}
	store.EXPECT().Recover(mock.Anything).Return(nil).Once()
	store.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil).Once()
	store.EXPECT().LoadDesired(mock.Anything, "blog").Return(desired, true, nil).Once()
	store.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{}, false, nil).Once()
	second, _, err := svc.Apply(ctx, spec, []byte("manifest"), false)
	require.NoError(t, err)
	assert.True(t, second.Noop)
	assert.Equal(t, first.ResultingRevision, second.ResultingRevision)
}

func TestApply_DryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	store := outmocks.NewMockAppState(t)
	svc := apps.NewService(store, zerowrap.Default())
	spec := testSpec("blog")

	store.EXPECT().Recover(mock.Anything).Return(nil).Once()
	store.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil).Once()
	store.EXPECT().LoadDesired(mock.Anything, "blog").Return(domain.AppDesiredRevision{}, false, nil).Once()
	store.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{}, false, nil).Once()
	result, dry, err := svc.Apply(ctx, spec, []byte("manifest"), true)
	require.NoError(t, err)
	assert.Nil(t, result)
	require.NotNil(t, dry)
	assert.True(t, dry.Valid)
	store.AssertNotCalled(t, "StageApply", mock.Anything, mock.Anything)
}

func TestApply_RejectsCrossAppConflict(t *testing.T) {
	ctx := context.Background()
	store := outmocks.NewMockAppState(t)
	svc := apps.NewService(store, zerowrap.Default())

	applySuccess(store, testSpec("blog"))
	_, _, err := svc.Apply(ctx, testSpec("blog"), []byte("m"), false)
	require.NoError(t, err)

	// Same host, different app → conflict before any write.
	other := testSpec("shop")
	other.Services[0].HTTP[0].Host = "blog.example.com"
	store.EXPECT().Recover(mock.Anything).Return(nil).Once()
	store.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{
		Reservations: []domain.AppListenerReservation{{Proto: "http", Host: "blog.example.com", Service: "web", App: "blog"}},
	}, nil).Once()
	store.EXPECT().LoadDesired(mock.Anything, "shop").Return(domain.AppDesiredRevision{}, false, nil).Once()
	store.EXPECT().LoadActive(mock.Anything, "shop").Return(domain.AppActive{}, false, nil).Once()
	_, _, err = svc.Apply(ctx, other, []byte("m"), false)
	require.ErrorIs(t, err, domain.ErrAppReservationConflict)
	store.AssertNotCalled(t, "StageApply", mock.Anything, mock.MatchedBy(func(intent domain.AppApplyIntent) bool {
		return intent.App == "shop"
	}))
}

func TestApply_RejectsDuplicateClaimInCandidate(t *testing.T) {
	ctx := context.Background()
	store := outmocks.NewMockAppState(t)
	svc := apps.NewService(store, zerowrap.Default())

	spec := testSpec("blog")
	dup := spec.Services[0]
	dup.Name = "web2"
	spec.Services = append(spec.Services, dup)

	store.EXPECT().Recover(mock.Anything).Return(nil).Once()
	store.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil).Once()
	store.EXPECT().LoadDesired(mock.Anything, "blog").Return(domain.AppDesiredRevision{}, false, nil).Once()
	store.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{}, false, nil).Once()
	_, _, err := svc.Apply(ctx, spec, []byte("m"), false)
	require.ErrorIs(t, err, domain.ErrAppReservationConflict)
}

func TestApply_RejectsOverlappingWildcardClaimInCandidate(t *testing.T) {
	ctx := context.Background()
	store := outmocks.NewMockAppState(t)
	svc := apps.NewService(store, zerowrap.Default())

	spec := domain.AppSpec{
		Name: "tcp-overlap",
		Services: []domain.AppService{
			{
				Name: "one", Image: "img:1", StopGrace: 10 * time.Second,
				Readiness: domain.AppReadiness{Type: "none", Timeout: 30 * time.Second},
				TCP:       []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 9000, Publish: "0.0.0.0:19090"}},
			},
			{
				Name: "two", Image: "img:1", StopGrace: 10 * time.Second,
				Readiness: domain.AppReadiness{Type: "none", Timeout: 30 * time.Second},
				TCP:       []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 9000, Publish: "127.0.0.1:19090"}},
			},
		},
	}

	store.EXPECT().Recover(mock.Anything).Return(nil).Once()
	store.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil).Once()
	store.EXPECT().LoadDesired(mock.Anything, spec.Name).Return(domain.AppDesiredRevision{}, false, nil).Once()
	store.EXPECT().LoadActive(mock.Anything, spec.Name).Return(domain.AppActive{}, false, nil).Once()
	_, _, err := svc.Apply(ctx, spec, []byte("m"), false)
	require.ErrorIs(t, err, domain.ErrAppReservationConflict)
	store.AssertNotCalled(t, "StageApply", mock.Anything, mock.Anything)
}

func TestApply_InvalidSpecFailsBeforeStore(t *testing.T) {
	ctx := context.Background()
	store := outmocks.NewMockAppState(t)
	svc := apps.NewService(store, zerowrap.Default())

	spec := testSpec("Bad!")
	_, _, err := svc.Apply(ctx, spec, []byte("m"), false)
	require.ErrorIs(t, err, domain.ErrInvalidAppSpec)
	store.AssertNotCalled(t, "Recover", mock.Anything)
}

// TestApply_RejectsPublishMismatchBeforeStore proves an L4 publish that does
// not match the entrypoint listener is refused before any persistence or
// workload effect, so the runtime can never widen a narrower declaration.
func TestApply_RejectsPublishMismatchBeforeStore(t *testing.T) {
	ctx := context.Background()
	store := outmocks.NewMockAppState(t)
	svc := apps.NewService(store, zerowrap.Default()).WithEntrypoints(map[string]domain.EntryPointListener{
		"tcp": {Address: "0.0.0.0:25432", Protocol: domain.EntryPointProtocolTCP},
	})

	spec := testSpec("game")
	spec.Services[0].HTTP = nil
	spec.Services[0].TCP = []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 25432, Publish: "127.0.0.1:25432"}}

	_, _, err := svc.Apply(ctx, spec, []byte("m"), false)
	require.ErrorIs(t, err, domain.ErrInvalidAppSpec)
	store.AssertNotCalled(t, "Recover", mock.Anything)
}

// TestApply_RejectsUnknownEntrypointBeforeStore proves an L4 interface that
// references a missing entrypoint fails closed at apply time.
func TestApply_RejectsUnknownEntrypointBeforeStore(t *testing.T) {
	ctx := context.Background()
	store := outmocks.NewMockAppState(t)
	svc := apps.NewService(store, zerowrap.Default()).WithEntrypoints(map[string]domain.EntryPointListener{})

	spec := testSpec("game")
	spec.Services[0].HTTP = nil
	spec.Services[0].TCP = []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 25432, Publish: "0.0.0.0:25432"}}

	_, _, err := svc.Apply(ctx, spec, []byte("m"), false)
	require.ErrorIs(t, err, domain.ErrInvalidAppSpec)
	store.AssertNotCalled(t, "Recover", mock.Anything)
}

func TestApply_SamePortDifferentProtoCoexists(t *testing.T) {
	ctx := context.Background()
	store := outmocks.NewMockAppState(t)
	svc := apps.NewService(store, zerowrap.Default())

	tcpApp := domain.AppSpec{
		Name: "tcpapp",
		Services: []domain.AppService{
			{
				Name: "s", Image: "img:1", StopGrace: 10 * time.Second,
				Readiness: domain.AppReadiness{Type: "none", Timeout: 30 * time.Second},
				TCP:       []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 9000, Publish: "0.0.0.0:9000"}},
			},
		},
	}
	applySuccess(store, tcpApp)
	_, _, err := svc.Apply(ctx, tcpApp, []byte("m"), false)
	require.NoError(t, err)

	udpApp := domain.AppSpec{
		Name: "udpapp",
		Services: []domain.AppService{
			{
				Name: "s", Image: "img:1", StopGrace: 10 * time.Second,
				Readiness: domain.AppReadiness{Type: "none", Timeout: 30 * time.Second},
				UDP:       []domain.AppUDPInterface{{Entrypoint: "udp", Port: 9000, Publish: "0.0.0.0:9000"}},
			},
		},
	}
	applySuccess(store, udpApp)
	_, _, err = svc.Apply(ctx, udpApp, []byte("m"), false)
	require.NoError(t, err)
}

func TestReservationsFor_DelegatesToDomain(t *testing.T) {
	spec := domain.AppSpec{
		Name: "blog",
		Services: []domain.AppService{
			{
				Name: "web", Image: "img:1",
				HTTP: []domain.AppHTTPInterface{{Host: "b.example.com", Port: 8080, TLS: "auto"}},
				TCP:  []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 9000, Publish: "9000"}},
			},
		},
	}
	// Apply derives reservations through the domain owner; derivation
	// policy itself is covered in the domain package.
	reservations := domain.ReservationsFor(spec)
	require.Len(t, reservations, 2)
	assert.Equal(t, "http", reservations[0].Proto)
	assert.Equal(t, "tcp", reservations[1].Proto)
	assert.Equal(t, "dual", reservations[1].IP)
}
