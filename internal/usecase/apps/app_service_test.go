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

func secretSpec() domain.AppSpec {
	return domain.AppSpec{
		Name: "blog",
		Services: []domain.AppService{
			{
				Name:      "web",
				Image:     "img:1",
				StopGrace: 10 * time.Second,
				Readiness: domain.AppReadiness{Type: "none", Timeout: 30 * time.Second},
				HTTP:      []domain.AppHTTPInterface{{Host: "blog.example.com", Port: 8080, TLS: "auto"}},
				Secrets:   map[string]string{"DATABASE_URL": "database-url"},
			},
		},
	}
}

func TestAppServiceImpl_SecretsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newMockAppState(t)
	deploy := newMockDeployEngine(t)
	secrets := newMockSecretWriter(t)

	// Apply persists desired state through the store mock.
	store.EXPECT().Recover(mock.Anything).Return(nil).Once()
	store.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil).Once()
	store.EXPECT().LoadDesired(mock.Anything, "blog").Return(domain.AppDesiredRevision{}, false, nil).Once()
	store.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{}, false, nil).Once()
	store.EXPECT().StageApply(mock.Anything, mock.Anything).Return(nil).Once()
	store.EXPECT().CommitApply(mock.Anything, "blog", mock.Anything).Return(nil).Once()
	store.EXPECT().MaterializeApply(mock.Anything, "blog", mock.Anything).Return(nil).Once()
	store.EXPECT().CollectGarbage(mock.Anything, "blog", mock.Anything).Return(nil).Once()

	svc := apps.NewAppServiceImpl(store, deploy, secrets, zerowrap.Default())
	_, _, err := svc.Apply(ctx, secretSpec(), []byte("m"), false)
	require.NoError(t, err)

	// SetSecrets resolves registered names from desired state.
	desired := domain.AppDesiredRevision{App: "blog", Revision: "rev-1", Spec: secretSpec()}
	store.EXPECT().LoadDesired(mock.Anything, "blog").Return(desired, true, nil).Once()
	store.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{}, false, nil).Once()
	store.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-uuid-1"}, nil).Once()
	secrets.EXPECT().SetSecret(mock.Anything, "gordon/apps/app-uuid-1/web/database-url", "v").Return(nil).Once()
	require.NoError(t, svc.SetSecrets(ctx, "blog", "web", map[string]string{"DATABASE_URL": "v"}))

	// Delete refused while still referenced (names + reference check).
	desiredWithSecret := desired
	store.EXPECT().LoadDesired(mock.Anything, "blog").Return(desiredWithSecret, true, nil).Once()
	store.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{}, false, nil).Once()
	store.EXPECT().LoadDesired(mock.Anything, "blog").Return(desiredWithSecret, true, nil).Once()
	require.ErrorIs(t, svc.DeleteSecret(ctx, "blog", "web", "DATABASE_URL"), domain.ErrAppStateConflict)

	// Unknown key refused.
	store.EXPECT().LoadDesired(mock.Anything, "blog").Return(desired, true, nil).Once()
	store.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{}, false, nil).Once()
	require.ErrorIs(t, svc.SetSecrets(ctx, "blog", "web", map[string]string{"NOPE": "v"}), domain.ErrAppSecretMissing)
}

func TestAppServiceImpl_OperationByKey(t *testing.T) {
	ctx := context.Background()
	store := newMockAppState(t)
	deploy := newMockDeployEngine(t)
	secrets := newMockSecretWriter(t)
	svc := apps.NewAppServiceImpl(store, deploy, secrets, zerowrap.Default())

	op := domain.AppOperation{Op: "op-1", Kind: "deploy", App: "blog", StartedAt: time.Now().UTC()}
	store.EXPECT().LoadOperation(mock.Anything, "blog", "op-1").Return(op, nil).Once()
	loaded, err := svc.OperationByKey(ctx, "blog", "op-1")
	require.NoError(t, err)
	assert.Equal(t, "op-1", loaded.Op)

	store.EXPECT().LoadOperation(mock.Anything, "blog", "missing").Return(domain.AppOperation{}, domain.ErrAppOperationNotFound).Once()
	_, err = svc.OperationByKey(ctx, "blog", "missing")
	require.ErrorIs(t, err, domain.ErrAppOperationNotFound)
}
