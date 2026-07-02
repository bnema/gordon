package compatoldnew

// ScenarioStatus describes whether a compatibility scenario has executable
// old/new coverage or is still a definition-only shell.
type ScenarioStatus string

const (
	ScenarioStatusPending ScenarioStatus = "pending"
)

// Scenario describes a planned compatibility scenario for an old/new surface.
type Scenario struct {
	Name           string
	Surface        Surface
	SpecSection    string
	PodmanRequired bool
	Status         ScenarioStatus
	BlockReason    string
}

// SkipReason reports whether this scenario should be skipped in the current environment.
func (s Scenario) SkipReason() (string, bool) {
	if s.Status == ScenarioStatusPending {
		if s.PodmanRequired && !PodmanEnabledFromEnv() {
			return s.BlockReason + "; scenario also requires Podman; set " + EnvCompatPodman + "=1 to opt in", true
		}
		return s.BlockReason, true
	}
	if s.PodmanRequired && !PodmanEnabledFromEnv() {
		return "scenario requires Podman; set " + EnvCompatPodman + "=1 to opt in", true
	}
	return "", false
}

// AllScenarios returns every Phase 5 compatibility scenario shell.
func AllScenarios() []Scenario {
	var scenarios []Scenario
	for _, group := range [][]Scenario{
		ConfigScenarios(),
		CLIScenarios(),
		HTTPScenarios(),
		RegistryScenarios(),
		ProxyScenarios(),
		RuntimeScenarios(),
		MigrationScenarios(),
		SecurityScenarios(),
	} {
		scenarios = append(scenarios, group...)
	}
	return scenarios
}

func pendingScenario(name string, surface Surface, specSection string, podmanRequired bool, reason string) Scenario {
	return Scenario{
		Name:           name,
		Surface:        surface,
		SpecSection:    specSection,
		PodmanRequired: podmanRequired,
		Status:         ScenarioStatusPending,
		BlockReason:    reason,
	}
}
