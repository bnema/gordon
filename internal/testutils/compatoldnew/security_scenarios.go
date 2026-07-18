package compatoldnew

// SecurityScenarios returns executable current-security gates and explicitly
// pending split-runtime shells. These gates validate the candidate contract;
// they intentionally do not compare a pre-split baseline with newer security.
func SecurityScenarios() []Scenario {
	return []Scenario{
		implementedScenario("security/edge-no-podman-socket", SurfaceSecurity, "10.2 Runtime isolation checks", false),
		implementedScenario("security/registry-no-podman-socket", SurfaceSecurity, "10.2 Runtime isolation checks", false),
		implementedScenario("security/control-no-podman-socket-after-split", SurfaceSecurity, "10.2 Runtime isolation checks", false),
		implementedScenario("security/missing-component-token-rejected", SurfaceSecurity, "10.2 Runtime isolation checks", false),
		implementedScenario("security/wrong-component-token-rejected", SurfaceSecurity, "10.2 Runtime isolation checks", false),
		implementedScenario("security/wrong-scope-component-token-rejected", SurfaceSecurity, "10.2 Runtime isolation checks", false),
		implementedScenario("security/unsafe-runtime-request-denied", SurfaceSecurity, "10.2 Runtime isolation checks", false),
	}
}
