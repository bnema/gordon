package app

import (
	"errors"
	"fmt"
	"strings"
)

// Role identifies the Gordon startup role.
type Role string

const (
	RoleMonolith Role = "monolith"
	RoleControl  Role = "control"
	RoleRuntime  Role = "runtime"
	RoleEdge     Role = "edge"
	RoleRegistry Role = "registry"
)

// ErrRoleNotImplemented is returned for parsed roles whose service graph is not wired yet.
var ErrRoleNotImplemented = errors.New("role not implemented")

// ErrRoleRuntimeOwnership is returned when a role tries to instantiate a runtime adapter it does not own.
var ErrRoleRuntimeOwnership = errors.New("role does not own runtime adapter")

// RoleNotImplementedError reports a parsed role whose service graph is not wired yet.
type RoleNotImplementedError struct {
	Role Role
}

func (e RoleNotImplementedError) Error() string {
	return fmt.Sprintf("role %q not implemented", e.Role)
}

func (e RoleNotImplementedError) Unwrap() error {
	return ErrRoleNotImplemented
}

func newRoleNotImplementedError(role Role) error {
	return RoleNotImplementedError{Role: role}
}

func roleMayInstantiateRuntimeAdapter(role Role) bool {
	switch role {
	case RoleMonolith, RoleRuntime:
		return true
	case RoleControl, RoleEdge, RoleRegistry:
		return false
	default:
		return false
	}
}

const acceptedRoleValues = "monolith, control, runtime, edge, registry"

// ParseRole parses a role value. Empty defaults to monolith.
func ParseRole(value string) (Role, error) {
	role := Role(strings.ToLower(strings.TrimSpace(value)))
	if role == "" {
		return RoleMonolith, nil
	}

	switch role {
	case RoleMonolith, RoleControl, RoleRuntime, RoleEdge, RoleRegistry:
		return role, nil
	default:
		return "", fmt.Errorf("invalid role %q; accepted values: %s", value, acceptedRoleValues)
	}
}

// ResolveRole applies flag/env precedence: explicit flag, then env, then monolith.
func ResolveRole(flagValue, envValue string) (Role, error) {
	if strings.TrimSpace(flagValue) != "" {
		return ParseRole(flagValue)
	}
	return ParseRole(envValue)
}
