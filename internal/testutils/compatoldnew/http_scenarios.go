package compatoldnew

// HTTPScenarios returns Phase 5 HTTP/admin API compatibility scenario shells.
func HTTPScenarios() []Scenario {
	return []Scenario{
		httpScenario("api/auth-missing-invalid"),
		httpScenario("api/route-list-detail"),
		httpScenario("api/route-add-update-remove"),
		httpScenario("api/reload"),
		httpScenario("api/health-status"),
		httpScenario("api/request-too-large"),
		httpScenario("api/forbidden-permission"),
	}
}

func httpScenario(name string) Scenario {
	return pendingScenario(name, SurfaceAPI, "6.3 Admin API compatibility", false, "old/new admin API compatibility scenario execution is not implemented yet")
}
