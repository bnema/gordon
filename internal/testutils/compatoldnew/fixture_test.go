package compatoldnew

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFixtureMetadata(t *testing.T) {
	t.Run("baseline ref defaults to origin main", func(t *testing.T) {
		t.Setenv(EnvCompatBaselineRef, "")

		require.Equal(t, "origin/main", BaselineRefFromEnv())
	})

	t.Run("baseline ref uses explicit override", func(t *testing.T) {
		t.Setenv(EnvCompatBaselineRef, "refs/tags/v0.8.0")

		require.Equal(t, "refs/tags/v0.8.0", BaselineRefFromEnv())
	})

	t.Run("surfaces round trip through stable string tags", func(t *testing.T) {
		for _, surface := range AllSurfaces() {
			parsed, ok := ParseSurface(surface.String())
			require.True(t, ok, "surface %q should parse", surface)
			require.Equal(t, surface, parsed)
		}
	})

	t.Run("podman required fixtures skip unless explicitly enabled", func(t *testing.T) {
		fixture := Fixture{Name: "podman-runtime", PodmanRequired: true}
		t.Setenv(EnvCompatPodman, "")

		reason, skip := fixture.SkipReason()
		require.True(t, skip)
		require.Contains(t, reason, EnvCompatPodman)

		t.Setenv(EnvCompatPodman, "1")
		reason, skip = fixture.SkipReason()
		require.False(t, skip)
		require.Empty(t, reason)
	})

	t.Run("config fixtures declare paths env files and surfaces", func(t *testing.T) {
		fixtures := ConfigFixtures()
		require.Len(t, fixtures, 4)

		byName := make(map[string]Fixture, len(fixtures))
		for _, fixture := range fixtures {
			byName[fixture.Name] = fixture
			require.NotEmpty(t, fixture.ExpectedSurfaces, fixture.Name)
			require.FileExists(t, fixture.ConfigPath)
		}

		minimal := byName["minimal"]
		require.Contains(t, minimal.EnvFiles, filepath.Join(FixtureRoot(), "env", "basic.env"))
		require.Contains(t, minimal.ExpectedSurfaces, SurfaceConfig)
		require.Contains(t, minimal.ExpectedSurfaces, SurfaceCLI)
		require.Contains(t, minimal.ExpectedSurfaces, SurfaceAPI)

		invalid := byName["invalid"]
		require.Contains(t, invalid.ExpectedSurfaces, SurfaceMigration)
		require.False(t, invalid.PodmanRequired)
	})
}
