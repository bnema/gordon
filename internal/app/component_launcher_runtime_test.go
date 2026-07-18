package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

type recordingRuntimeSelfUpdater struct {
	commands []domain.RuntimeSelfUpdateCommand
}

func (r *recordingRuntimeSelfUpdater) SelfUpdateRuntime(_ context.Context, command domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error) {
	r.commands = append(r.commands, command)
	return domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusSucceeded}, nil
}

func TestRuntimeComponentLauncherUsesOnlyLifecycleCommands(t *testing.T) {
	updater := &recordingRuntimeSelfUpdater{}
	launcher, err := NewRuntimeComponentLauncher(updater)
	require.NoError(t, err)
	plan, err := NewComponentLaunchPlan(MigrationCheckpoint{MigrationID: "fixture-migration", ComponentGeneration: 1, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2"})
	require.NoError(t, err)
	require.NoError(t, launcher.CreateInternalNetwork(context.Background(), plan))
	require.NoError(t, launcher.StartComponent(context.Background(), plan.Components[0]))
	require.NoError(t, launcher.TransferRuntimeCommandChannel(context.Background(), plan.Components[0]))
	require.Len(t, updater.commands, 3)
	assert.Equal(t, domain.RuntimeComponentLifecycleEnsureNetwork, updater.commands[0].LifecycleAction)
	assert.Equal(t, domain.RuntimeComponentLifecycleStart, updater.commands[1].LifecycleAction)
	assert.Equal(t, plan.Components[0].DesiredStateHash, updater.commands[1].DesiredStateHash)
	assert.True(t, updater.commands[1].PreserveVolumes)
	assert.Equal(t, domain.RuntimeComponentLifecycleTransferChannel, updater.commands[2].LifecycleAction)
	assert.NotEmpty(t, updater.commands[1].IdempotencyKey)
}
