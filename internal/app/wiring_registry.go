package app

import "context"

// runRegistryImpl owns future registry-only wiring.
func runRegistryImpl(ctx context.Context, configPath string) error {
	return newRoleNotImplementedError(RoleRegistry)
}
