package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

func TestRuntimeSelfUpdateCommandCarriesLifecycleProfile(t *testing.T) {
	field, ok := reflect.TypeFor[domain.RuntimeSelfUpdateCommand]().FieldByName("LifecycleProfile")
	require.True(t, ok, "component lifecycle commands need an explicit authenticated runtime profile")
	assert.Equal(t, reflect.TypeFor[domain.RuntimeComponentLifecycleProfile](), field.Type)
}

func TestComponentLifecycleReadCommandCarriesOnlyFixedProcessIdentity(t *testing.T) {
	component := ComponentLaunchComponent{
		Role: domain.ComponentRoleControl, ComponentID: "gordon-control-fixture-g1", Image: "example.invalid/gordon:v2",
		InternalNetwork: "gordon-internal-fixture-g1", EnvironmentFile: "/private/control.env", ConfigFile: "/private/control.toml",
		DesiredStateHash: "state-hash", PortPublishes: []domain.ContainerPortPublish{{HostIP: "127.0.0.1", HostPort: 18080, ContainerPort: 8080, Protocol: domain.NetworkProtocolTCP}},
		Labels: map[string]string{domain.LabelComponentVersion: "v2", domain.LabelComponentGeneration: "1", domain.LabelComponentMigrationID: "fixture"},
	}
	for _, action := range []domain.RuntimeComponentLifecycleAction{domain.RuntimeComponentLifecycleHealth, domain.RuntimeComponentLifecycleLogs} {
		command, err := newComponentLifecycleCommand(component, action, "fixture", string(action), time.Unix(1, 0).UTC())
		require.NoError(t, err)
		require.NoError(t, command.Validate())
		expectedIdentity, ok := domain.FixedComponentProcessIdentity(component.Role)
		require.True(t, ok)
		assert.Equal(t, expectedIdentity, command.LifecycleProfile.ProcessIdentity)
		assert.Empty(t, command.LifecycleProfile.UsernsMode)
		assert.Empty(t, command.LifecycleProfile.CapDrop)
		assert.False(t, command.LifecycleProfile.NoNewPrivileges)
		assert.Empty(t, command.LifecycleProfile.GenerationVolumeOptions)
		assert.Empty(t, command.TargetVersion)
		assert.Empty(t, command.Policy)
		assert.Empty(t, command.DesiredImage)
		assert.Empty(t, command.DesiredStateHash)
		assert.Empty(t, command.InternalNetwork)
		assert.Empty(t, command.EnvironmentFile)
		assert.Empty(t, command.ConfigFile)
		assert.Empty(t, command.PortPublishes)
		assert.False(t, command.PreserveVolumes)

		command.LifecycleProfile.ProcessIdentity = domain.ComponentProcessIdentity{}
		require.ErrorIs(t, command.Validate(), domain.ErrInvalidRuntimeCommand)
	}
}

func TestComponentLifecycleMutationCommandUsesExactFixedRoleProfile(t *testing.T) {
	tests := []struct {
		name   string
		role   domain.ComponentRole
		action domain.RuntimeComponentLifecycleAction
	}{
		{name: "replace", role: domain.ComponentRoleControl, action: domain.RuntimeComponentLifecycleReplace},
		{name: "start", role: domain.ComponentRoleRuntime, action: domain.RuntimeComponentLifecycleStart},
		{name: "stop", role: domain.ComponentRoleRegistry, action: domain.RuntimeComponentLifecycleStop},
		{name: "connect", role: domain.ComponentRoleEdge, action: domain.RuntimeComponentLifecycleConnect},
		{name: "remove", role: domain.ComponentRoleRegistry, action: domain.RuntimeComponentLifecycleRemove},
		{name: "transfer", role: domain.ComponentRoleRuntime, action: domain.RuntimeComponentLifecycleTransferChannel},
		{name: "activate", role: domain.ComponentRoleEdge, action: domain.RuntimeComponentLifecycleActivate},
		{name: "drain", role: domain.ComponentRoleEdge, action: domain.RuntimeComponentLifecycleDrain},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			component := ComponentLaunchComponent{
				Role: test.role, ComponentID: "gordon-" + string(test.role) + "-fixture-g1",
				Labels: map[string]string{domain.LabelComponentVersion: "v2", domain.LabelComponentGeneration: "1", domain.LabelComponentMigrationID: "fixture"},
			}
			command, err := newComponentLifecycleCommand(component, test.action, "fixture", test.name, time.Unix(1, 0).UTC())
			require.NoError(t, err)
			expected, ok := domain.FixedRuntimeComponentLifecycleProfile(test.role)
			require.True(t, ok)
			assert.Equal(t, expected, command.LifecycleProfile)
		})
	}
}

type startupBudgetHandoffRuntime struct {
	handoffRuntime
	proofDeadline    time.Time
	proofHasDeadline bool
}

func (r *startupBudgetHandoffRuntime) ProbeRuntimeEnvironment(ctx context.Context) (out.RuntimeEnvironment, error) {
	r.proofDeadline, r.proofHasDeadline = ctx.Deadline()
	return r.handoffRuntime.ProbeRuntimeEnvironment(ctx)
}

