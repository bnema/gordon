package apps_test

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apps"
)

func TestAppService_Show_FreshDesiredOnlyApp(t *testing.T) {
	ctx := context.Background()
	store := newMockAppState(t)
	svc := apps.NewAppServiceImpl(store, newMockDeployEngine(t), newMockSecretWriter(t), zerowrap.Default())

	store.EXPECT().AppExists(mock.Anything, "blog").Return(true, nil).Once()
	store.EXPECT().LoadDesired(mock.Anything, "blog").Return(domain.AppDesiredRevision{
		App: "blog", Revision: "rev-1", Status: "pending",
	}, true, nil).Once()
	store.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{}, false, nil).Once()
	store.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	store.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-1"}, nil).Once()
	store.EXPECT().LoadLatestOperation(mock.Anything, "blog").
		Return(domain.AppOperation{}, false, nil).Once()

	detail, err := svc.Show(ctx, "blog")
	require.NoError(t, err)
	assert.Equal(t, "rev-1", detail.DesiredRevision)
	assert.Equal(t, "pending", detail.DesiredStatus)
	assert.True(t, detail.Pending, "desired state ACTIVE never reached is pending")
	assert.False(t, detail.Converged)
	assert.Empty(t, detail.ConvergedRevision)
	assert.Empty(t, detail.Services)
	assert.Empty(t, detail.LastOp)
	assert.Empty(t, detail.Retained.Volumes)
}

func TestAppService_Show_ConvergedAppWithRetainedResources(t *testing.T) {
	ctx := context.Background()
	store := newMockAppState(t)
	svc := apps.NewAppServiceImpl(store, newMockDeployEngine(t), newMockSecretWriter(t), zerowrap.Default())
	started := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)

	store.EXPECT().AppExists(mock.Anything, "blog").Return(true, nil).Once()
	store.EXPECT().LoadDesired(mock.Anything, "blog").Return(domain.AppDesiredRevision{
		App: "blog", Revision: "rev-1", Status: "active",
	}, true, nil).Once()
	store.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog", Converged: true, ConvergedRevision: "rev-1",
		Services: map[string]domain.AppEffectiveService{
			"web": {EffectiveRevision: "rev-1", Digest: "sha256:abc", Container: "ctr-1"},
		},
	}, true, nil).Once()
	store.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	store.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{
		App: "blog", ID: "app-1",
		Volumes: []domain.AppOwnedVolume{
			{Name: "data", Service: "web", RuntimeName: "gordon-blog--web--vol--data", State: domain.AppResourceAttached},
		},
		Secrets: []domain.AppOwnedSecret{
			{Service: "web", Env: "DATABASE_URL", Name: "database-url", Path: "gordon/apps/app-1/blog/web/database-url", State: domain.AppResourceAttached},
		},
		Images: []domain.AppOwnedImage{
			{Service: "web", Reference: "registry.example.com/blog/web:1.4.2", State: domain.AppResourceAttached},
		},
		Services: map[string]domain.AppServiceRecovery{"web": {RestartUnsafe: true}},
	}, nil).Once()
	store.EXPECT().LoadLatestOperation(mock.Anything, "blog").Return(domain.AppOperation{
		Op: "op-9", Kind: "deploy", App: "blog", Outcome: domain.AppOutcomeSuccess, StartedAt: started,
	}, true, nil).Once()

	detail, err := svc.Show(ctx, "blog")
	require.NoError(t, err)
	assert.False(t, detail.Pending, "desired == ACTIVE is not pending")
	assert.True(t, detail.Converged)
	assert.Equal(t, "rev-1", detail.ConvergedRevision)
	require.Contains(t, detail.Services, "web")
	assert.Equal(t, "ctr-1", detail.Services["web"].Container)
	assert.Equal(t, "sha256:abc", detail.Services["web"].Digest)
	assert.True(t, detail.Services["web"].RestartUnsafe)
	assert.Equal(t, []string{"gordon-blog--web--vol--data"}, detail.Retained.Volumes)
	assert.Equal(t, []string{"gordon/apps/app-1/blog/web/database-url"}, detail.Retained.Secrets)
	assert.Equal(t, []string{"registry.example.com/blog/web:1.4.2"}, detail.Retained.Images)
	assert.Equal(t, "op-9", detail.LastOp)
	assert.Equal(t, "deploy", detail.LastOpKind)
	assert.Equal(t, domain.AppOutcomeSuccess, detail.LastOutcome)
	assert.Equal(t, started, detail.LastOpStartedAt)
}

