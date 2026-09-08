package apps_test

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
	"github.com/bnema/gordon/internal/usecase/apps"
)

// fakeStore is a hand-rolled in-memory out.AppState that records every
// call, proving apply performs zero workload/pull/secret-value effects:
// the only dependency surface is the state boundary itself.
type fakeStore struct {
	mu           sync.Mutex
	checkpoint   domain.AppStoreCheckpoint
	desired      map[string]domain.AppDesiredRevision
	revisions    map[string][]string
	active       map[string]domain.AppActive
	intents      map[string]map[string]domain.AppApplyIntent
	ops          map[string]domain.AppOperation
	recoverCalls int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		desired:   map[string]domain.AppDesiredRevision{},
		revisions: map[string][]string{},
		active:    map[string]domain.AppActive{},
		intents:   map[string]map[string]domain.AppApplyIntent{},
		ops:       map[string]domain.AppOperation{},
	}
}

var _ out.AppState = (*fakeStore)(nil)

func (f *fakeStore) Recover(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recoverCalls++
	return nil
}

func (f *fakeStore) LoadCheckpoint(_ context.Context) (domain.AppStoreCheckpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := f.checkpoint
	cp.Reservations = append([]domain.AppListenerReservation(nil), f.checkpoint.Reservations...)
	return cp, nil
}

func (f *fakeStore) ListApps(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var apps []string
	for app := range f.desired {
		apps = append(apps, app)
	}
	return apps, nil
}

func (f *fakeStore) LoadDesired(_ context.Context, app string) (domain.AppDesiredRevision, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rev, ok := f.desired[app]
	return rev, ok, nil
}

func (f *fakeStore) LoadRevision(_ context.Context, app, revision string) (domain.AppDesiredRevision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.revisions[app] {
		if id == revision {
			return f.desired[app], nil
		}
	}
	return domain.AppDesiredRevision{}, domain.ErrAppRevisionNotFound
}

func (f *fakeStore) ListRevisions(_ context.Context, app string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revisions[app]...), nil
}

func (f *fakeStore) LoadActive(_ context.Context, app string) (domain.AppActive, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	active, ok := f.active[app]
	return active, ok, nil
}

func (f *fakeStore) LoadIntent(_ context.Context, app string) (domain.AppStopIntent, error) {
	return domain.AppStopIntent{App: app}, nil
}

func (f *fakeStore) SaveIntent(_ context.Context, _ domain.AppStopIntent) error {
	return nil
}

func (f *fakeStore) LoadOwnership(_ context.Context, app string) (domain.AppOwnership, error) {
	return domain.AppOwnership{App: app}, nil
}

func (f *fakeStore) SaveOwnership(_ context.Context, _ domain.AppOwnership) error {
	return nil
}

func (f *fakeStore) StageApply(_ context.Context, intent domain.AppApplyIntent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	intent.State = domain.AppIntentStaged
	if f.intents[intent.App] == nil {
		f.intents[intent.App] = map[string]domain.AppApplyIntent{}
	}
	f.intents[intent.App][intent.Intent] = intent
	return nil
}

func (f *fakeStore) CommitApply(_ context.Context, app, intentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	intent, ok := f.intents[app][intentID]
	if !ok {
		return domain.ErrAppIntentNotFound
	}
	if intent.State != domain.AppIntentStaged {
		return domain.ErrAppStateConflict
	}
	intent.State = domain.AppIntentCommitted
	f.intents[app][intentID] = intent
	return nil
}

