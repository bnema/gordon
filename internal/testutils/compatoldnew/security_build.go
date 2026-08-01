package compatoldnew

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// RepositoryRoot returns the Gordon repository root derived from FixtureRoot
// without requiring testing.T. Production fixtures and tests share this path.
func RepositoryRoot() string {
	fixtureRoot := FixtureRoot()
	if !filepath.IsAbs(fixtureRoot) {
		return ""
	}
	return filepath.Clean(filepath.Join(fixtureRoot, "..", "..", "..", ".."))
}

// securityBuildCandidate builds the candidate Gordon binary into output.
// Kept outside *_test.go so production fixtures can call it under go build.
func securityBuildCandidate(ctx context.Context, repoRoot, output string) error {
	cmd, err := newIsolatedCommand(ctx, "go", []string{"build", "-o", output, "./main.go"}, []string{"CGO_ENABLED=0"}, nil, false)
	if err != nil {
		return fmt.Errorf("prepare go build: %w", err)
	}
	if strings.TrimSpace(repoRoot) == "" {
		return fmt.Errorf("go build repository root is unavailable")
	}
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build in %s failed: %w: %s", repoRoot, err, strings.TrimSpace(string(output)))
	}
	return nil
}
