package compatoldnew

// MigrationScenarios defines the required migration contract before a real
// rootless-Podman runner exists. Every scenario remains explicitly pending so
// selecting it fails rather than reporting a false success.
func MigrationScenarios() []Scenario {
	return []Scenario{
		pendingScenario(
			"migration/monolith-existing-deployment-inventory",
			SurfaceMigration,
			"9.3 Preflight requirements",
			true,
			"requires a rootless Podman fixture that inventories a pre-existing monolith deployment",
		),
		pendingScenario(
			"migration/monolith-to-split-preflight",
			SurfaceMigration,
			"9.3 Preflight requirements",
			true,
			"requires a rootless Podman fixture and migration preflight implementation",
		),
		pendingScenario(
			"migration/component-startup-health",
			SurfaceMigration,
			"9.6 Migration phases",
			true,
			"requires a rootless Podman fixture and startable split components",
		),
		pendingScenario(
			"migration/no-unsafe-traffic-switch",
			SurfaceMigration,
			"9.2 Migration posture",
			true,
			"requires a rootless Podman fixture and safe traffic-switch implementation",
		),
		pendingScenario(
			"migration/env-transfer",
			SurfaceMigration,
			"9.4 Host env vars to container env vars",
			true,
			"requires a rootless Podman fixture and explicit redacted component env transfer",
		),
		pendingScenario(
			"migration/interrupted-retry",
			SurfaceMigration,
			"9.7 Idempotence and generations",
			true,
			"requires a rootless Podman fixture and checkpointed retry implementation",
		),
	}
}