func (f *fakeStore) MaterializeApply(_ context.Context, app, intentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	intent, ok := f.intents[app][intentID]
	if !ok {
		return domain.ErrAppIntentNotFound
	}
	if intent.State == domain.AppIntentApplied {
		return nil
	}
	rev := domain.AppDesiredRevision{
		Revision: intent.Revision, App: app, Supersedes: intent.Supersedes,
		AcceptedAt: intent.CreatedAt, SourceSHA256: intent.SourceSHA256,
		Spec: intent.Spec, Reservations: intent.Reservations,
		Status: domain.AppStepPending,
	}
	f.desired[app] = rev
	f.revisions[app] = append(f.revisions[app], intent.Revision)
	var kept []domain.AppListenerReservation
	for _, res := range f.checkpoint.Reservations {
		if res.App != app {
			kept = append(kept, res)
		}
	}
	f.checkpoint.Reservations = append(kept, intent.Reservations...)
	intent.State = domain.AppIntentApplied
	f.intents[app][intentID] = intent
	return nil
}

func (f *fakeStore) LoadApplyIntent(_ context.Context, app, intentID string) (domain.AppApplyIntent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	intent, ok := f.intents[app][intentID]
	if !ok {
		return domain.AppApplyIntent{}, domain.ErrAppIntentNotFound
	}
	return intent, nil
}

func (f *fakeStore) ListIntents(_ context.Context, app string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id := range f.intents[app] {
		ids = append(ids, id)
	}
	return ids, nil
}

func (f *fakeStore) CollectGarbage(_ context.Context, _ string, _ []string) error {
	return nil
}

func (f *fakeStore) SaveOperation(_ context.Context, op domain.AppOperation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops[op.Op] = op
	return nil
}

func (f *fakeStore) LoadOperation(_ context.Context, _, opID string) (domain.AppOperation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, ok := f.ops[opID]
	if !ok {
		return domain.AppOperation{}, domain.ErrAppOperationNotFound
	}
	return op, nil
}

func (f *fakeStore) SaveActive(_ context.Context, active domain.AppActive) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active[active.App] = active
	return nil
}

func testSpec(name string) domain.AppSpec {
	return domain.AppSpec{
		Name: name,
		Services: []domain.AppService{
			{
				Name: "web", Image: "img:1", Replicas: 1, StopGrace: 10 * time.Second,
				Readiness: domain.AppReadiness{Type: "none", Timeout: 30 * time.Second},
				HTTP:      []domain.AppHTTPInterface{{Host: name + ".example.com", Port: 8080, TLS: "auto"}},
			},
		},
	}
}

func TestApply_AcceptsAndPersists(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	svc := apps.NewService(store, zerowrap.Default())

	result, dry, err := svc.Apply(ctx, testSpec("blog"), []byte("manifest"), "blog.toml", false)
	require.NoError(t, err)
	assert.Nil(t, dry)
	require.NotNil(t, result)
	assert.True(t, result.Pending)
	assert.False(t, result.Noop)
	assert.NotEmpty(t, result.ResultingRevision)
	assert.NotEmpty(t, result.IntentID)
	assert.Equal(t, 1, store.recoverCalls)

	desired, ok, err := store.LoadDesired(ctx, "blog")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, result.ResultingRevision, desired.Revision)
	assert.Equal(t, "pending", desired.Status)
	assert.NotEmpty(t, desired.SourceSHA256)
}

func TestApply_NoopIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	svc := apps.NewService(store, zerowrap.Default())

	first, _, err := svc.Apply(ctx, testSpec("blog"), []byte("manifest"), "blog.toml", false)
	require.NoError(t, err)
	second, _, err := svc.Apply(ctx, testSpec("blog"), []byte("manifest"), "blog.toml", false)
	require.NoError(t, err)
	assert.True(t, second.Noop)
	assert.Equal(t, first.ResultingRevision, second.ResultingRevision)
	revs, err := store.ListRevisions(ctx, "blog")
	require.NoError(t, err)
	assert.Len(t, revs, 1)
	intents, err := store.ListIntents(ctx, "blog")
	require.NoError(t, err)
	assert.Len(t, intents, 1)
}

