package compatoldnew

import "fmt"

// ScenarioStatus describes whether a compatibility scenario has executable
// old/new coverage or is still a definition-only shell.
type ScenarioStatus string

const (
	// ScenarioStatusPending is the fail-safe default for unimplemented coverage.
	ScenarioStatusPending ScenarioStatus = "pending"
	// ScenarioStatusImplemented marks a scenario that may be selected for execution.
	ScenarioStatusImplemented ScenarioStatus = "implemented"
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
	if s.Status != ScenarioStatusImplemented {
		reason := s.BlockReason
		if reason == "" {
			reason = "scenario is not implemented"
		}
		if s.PodmanRequired && !PodmanEnabledFromEnv() {
			return reason + "; scenario also requires Podman; set " + EnvCompatPodman + "=1 to opt in", true
		}
		return reason, true
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

// SelectImplementedScenarios selects requested executable scenarios. An empty
// selection means every implemented scenario; pending and unknown names fail
// rather than silently disappearing from a requested test run.
func SelectImplementedScenarios(scenarios []Scenario, names []string) ([]Scenario, error) {
	byName := make(map[string]Scenario, len(scenarios))
	for _, scenario := range scenarios {
		byName[scenario.Name] = scenario
	}
	if len(names) == 0 {
		selected := make([]Scenario, 0, len(scenarios))
		for _, scenario := range scenarios {
			if scenario.Status == ScenarioStatusImplemented {
				selected = append(selected, scenario)
			}
		}
		return selected, nil
	}
	selected := make([]Scenario, 0, len(names))
	for _, name := range names {
		scenario, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("compatibility scenario %q is not defined", name)
		}
		if scenario.Status != ScenarioStatusImplemented {
			return nil, fmt.Errorf("compatibility scenario %q is not implemented: %s", name, scenario.BlockReason)
		}
		selected = append(selected, scenario)
	}
	return selected, nil
}

func implementedScenario(name string, surface Surface, specSection string, podmanRequired bool) Scenario {
	return Scenario{Name: name, Surface: surface, SpecSection: specSection, PodmanRequired: podmanRequired, Status: ScenarioStatusImplemented}
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
