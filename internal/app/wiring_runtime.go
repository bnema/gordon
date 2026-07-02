package app

import "context"

// runRuntimeImpl owns future runtime-worker-only wiring.
func runRuntimeImpl(ctx context.Context, configPath string) error {
	return newRoleNotImplementedError(RoleRuntime)
}
