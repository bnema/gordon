package container

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestRuntimeStandaloneServicePolicyManagerAllowsCompliantApply(t *testing.T) {
	inner := &fakeRuntimeStandaloneServiceManager{}
	manager := NewRuntimeStandaloneServicePolicyManager(inner, RuntimePolicy{
		Mode:                   RuntimePolicyModeEnforce,
		AllowedImageRegistries: []string{"registry.example.com"},
		RequireImageDigest:     true,
	})
	command := standaloneServicePolicyApplyCommand("registry.example.com/game@sha256:abc")

	result, err := manager.ApplyStandaloneService(context.Background(), command)

	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, result.Status)
	assert.Equal(t, []domain.ApplyStandaloneServiceCommand{command}, inner.applyCommands)
	assert.Empty(t, manager.PolicyDeniedEvents())
}

func TestRuntimeStandaloneServicePolicyManagerDeniesUnsafeApplyWithoutCallingInner(t *testing.T) {
	tests := []struct {
		name       string
		policy     RuntimePolicy
		image      string
		wantReason string
	}{
		{
			name:       "registry",
			policy:     RuntimePolicy{Mode: RuntimePolicyModeEnforce, AllowedImageRegistries: []string{"registry.example.com"}},
			image:      "untrusted.example.com/game@sha256:abc",
			wantReason: RuntimePolicyReasonImageRegistryDenied,
		},
		{
			name:       "digest",
			policy:     RuntimePolicy{Mode: RuntimePolicyModeEnforce, RequireImageDigest: true},
			image:      "registry.example.com/game:latest",
			wantReason: RuntimePolicyReasonDigestRequired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &fakeRuntimeStandaloneServiceManager{}
			manager := NewRuntimeStandaloneServicePolicyManager(inner, tt.policy)
			command := standaloneServicePolicyApplyCommand(tt.image)
			command.ResolvedEnv = []string{"TOKEN=super-secret"}

			result, err := manager.ApplyStandaloneService(context.Background(), command)

			require.NoError(t, err)
			assert.Equal(t, domain.RuntimeCommandStatusDenied, result.Status)
			require.NotNil(t, result.Error)
			assert.Equal(t, "runtime_policy_denied:"+tt.wantReason, result.Error.Code)
			assert.Equal(t, "runtime policy denied", result.Error.Message)
			assert.False(t, result.Error.Retryable)
			assert.NotContains(t, result.Error.Message, "super-secret")
			assert.NotContains(t, result.Error.Message, tt.image)
			assert.Empty(t, inner.applyCommands)

			events := manager.PolicyDeniedEvents()
			require.Len(t, events, 1)
			assert.Equal(t, tt.wantReason, events[0].Reason)
		})
	}
}

func TestRuntimeStandaloneServicePolicyManagerObservesAndAllowsPolicyFindings(t *testing.T) {
	inner := &fakeRuntimeStandaloneServiceManager{}
	manager := NewRuntimeStandaloneServicePolicyManager(inner, RuntimePolicy{Mode: RuntimePolicyModeObserve, RequireImageDigest: true})
	command := standaloneServicePolicyApplyCommand("registry.example.com/game:latest")

	result, err := manager.ApplyStandaloneService(context.Background(), command)

	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, result.Status)
	assert.Equal(t, []domain.ApplyStandaloneServiceCommand{command}, inner.applyCommands)
	events := manager.PolicyDeniedEvents()
	require.Len(t, events, 1)
	assert.Equal(t, RuntimePolicyReasonDigestRequired, events[0].Reason)
}

func TestRuntimeStandaloneServicePolicyManagerRequiresManagedRemoveName(t *testing.T) {
	inner := &fakeRuntimeStandaloneServiceManager{}
	manager := NewRuntimeStandaloneServicePolicyManager(inner, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
	command := standaloneServicePolicyRemoveCommand(" game ")

	result, err := manager.RemoveStandaloneService(context.Background(), command)

	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusDenied, result.Status)
	require.NotNil(t, result.Error)
	assert.Equal(t, "runtime_policy_denied:"+RuntimePolicyReasonUnmanagedMutation, result.Error.Code)
	assert.Equal(t, "runtime policy denied", result.Error.Message)
	assert.Empty(t, inner.removeCommands)
	assert.Len(t, manager.PolicyDeniedEvents(), 1)
}

func TestRuntimeStandaloneServicePolicyManagerDelegatesListAndSanitizesInvalidCommandFailure(t *testing.T) {
	states := []domain.RuntimeStandaloneServiceState{{Name: "game", ContainerID: "container-1", Status: domain.ContainerStatusRunning}}
	inner := &fakeRuntimeStandaloneServiceManager{states: states, applyErr: errors.New("TOKEN=super-secret")}
	manager := NewRuntimeStandaloneServicePolicyManager(inner, RuntimePolicy{Mode: RuntimePolicyModeEnforce})

	gotStates, err := manager.ListStandaloneServiceState(context.Background())
	require.NoError(t, err)
	assert.Equal(t, states, gotStates)
	assert.Equal(t, 1, inner.listCalls)

	invalid := standaloneServicePolicyApplyCommand("registry.example.com/game:latest")
	invalid.ConfigHash = ""
	result, err := manager.ApplyStandaloneService(context.Background(), invalid)
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusFailed, result.Status)
	require.NotNil(t, result.Error)
	assert.Equal(t, "invalid_runtime_command", result.Error.Code)
	assert.NotContains(t, result.Error.Message, "super-secret")
	assert.Empty(t, inner.applyCommands)
}

type fakeRuntimeStandaloneServiceManager struct {
	applyCommands  []domain.ApplyStandaloneServiceCommand
	removeCommands []domain.RemoveStandaloneServiceCommand
	states         []domain.RuntimeStandaloneServiceState
	applyErr       error
	listCalls      int
}

func (f *fakeRuntimeStandaloneServiceManager) ApplyStandaloneService(_ context.Context, command domain.ApplyStandaloneServiceCommand) (domain.RuntimeCommandResult, error) {
	f.applyCommands = append(f.applyCommands, command)
	if f.applyErr != nil {
		return domain.RuntimeCommandResult{}, f.applyErr
	}
	return domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusSucceeded}, nil
}

func (f *fakeRuntimeStandaloneServiceManager) RemoveStandaloneService(_ context.Context, command domain.RemoveStandaloneServiceCommand) (domain.RuntimeCommandResult, error) {
	f.removeCommands = append(f.removeCommands, command)
	return domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusSucceeded}, nil
}

func (f *fakeRuntimeStandaloneServiceManager) ListStandaloneServiceState(context.Context) ([]domain.RuntimeStandaloneServiceState, error) {
	f.listCalls++
	return f.states, nil
}

func standaloneServicePolicyApplyCommand(image string) domain.ApplyStandaloneServiceCommand {
	return domain.ApplyStandaloneServiceCommand{
		RuntimeCommandIdentity: domain.RuntimeCommandIdentity{ID: "standalone-apply", IdempotencyKey: "standalone-apply-game", Generation: 1, SourceComponentID: "control-1"},
		Service:                domain.StandaloneService{Name: "game", Image: image, Enabled: true},
		ConfigHash:             "config-hash",
	}
}

func standaloneServicePolicyRemoveCommand(name string) domain.RemoveStandaloneServiceCommand {
	return domain.RemoveStandaloneServiceCommand{
		RuntimeCommandIdentity: domain.RuntimeCommandIdentity{ID: "standalone-remove", IdempotencyKey: "standalone-remove-game", Generation: 1, SourceComponentID: "control-1"},
		Name:                   name,
	}
}
