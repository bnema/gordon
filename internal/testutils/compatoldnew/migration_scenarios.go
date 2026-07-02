package compatoldnew

// MigrationScenarios returns Phase 5 migration compatibility scenario shells.
func MigrationScenarios() []Scenario {
	return []Scenario{
		migrationScenario("migration/monolith-to-split-preflight"),
		migrationScenario("migration/component-startup-health"),
		migrationScenario("migration/no-unsafe-traffic-switch"),
		migrationScenario("migration/env-transfer"),
		migrationScenario("migration/interrupted-retry"),
	}
}

func migrationScenario(name string) Scenario {
	return pendingScenario(name, SurfaceMigration, "9.6 Migration phases", false, "old/new migration compatibility scenario execution is not implemented yet")
}
