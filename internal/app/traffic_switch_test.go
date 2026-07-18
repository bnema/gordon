package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

type fixtureTrafficChecks struct {
	fail string
}

func (c fixtureTrafficChecks) ComponentHealthy(_ context.Context, role domain.ComponentRole) error {
	return c.err("health:" + string(role))
}
func (c fixtureTrafficChecks) ComponentAuthenticationHealthy(_ context.Context, role domain.ComponentRole) error {
	return c.err("auth:" + string(role))
}
func (c fixtureTrafficChecks) AppliedRouteGeneration(context.Context) (uint64, error) {
	return 7, c.err("route-generation")
}
func (c fixtureTrafficChecks) AppliedTrafficGeneration(context.Context) (uint64, error) {
	return 7, c.err("traffic-generation")
}
func (c fixtureTrafficChecks) TestApplicationThroughEdge(context.Context) error {
	return c.err("application-edge")
}
func (c fixtureTrafficChecks) TestRegistryV2ThroughEdge(context.Context) error {
	return c.err("registry-v2-edge")
}
func (c fixtureTrafficChecks) OldServingPathHealthy(context.Context, string) error {
	return c.err("old-path")
}
func (c fixtureTrafficChecks) err(check string) error {
	if c.fail == check {
		return errors.New("fixture failure")
	}
	return nil
}

type recordingTrafficRuntime struct {
	commands []domain.RuntimeSelfUpdateCommand
}

func (r *recordingTrafficRuntime) SelfUpdateRuntime(_ context.Context, command domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error) {
	r.commands = append(r.commands, command)
	return domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusSucceeded}, nil
}

func fixtureSwitchPlan(t *testing.T) ComponentLaunchPlan {
	t.Helper()
	plan, err := NewComponentLaunchPlan(MigrationCheckpoint{MigrationID: "fixture", ComponentGeneration: 7, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2", Phase: MigrationPhasePrepared, RouteSnapshotGeneration: 7, OldServingPath: "monolith", EdgeAppNetworks: []string{"gordon-app-fixture"}})
	require.NoError(t, err)
	return plan
}

func TestTrafficSwitchPreconditionsFailClosed(t *testing.T) {
	checks := []string{
		"health:control", "health:runtime", "health:registry", "health:edge",
		"auth:control", "auth:runtime", "auth:registry", "auth:edge",
		"route-generation", "traffic-generation", "application-edge", "registry-v2-edge", "old-path",
	}
	for _, check := range checks {
		t.Run(check, func(t *testing.T) {
			runtime := &recordingTrafficRuntime{}
			switcher, err := NewTrafficSwitch(runtime, fixtureTrafficChecks{fail: check})
			require.NoError(t, err)
			err = switcher.Switch(context.Background(), MigrationCheckpoint{MigrationID: "fixture", ComponentGeneration: 7, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2", Phase: MigrationPhasePrepared, RouteSnapshotGeneration: 7, OldServingPath: "monolith"}, fixtureSwitchPlan(t))
			require.Error(t, err)
			assert.Empty(t, runtime.commands, "failed prerequisite must not mutate traffic")
		})
	}
}

func TestTrafficSwitchRejectsGenerationMismatchAndSwitchesOnlyViaRuntime(t *testing.T) {
	runtime := &recordingTrafficRuntime{}
	switcher, err := NewTrafficSwitch(runtime, fixtureTrafficChecks{})
	require.NoError(t, err)
	checkpoint := MigrationCheckpoint{MigrationID: "fixture", ComponentGeneration: 7, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2", Phase: MigrationPhasePrepared, RouteSnapshotGeneration: 7, OldServingPath: "monolith"}
	require.NoError(t, switcher.Switch(context.Background(), checkpoint, fixtureSwitchPlan(t)))
	require.Len(t, runtime.commands, 1)
	command := runtime.commands[0]
	assert.Equal(t, domain.ComponentRoleEdge, command.TargetComponentRole)
	assert.Equal(t, domain.RuntimeComponentLifecycleActivate, command.LifecycleAction)
	assert.True(t, command.PreserveVolumes)
	assert.Equal(t, "monolith", command.OldServingComponentID)
	assert.Empty(t, command.FinalPortPublishes, "a malformed checkpoint cannot invent public ports")
	assert.Equal(t, []string{"gordon-app-fixture"}, command.EdgeAppNetworks)
	assert.Equal(t, uint64(7), command.Generation)
	assert.Contains(t, command.TargetComponentID, "gordon-edge-")
}

type mismatchedTrafficChecks struct{ fixtureTrafficChecks }

func (m mismatchedTrafficChecks) AppliedTrafficGeneration(context.Context) (uint64, error) {
	return 6, nil
}
func TestTrafficSwitchRejectsRouteTrafficGenerationMismatch(t *testing.T) {
	runtime := &recordingTrafficRuntime{}
	switcher, err := NewTrafficSwitch(runtime, mismatchedTrafficChecks{})
	require.NoError(t, err)
	err = switcher.Switch(context.Background(), MigrationCheckpoint{MigrationID: "fixture", ComponentGeneration: 7, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2", Phase: MigrationPhasePrepared, RouteSnapshotGeneration: 7, OldServingPath: "monolith"}, fixtureSwitchPlan(t))
	require.Error(t, err)
	assert.Empty(t, runtime.commands)
}
