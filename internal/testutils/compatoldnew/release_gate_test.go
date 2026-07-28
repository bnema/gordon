package compatoldnew

import (
	"os"
	"path/filepath"
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
	harnessSource, err := releasesmoke.LoadHarnessSource(projectRoot(t))
	require.NoError(t, err)

	require.Contains(t, harnessSource, "waitManagedPassReadiness")
	require.Contains(t, harnessSource, "ReadinessPollAttempts")
	require.Equal(t, 30, releasesmoke.ReadinessPollAttempts)
	require.Contains(t, harnessSource, "Process.Kill")
	require.Contains(t, harnessSource, "Process.Wait")
	require.Contains(t, harnessSource, "StdoutPipe")
	require.Contains(t, harnessSource, "ManagedPassLockMessage")
	require.NotContains(t, harnessSource, `IFS= read -r readiness`)
	require.NotContains(t, harnessSource, "mkfifo")
	require.NotContains(t, harnessSource, "O_WRONLY")

	makefile, err := os.ReadFile(filepath.Join(projectRoot(t), "Makefile"))
	require.NoError(t, err)
	for _, tc := range releasesmoke.MakeDelegationTargets {
		recipe, err := releasesmoke.MakeTargetRecipe(string(makefile), tc.Target, tc.Next)
		require.NoError(t, err, tc.Target)
		require.NoError(t, releasesmoke.ValidateMakeDelegationRecipe(recipe), tc.Target)
	}
}

func TestReleaseRoleOwnerSmokeContract(t *testing.T) {
	harnessSource, err := releasesmoke.LoadHarnessSource(projectRoot(t))
	require.NoError(t, err)

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
	dockerGate, err := releasesmoke.MakeTargetRecipe(string(makefile), "release-image-smoke", "build-push")
	require.NoError(t, err)
	require.NotContains(t, dockerGate, ":/var/lib/gordon:U", "Docker must never receive Podman's U option")
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
