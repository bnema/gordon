package compatoldnew

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/require"
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
	require.Contains(t, dockerfile, "pass")
	require.Contains(t, dockerfile, "gnupg")
	require.Contains(t, dockerfile, "/var/lib/gordon/secrets")
}

func TestReleaseManagedPassSmokeReadinessIsBoundedAndReaped(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(projectRoot(t), "Makefile"))
	require.NoError(t, err)
	makefile := string(contents)

	for _, target := range []struct {
		name string
		next string
	}{
		{name: "release-podman-managed-pass-smoke", next: "release-image-smoke"},
		{name: "release-image-smoke", next: "build-push"},
	} {
		t.Run(target.name, func(t *testing.T) {
			recipe := makeTargetRecipe(t, makefile, target.name, target.next)
			require.Contains(t, recipe, "readiness_pid=$$!")
			require.Contains(t, recipe, "for attempt in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30")
			require.Contains(t, recipe, "kill -0 \"$$readiness_pid\"")
			require.Contains(t, recipe, "kill \"$$readiness_pid\"")
			require.Contains(t, recipe, "wait \"$$readiness_pid\"")
			require.Contains(t, recipe, "kill \"$$owner_pid\"")
			require.Contains(t, recipe, "wait \"$$owner_pid\"")
			require.Contains(t, recipe, "test \"$$readiness\" = 'Managed pass backend lock acquired'")
			require.NotContains(t, recipe, "IFS= read -r readiness <\"$$tmp/ready\"; test")
			require.NotContains(t, recipe, "IFS= read -r readiness <\"$$lease_dir/ready\"; test")
		})
	}
}

func makeTargetRecipe(t *testing.T, makefile, target, nextTarget string) string {
	t.Helper()
	start := strings.Index(makefile, target+":")
	require.NotEqual(t, -1, start, "missing Make target %s", target)
	end := strings.Index(makefile[start:], "\n"+nextTarget+":")
	require.NotEqual(t, -1, end, "missing target boundary %s", nextTarget)
	return makefile[start : start+end]
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
