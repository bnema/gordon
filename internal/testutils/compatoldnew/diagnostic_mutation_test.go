package compatoldnew

import (
	"context"
	"errors"
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
	report, err := CompareSideResults(
		SideResult{Side: SideOld, Artifact: oldArtifact, ValidationError: oldErr},
		SideResult{Side: SideNew, Artifact: newArtifact, ValidationError: newErr}, nil, dir)
	if err == nil {
		t.Fatal("identical invalid JSON must fail after report emission")
	}
	if report.Failed != 2 || len(report.Failures) != 2 {
		t.Fatalf("invalid CLI report=%+v, want serialized validation failures", report)
	}
	body, readErr := os.ReadFile(filepath.Join(dir, "compat-report.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(body), "validation failed") || !strings.Contains(string(body), "decode JSON") {
		t.Fatalf("invalid CLI report lacks validation evidence: %s", body)
	}
	for _, name := range []string{"old.raw.json", "new.raw.json", "old.normalized.json", "new.normalized.json", "compat-report.json"} {
		if _, statErr := os.Stat(filepath.Join(dir, name)); statErr != nil {
			t.Fatalf("missing %s after invalid JSON: %v", name, statErr)
		}
	}
}

func TestCLIJSONArtifactPreservesRawOutputAndComparesParsedJSON(t *testing.T) {
	capture := NewCLIArtifact("capture", map[string]any{
		"command":     "ignored-binary-path",
		"environment": map[string]string{"TOKEN": "<redacted>"},
		"exitCode":    0,
		"stdout":      `{"routes":[]}`,
		"stderr":      "warning\n",
	}, LevelExact)
	artifact, err := routesListJSONArtifact(capture)
	if err != nil {
		t.Fatal(err)
	}
	raw := artifact.RawValue().(map[string]any)
	if raw["stdout"] != `{"routes":[]}` || raw["stderr"] != "warning\n" || raw["exitCode"] != 0 {
		t.Fatalf("raw CLI observation=%#v", raw)
	}
	if _, ok := raw["command"]; ok {
		t.Fatalf("raw CLI observation includes command metadata: %#v", raw)
	}
	normalized := artifact.NormalizedValue().(map[string]any)
	if _, ok := normalized["stdout"]; ok {
		t.Fatalf("normalized CLI observation includes raw stdout: %#v", normalized)
	}
	if normalized["json"].(map[string]any)["routes"] == nil || normalized["decodeError"] != "" {
		t.Fatalf("normalized CLI observation is not strict parsed JSON: %#v", normalized)
	}

	dir := t.TempDir()
	report, err := CompareSideResults(
		SideResult{Side: SideOld, Artifact: artifact},
		SideResult{Side: SideNew, Artifact: artifact}, nil, dir)
	if err != nil || report.Failed != 0 {
		t.Fatalf("successful CLI report=%+v err=%v", report, err)
	}
	persistedRaw, err := os.ReadFile(filepath.Join(dir, "old.raw.json"))
	if err != nil {
		t.Fatal(err)
	}
	persistedNormalized, err := os.ReadFile(filepath.Join(dir, "old.normalized.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persistedRaw), "\"stdout\": \"{\\\"routes\\\":[]}\"") || strings.Contains(string(persistedRaw), "ignored-binary-path") {
		t.Fatalf("raw artifact does not preserve only raw capture: %s", persistedRaw)
	}
	if !strings.Contains(string(persistedNormalized), `"json"`) || strings.Contains(string(persistedNormalized), `"stdout"`) {
		t.Fatalf("normalized artifact is not parsed JSON only: %s", persistedNormalized)
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
	validationErr := errors.New(strings.Join(op.ValidationErrors, "; "))
	dir := t.TempDir()
	report, err := CompareSideResults(
		SideResult{Side: SideOld, Artifact: artifact, ValidationError: validationErr},
		SideResult{Side: SideNew, Artifact: artifact, ValidationError: validationErr}, nil, dir)
	if err == nil {
		t.Fatal("identical API contract regressions must fail")
	}
	if report.Failed != 2 || len(report.Failures) != 2 {
		t.Fatalf("identical API regression report=%+v, want serialized validation failures", report)
	}
	persistedReport, readErr := os.ReadFile(filepath.Join(dir, "compat-report.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(persistedReport), "validation failed") || !strings.Contains(string(persistedReport), "expected HTTP 201, got 418") {
		t.Fatalf("API report lacks validation failure details: %s", persistedReport)
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
	report, err = CompareSideResults(
		SideResult{Side: SideOld, Artifact: artifact},
		SideResult{Side: SideNew, Artifact: changed}, nil, t.TempDir())
	if err != nil || report.Failed != 1 {
		t.Fatalf("status/header/DTO/decode mutation report=%+v err=%v", report, err)
	}
}
