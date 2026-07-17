package compatoldnew

import "testing"

func TestNormalizeDynamicValues(t *testing.T) {
	got := Normalize(`{"id":"abcdef1234567890","at":"2026-07-02T12:34:56Z","url":"localhost:49153","tmp":"/tmp/gordon-123/file","took":"15.2ms"}`)
	want := `{"at":"<timestamp>","id":"<container-id>","tmp":"<tmp-path>","took":"<duration>","url":"localhost:<port>"}`
	if got != want {
		t.Fatalf("Normalize()=%v want %v", got, want)
	}
}

func TestNormalizeRouteMapsAreUnorderedButMeaningfulFieldsRemain(t *testing.T) {
	old := NewCLIArtifact("gordon routes list --json", map[string]any{"routes": []any{map[string]any{"domain": "b.test", "image": "b"}, map[string]any{"domain": "a.test", "image": "a"}}, "exitCode": 0}, LevelSemantic)
	new := NewCLIArtifact("gordon routes list --json", map[string]any{"routes": []any{map[string]any{"domain": "a.test", "image": "a"}, map[string]any{"domain": "b.test", "image": "b"}}, "exitCode": 1}, LevelSemantic)
	fails := Compare(old, new, nil)
	if len(fails) != 1 {
		t.Fatalf("route map order or exit difference was normalized incorrectly: %#v", fails)
	}
}

func TestNormalizeOnlySortsRouteCollections(t *testing.T) {
	routesA := NewCLIArtifact("gordon routes list --json", map[string]any{"routes": []any{"b", "a"}}, LevelSemantic)
	routesB := NewCLIArtifact("gordon routes list --json", map[string]any{"routes": []any{"a", "b"}}, LevelSemantic)
	if fails := Compare(routesA, routesB, nil); len(fails) != 0 {
		t.Fatalf("route collection order should be ignored: %#v", fails)
	}

	namesA := NewCLIArtifact("gordon routes list --json", map[string]any{"names": []any{"b", "a"}}, LevelSemantic)
	namesB := NewCLIArtifact("gordon routes list --json", map[string]any{"names": []any{"a", "b"}}, LevelSemantic)
	if fails := Compare(namesA, namesB, nil); len(fails) != 1 {
		t.Fatalf("non-route collection order was normalized incorrectly: %#v", fails)
	}
}

func TestNormalizeForbiddenPreservesMeaningfulDiffs(t *testing.T) {
	old := Normalize(map[string]any{"statusCode": 200, "exitCode": 0, "labels": map[string]any{"app": "old"}, "networks": []any{"blue"}, "envHash": "aaa", "domain": "old.example", "digest": "sha256:old", "deletedField": "present"})
	new := Normalize(map[string]any{"statusCode": 500, "exitCode": 1, "labels": map[string]any{"app": "new"}, "networks": []any{"green"}, "envHash": "bbb", "domain": "new.example", "digest": "sha256:new"})
	fails := Compare(NewCLIArtifact("gordon test", old, LevelSemantic), NewCLIArtifact("gordon test", new, LevelSemantic), nil)
	if len(fails) != 1 {
		t.Fatalf("forbidden diff normalized away: %#v vs %#v", old, new)
	}
}

func TestNormalizePlainStringPreservesDigestAndEnvHash(t *testing.T) {
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	envHash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	got := Normalize("pulled " + digest + " envHash=" + envHash + " image cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc container abcdef123456")
	want := "pulled " + digest + " envHash=" + envHash + " image <image-id> container <container-id>"
	if got != want {
		t.Fatalf("Normalize()=%v want %v", got, want)
	}
}