func TestAppService_Show_MixedRevisionsArePending(t *testing.T) {
	ctx := context.Background()
	store := newMockAppState(t)
	svc := apps.NewAppServiceImpl(store, newMockDeployEngine(t), newMockSecretWriter(t), zerowrap.Default())

	store.EXPECT().AppExists(mock.Anything, "blog").Return(true, nil).Once()
	store.EXPECT().LoadDesired(mock.Anything, "blog").Return(domain.AppDesiredRevision{
		App: "blog", Revision: "rev-2", Status: "pending",
	}, true, nil).Once()
	store.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog", Converged: true, ConvergedRevision: "rev-1",
		Services: map[string]domain.AppEffectiveService{
			"web": {EffectiveRevision: "rev-1", Container: "ctr-1"},
		},
	}, true, nil).Once()
	store.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	store.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-1"}, nil).Once()
	store.EXPECT().LoadLatestOperation(mock.Anything, "blog").Return(domain.AppOperation{
		Op: "op-8", Kind: "deploy", App: "blog", Outcome: domain.AppOutcomeFailed,
	}, true, nil).Once()

	detail, err := svc.Show(ctx, "blog")
	require.NoError(t, err)
	assert.True(t, detail.Pending, "a newer desired revision is pending")
	assert.Equal(t, "rev-1", detail.ConvergedRevision, "ACTIVE stays authoritative")
	assert.Equal(t, domain.AppOutcomeFailed, detail.LastOutcome, "the failed journal is reported")
}

func TestAppService_Show_StoppedAppKeepsReadModel(t *testing.T) {
	ctx := context.Background()
	store := newMockAppState(t)
	svc := apps.NewAppServiceImpl(store, newMockDeployEngine(t), newMockSecretWriter(t), zerowrap.Default())

	store.EXPECT().AppExists(mock.Anything, "blog").Return(true, nil).Once()
	store.EXPECT().LoadDesired(mock.Anything, "blog").Return(domain.AppDesiredRevision{
		App: "blog", Revision: "rev-1", Status: "active",
	}, true, nil).Once()
	store.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog", Converged: true, ConvergedRevision: "rev-1",
		Services: map[string]domain.AppEffectiveService{"web": {EffectiveRevision: "rev-1", Container: "ctr-1"}},
	}, true, nil).Once()
	store.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog", Stopped: true}, nil).Once()
	store.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-1"}, nil).Once()
	store.EXPECT().LoadLatestOperation(mock.Anything, "blog").Return(domain.AppOperation{}, false, nil).Once()

	detail, err := svc.Show(ctx, "blog")
	require.NoError(t, err)
	assert.True(t, detail.Stopped)
	assert.False(t, detail.Pending, "stopping does not make desired state pending")
	require.Contains(t, detail.Services, "web")
}

// TestAppService_Show_UnknownAppIsNotFound proves a name with no live app
// identity reports not-found instead of an empty detail.
func TestAppService_Show_UnknownAppIsNotFound(t *testing.T) {
	ctx := context.Background()
	store := newMockAppState(t)
	svc := apps.NewAppServiceImpl(store, newMockDeployEngine(t), newMockSecretWriter(t), zerowrap.Default())

	store.EXPECT().AppExists(mock.Anything, "ghost").Return(false, nil).Once()

	_, err := svc.Show(ctx, "ghost")
	require.ErrorIs(t, err, domain.ErrAppNotFound)
}

func TestAppService_List_SkipsRetiredNamesAndReportsReadModel(t *testing.T) {
	ctx := context.Background()
	store := newMockAppState(t)
	svc := apps.NewAppServiceImpl(store, newMockDeployEngine(t), newMockSecretWriter(t), zerowrap.Default())

	store.EXPECT().ListApps(mock.Anything).Return([]string{"blog", "gone"}, nil).Once()
	// blog is live: desired rev-2 while ACTIVE holds rev-1, latest deploy partial.
	store.EXPECT().AppExists(mock.Anything, "blog").Return(true, nil).Once()
	store.EXPECT().LoadDesired(mock.Anything, "blog").Return(domain.AppDesiredRevision{
		App: "blog", Revision: "rev-2", Status: "pending",
	}, true, nil).Once()
	store.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog", Converged: true, ConvergedRevision: "rev-1",
	}, true, nil).Once()
	store.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	store.EXPECT().LoadLatestOperation(mock.Anything, "blog").Return(domain.AppOperation{
		Op: "op-7", Kind: "deploy", App: "blog", Outcome: domain.AppOutcomePartial,
	}, true, nil).Once()
	// gone keeps only its retired journal and is not an app any more.
	store.EXPECT().AppExists(mock.Anything, "gone").Return(false, nil).Once()

	summaries, err := svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.Equal(t, "blog", summaries[0].App)
	assert.Equal(t, "rev-2", summaries[0].Desired)
	assert.Equal(t, "pending", summaries[0].DesiredStatus)
	assert.Equal(t, "rev-1", summaries[0].Active)
	assert.True(t, summaries[0].Pending)
	assert.Equal(t, domain.AppOutcomePartial, summaries[0].LastOutcome)
}
