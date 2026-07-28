package releasesmoke

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ManagedPassArtifactShellCheck is the exact shell predicate used by Docker and
// Podman release smokes to verify managed-pass store artifacts after doctor.
const ManagedPassArtifactShellCheck = `test -s /var/lib/gordon/secrets/current/.gordon-managed-pass-fingerprint; test -s /var/lib/gordon/secrets/current/password-store/.gpg-id; test -d /var/lib/gordon/secrets/current/gnupg` //nolint:gosec // G101: path fragment "password-store", not a credential.

// HarnessSourceRelPaths lists repo-relative sources concatenated for release-smoke
// behavioral/characterization contracts shared by releasesmoke and compatoldnew.
var HarnessSourceRelPaths = []string{
	"internal/testutils/releasesmoke/harness.go",
	"internal/testutils/releasesmoke/podman.go",
	"internal/testutils/releasesmoke/readiness.go",
	"internal/testutils/releasesmoke/characterization.go",
}

// MakeDelegationTarget documents a Makefile target that must delegate to the Go harness.
type MakeDelegationTarget struct {
	Target string
	Next   string
}

// MakeDelegationTargets is the canonical Make→harness delegation table.
var MakeDelegationTargets = []MakeDelegationTarget{
	{Target: "release-podman-managed-pass-smoke", Next: "release-image-smoke"},
	{Target: "release-image-smoke", Next: "build-push"},
}

const releaseSmokeHarnessInvocation = "go run ./cmd/release-smoke"

// LoadHarnessSource concatenates the release-smoke source files used by
// characterization and release-gate proofs. Missing files fail closed.
func LoadHarnessSource(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("harness source root must not be empty")
	}
	var b strings.Builder
	for _, rel := range HarnessSourceRelPaths {
		path := filepath.Join(root, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read harness source %s: %w", rel, err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// MakeTargetRecipe returns the Makefile recipe body for target bounded by nextTarget.
func MakeTargetRecipe(makefile, target, nextTarget string) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", fmt.Errorf("make target must not be empty")
	}
	if strings.TrimSpace(nextTarget) == "" {
		return "", fmt.Errorf("make target boundary must not be empty")
	}
	startPattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(target) + `:`)
	startLoc := startPattern.FindStringIndex(makefile)
	if startLoc == nil {
		return "", fmt.Errorf("missing Make target %s", target)
	}
	start := startLoc[0]
	endPattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(nextTarget) + `:`)
	endLoc := endPattern.FindStringIndex(makefile[start:])
	if endLoc == nil {
		return "", fmt.Errorf("missing target boundary %s", nextTarget)
	}
	return makefile[start : start+endLoc[0]], nil
}

// ValidateMakeDelegationRecipe asserts a Make recipe delegates to cmd/release-smoke
// and does not retain inline readiness/FIFO/podman-service shell.
func ValidateMakeDelegationRecipe(recipe string) error {
	if !strings.Contains(recipe, releaseSmokeHarnessInvocation) {
		return fmt.Errorf("recipe must contain %q", releaseSmokeHarnessInvocation)
	}
	for _, forbidden := range []string{
		"readiness_pid=$$!",
		"podman system service",
	} {
		if strings.Contains(recipe, forbidden) {
			return fmt.Errorf("recipe must not contain %q", forbidden)
		}
	}
	return nil
}
