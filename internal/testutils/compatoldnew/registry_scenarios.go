package compatoldnew

// RegistryScenarios returns real old/new OCI distribution compatibility gates.
// They use Docker only to provide Gordon's runtime dependency; registry traffic
// itself is raw, specification-correct HTTP against sequential local binaries.
func RegistryScenarios() []Scenario {
	return []Scenario{
		implementedScenario("registry/v2-ping", SurfaceRegistry, "6.4 Registry compatibility", false),
		implementedScenario("registry/auth-challenge", SurfaceRegistry, "6.4 Registry compatibility", false),
		implementedScenario("registry/push-image", SurfaceRegistry, "6.4 Registry compatibility", false),
		implementedScenario("registry/pull-image", SurfaceRegistry, "6.4 Registry compatibility", false),
		implementedScenario("registry/tag-list", SurfaceRegistry, "6.4 Registry compatibility", false),
		implementedScenario("registry/upload-too-large", SurfaceRegistry, "6.4 Registry compatibility", false),
		implementedScenario("registry/invalid-name-reference", SurfaceRegistry, "6.4 Registry compatibility", false),
		implementedScenario("registry/image-push-event", SurfaceRegistry, "6.4 Registry compatibility", false),
	}
}
