package compatoldnew

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/testutils/releasesmoke"
)

// TestReleaseGateExampleConfigTOML is invoked by pre-release-acceptance. It
// intentionally parses the shipped file without initializing secrets, a
// runtime, or operator-specific environment.
func TestReleaseGateExampleConfigTOML(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(projectRoot(t), "gordon.toml.example"))
	require.NoError(t, err)

	var document map[string]any
	require.NoError(t, toml.Unmarshal(contents, &document))
	require.Contains(t, document, "server")
	require.Contains(t, document, "runtime")
}

func TestReleaseGateArtifactImageIncludesManagedPassTools(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(projectRoot(t), "Dockerfile"))
	require.NoError(t, err)
	dockerfile := string(contents)
	require.Contains(t, dockerfile, "apk add --no-cache ca-certificates docker-cli curl wget tzdata pass gnupg")
}

func TestReleaseManagedPassSmokeReadinessIsBoundedAndReaped(t *testing.T) {
	harnessSource := readReleaseSmokeHarnessSource(t)

	require.Contains(t, harnessSource, "waitManagedPassReadiness")
	require.Contains(t, harnessSource, "ReadinessPollAttempts")
	require.Equal(t, 30, releasesmoke.ReadinessPollAttempts)
	require.Contains(t, harnessSource, "ownerCmd.Process.Kill")
	require.Contains(t, harnessSource, "ownerCmd.Process.Wait")
	require.Contains(t, harnessSource, "ManagedPassLockMessage")
	require.NotContains(t, harnessSource, `IFS= read -r readiness`)

	makefile, err := os.ReadFile(filepath.Join(projectRoot(t), "Makefile"))
	require.NoError(t, err)
	for _, target := range []string{"release-podman-managed-pass-smoke", "release-image-smoke"} {
		next := "build-push"
		if target == "release-podman-managed-pass-smoke" {
			next = "release-image-smoke"
		}
		recipe := makeTargetRecipe(t, string(makefile), target, next)
		require.Contains(t, recipe, "go run ./cmd/release-smoke")
		require.NotContains(t, recipe, "readiness_pid=$$!")
	}
}

func TestReleaseRoleOwnerSmokeContract(t *testing.T) {
	harnessSource := readReleaseSmokeHarnessSource(t)

	require.Contains(t, harnessSource, "/var/lib/gordon:U")
	require.Contains(t, harnessSource, "identity-write-check")
	require.Contains(t, harnessSource, "/run/gordon/runtime.sock:ro")
	require.NotContains(t, harnessSource, "runtime.sock:ro,U")
	require.NotContains(t, harnessSource, "/var/lib/gordon/secrets:U")
	for _, removed := range []string{"--group-add", "21900"} {
		require.NotContains(t, harnessSource, removed)
	}

	makefile, err := os.ReadFile(filepath.Join(projectRoot(t), "Makefile"))
	require.NoError(t, err)
	dockerGate := makeTargetRecipe(t, string(makefile), "release-image-smoke", "build-push")
	require.NotContains(t, dockerGate, ":/var/lib/gordon:U", "Docker must never receive Podman's U option")
}

func readReleaseSmokeHarnessSource(t *testing.T) string {
	t.Helper()
	root := projectRoot(t)
	var b strings.Builder
	for _, name := range []string{
		"internal/testutils/releasesmoke/harness.go",
		"internal/testutils/releasesmoke/podman.go",
	} {
		data, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		b.Write(data)
	}
	return b.String()
}

func makeTargetRecipe(t *testing.T, makefile, target, nextTarget string) string {
	t.Helper()
	startPattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(target) + `:`)
	startLoc := startPattern.FindStringIndex(makefile)
	require.NotNil(t, startLoc, "missing Make target %s", target)
	start := startLoc[0]
	endPattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(nextTarget) + `:`)
	endLoc := endPattern.FindStringIndex(makefile[start:])
	require.NotNil(t, endLoc, "missing target boundary %s", nextTarget)
	return makefile[start : start+endLoc[0]]
}

func TestMigrationInvocationReportFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration-report.json")
	require.NoError(t, writeMigrationInvocationReport(path, migrationInvocationReport{
		Scenario: "rootless-podman-old-to-split",
		Skipped:  false,
		Passed:   true,
		Probes: migrationProbeAssertions{
			Application: true,
			Registry:    true,
			Listeners:   true,
			Resume:      true,
		},
	}))

	report, err := readMigrationInvocationReport(path)
	require.NoError(t, err)
	require.True(t, validMigrationInvocationReport(report))

	report.Skipped = true
	require.False(t, validMigrationInvocationReport(report))
}
