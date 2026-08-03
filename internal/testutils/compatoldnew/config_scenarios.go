package compatoldnew

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const configShowJSONScenarioName = "cli/config-show-json"

// ConfigScenarios returns Phase 5 config compatibility scenario shells.
func ConfigScenarios() []Scenario {
	return []Scenario{
		configScenario("config/minimal-load"),
		configScenario("config/realistic-load"),
		configScenario("config/legacy-registry-domain-keys"),
		configScenario("config/invalid-error"),
		configScenario("config/env-override-precedence"),
		configScenario("config/server-settings"),
		configScenario("config/auth-settings"),
		configScenario("config/api-rate-limits"),
		configScenario("config/deploy-settings"),
		configScenario("config/network-isolation"),
		configScenario("config/volume-settings"),
		configScenario("config/auto-route-preview"),
		configScenario("config/routes-save-load"),
		configScenario("config/external-routes"),
		configScenario("config/attachments"),
		configScenario("config/public-tls-acme"),
		configScenario("config/dns-settings"),
		configScenario("config/entrypoints-traffic-graph"),
		configScenario("config/standalone-network-services"),
		configScenario("config/logging"),
		configScenario("config/telemetry"),
		configScenario("config/backups"),
		configScenario("config/images"),
		configScenario("config/container-security-defaults"),
		configScenario("config/reload-preserves-critical-fields"),
		configScenario("config/backup-canonical-save"),
	}
}

func configScenario(name string) Scenario {
	return pendingScenario(name, SurfaceConfig, "6.1 Config compatibility", false, "old/new config compatibility scenario execution is not implemented yet")
}

// RunConfigShowJSON executes the real config-show compatibility slice. It
// builds the baseline in a detached worktree and the candidate from the
// caller's worktree, then runs identical isolated fixtures against both.
func RunConfigShowJSON(ctx context.Context, repoRoot, artifactDir string) (Report, error) {
	if repoRoot == "" {
		return Report{}, fmt.Errorf("config show JSON: repository root is required")
	}
	if artifactDir == "" {
		return Report{}, fmt.Errorf("config show JSON: report artifact directory is required")
	}

	binaries, err := BuildOldAndNew(ctx, nil, repoRoot, filepath.Join(artifactDir, "bin"))
	if err != nil {
		return Report{}, err
	}

	fixtureParent, err := os.MkdirTemp("", "gordon-compat-config-show-*")
	if err != nil {
		return Report{}, fmt.Errorf("config show JSON: create fixture parent: %w", err)
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

	old, err := executeConfigShowJSON(ctx, SideOld, binaries.Old.BinaryPath, oldFixture)
	if err != nil {
		return Report{}, err
	}
	new, err := executeConfigShowJSON(ctx, SideNew, binaries.New.BinaryPath, newFixture)
	if err != nil {
		return Report{}, err
	}

	return CompareSideResultsWithMetadata(old, new, nil, artifactDir, ReportMetadata{
		BaselineCommit:  binaries.Old.Commit,
		CandidateCommit: binaries.New.Commit,
		RerunCommand:    configShowJSONRerunCommand(),
	})
}

func executeConfigShowJSON(ctx context.Context, side, binaryPath string, fixture SideFixture) (SideResult, error) {
	result, err := ExecuteSide(ctx, side, CommandCaptureRequest{
		BinaryPath: binaryPath,
		Args:       []string{"config", "show", "--json"},
		Dir:        fixture.Root,
		Env:        configShowJSONEnvironment(fixture),
		Source:     "gordon config show --json",
		Level:      LevelSemantic,
	})
	if err != nil {
		return SideResult{Side: side, Artifact: newExactCLIObservation("gordon config show --json", map[string]any{"captureError": "command capture failed"}), ValidationError: err}, nil
	}

	artifact, validationErr := configShowJSONArtifact(result.Artifact)
	return SideResult{Side: side, Artifact: artifact, ValidationError: validationErr}, nil
}

func configShowJSONEnvironment(fixture SideFixture) []string {
	return append(append([]string{}, fixture.Env...),
		"XDG_CONFIG_HOME="+filepath.Join(fixture.Root, "xdg-config"),
		"GORDON_REMOTE=",
		"GORDON_TOKEN=",
		"GORDON_INSECURE=",
	)
}

func configShowJSONArtifact(capture Artifact) (CLIArtifact, error) {
	return commandJSONArtifact(capture, "gordon config show --json")
}

// commandJSONArtifact preserves the command's raw output for diagnostics while
// comparing only its exit status, parsed JSON, stderr, and decode state.
func commandJSONArtifact(capture Artifact, source string) (CLIArtifact, error) {
	captured, ok := capture.RawValue().(map[string]any)
	if !ok {
		artifact := newExactCLIObservation(source, map[string]any{"captureError": fmt.Sprintf("unexpected capture type %T", capture.RawValue())})
		return artifact, fmt.Errorf("unexpected capture type %T", capture.RawValue())
	}
	exitCode, exitOK := captured["exitCode"].(int)
	stdout, stdoutOK := captured["stdout"].(string)
	stderr, stderrOK := captured["stderr"].(string)
	raw := map[string]any{"exitCode": exitCode, "stdout": stdout, "stderr": stderr}
	normalized := map[string]any{"exitCode": exitCode, "stderr": stderr, "decodeError": ""}
	if !exitOK || !stdoutOK || !stderrOK {
		raw["decodeError"] = "missing command observation fields"
		normalized["decodeError"] = "missing command observation fields"
		return newCLIJSONObservation(source, raw, normalized), fmt.Errorf("missing command observation fields")
	}
	var payload any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		raw["decodeError"] = "invalid JSON"
		normalized["decodeError"] = "invalid JSON"
		return newCLIJSONObservation(source, raw, normalized), fmt.Errorf("decode JSON: %w", err)
	}
	normalized["json"] = payload
	return newCLIJSONObservation(source, raw, normalized), nil
}

// newExactCLIObservation is used for capture failures that have no command
// observation to parse.
func newExactCLIObservation(source string, observed map[string]any) CLIArtifact {
	return CLIArtifact{baseArtifact{Raw: observed, Normalized: observed, SourceRef: source, Compare: LevelExact}}
}

func newCLIJSONObservation(source string, raw, normalized map[string]any) CLIArtifact {
	return CLIArtifact{baseArtifact{Raw: raw, Normalized: Normalize(normalized), SourceRef: source, Compare: LevelExact}}
}

func configShowJSONRerunCommand() string {
	return "GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_BASELINE_REF=" + BaselineRefFromEnv() + " go test ./internal/testutils/compatoldnew -run '^TestCompatibilityConfigShowJSON$' -count=1"
}
