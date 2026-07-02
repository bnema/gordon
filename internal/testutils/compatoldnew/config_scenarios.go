package compatoldnew

// ConfigScenarios returns Phase 5 config compatibility scenario shells.
func ConfigScenarios() []Scenario {
	return []Scenario{
		configScenario("config/minimal-load"),
		configScenario("config/realistic-load"),
		configScenario("config/legacy-registry-domain-keys"),
		configScenario("config/invalid-error"),
		configScenario("config/env-override-precedence"),
		configScenario("config/server-settings"),
		configScenario("config/auth-settings"),
		configScenario("config/api-rate-limits"),
		configScenario("config/deploy-settings"),
		configScenario("config/network-isolation"),
		configScenario("config/volume-settings"),
		configScenario("config/auto-route-preview"),
		configScenario("config/routes-save-load"),
		configScenario("config/external-routes"),
		configScenario("config/attachments"),
		configScenario("config/public-tls-acme"),
		configScenario("config/dns-settings"),
		configScenario("config/entrypoints-traffic-graph"),
		configScenario("config/standalone-network-services"),
		configScenario("config/logging"),
		configScenario("config/telemetry"),
		configScenario("config/backups"),
		configScenario("config/images"),
		configScenario("config/container-security-defaults"),
		configScenario("config/reload-preserves-critical-fields"),
		configScenario("config/backup-canonical-save"),
	}
}

func configScenario(name string) Scenario {
	return pendingScenario(name, SurfaceConfig, "6.1 Config compatibility", false, "old/new config compatibility scenario execution is not implemented yet")
}
