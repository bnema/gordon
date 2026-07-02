package compatoldnew

// RegistryScenarios returns Phase 5 registry compatibility scenario shells.
func RegistryScenarios() []Scenario {
	return []Scenario{
		registryScenario("registry/v2-ping"),
		registryScenario("registry/auth-challenge"),
		registryScenario("registry/push-image"),
		registryScenario("registry/pull-image"),
		registryScenario("registry/tag-list"),
		registryScenario("registry/upload-too-large"),
		registryScenario("registry/invalid-name-reference"),
		registryScenario("registry/image-push-event"),
	}
}

func registryScenario(name string) Scenario {
	return pendingScenario(name, SurfaceRegistry, "6.4 Registry compatibility", true, "old/new registry compatibility scenario execution requires real Podman-backed Gordon instances")
}
