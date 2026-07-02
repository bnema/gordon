package compatoldnew

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	EnvCompatBaselineRef = "GORDON_COMPAT_BASELINE_REF"
	EnvCompatPodman      = "GORDON_COMPAT_PODMAN"
)

// Surface is a stable compatibility surface tag.
type Surface string

const (
	SurfaceConfig    Surface = "config"
	SurfaceCLI       Surface = "cli"
	SurfaceAPI       Surface = "api"
	SurfaceRegistry  Surface = "registry"
	SurfaceProxy     Surface = "proxy"
	SurfaceRuntime   Surface = "runtime"
	SurfaceMigration Surface = "migration"
	SurfaceSecurity  Surface = "security"
)

var allSurfaces = []Surface{
	SurfaceConfig,
	SurfaceCLI,
	SurfaceAPI,
	SurfaceRegistry,
	SurfaceProxy,
	SurfaceRuntime,
	SurfaceMigration,
	SurfaceSecurity,
}

// Fixture describes one old/new compatibility scenario.
type Fixture struct {
	Name             string
	ConfigPath       string
	EnvFiles         []string
	ExpectedSurfaces []Surface
	PodmanRequired   bool
}

// BaselineRefFromEnv returns the git ref used for the old-side baseline.
func BaselineRefFromEnv() string {
	ref := strings.TrimSpace(os.Getenv(EnvCompatBaselineRef))
	if ref == "" {
		return "origin/main"
	}
	return ref
}

// PodmanEnabledFromEnv reports whether Podman-backed compatibility scenarios are enabled.
func PodmanEnabledFromEnv() bool {
	return os.Getenv(EnvCompatPodman) == "1"
}

// AllSurfaces returns all known compatibility surface tags in declaration order.
func AllSurfaces() []Surface {
	return append([]Surface(nil), allSurfaces...)
}

func (s Surface) String() string {
	return string(s)
}

// ParseSurface parses a stable surface tag.
func ParseSurface(tag string) (Surface, bool) {
	for _, surface := range allSurfaces {
		if string(surface) == tag {
			return surface, true
		}
	}
	return "", false
}

// SkipReason reports whether this fixture should be skipped in the current environment.
func (f Fixture) SkipReason() (string, bool) {
	if f.PodmanRequired && !PodmanEnabledFromEnv() {
		return "fixture requires Podman; set " + EnvCompatPodman + "=1 to opt in", true
	}
	return "", false
}

// FixtureRoot returns the directory containing compatibility fixture data.
func FixtureRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return filepath.Join("internal", "testutils", "compatoldnew", "fixtures")
	}
	return filepath.Join(filepath.Dir(file), "fixtures")
}

// ConfigFixtures returns the initial generic config compatibility fixtures.
func ConfigFixtures() []Fixture {
	root := FixtureRoot()
	basicEnv := filepath.Join(root, "env", "basic.env")

	return []Fixture{
		{
			Name:       "minimal",
			ConfigPath: filepath.Join(root, "configs", "minimal.toml"),
			EnvFiles:   []string{basicEnv},
			ExpectedSurfaces: []Surface{
				SurfaceConfig,
				SurfaceCLI,
				SurfaceAPI,
			},
		},
		{
			Name:       "realistic",
			ConfigPath: filepath.Join(root, "configs", "realistic.toml"),
			EnvFiles:   []string{basicEnv},
			ExpectedSurfaces: []Surface{
				SurfaceConfig,
				SurfaceCLI,
				SurfaceAPI,
				SurfaceRegistry,
				SurfaceProxy,
				SurfaceRuntime,
				SurfaceSecurity,
			},
		},
		{
			Name:       "legacy",
			ConfigPath: filepath.Join(root, "configs", "legacy.toml"),
			EnvFiles:   []string{basicEnv},
			ExpectedSurfaces: []Surface{
				SurfaceConfig,
				SurfaceCLI,
				SurfaceAPI,
				SurfaceMigration,
			},
		},
		{
			Name:       "invalid",
			ConfigPath: filepath.Join(root, "configs", "invalid.toml"),
			ExpectedSurfaces: []Surface{
				SurfaceConfig,
				SurfaceMigration,
			},
		},
	}
}
