package compatoldnew

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareSidesAlwaysWritesActionableReportOnDiff(t *testing.T) {
	dir := t.TempDir()
	old := SideResult{Side: SideOld, Artifact: NewCLIArtifact("gordon routes list --json", map[string]any{"exitCode": 0, "stdout": "[]"}, LevelSemantic)}
	new := SideResult{Side: SideNew, Artifact: NewCLIArtifact("gordon routes list --json", map[string]any{"exitCode": 1, "stdout": "[]"}, LevelSemantic)}
	report, err := CompareSideResults(old, new, nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	if report.Failed != 1 {
		t.Fatalf("report=%+v", report)
	}
	for _, name := range []string{"compat-report.json", "normalized.diff"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing report artifact %s: %v", name, err)
		}
	}
}

func TestReportOutputs(t *testing.T) {
	r := NewReport([]Failure{{OldValue: "old", NewValue: "new", Source: "cmd", SuggestedCommand: "cmd"}}, 1)
	if !strings.Contains(r.ConsoleSummary(), "1 failed") {
		t.Fatalf("summary=%q", r.ConsoleSummary())
	}
	if b, err := r.JSON(); err != nil || !strings.Contains(string(b), "failures") {
		t.Fatalf("json=%s err=%v", b, err)
	}
	dir := t.TempDir()
	if err := r.WriteArtifactDirectory(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"compat-report.json", "normalized.diff"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}
