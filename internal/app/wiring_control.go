package app

import "context"

// runControlImpl owns future control-plane-only wiring.
func runControlImpl(ctx context.Context, configPath string) error {
	return newRoleNotImplementedError(RoleControl)
}
