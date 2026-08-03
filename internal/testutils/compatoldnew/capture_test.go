package compatoldnew

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureCommandPreservesProcessFieldsAndRedactsMetadata(t *testing.T) {
	request := CommandCaptureRequest{
		BinaryPath:   os.Args[0],
		Args:         []string{"-test.run=TestCaptureCommandHelper"},
		Env:          []string{"GO_WANT_CAPTURE_HELPER=1"},
		SensitiveEnv: []SensitiveEnvironment{{Side: SideOld, Key: "GORDON_AUTH_TOKEN_SECRET", Value: "super-secret"}},
		Source:       "gordon --token super-secret routes list --json",
		Level:        LevelSemantic,
	}
	artifact, err := CaptureCommand(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := artifact.RawValue().(map[string]any)
	if !ok {
		t.Fatalf("raw=%T", artifact.RawValue())
	}
	if raw["exitCode"] != float64(7) && raw["exitCode"] != 7 {
		t.Fatalf("exitCode=%#v", raw["exitCode"])
	}
	if raw["stdout"] != "capture stdout\n" || raw["stderr"] != "capture stderr\n" {
		t.Fatalf("process fields lost: %#v", raw)
	}
	if strings.Contains(artifact.Source(), "super-secret") || strings.Contains(raw["environment"].(map[string]string)["ADMIN_TOKEN"], "super-secret") || strings.Contains(strings.Join(raw["args"].([]string), " "), "super-secret") {
		t.Fatalf("secret leaked in capture metadata: %#v", raw)
	}
	result, err := ExecuteSide(context.Background(), SideOld, request)
	if err != nil || result.Side != SideOld || result.Artifact.ArtifactType() != "cli" {
		t.Fatalf("side execution result=%+v err=%v", result, err)
	}
}

func TestCaptureCommandDoesNotInheritAmbientEnvironmentAndRedactsSensitiveOutput(t *testing.T) {
	t.Setenv("COMPAT_AMBIENT_CANARY", "must-not-reach-child")
	request := CommandCaptureRequest{
		BinaryPath:   os.Args[0],
		Args:         []string{"-test.run=TestCaptureCommandHelper"},
		Env:          []string{"GO_WANT_CAPTURE_HELPER=canary"},
		SensitiveEnv: []SensitiveEnvironment{{Side: SideOld, Key: "GORDON_AUTH_TOKEN_SECRET", Value: "provided-sensitive-fixture"}},
	}
	artifact, err := CaptureCommand(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	raw := artifact.RawValue().(map[string]any)
	output := raw["stdout"].(string) + raw["stderr"].(string)
	if strings.Contains(output, "must-not-reach-child") || !strings.Contains(output, "ambient=") {
		t.Fatalf("ambient environment leaked into child: %q", output)
	}
	if strings.Contains(output, "provided-sensitive-fixture") || !strings.Contains(output, "<redacted>") {
		t.Fatalf("sensitive fixture was retained in captured output: %q", output)
	}
	dir := t.TempDir()
	if _, err := CompareSideResults(SideResult{Side: SideOld, Artifact: artifact}, SideResult{Side: SideNew, Artifact: artifact}, nil, dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"old.raw.json", "new.raw.json", "old.normalized.json", "new.normalized.json", "compat-report.json", "normalized.diff"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "provided-sensitive-fixture") {
			t.Fatalf("sensitive fixture persisted in %s: %s", name, body)
		}
	}
}

func TestCaptureCommandHelper(t *testing.T) {
	switch os.Getenv("GO_WANT_CAPTURE_HELPER") {
	case "1":
		_, _ = os.Stdout.WriteString("capture stdout\n")
		_, _ = os.Stderr.WriteString("capture stderr\n")
		os.Exit(7)
	case "canary":
		_, _ = os.Stdout.WriteString("ambient=" + os.Getenv("COMPAT_AMBIENT_CANARY") + " secret=" + os.Getenv("GORDON_AUTH_TOKEN_SECRET") + "\n")
		os.Exit(0)
	}
}

func TestCaptureArtifactTypes(t *testing.T) {
	arts := []Artifact{
		NewCLIArtifact("gordon ps", "out", LevelExact),
		NewHTTPArtifact("/health", map[string]any{"ok": true}, LevelSemantic),
		NewRegistryArtifact("registry", "r", LevelExact),
		NewProxyArtifact("proxy", "p", LevelExact),
		NewRuntimeArtifact("runtime", "rt", LevelExact),
		NewConfigArtifact("config", "c", LevelExact),
		NewLogArtifact("log", "l", LevelExact),
		NewMigrationArtifact("migration", "m", LevelExact),
	}
	for _, a := range arts {
		if a.ArtifactType() == "" || a.Source() == "" || a.RawValue() == nil || a.NormalizedValue() == nil {
			t.Fatalf("incomplete artifact %#v", a)
		}
	}
}
