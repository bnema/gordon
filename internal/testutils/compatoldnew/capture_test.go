package compatoldnew

import "testing"

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
