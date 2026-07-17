package compatoldnew

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

const routesListJSONScenarioName = "cli/routes-list-json"

// CLIScenarios returns Phase 5 CLI compatibility scenario shells.
func CLIScenarios() []Scenario {
	return []Scenario{
		implementedScenario(configShowJSONScenarioName, SurfaceCLI, "6.2 CLI compatibility", false),
		implementedScenario(routesListJSONScenarioName, SurfaceCLI, "6.2 CLI compatibility", false),
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

// RunRoutesListJSON executes the real routes-list compatibility slice. It
// builds the baseline in a detached worktree and the candidate from the
// caller's worktree, then runs identical isolated local-only fixtures against
// both binaries.
func RunRoutesListJSON(ctx context.Context, repoRoot, artifactDir string) (Report, error) {
	if repoRoot == "" {
		return Report{}, fmt.Errorf("routes list JSON: repository root is required")
	}
	if artifactDir == "" {
		return Report{}, fmt.Errorf("routes list JSON: report artifact directory is required")
	}

	binaries, err := BuildOldAndNew(ctx, nil, repoRoot, filepath.Join(artifactDir, "bin"))
	if err != nil {
		return Report{}, err
	}

	fixtureParent, err := os.MkdirTemp("", "gordon-compat-routes-list-*")
	if err != nil {
		return Report{}, fmt.Errorf("routes list JSON: create fixture parent: %w", err)
	}
	defer os.RemoveAll(fixtureParent)

	sourceConfig := filepath.Join(FixtureRoot(), "configs", "realistic.toml")
	oldFixture, err := StageSideFixture(fixtureParent, sourceConfig)
	if err != nil {
		return Report{}, err
	}
	newFixture, err := StageSideFixture(fixtureParent, sourceConfig)
	if err != nil {
		return Report{}, err
	}

	old, err := executeRoutesListJSON(ctx, SideOld, binaries.Old.BinaryPath, oldFixture)
	if err != nil {
		return Report{}, err
	}
	new, err := executeRoutesListJSON(ctx, SideNew, binaries.New.BinaryPath, newFixture)
	if err != nil {
		return Report{}, err
	}

	return CompareSideResultsWithMetadata(old, new, nil, artifactDir, ReportMetadata{
		BaselineCommit:  binaries.Old.Commit,
		CandidateCommit: binaries.New.Commit,
		RerunCommand:    routesListJSONRerunCommand(),
	})
}

func executeRoutesListJSON(ctx context.Context, side, binaryPath string, fixture SideFixture) (SideResult, error) {
	result, err := ExecuteSide(ctx, side, CommandCaptureRequest{
		BinaryPath: binaryPath,
		Args:       []string{"routes", "list", "--json"},
		Dir:        fixture.Root,
		Env:        routesListJSONEnvironment(fixture),
		Source:     "gordon routes list --json",
		Level:      LevelSemantic,
	})
	if err != nil {
		return SideResult{Side: side, Artifact: newExactCLIObservation("gordon routes list --json", map[string]any{"captureError": "command capture failed"}), ValidationError: err}, nil
	}

	artifact, validationErr := routesListJSONArtifact(result.Artifact)
	return SideResult{Side: side, Artifact: artifact, ValidationError: validationErr}, nil
}

func routesListJSONEnvironment(fixture SideFixture) []string {
	return append(append([]string{}, fixture.Env...),
		"XDG_CONFIG_HOME="+filepath.Join(fixture.Root, "xdg-config"),
		"XDG_DATA_HOME="+filepath.Join(fixture.Root, "xdg-data"),
		"GORDON_REMOTE=",
		"GORDON_TOKEN=",
		"GORDON_INSECURE=",
	)
}

func routesListJSONArtifact(capture Artifact) (CLIArtifact, error) {
	return commandJSONArtifact(capture, "gordon routes list --json")
}

func routesListJSONRerunCommand() string {
	return "GORDON_COMPAT_BASELINE_REF=" + BaselineRefFromEnv() + " go test ./internal/testutils/compatoldnew -run '^TestCompatibilityRoutesListJSON$' -count=1"
}
