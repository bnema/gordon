package compatoldnew

// SecurityScenarios returns Phase 5 security compatibility scenario shells.
func SecurityScenarios() []Scenario {
	return []Scenario{
		securityScenario("security/edge-no-podman-socket", true),
		securityScenario("security/registry-no-podman-socket", true),
		securityScenario("security/control-no-podman-socket-after-split", true),
		securityScenario("security/missing-component-token-rejected", false),
		securityScenario("security/wrong-scope-component-token-rejected", false),
		securityScenario("security/unsafe-runtime-request-denied", false),
	}
}

func securityScenario(name string, podmanRequired bool) Scenario {
	return pendingScenario(name, SurfaceSecurity, "10.2 Runtime isolation checks", podmanRequired, "old/new security compatibility scenario execution is not implemented yet")
}
