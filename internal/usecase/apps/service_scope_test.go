package apps_test

import (
	"context"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apps"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

func scopeDesired(names ...string) domain.AppDesiredRevision {
	rev := domain.AppDesiredRevision{Revision: "rev-1", App: "blog"}
	for _, name := range names {
		rev.Spec.Services = append(rev.Spec.Services, domain.AppService{Name: name})
	}
	return rev
}

func scopeRevision(revision string, names ...string) domain.AppDesiredRevision {
	rev := scopeDesired(names...)
	rev.Revision = revision
	return rev
}

func scopeActive(names ...string) domain.AppActive {
	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{}}
	for _, name := range names {
		active.Services[name] = domain.AppEffectiveService{}
	}
	return active
}

func TestAppServiceImpl_Deploy_MultiServiceRequiresScope(t *testing.T) {
	store := newMockAppState(t)
	deploy := newMockDeployEngine(t)
	svc := apps.NewAppServiceImpl(store, deploy, newMockSecretWriter(t), zerowrap.Default())
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(context.Background())) })

	// A first deploy has no ACTIVE yet: desired services still count.
	store.EXPECT().LoadDesired(mock.Anything, "blog").Return(scopeDesired("web", "db"), true, nil)
	store.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{}, false, nil)
	deploy.startDeployFn = func(context.Context, deployment.DeployInput) (*deployment.StartDeployResult, error) {
		t.Fatal("no claim may be made without a service scope")
		return nil, nil
	}

	op, err := svc.Deploy(context.Background(), "blog", "", "", false, "key-1")
	require.ErrorIs(t, err, domain.ErrAppServiceScope)
	assert.Nil(t, op)
	assert.Contains(t, err.Error(), "app blog has several services (db, web): pass --service NAME or --all")
}

func TestAppServiceImpl_Deploy_OldRevisionWithMultipleServicesRequiresScope(t *testing.T) {
	store := newMockAppState(t)
	deploy := newMockDeployEngine(t)
	svc := apps.NewAppServiceImpl(store, deploy, newMockSecretWriter(t), zerowrap.Default())
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(context.Background())) })

	// The head and ACTIVE know only one service, but the revision the
	// deploy would activate declares two: the scope check must count the
	// selected revision, not the head.
	store.EXPECT().LoadRevision(mock.Anything, "blog", "rev-2").Return(scopeRevision("rev-2", "web", "db"), nil)
	store.EXPECT().LoadActive(mock.Anything, "blog").Return(scopeActive("web"), true, nil)
	deploy.startDeployFn = func(context.Context, deployment.DeployInput) (*deployment.StartDeployResult, error) {
		t.Fatal("no claim may be made without a service scope")
		return nil, nil
	}

	op, err := svc.Deploy(context.Background(), "blog", "rev-2", "", false, "key-1")
	require.ErrorIs(t, err, domain.ErrAppServiceScope)
	assert.Nil(t, op)
	assert.Contains(t, err.Error(), "app blog has several services (db, web): pass --service NAME or --all")
}

func TestAppServiceImpl_Deploy_SingleServiceNeedsNoScope(t *testing.T) {
	store := newMockAppState(t)
	deploy := newMockDeployEngine(t)
	svc := apps.NewAppServiceImpl(store, deploy, newMockSecretWriter(t), zerowrap.Default())
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(context.Background())) })

	store.EXPECT().LoadDesired(mock.Anything, "blog").Return(scopeDesired("web"), true, nil)
	store.EXPECT().LoadActive(mock.Anything, "blog").Return(scopeActive("web"), true, nil)
	started := make(chan struct{}, 1)
	deploy.startDeployFn = func(context.Context, deployment.DeployInput) (*deployment.StartDeployResult, error) {
		started <- struct{}{}
		return nil, domain.ErrAppStateConflict
	}

	_, err := svc.Deploy(context.Background(), "blog", "", "", false, "key-1")
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	require.Len(t, started, 1, "a single-service app deploys without --service or --all")
}

func TestAppServiceImpl_Deploy_AllSkipsScopeCheck(t *testing.T) {
	store := newMockAppState(t)
	deploy := newMockDeployEngine(t)
	svc := apps.NewAppServiceImpl(store, deploy, newMockSecretWriter(t), zerowrap.Default())
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(context.Background())) })

	var got deployment.DeployInput
	deploy.startDeployFn = func(_ context.Context, input deployment.DeployInput) (*deployment.StartDeployResult, error) {
		got = input
		return nil, domain.ErrAppStateConflict
	}

	_, err := svc.Deploy(context.Background(), "blog", "", "", true, "key-1")
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	assert.Equal(t, deployment.DeployInput{App: "blog", Op: "key-1"}, got, "--all deploys every service")
}

func TestAppServiceImpl_Deploy_AllAndServiceAreExclusive(t *testing.T) {
	svc := apps.NewAppServiceImpl(newMockAppState(t), newMockDeployEngine(t), newMockSecretWriter(t), zerowrap.Default())
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(context.Background())) })

	_, err := svc.Deploy(context.Background(), "blog", "", "web", true, "key-1")
	require.ErrorIs(t, err, domain.ErrAppServiceScope)
}

func TestAppServiceImpl_Restart_MultiServiceRequiresScope(t *testing.T) {
	store := newMockAppState(t)
	svc := apps.NewAppServiceImpl(store, newMockDeployEngine(t), newMockSecretWriter(t), zerowrap.Default())
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(context.Background())) })

	store.EXPECT().LoadActive(mock.Anything, "blog").Return(scopeActive("web", "db"), true, nil)

	// The engine mock has no Restart expectation: reaching it fails the test.
	op, err := svc.Restart(context.Background(), "blog", "", false, "key-1")
	require.ErrorIs(t, err, domain.ErrAppServiceScope)
	assert.Nil(t, op)
	assert.Contains(t, err.Error(), "(db, web)")
}

func TestAppServiceImpl_Restart_AllAndServiceAreExclusive(t *testing.T) {
	svc := apps.NewAppServiceImpl(newMockAppState(t), newMockDeployEngine(t), newMockSecretWriter(t), zerowrap.Default())
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(context.Background())) })

	_, err := svc.Restart(context.Background(), "blog", "web", true, "key-1")
	require.ErrorIs(t, err, domain.ErrAppServiceScope)
}
