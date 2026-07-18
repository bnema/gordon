package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestComponentAuthScopes(t *testing.T) {
	assert.Equal(t, []ComponentScope{
		"routes:watch",
		"traffic:watch",
		"edge:drain",
		"runtime:deploy",
		"runtime:reconcile",
		"runtime:logs",
		"runtime:status",
		"runtime:state:publish",
		"runtime:event:publish",
		"runtime:selfupdate",
		"runtime:drain:ack",
		"registry:event:publish",
		"registry:status",
		"registry:inspect",
	}, AllComponentScopes())

	assert.Equal(t, []ComponentScope{
		ComponentScopeRuntimeDeploy,
		ComponentScopeRuntimeReconcile,
		ComponentScopeRuntimeLogs,
		ComponentScopeRuntimeStatus,
		ComponentScopeRuntimeSelfUpdate,
		ComponentScopeRuntimeDrainAck,
		ComponentScopeRegistryInspect,
	}, DefaultComponentScopesForRole(ComponentRoleControl))

	assert.Equal(t, []ComponentScope{
		ComponentScopeRuntimeStatePublish,
		ComponentScopeRuntimeEventPublish,
	}, DefaultComponentScopesForRole(ComponentRoleRuntime))

	assert.Equal(t, []ComponentScope{
		ComponentScopeRoutesWatch,
		ComponentScopeTrafficWatch,
		ComponentScopeEdgeDrain,
	}, DefaultComponentScopesForRole(ComponentRoleEdge))

	assert.Equal(t, []ComponentScope{
		ComponentScopeRegistryEventPublish,
		ComponentScopeRegistryStatus,
	}, DefaultComponentScopesForRole(ComponentRoleRegistry))

	assert.Nil(t, DefaultComponentScopesForRole(ComponentRole("unknown")))
}

func TestComponentAuthRoleAndScopeValidation(t *testing.T) {
	assert.True(t, IsKnownComponentRole(ComponentRoleRuntime))
	assert.False(t, IsKnownComponentRole(ComponentRole("unknown")))

	assert.True(t, IsKnownComponentScope(ComponentScopeRuntimeDeploy))
	assert.False(t, IsKnownComponentScope(ComponentScope("runtime:unknown")))

	assert.True(t, ComponentRoleAllowsScope(ComponentRoleEdge, ComponentScopeRoutesWatch))
	assert.False(t, ComponentRoleAllowsScope(ComponentRoleEdge, ComponentScopeRuntimeDeploy))
	assert.True(t, ComponentRoleAllowsScope(ComponentRoleControl, ComponentScopeRegistryInspect))
}
