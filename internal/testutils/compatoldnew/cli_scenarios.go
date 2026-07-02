package compatoldnew

// CLIScenarios returns Phase 5 CLI compatibility scenario shells.
func CLIScenarios() []Scenario {
	return []Scenario{
		cliScenario("cli/config-show-json", false),
		cliScenario("cli/routes-list-json", false),
		cliScenario("cli/routes-add-remove", false),
		cliScenario("cli/status-text", false),
		cliScenario("cli/status-json", false),
		cliScenario("cli/networks-list-json", true),
		cliScenario("cli/logs", true),
	}
}

func cliScenario(name string, podmanRequired bool) Scenario {
	return pendingScenario(name, SurfaceCLI, "6.2 CLI compatibility", podmanRequired, "old/new CLI compatibility scenario execution is not implemented yet")
}
