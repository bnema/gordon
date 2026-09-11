package apps_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/bnema/gordon/internal/boundaries/out"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

// mockDeployEngine is a mockery-style test double for the unexported
// apps.deployEngine interface. mockery cannot generate it (unexported),
// so this hand-written mock follows the same EXPECT()/Return() pattern.
type mockDeployEngine struct {
	mock.Mock
}

func newMockDeployEngine(t *testing.T) *mockDeployEngine {
	m := &mockDeployEngine{}
	m.Test(t)
	t.Cleanup(func() { m.AssertExpectations(t) })
	return m
}

func (m *mockDeployEngine) Deploy(ctx context.Context, input deployment.DeployInput) (*deployment.DeployResult, error) {
	args := m.Called(ctx, input)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*deployment.DeployResult), args.Error(1)
}

func (m *mockDeployEngine) Stop(ctx context.Context, app, opID string) (*deployment.LifecycleResult, error) {
	args := m.Called(ctx, app, opID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*deployment.LifecycleResult), args.Error(1)
}

func (m *mockDeployEngine) Start(ctx context.Context, app, opID string) (*deployment.LifecycleResult, error) {
	args := m.Called(ctx, app, opID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*deployment.LifecycleResult), args.Error(1)
}

func (m *mockDeployEngine) Restart(ctx context.Context, app, service, opID string) (*deployment.LifecycleResult, error) {
	args := m.Called(ctx, app, service, opID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*deployment.LifecycleResult), args.Error(1)
}

func (m *mockDeployEngine) Remove(ctx context.Context, app, opID string) (*deployment.LifecycleResult, error) {
	args := m.Called(ctx, app, opID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*deployment.LifecycleResult), args.Error(1)
}

func newMockAppState(t *testing.T) *outmocks.MockAppState {
	return outmocks.NewMockAppState(t)
}

func newMockSecretWriter(t *testing.T) *outmocks.MockSecretWriter {
	return outmocks.NewMockSecretWriter(t)
}

var _ out.AppState = (*outmocks.MockAppState)(nil)
var _ out.SecretWriter = (*outmocks.MockSecretWriter)(nil)
