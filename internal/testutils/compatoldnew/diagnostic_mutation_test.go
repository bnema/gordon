package compatoldnew

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIObservationsCompareExactAndInvalidJSONStillWritesReport(t *testing.T) {
	base := map[string]any{"exitCode": 0, "stderr": "", "json": map[string]any{"routes": []any{}}}
	mutations := []struct {
		name string
		new  map[string]any
	}{
		{"exit", map[string]any{"exitCode": 1, "stderr": "", "json": map[string]any{"routes": []any{}}}},
		{"stderr", map[string]any{"exitCode": 0, "stderr": "warning", "json": map[string]any{"routes": []any{}}}},
		{"json field", map[string]any{"exitCode": 0, "stderr": "", "json": map[string]any{"routes": []any{"changed"}}}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			dir := t.TempDir()
			report, err := CompareSideResults(
				SideResult{Side: SideOld, Artifact: NewCLIArtifact("gordon routes list --json", base, LevelExact)},
				SideResult{Side: SideNew, Artifact: NewCLIArtifact("gordon routes list --json", mutation.new, LevelExact)}, nil, dir)
			if err != nil || report.Failed != 1 {
				t.Fatalf("report=%+v err=%v", report, err)
			}
		})
	}

	invalid := NewCLIArtifact("capture", map[string]any{"exitCode": 0, "stdout": "not-json", "stderr": ""}, LevelExact)
	oldArtifact, oldErr := routesListJSONArtifact(invalid)
	newArtifact, newErr := routesListJSONArtifact(invalid)
	if oldErr == nil || newErr == nil {
		t.Fatal("invalid JSON must be a validation failure")
	}
	dir := t.TempDir()
	_, err := CompareSideResults(
		SideResult{Side: SideOld, Artifact: oldArtifact, ValidationError: oldErr},
		SideResult{Side: SideNew, Artifact: newArtifact, ValidationError: newErr}, nil, dir)
	if err == nil {
		t.Fatal("identical invalid JSON must fail after report emission")
	}
	for _, name := range []string{"old.raw.json", "new.raw.json", "old.normalized.json", "new.normalized.json", "compat-report.json"} {
		if _, statErr := os.Stat(filepath.Join(dir, name)); statErr != nil {
			t.Fatalf("missing %s after invalid JSON: %v", name, statErr)
		}
	}
}

func TestAPIContractFailuresAndDriftWriteSafeArtifacts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`not-json token=super-secret Authorization: Bearer eyJheader.payload.signature`))
	}))
	defer server.Close()

	op := captureAdminOperation(context.Background(), server.Client(), server.URL, "create", http.MethodPost, "/routes", `{}`, "Bearer private-value", http.StatusCreated, []string{"domain"})
	if op.Status != http.StatusTeapot || op.DecodeError == "" || len(op.ValidationErrors) < 3 {
		t.Fatalf("incomplete API observation: %+v", op)
	}
	artifact := NewHTTPArtifact("api", map[string]any{"operations": []adminOperation{op}}, LevelExact)
	dir := t.TempDir()
	_, err := CompareSideResults(
		SideResult{Side: SideOld, Artifact: artifact, ValidationError: context.DeadlineExceeded},
		SideResult{Side: SideNew, Artifact: artifact, ValidationError: context.DeadlineExceeded}, nil, dir)
	if err == nil {
		t.Fatal("identical API contract regressions must fail")
	}
	for _, name := range []string{"old.raw.json", "new.raw.json", "old.normalized.json", "new.normalized.json", "compat-report.json"} {
		body, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if strings.Contains(string(body), "super-secret") || strings.Contains(string(body), "private-value") || strings.Contains(string(body), "eyJheader.payload.signature") {
			t.Fatalf("credential leaked into %s: %s", name, body)
		}
	}

	changed := NewHTTPArtifact("api", map[string]any{"operations": []adminOperation{{Name: "create", Status: http.StatusCreated, Headers: map[string]string{"Content-Type": "application/json"}, JSON: map[string]any{"domain": "changed"}}}}, LevelExact)
	report, err := CompareSideResults(
		SideResult{Side: SideOld, Artifact: artifact},
		SideResult{Side: SideNew, Artifact: changed}, nil, t.TempDir())
	if err != nil || report.Failed != 1 {
		t.Fatalf("status/header/DTO/decode mutation report=%+v err=%v", report, err)
	}
}