func TestRuntimeHandoffStartupBudgetCoversFactoryAndProtocolProof(t *testing.T) {
	component := ComponentLaunchComponent{
		Role:        domain.ComponentRoleRuntime,
		ComponentID: "gordon-runtime-fixture-g1",
		Labels: map[string]string{
			domain.LabelComponentVersion:     "v2",
			domain.LabelComponentGeneration:  "1",
			domain.LabelComponentMigrationID: "fixture",
		},
	}
	state := domain.RuntimeActualStateSnapshot{
		SourceComponentID: component.ComponentID,
		Containers: []domain.RuntimeContainerState{{
			Name:   component.ComponentID,
			Status: domain.ContainerStatusRunning,
			Labels: map[string]string{
				domain.LabelComponent:           "true",
				domain.LabelComponentRole:       string(domain.ComponentRoleRuntime),
				domain.LabelComponentGeneration: "1",
			},
		}},
	}
	target := &startupBudgetHandoffRuntime{handoffRuntime: handoffRuntime{
		probe:  out.RuntimeEnvironment{APIReachable: true, Rootless: true},
		states: []domain.RuntimeActualStateSnapshot{state},
	}}
	var factoryDeadline time.Time
	var factoryHasDeadline bool
	launcher, err := NewRuntimeComponentLauncherWithHandoff(&handoffRuntime{}, func(ctx context.Context, _ ComponentLaunchComponent) (RuntimeHandoffClient, error) {
		factoryDeadline, factoryHasDeadline = ctx.Deadline()
		return target, nil
	})
	require.NoError(t, err)

	require.NoError(t, launcher.TransferRuntimeCommandChannel(context.Background(), component))
	require.True(t, factoryHasDeadline, "handoff target creation must receive the startup deadline")
	require.True(t, target.proofHasDeadline, "protocol proof must receive the startup deadline")
	assert.Equal(t, factoryDeadline, target.proofDeadline, "factory readiness and protocol proof must share one budget")
}

func TestComponentLaunchPlanRejectsRoleSwappedGeneratedReferences(t *testing.T) {
	checkpoint := MigrationCheckpoint{
		MigrationID: "fixture", ComponentGeneration: 1, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2",
		ConfigFileReferences: []string{
			"/private/migration/config/fixture/1/control.toml",
			"/private/migration/config/fixture/1/runtime.toml",
			"/private/migration/config/fixture/1/registry.toml",
			"/private/migration/config/other/1/edge.toml",
		},
	}
	_, err := NewComponentLaunchPlan(checkpoint)
	require.Error(t, err)
}

func TestComponentLaunchPlanRejectsNonCanonicalGeneratedReferences(t *testing.T) {
	for name, references := range map[string][]string{
		"config": {
			"/private/migration/config/fixture/1/../1/control.toml",
			"/private/migration/config/fixture/1/runtime.toml",
			"/private/migration/config/fixture/1/registry.toml",
			"/private/migration/config/fixture/1/edge.toml",
		},
		"env": {
			"/private/migration/env/fixture/1/../1/control.env",
			"/private/migration/env/fixture/1/runtime.env",
			"/private/migration/env/fixture/1/registry.env",
			"/private/migration/env/fixture/1/edge.env",
		},
	} {
		t.Run(name, func(t *testing.T) {
			checkpoint := MigrationCheckpoint{MigrationID: "fixture", ComponentGeneration: 1, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2"}
			if name == "config" {
				checkpoint.ConfigFileReferences = references
			} else {
				checkpoint.EnvFileReferences = references
			}
			_, err := NewComponentLaunchPlan(checkpoint)
			require.Error(t, err)
		})
	}
}

func TestComponentRoleLaunchHashIncludesExactRuntimeProfile(t *testing.T) {
	profile, ok := domain.FixedRuntimeComponentLifecycleProfile(domain.ComponentRoleRuntime)
	require.True(t, ok)
	const componentID = "gordon-runtime-fixture-g1"
	const image = "example.invalid/gordon:v2"
	const network = "gordon-internal-fixture-g1"
	sum := sha256.Sum256([]byte(componentID + "\x00" + image + "\x00" + network + "\x00" + profile.ProcessIdentity.User + "\x00" + profile.UsernsMode + "\x00ALL\x00true\x00" + domain.ContainerVolumeOptionChown))

	assert.Equal(t, hex.EncodeToString(sum[:]), componentRoleLaunchHash(componentID, image, network, profile))
}

func TestComponentLauncherPlanIsOrderedAndNoCutover(t *testing.T) {
	plan, err := NewComponentLaunchPlan(MigrationCheckpoint{MigrationID: "fixture-migration", ComponentGeneration: 2, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2"})
	require.NoError(t, err)
	assert.Equal(t, "gordon-internal-fixture-migration-g2", plan.InternalNetwork)
	assert.Equal(t, []domain.ComponentRole{domain.ComponentRoleControl, domain.ComponentRoleRuntime, domain.ComponentRoleRegistry, domain.ComponentRoleEdge}, plan.Roles())
	assert.False(t, plan.PublicCutover)
	for _, component := range plan.Components {
		assert.NotEmpty(t, component.Labels[domain.LabelComponentDesiredStateHash])
		assert.NotEmpty(t, component.ComponentID)
		profile, ok := domain.FixedRuntimeComponentLifecycleProfile(component.Role)
		require.True(t, ok)
		assert.Equal(t, componentRoleLaunchHash(component.ComponentID, component.Image, component.InternalNetwork, profile), component.DesiredStateHash)
	}
}
