package app

import "context"

// runEdgeImpl owns future edge-proxy-only wiring.
func runEdgeImpl(ctx context.Context, configPath string) error {
	return newRoleNotImplementedError(RoleEdge)
}
