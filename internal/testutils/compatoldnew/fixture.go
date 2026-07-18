package compatoldnew

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// ImmutableCompatibilityBaseline is the pre-refactor release baseline.
	// Never replace it with a moving branch: that can compare a candidate to itself.
	ImmutableCompatibilityBaseline = "8f4a170d141b3e6f9ced7632dd5ac76cf7f9f842"
	EnvCompatBaselineRef           = "GORDON_COMPAT_BASELINE_REF"
	EnvCompatPodman                = "GORDON_COMPAT_PODMAN"
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

// SideFixture is an isolated filesystem view for one old/new side.
type SideFixture struct {
	Root       string
	HomeDir    string
	DataDir    string
	ConfigPath string
	Env        []string
}

// StageSideFixture copies a selected generic fixture config to gordon.toml and
// isolates HOME (and therefore Gordon's default data directory) per side.
func StageSideFixture(parentDir, sourceConfig string) (SideFixture, error) {
	if sourceConfig == "" {
		return SideFixture{}, fmt.Errorf("stage fixture: config path is required")
	}
	contents, err := os.ReadFile(sourceConfig)
	if err != nil {
		return SideFixture{}, fmt.Errorf("stage fixture read config: %w", err)
	}
	root, err := os.MkdirTemp(parentDir, "gordon-compat-side-*")
	if err != nil {
		return SideFixture{}, fmt.Errorf("stage fixture create side directory: %w", err)
	}
	cleanup := func(err error) (SideFixture, error) {
		_ = os.RemoveAll(root)
		return SideFixture{}, err
	}
	homeDir := filepath.Join(root, "home")
	dataDir := filepath.Join(homeDir, ".gordon")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return cleanup(fmt.Errorf("stage fixture create isolated data dir: %w", err))
	}
	configPath := filepath.Join(root, "gordon.toml")
	if err := os.WriteFile(configPath, contents, 0o600); err != nil {
		return cleanup(fmt.Errorf("stage fixture write config: %w", err))
	}
	return SideFixture{Root: root, HomeDir: homeDir, DataDir: dataDir, ConfigPath: configPath, Env: []string{"HOME=" + homeDir}}, nil
}

// BaselineRefFromEnv returns the git ref used for the old-side baseline.
func BaselineRefFromEnv() string {
	ref := strings.TrimSpace(os.Getenv(EnvCompatBaselineRef))
	if ref == "" {
		return ImmutableCompatibilityBaseline
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
