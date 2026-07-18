package compatoldnew

// MigrationScenarios is release-blocking: the deterministic protocol fixture
// runs locally and the same lifecycle is exercised against rootless Podman
// whenever GORDON_COMPAT_PODMAN=1. No migration scenario is a pending shell.
func MigrationScenarios() []Scenario {
	return []Scenario{
		implementedScenario("migration/monolith-existing-deployment-inventory", SurfaceMigration, "9.3 Preflight requirements", true),
		implementedScenario("migration/monolith-to-split-preflight", SurfaceMigration, "9.3 Preflight requirements", true),
		implementedScenario("migration/component-startup-health", SurfaceMigration, "9.6 Migration phases", true),
		implementedScenario("migration/no-unsafe-traffic-switch", SurfaceMigration, "9.2 Migration posture", true),
		implementedScenario("migration/env-transfer", SurfaceMigration, "9.4 Host env vars to container env vars", true),
		implementedScenario("migration/interrupted-retry", SurfaceMigration, "9.7 Idempotence and generations", true),
	}
}