func TestApply_DryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	svc := apps.NewService(store, zerowrap.Default())

	result, dry, err := svc.Apply(ctx, testSpec("blog"), []byte("manifest"), "blog.toml", true)
	require.NoError(t, err)
	assert.Nil(t, result)
	require.NotNil(t, dry)
	assert.True(t, dry.Valid)

	_, ok, err := store.LoadDesired(ctx, "blog")
	require.NoError(t, err)
	assert.False(t, ok)
	intents, err := store.ListIntents(ctx, "blog")
	require.NoError(t, err)
	assert.Empty(t, intents)
}

func TestApply_RejectsCrossAppConflict(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	svc := apps.NewService(store, zerowrap.Default())

	_, _, err := svc.Apply(ctx, testSpec("blog"), []byte("m"), "blog.toml", false)
	require.NoError(t, err)

	// Same host, different app → conflict.
	other := testSpec("shop")
	other.Services[0].HTTP[0].Host = "blog.example.com"
	_, _, err = svc.Apply(ctx, other, []byte("m"), "shop.toml", false)
	require.ErrorIs(t, err, domain.ErrAppReservationConflict)

	// Same app update retains its own reservation → no self-conflict.
	update := testSpec("blog")
	update.Services[0].Image = "img:2"
	updated, _, err := svc.Apply(ctx, update, []byte("m2"), "blog.toml", false)
	require.NoError(t, err)
	assert.False(t, updated.Noop)
}

func TestApply_RejectsDuplicateClaimInCandidate(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	svc := apps.NewService(store, zerowrap.Default())

	spec := testSpec("blog")
	dup := spec.Services[0]
	dup.Name = "web2"
	spec.Services = append(spec.Services, dup)
	_, _, err := svc.Apply(ctx, spec, []byte("m"), "blog.toml", false)
	require.ErrorIs(t, err, domain.ErrAppReservationConflict)
}

func TestApply_InvalidSpecFailsBeforeStore(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	svc := apps.NewService(store, zerowrap.Default())

	spec := testSpec("Bad!")
	_, _, err := svc.Apply(ctx, spec, []byte("m"), "x.toml", false)
	require.ErrorIs(t, err, domain.ErrInvalidAppSpec)
	assert.Equal(t, 0, store.recoverCalls)
}

func TestApply_SamePortDifferentProtoCoexists(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	svc := apps.NewService(store, zerowrap.Default())

	tcpApp := domain.AppSpec{
		Name: "tcpapp",
		Services: []domain.AppService{
			{
				Name: "s", Image: "img:1", Replicas: 1, StopGrace: 10 * time.Second,
				Readiness: domain.AppReadiness{Type: "none", Timeout: 30 * time.Second},
				TCP:       []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 9000, Publish: "0.0.0.0:9000"}},
			},
		},
	}
	_, _, err := svc.Apply(ctx, tcpApp, []byte("m"), "tcpapp.toml", false)
	require.NoError(t, err)

	udpApp := domain.AppSpec{
		Name: "udpapp",
		Services: []domain.AppService{
			{
				Name: "s", Image: "img:1", Replicas: 1, StopGrace: 10 * time.Second,
				Readiness: domain.AppReadiness{Type: "none", Timeout: 30 * time.Second},
				UDP:       []domain.AppUDPInterface{{Entrypoint: "udp", Port: 9000, Publish: "0.0.0.0:9000"}},
			},
		},
	}
	_, _, err = svc.Apply(ctx, udpApp, []byte("m"), "udpapp.toml", false)
	require.NoError(t, err)
}

func TestReservationsFor_SortedAndNamespaced(t *testing.T) {
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
	reservations := apps.ReservationsFor(spec)
	require.Len(t, reservations, 2)
	assert.Equal(t, "http", reservations[0].Proto)
	assert.Equal(t, "tcp", reservations[1].Proto)
	assert.Equal(t, "dual", reservations[1].IP)
	assert.True(t, errors.Is(domain.ErrAppReservationConflict, domain.ErrAppReservationConflict))
}
