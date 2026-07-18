package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

type recordingRedeployExecutor struct {
	calls []string
	fail  string
}

func (e *recordingRedeployExecutor) StartReplacement(_ context.Context, component ComponentLaunchComponent) error {
	return e.call("replace:" + string(component.Role))
}
func (e *recordingRedeployExecutor) CheckReplacementHealth(_ context.Context, component ComponentLaunchComponent) error {
	return e.call("health:" + string(component.Role))
}
func (e *recordingRedeployExecutor) DrainPrevious(_ context.Context, component ComponentLaunchComponent) error {
	return e.call("drain:" + string(component.Role))
}
func (e *recordingRedeployExecutor) ActivateReplacement(_ context.Context, component ComponentLaunchComponent) error {
	return e.call("activate:" + string(component.Role))
}
func (e *recordingRedeployExecutor) PostCheck(_ context.Context, component ComponentLaunchComponent) error {
	return e.call("postcheck:" + string(component.Role))
}
func (e *recordingRedeployExecutor) RecoverRuntime(_ context.Context, component ComponentLaunchComponent) error {
	return e.call("recover:" + string(component.Role))
}
func (e *recordingRedeployExecutor) call(name string) error {
	e.calls = append(e.calls, name)
	if e.fail == name {
		return errors.New("fixture failure")
	}
	return nil
}

func TestSelfRedeployOrderingRetainsPreviousGeneration(t *testing.T) {
	previous := fixtureRedeployPlan(t, 1)
	desired := fixtureRedeployPlan(t, 2)
	executor := &recordingRedeployExecutor{}
	redeploy, err := NewSelfRedeployer(executor)
	require.NoError(t, err)
	report, err := redeploy.Redeploy(context.Background(), previous, desired)
	require.NoError(t, err)
	assert.True(t, report.PreviousGenerationRetained)
	assert.False(t, report.Retryable)
	assert.Equal(t, []string{
		"replace:registry", "health:registry", "activate:registry", "postcheck:registry",
		"replace:edge", "health:edge", "drain:edge", "activate:edge", "postcheck:edge",
		"replace:control", "health:control", "activate:control", "postcheck:control",
		"replace:runtime", "health:runtime", "activate:runtime", "postcheck:runtime", "recover:runtime",
	}, executor.calls)
	assert.NotContains(t, executor.calls, "remove:registry")
}

func TestSelfRedeployFailureAtEachComponentIsRetryableAndDoesNotDelete(t *testing.T) {
	for _, role := range []string{"registry", "edge", "control", "runtime"} {
		t.Run(role, func(t *testing.T) {
			executor := &recordingRedeployExecutor{fail: "replace:" + role}
			redeploy, err := NewSelfRedeployer(executor)
			require.NoError(t, err)
			report, err := redeploy.Redeploy(context.Background(), fixtureRedeployPlan(t, 1), fixtureRedeployPlan(t, 2))
			require.Error(t, err)
			assert.True(t, report.PreviousGenerationRetained)
			assert.True(t, report.Retryable)
			assert.Equal(t, domain.ComponentRole(role), report.FailedRole)
			assert.NotContains(t, executor.calls, "remove:"+role)
		})
	}
}

func TestSelfRedeployRuntimeRecoveryFailureLeavesOldGenerationRetryable(t *testing.T) {
	executor := &recordingRedeployExecutor{fail: "recover:runtime"}
	redeploy, err := NewSelfRedeployer(executor)
	require.NoError(t, err)
	report, err := redeploy.Redeploy(context.Background(), fixtureRedeployPlan(t, 1), fixtureRedeployPlan(t, 2))
	require.Error(t, err)
	assert.True(t, report.PreviousGenerationRetained)
	assert.True(t, report.Retryable)
	assert.Equal(t, domain.ComponentRoleRuntime, report.FailedRole)
}

func fixtureRedeployPlan(t *testing.T, generation uint64) ComponentLaunchPlan {
	t.Helper()
	plan, err := NewComponentLaunchPlan(MigrationCheckpoint{MigrationID: "fixture", ComponentGeneration: generation, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2"})
	require.NoError(t, err)
	return plan
}
