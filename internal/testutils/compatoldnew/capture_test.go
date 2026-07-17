package compatoldnew

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestCaptureCommandPreservesProcessFieldsAndRedactsMetadata(t *testing.T) {
	request := CommandCaptureRequest{
		BinaryPath: os.Args[0],
		Args:       []string{"-test.run=TestCaptureCommandHelper"},
		Env:        []string{"GO_WANT_CAPTURE_HELPER=1", "ADMIN_TOKEN=super-secret"},
		Source:     "gordon --token super-secret routes list --json",
		Level:      LevelSemantic,
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

func TestCaptureCommandHelper(t *testing.T) {
	if os.Getenv("GO_WANT_CAPTURE_HELPER") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString("capture stdout\n")
	_, _ = os.Stderr.WriteString("capture stderr\n")
	os.Exit(7)
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
