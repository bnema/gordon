package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

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
func (l *recordingComponentLauncher) ReadComponentLogs(context.Context, ComponentLaunchComponent) (string, error) {
	return "", nil
}
func (l *recordingComponentLauncher) ConnectEdgeToAppNetwork(_ context.Context, component ComponentLaunchComponent, network string) error {
	l.calls = append(l.calls, "connect:"+network)
	return nil
}
func (l *recordingComponentLauncher) RemovePreparedComponent(_ context.Context, component ComponentLaunchComponent) error {
	l.calls = append(l.calls, "remove:"+string(component.Role))
	return nil
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
	}
}
