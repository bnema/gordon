package compatoldnew

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Report struct {
	Total    int       `json:"total"`
	Failed   int       `json:"failed"`
	Failures []Failure `json:"failures"`
}

// SideResult associates a captured artifact with the target that produced it.
type SideResult struct {
	Side     string
	Artifact Artifact
}

// CompareSideResults routes every comparison through Compare and writes the
// report artifacts even when the values match, so failures are diagnosable.
func CompareSideResults(old, new SideResult, allow *AllowlistedDifference, artifactDir string) (Report, error) {
	if old.Side != SideOld || new.Side != SideNew {
		return Report{}, fmt.Errorf("compare sides: expected old and new results")
	}
	if old.Artifact == nil || new.Artifact == nil {
		return Report{}, fmt.Errorf("compare sides: both artifacts are required")
	}
	if artifactDir == "" {
		return Report{}, fmt.Errorf("compare sides: report artifact directory is required")
	}
	report := NewReport(Compare(old.Artifact, new.Artifact, allow), 1)
	if err := report.WriteArtifactDirectory(artifactDir); err != nil {
		return Report{}, fmt.Errorf("compare sides write report: %w", err)
	}
	return report, nil
}

func NewReport(failures []Failure, total int) Report {
	return Report{Total: total, Failed: len(failures), Failures: failures}
}

func (r Report) ConsoleSummary() string {
	if r.Failed == 0 {
		return fmt.Sprintf("compat: %d compared, 0 failed", r.Total)
	}
	return fmt.Sprintf("compat: %d compared, %d failed", r.Total, r.Failed)
}

func (r Report) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

func (r Report) WriteArtifactDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	b, err := r.JSON()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "compat-report.json"), b, 0o600); err != nil {
		return err
	}
	var diffs []string
	for _, f := range r.Failures {
		diffs = append(diffs, NormalizedDiff(f.OldValue, f.NewValue))
	}
	return os.WriteFile(filepath.Join(dir, "normalized.diff"), []byte(strings.Join(diffs, "\n")), 0o600)
}
