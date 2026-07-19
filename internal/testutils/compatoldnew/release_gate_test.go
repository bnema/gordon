package compatoldnew

import (
	"os"
	"path/filepath"
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
