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
