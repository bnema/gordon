package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

type handoffRuntime struct {
	commands    []domain.RuntimeSelfUpdateCommand
	probe       out.RuntimeEnvironment
	probeErrors []error
	probeCalls  int
	pingErr     error
	states      []domain.RuntimeActualStateSnapshot
}

func (r *handoffRuntime) SelfUpdateRuntime(_ context.Context, command domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error) {
	r.commands = append(r.commands, command)
	return domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusSucceeded}, nil
}
func (r *handoffRuntime) ProbeRuntimeEnvironment(context.Context) (out.RuntimeEnvironment, error) {
	r.probeCalls++
	if len(r.probeErrors) >= r.probeCalls {
		return out.RuntimeEnvironment{}, r.probeErrors[r.probeCalls-1]
	}
	return r.probe, nil
}
func (r *handoffRuntime) PingRuntime(context.Context) error              { return r.pingErr }
func (r *handoffRuntime) RuntimeVersion(context.Context) (string, error) { return "fixture", nil }
func (r *handoffRuntime) SubscribeRuntimeState(context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
	updates := make(chan domain.RuntimeActualStateSnapshot, len(r.states))
	for _, state := range r.states {
		updates <- state
	}
	close(updates)
	return updates, nil
}

func TestRuntimeComponentLauncherHandoffProvesAndSwapsRuntime(t *testing.T) {
	old := &handoffRuntime{}
	component := ComponentLaunchComponent{Role: domain.ComponentRoleRuntime, ComponentID: "gordon-runtime-fixture-g1", Image: "example.invalid/gordon:v2", InternalNetwork: "gordon-internal-fixture-g1", Labels: map[string]string{domain.LabelComponentVersion: "v2", domain.LabelComponentGeneration: "1", domain.LabelComponentMigrationID: "fixture"}}
	newRuntime := &handoffRuntime{probe: out.RuntimeEnvironment{APIReachable: true, Rootless: true}, states: []domain.RuntimeActualStateSnapshot{{SourceComponentID: component.ComponentID, Containers: []domain.RuntimeContainerState{{Name: component.ComponentID, Status: domain.ContainerStatusRunning, Generation: 1, Labels: map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: "runtime", domain.LabelComponentGeneration: "1"}}}}}}
	launcher, err := NewRuntimeComponentLauncherWithHandoff(old, func(context.Context, ComponentLaunchComponent) (RuntimeHandoffClient, error) { return newRuntime, nil })
	require.NoError(t, err)
	require.NoError(t, launcher.TransferRuntimeCommandChannel(context.Background(), component))
	require.Len(t, old.commands, 1)
	assert.Equal(t, domain.RuntimeComponentLifecycleTransferChannel, old.commands[0].LifecycleAction)
	require.NoError(t, launcher.StartComponent(context.Background(), ComponentLaunchComponent{Role: domain.ComponentRoleRegistry, ComponentID: "gordon-registry-fixture-g1", Image: "example.invalid/gordon:v2", InternalNetwork: component.InternalNetwork, Labels: map[string]string{domain.LabelComponentVersion: "v2", domain.LabelComponentGeneration: "1", domain.LabelComponentMigrationID: "fixture"}}))
	assert.Len(t, newRuntime.commands, 1, "post-proof component operations must use the new runtime")
}

func TestProveRuntimeHandoffRetriesTransientRuntimeStartup(t *testing.T) {
	component := ComponentLaunchComponent{Role: domain.ComponentRoleRuntime, ComponentID: "gordon-runtime-fixture-g1", Labels: map[string]string{domain.LabelComponentVersion: "v2", domain.LabelComponentGeneration: "1", domain.LabelComponentMigrationID: "fixture"}}
	target := &handoffRuntime{probe: out.RuntimeEnvironment{APIReachable: true, Rootless: true}, probeErrors: []error{errors.New("connection refused")}, states: []domain.RuntimeActualStateSnapshot{{SourceComponentID: component.ComponentID, Containers: []domain.RuntimeContainerState{{Name: component.ComponentID, Status: domain.ContainerStatusRunning, Generation: 1, Labels: map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: "runtime", domain.LabelComponentGeneration: "1"}}}}}}

	require.NoError(t, proveRuntimeHandoff(t.Context(), target, component))
	assert.Equal(t, 2, target.probeCalls)
}

func TestRuntimeComponentLauncherHandoffDoesNotSwapWithoutTargetProof(t *testing.T) {
	old := &handoffRuntime{}
	component := ComponentLaunchComponent{Role: domain.ComponentRoleRuntime, ComponentID: "gordon-runtime-fixture-g1", Image: "example.invalid/gordon:v2", InternalNetwork: "gordon-internal-fixture-g1", Labels: map[string]string{domain.LabelComponentVersion: "v2", domain.LabelComponentGeneration: "1", domain.LabelComponentMigrationID: "fixture"}}
	newRuntime := &handoffRuntime{probe: out.RuntimeEnvironment{APIReachable: true, Rootless: true}, pingErr: errors.New("unreachable")}
	launcher, err := NewRuntimeComponentLauncherWithHandoff(old, func(context.Context, ComponentLaunchComponent) (RuntimeHandoffClient, error) { return newRuntime, nil })
	require.NoError(t, err)
	require.Error(t, launcher.TransferRuntimeCommandChannel(context.Background(), component))
	require.NoError(t, launcher.StopComponent(context.Background(), component))
	assert.Len(t, old.commands, 2, "failure must retain old authority")
}

func TestVerifyHandoffSnapshotRejectsWrongRuntimeIdentity(t *testing.T) {
	component := ComponentLaunchComponent{Role: domain.ComponentRoleRuntime, ComponentID: "gordon-runtime-fixture-g1", Labels: map[string]string{domain.LabelComponentGeneration: "1"}}
	err := verifyRuntimeHandoffSnapshot(component, domain.RuntimeActualStateSnapshot{SourceComponentID: "other-runtime"})
	require.Error(t, err)
	assert.False(t, errors.Is(err, context.DeadlineExceeded))
	_ = time.Second
}
