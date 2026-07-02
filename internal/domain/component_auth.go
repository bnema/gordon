package domain

import (
	"slices"
	"time"
)

// ComponentRole identifies a Gordon component class that can authenticate to the control plane.
type ComponentRole string

const (
	ComponentRoleControl  ComponentRole = "control"
	ComponentRoleRuntime  ComponentRole = "runtime"
	ComponentRoleEdge     ComponentRole = "edge"
	ComponentRoleRegistry ComponentRole = "registry"
)

// ComponentScope identifies a component RPC permission.
type ComponentScope string

const (
	ComponentScopeRoutesWatch          ComponentScope = "routes:watch"
	ComponentScopeEdgeDrain            ComponentScope = "edge:drain"
	ComponentScopeRuntimeDeploy        ComponentScope = "runtime:deploy"
	ComponentScopeRuntimeReconcile     ComponentScope = "runtime:reconcile"
	ComponentScopeRuntimeLogs          ComponentScope = "runtime:logs"
	ComponentScopeRuntimeStatus        ComponentScope = "runtime:status"
	ComponentScopeRuntimeStatePublish  ComponentScope = "runtime:state:publish"
	ComponentScopeRuntimeEventPublish  ComponentScope = "runtime:event:publish"
	ComponentScopeRuntimeSelfUpdate    ComponentScope = "runtime:selfupdate"
	ComponentScopeRuntimeDrainAck      ComponentScope = "runtime:drain:ack"
	ComponentScopeRegistryEventPublish ComponentScope = "registry:event:publish"
	ComponentScopeRegistryStatus       ComponentScope = "registry:status"
	ComponentScopeRegistryInspect      ComponentScope = "registry:inspect"
)

// AllComponentScopes returns every recognized component scope in stable order.
func AllComponentScopes() []ComponentScope {
	return []ComponentScope{
		ComponentScopeRoutesWatch,
		ComponentScopeEdgeDrain,
		ComponentScopeRuntimeDeploy,
		ComponentScopeRuntimeReconcile,
		ComponentScopeRuntimeLogs,
		ComponentScopeRuntimeStatus,
		ComponentScopeRuntimeStatePublish,
		ComponentScopeRuntimeEventPublish,
		ComponentScopeRuntimeSelfUpdate,
		ComponentScopeRuntimeDrainAck,
		ComponentScopeRegistryEventPublish,
		ComponentScopeRegistryStatus,
		ComponentScopeRegistryInspect,
	}
}

// IsKnownComponentRole reports whether role is a recognized Gordon component role.
func IsKnownComponentRole(role ComponentRole) bool {
	return len(DefaultComponentScopesForRole(role)) > 0
}

// IsKnownComponentScope reports whether scope is a recognized component RPC permission.
func IsKnownComponentScope(scope ComponentScope) bool {
	return slices.Contains(AllComponentScopes(), scope)
}

// ComponentRoleAllowsScope reports whether scope may be granted to role.
func ComponentRoleAllowsScope(role ComponentRole, scope ComponentScope) bool {
	return slices.Contains(DefaultComponentScopesForRole(role), scope)
}

// DefaultComponentScopesForRole returns the default permissions for a component role.
func DefaultComponentScopesForRole(role ComponentRole) []ComponentScope {
	switch role {
	case ComponentRoleControl:
		return AllComponentScopes()
	case ComponentRoleRuntime:
		return []ComponentScope{
			ComponentScopeRuntimeDeploy,
			ComponentScopeRuntimeReconcile,
			ComponentScopeRuntimeLogs,
			ComponentScopeRuntimeStatus,
			ComponentScopeRuntimeStatePublish,
			ComponentScopeRuntimeEventPublish,
			ComponentScopeRuntimeSelfUpdate,
			ComponentScopeRuntimeDrainAck,
		}
	case ComponentRoleEdge:
		return []ComponentScope{
			ComponentScopeRoutesWatch,
			ComponentScopeEdgeDrain,
		}
	case ComponentRoleRegistry:
		return []ComponentScope{
			ComponentScopeRegistryEventPublish,
			ComponentScopeRegistryStatus,
			ComponentScopeRegistryInspect,
		}
	default:
		return nil
	}
}

// ComponentTokenRecord is the persisted component token secret and metadata.
// TokenHash stores a hash of the token; plaintext tokens must never be persisted.
type ComponentTokenRecord struct {
	KeyID      string
	Prefix     string
	Name       string
	Role       ComponentRole
	Scopes     []ComponentScope
	TokenHash  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	RevokedAt  time.Time
	LastUsedAt time.Time
}

// ComponentTokenMetadata is safe-to-list token metadata without token material.
type ComponentTokenMetadata struct {
	KeyID      string
	Prefix     string
	Name       string
	Role       ComponentRole
	Scopes     []ComponentScope
	CreatedAt  time.Time
	ExpiresAt  time.Time
	RevokedAt  time.Time
	LastUsedAt time.Time
}

// Metadata returns the non-secret representation of a component token record.
func (r ComponentTokenRecord) Metadata() ComponentTokenMetadata {
	return ComponentTokenMetadata{
		KeyID:      r.KeyID,
		Prefix:     r.Prefix,
		Name:       r.Name,
		Role:       r.Role,
		Scopes:     append([]ComponentScope(nil), r.Scopes...),
		CreatedAt:  r.CreatedAt,
		ExpiresAt:  r.ExpiresAt,
		RevokedAt:  r.RevokedAt,
		LastUsedAt: r.LastUsedAt,
	}
}

// ComponentIdentity is returned after successful component token validation.
type ComponentIdentity struct {
	KeyID  string
	Name   string
	Role   ComponentRole
	Scopes []ComponentScope
}

// ComponentScopesContain reports whether scopes includes required.
func ComponentScopesContain(scopes []ComponentScope, required ComponentScope) bool {
	return slices.Contains(scopes, required)
}
