package compatoldnew

// RuntimeScenarios returns Phase 5 runtime compatibility scenario shells.
func RuntimeScenarios() []Scenario {
	return []Scenario{
		runtimeScenario("runtime/deploy-image-proxy-port-label"),
		runtimeScenario("runtime/exposed-port-fallback"),
		runtimeScenario("runtime/env-file"),
		runtimeScenario("runtime/secrets"),
		runtimeScenario("runtime/volume"),
		runtimeScenario("runtime/attachment"),
		runtimeScenario("runtime/network-group"),
		runtimeScenario("runtime/readiness-failure"),
		runtimeScenario("runtime/route-removal-cleanup"),
		runtimeScenario("runtime/startup-recovery"),
	}
}

func runtimeScenario(name string) Scenario {
	return pendingScenario(name, SurfaceRuntime, "6.6 Deploy/runtime behavior compatibility", true, "old/new runtime compatibility scenario execution requires real Podman-backed Gordon instances")
}
