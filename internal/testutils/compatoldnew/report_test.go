package compatoldnew

import (
	"encoding/json"
	"fmt"
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
	for _, name := range []string{"compat-report.json", "normalized.diff", "old.raw.json", "new.raw.json", "old.normalized.json", "new.normalized.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("missing report artifact %s: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("artifact %s mode=%o, want 600", name, info.Mode().Perm())
		}
	}
}

func TestCompareSideResultsSerializesValidationFailuresBeforeReturningError(t *testing.T) {
	dir := t.TempDir()
	old := SideResult{
		Side:            SideOld,
		Artifact:        NewCLIArtifact("gordon routes list --json", map[string]any{"exitCode": 0, "stdout": "[]", "stderr": ""}, LevelExact),
		ValidationError: fmt.Errorf("old contract token=old-secret is invalid"),
	}
	new := SideResult{
		Side:            SideNew,
		Artifact:        NewCLIArtifact("gordon routes list --json", map[string]any{"exitCode": 0, "stdout": "[]", "stderr": ""}, LevelExact),
		ValidationError: fmt.Errorf("new contract token=new-secret is invalid"),
	}

	report, err := CompareSideResults(old, new, nil, dir)
	if err == nil {
		t.Fatal("validation failures must return an error after artifact emission")
	}
	if report.Failed != 2 || len(report.Failures) != 2 {
		t.Fatalf("report=%+v, want two validation failures", report)
	}
	body, err := os.ReadFile(filepath.Join(dir, "compat-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "old-secret") || strings.Contains(string(body), "new-secret") {
		t.Fatalf("report leaked validation secret: %s", body)
	}
	var persisted Report
	if err := json.Unmarshal(body, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Failed != 2 || len(persisted.Failures) != 2 {
		t.Fatalf("persisted report=%+v, want two validation failures", persisted)
	}
	for _, failure := range persisted.Failures {
		if !strings.Contains(failure.Problem, "validation failed") || !strings.Contains(failure.Problem, "contract") {
			t.Fatalf("validation failure is not actionable: %+v", failure)
		}
	}
	diff, err := os.ReadFile(filepath.Join(dir, "normalized.diff"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(diff), "validationError") {
		t.Fatalf("normalized diff lacks validation evidence: %s", diff)
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
