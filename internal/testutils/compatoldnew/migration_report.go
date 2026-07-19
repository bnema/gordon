package compatoldnew

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// migrationInvocationReport is intentionally narrow and contains only release
// assertions, never command output, credentials, paths, or engine metadata.
type migrationInvocationReport struct {
	Scenario string                   `json:"scenario"`
	Skipped  bool                     `json:"skipped"`
	Passed   bool                     `json:"passed"`
	Probes   migrationProbeAssertions `json:"probes"`
}

type migrationProbeAssertions struct {
	Application bool `json:"application"`
	Registry    bool `json:"registry"`
	Listeners   bool `json:"listeners"`
	Resume      bool `json:"resume"`
}

func (p migrationProbeAssertions) passed() bool {
	return p.Application && p.Registry && p.Listeners && p.Resume
}

func validMigrationInvocationReport(report migrationInvocationReport) bool {
	return report.Scenario == "rootless-podman-old-to-split" && !report.Skipped && report.Passed && report.Probes.passed()
}

func writeMigrationInvocationReport(path string, report migrationInvocationReport) error {
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) && filepath.Clean(path) == "." {
		return fmt.Errorf("migration report path is invalid")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create migration report directory: %w", err)
	}
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode migration report: %w", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("write migration report: %w", err)
	}
	return os.Chmod(path, 0o600)
}

func readMigrationInvocationReport(path string) (migrationInvocationReport, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return migrationInvocationReport{}, fmt.Errorf("read migration report: %w", err)
	}
	var report migrationInvocationReport
	if err := json.Unmarshal(body, &report); err != nil {
		return migrationInvocationReport{}, fmt.Errorf("decode migration report: %w", err)
	}
	return report, nil
}
