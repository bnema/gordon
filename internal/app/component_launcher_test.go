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

	"github.com/bnema/gordon/internal/domain"
)

func TestRuntimeSelfUpdateCommandCarriesLifecycleProfile(t *testing.T) {
	field, ok := reflect.TypeFor[domain.RuntimeSelfUpdateCommand]().FieldByName("LifecycleProfile")
	require.True(t, ok, "component lifecycle commands need an explicit authenticated runtime profile")
	assert.Equal(t, reflect.TypeFor[domain.RuntimeComponentLifecycleProfile](), field.Type)
}

func TestComponentLifecycleCommandValidationRejectsMissingProfile(t *testing.T) {
	component := ComponentLaunchComponent{
		Role: domain.ComponentRoleControl, ComponentID: "gordon-control-fixture-g1",
		Labels: map[string]string{domain.LabelComponentVersion: "v2", domain.LabelComponentGeneration: "1", domain.LabelComponentMigrationID: "fixture"},
	}
	command, err := newComponentLifecycleCommand(component, domain.RuntimeComponentLifecycleHealth, "fixture", "health", time.Unix(1, 0).UTC())
	require.NoError(t, err)
	require.NoError(t, command.Validate())
	command.LifecycleProfile = domain.RuntimeComponentLifecycleProfile{}
	require.ErrorIs(t, command.Validate(), domain.ErrInvalidRuntimeCommand)
}

func TestComponentLifecycleCommandUsesExactFixedRoleProfileForEveryAction(t *testing.T) {
	tests := []struct {
		name   string
		role   domain.ComponentRole
		action domain.RuntimeComponentLifecycleAction
	}{
		{name: "replace", role: domain.ComponentRoleControl, action: domain.RuntimeComponentLifecycleReplace},
		{name: "start", role: domain.ComponentRoleRuntime, action: domain.RuntimeComponentLifecycleStart},
		{name: "stop", role: domain.ComponentRoleRegistry, action: domain.RuntimeComponentLifecycleStop},
		{name: "health", role: domain.ComponentRoleEdge, action: domain.RuntimeComponentLifecycleHealth},
		{name: "logs", role: domain.ComponentRoleControl, action: domain.RuntimeComponentLifecycleLogs},
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

type recordingComponentLauncher struct{ calls []string }

func (l *recordingComponentLauncher) CreateInternalNetwork(context.Context, ComponentLaunchPlan) error {
	l.calls = append(l.calls, "network")
	return nil
}
func (l *recordingComponentLauncher) StartComponent(_ context.Context, component ComponentLaunchComponent) error {
	l.calls = append(l.calls, "start:"+string(component.Role))
	return nil
}
func (l *recordingComponentLauncher) StopComponent(_ context.Context, component ComponentLaunchComponent) error {
	l.calls = append(l.calls, "stop:"+string(component.Role))
	return nil
}
func (l *recordingComponentLauncher) CheckComponentHealth(_ context.Context, component ComponentLaunchComponent) error {
	l.calls = append(l.calls, "health:"+string(component.Role))
	return nil
}
func (l *recordingComponentLauncher) ConnectEdgeToAppNetwork(_ context.Context, component ComponentLaunchComponent, network string) error {
	l.calls = append(l.calls, "connect:"+network)
	return nil
}
func (l *recordingComponentLauncher) RemovePreparedComponent(_ context.Context, component ComponentLaunchComponent) error {
	l.calls = append(l.calls, "remove:"+string(component.Role))
	return nil
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
