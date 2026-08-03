package compatoldnew

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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
	for _, contract := range releasesmoke.HarnessContracts {
		source, err := releasesmoke.LoadEngineHarnessSource(projectRoot(t), contract.Engine)
		require.NoError(t, err)
		for _, fragment := range contract.Contains {
			require.Contains(t, source, fragment, contract.Name)
		}
		for _, fragment := range contract.NotContains {
			require.NotContains(t, source, fragment, contract.Name)
		}
	}
	require.Equal(t, 30*time.Second, releasesmoke.ReadinessTimeout)

	makefile, err := os.ReadFile(filepath.Join(projectRoot(t), "Makefile"))
	require.NoError(t, err)
	for _, tc := range releasesmoke.MakeDelegationTargets {
		recipe, err := releasesmoke.MakeTargetRecipe(string(makefile), tc.Target, tc.Next)
		require.NoError(t, err, tc.Target)
		require.NoError(t, releasesmoke.ValidateMakeDelegationRecipe(recipe), tc.Target)
	}
}

func TestReleaseRoleOwnerSmokeContract(t *testing.T) {
	podmanSource, err := releasesmoke.LoadEngineHarnessSource(projectRoot(t), "podman")
	require.NoError(t, err)
	require.NotContains(t, podmanSource, "runtime.sock:ro,U")

	dockerSource, err := releasesmoke.LoadEngineHarnessSource(projectRoot(t), "docker")
	require.NoError(t, err)
	require.NotContains(t, dockerSource, ":/var/lib/gordon:U", "Docker must never receive Podman's U option")
}
