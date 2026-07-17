package compatoldnew

// SecurityScenarios returns executable current-security gates and explicitly
// pending split-runtime shells. These gates validate the candidate contract;
// they intentionally do not compare a pre-split baseline with newer security.
func SecurityScenarios() []Scenario {
	return []Scenario{
		implementedScenario("security/edge-no-podman-socket", SurfaceSecurity, "10.2 Runtime isolation checks", false),
		securityScenario("security/registry-no-podman-socket", true),
		securityScenario("security/control-no-podman-socket-after-split", true),
		implementedScenario("security/missing-component-token-rejected", SurfaceSecurity, "10.2 Runtime isolation checks", false),
		implementedScenario("security/wrong-component-token-rejected", SurfaceSecurity, "10.2 Runtime isolation checks", false),
		implementedScenario("security/wrong-scope-component-token-rejected", SurfaceSecurity, "10.2 Runtime isolation checks", false),
		securityScenario("security/unsafe-runtime-request-denied", false),
	}
}

func securityScenario(name string, podmanRequired bool) Scenario {
	return pendingScenario(name, SurfaceSecurity, "10.2 Runtime isolation checks", podmanRequired, "old/new security compatibility scenario execution is not implemented yet")
}
