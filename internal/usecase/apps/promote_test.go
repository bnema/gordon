package apps_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apps"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

var promoter = domain.WorkloadPrincipal{Issuer: "https://ci.example.com", Subject: "repo:acme/blog"}

func promoteOp(outcome string) domain.AppOperation {
	return domain.AppOperation{
		Op: "key-1", Kind: domain.AppOperationKindPromote, App: "blog", InputRevision: "rev-2",
		Principal: promoter.String(), Outcome: outcome, StartedAt: time.Now().UTC(),
	}
}

// TestAppServiceImpl_Promote_SchedulesOwnedClaim proves Promote mirrors
// Deploy: the input reaches StartPromotion with the apply validator, the
// owned claim executes in the background, and the running journal returns.
// It runs in a synctest bubble: the background execution goroutine is
// created inside it and synctest.Wait observes its completion without a
// timeout guard (mocks only, no real I/O).
func TestAppServiceImpl_Promote_SchedulesOwnedClaim(t *testing.T) {
	synctest.Test(t, testPromoteSchedulesOwnedClaim)
}

func testPromoteSchedulesOwnedClaim(t *testing.T) {
	store := newMockAppState(t)
	deploy := newMockDeployEngine(t)
	svc := apps.NewAppServiceImpl(store, deploy, newMockSecretWriter(t), zerowrap.Default())
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(context.Background())) })

	op := promoteOp("")
	store.EXPECT().LoadOperation(mock.Anything, "blog", "key-1").Return(op, nil)
	var got deployment.PromotionInput
	deploy.startPromotionFn = func(_ context.Context, input deployment.PromotionInput) (*deployment.StartDeployResult, error) {
		got = input
		return &deployment.StartDeployResult{Claim: deployment.DeployClaim{App: "blog", Op: "key-1", Service: "web", Journal: op}, Owned: true}, nil
	}
	var executed []deployment.DeployClaim
	deploy.executeDeployFn = func(_ context.Context, claim deployment.DeployClaim) (*deployment.DeployResult, error) {
		executed = append(executed, claim)
		return &deployment.DeployResult{}, nil
	}

	result, err := svc.Promote(context.Background(), in.PromoteInput{
		App: "blog", Service: "web", Image: "web@sha256:abc", ExpectedRevision: "rev-1",
		IdempotencyKey: "key-1", Principal: promoter,
	})
	require.NoError(t, err)
	assert.Equal(t, "key-1", result.Op)
	assert.Equal(t, "blog", got.App)
	assert.Equal(t, "web", got.Service)
	assert.Equal(t, "rev-1", got.ExpectedRevision)
	assert.Equal(t, "key-1", got.Op)
	assert.Equal(t, promoter, got.Principal)
	assert.False(t, got.LocalOwner)
	require.NotNil(t, got.Validate, "the candidate is validated like an applied manifest")
	require.Error(t, got.Validate(domain.AppSpec{}), "the validator rejects an invalid spec")
	synctest.Wait()
	require.Len(t, executed, 1, "the owned claim executes exactly once")
	assert.Equal(t, "key-1", executed[0].Op)
}

// TestAppServiceImpl_Promote_ReplayNeverExecutes proves a replayed key
// returns its stored journal and failure without a second execution.
func TestAppServiceImpl_Promote_ReplayNeverExecutes(t *testing.T) {
	store := newMockAppState(t)
	deploy := newMockDeployEngine(t)
	svc := apps.NewAppServiceImpl(store, deploy, newMockSecretWriter(t), zerowrap.Default())
	op := promoteOp(domain.AppOutcomeFailed)
	store.EXPECT().LoadOperation(mock.Anything, "blog", "key-1").Return(op, nil)
	deploy.startPromotionFn = func(context.Context, deployment.PromotionInput) (*deployment.StartDeployResult, error) {
		return &deployment.StartDeployResult{Claim: deployment.DeployClaim{App: "blog", Op: "key-1", Journal: op}}, nil
	}

	result, err := svc.Promote(context.Background(), in.PromoteInput{App: "blog", Service: "web", IdempotencyKey: "key-1", Principal: promoter})
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	require.NotNil(t, result)
	assert.Equal(t, domain.AppOutcomeFailed, result.Outcome)
}

// TestAppServiceImpl_Promote_RefusedAfterShutdown proves no claim is made
// once the daemon is stopping.
func TestAppServiceImpl_Promote_RefusedAfterShutdown(t *testing.T) {
	svc := apps.NewAppServiceImpl(newMockAppState(t), newMockDeployEngine(t), newMockSecretWriter(t), zerowrap.Default())
	require.NoError(t, svc.Shutdown(context.Background()))
	_, err := svc.Promote(context.Background(), in.PromoteInput{App: "blog", Service: "web", IdempotencyKey: "k"})
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
}

func TestAppServiceImpl_PrincipalOperationByKey(t *testing.T) {
	tests := []struct {
		name      string
		recorded  string
		principal domain.WorkloadPrincipal
		found     bool
	}{
		{name: "same principal", recorded: promoter.String(), principal: promoter, found: true},
		{name: "other subject", recorded: promoter.String(), principal: domain.WorkloadPrincipal{Issuer: promoter.Issuer, Subject: "repo:acme/other"}},
		{name: "other issuer", recorded: promoter.String(), principal: domain.WorkloadPrincipal{Issuer: domain.GordonIssuer, Subject: promoter.Subject}},
		{name: "unrecorded principal", recorded: "", principal: domain.WorkloadPrincipal{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newMockAppState(t)
			svc := apps.NewAppServiceImpl(store, newMockDeployEngine(t), newMockSecretWriter(t), zerowrap.Default())
			op := promoteOp(domain.AppOutcomeSuccess)
			op.Principal = tc.recorded
			store.EXPECT().LoadOperation(mock.Anything, "blog", "key-1").Return(op, nil).Once()
			got, err := svc.PrincipalOperationByKey(context.Background(), "blog", "key-1", tc.principal)
			if tc.found {
				require.NoError(t, err)
				assert.Equal(t, "key-1", got.Op)
				return
			}
			require.ErrorIs(t, err, domain.ErrAppOperationNotFound)
			assert.Nil(t, got)
		})
	}
}
