package compatoldnew

import "testing"

func TestCompareExactAndPresence(t *testing.T) {
	if got := Compare(NewCLIArtifact("cmd", "same", LevelExact), NewCLIArtifact("cmd", "same", LevelExact), nil); len(got) != 0 {
		t.Fatalf("unexpected failures: %#v", got)
	}
	if got := Compare(NewHTTPArtifact("/v1", "", LevelPresence), NewHTTPArtifact("/v1", "body", LevelPresence), nil); len(got) != 1 {
		t.Fatalf("presence mismatch not reported")
	}
}

func TestCompareFailureIncludesDebugCommand(t *testing.T) {
	got := Compare(NewHTTPArtifact("http://example.test", "old", LevelSemantic), NewHTTPArtifact("http://example.test", "new", LevelSemantic), nil)
	if len(got) != 1 || got[0].OldValue == nil || got[0].NewValue == nil || got[0].Source == "" || got[0].SuggestedCommand == "" {
		t.Fatalf("bad failure: %#v", got)
	}
}

func TestCompareMetadataMismatch(t *testing.T) {
	cases := []struct {
		name string
		oldA Artifact
		newA Artifact
	}{
		{name: "type", oldA: NewCLIArtifact("cmd", "same", LevelExact), newA: NewHTTPArtifact("cmd", "same", LevelExact)},
		{name: "source", oldA: NewCLIArtifact("old", "same", LevelExact), newA: NewCLIArtifact("new", "same", LevelExact)},
		{name: "level", oldA: NewCLIArtifact("cmd", "same", LevelExact), newA: NewCLIArtifact("cmd", "same", LevelSemantic)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Compare(tc.oldA, tc.newA, nil)
			if len(got) != 1 || got[0].Source == "" || got[0].Level == "" || got[0].SuggestedCommand == "" || got[0].Problem == "" {
				t.Fatalf("bad metadata failure: %#v", got)
			}
		})
	}
}

func TestCompareAllowlistedDifference(t *testing.T) {
	allow := &AllowlistedDifference{Reason: "phase split", SpecSection: "phase4"}
	got := Compare(NewCLIArtifact("cmd", "old", LevelAllowlistedDifference), NewCLIArtifact("cmd", "new", LevelAllowlistedDifference), allow)
	if len(got) != 0 {
		t.Fatalf("allowlisted diff failed: %#v", got)
	}
}

func TestCompareAllowlistedDifferenceRequiresReasonAndSpecSection(t *testing.T) {
	cases := []*AllowlistedDifference{
		{Reason: "phase split"},
		{SpecSection: "phase4"},
		nil,
	}
	for _, allow := range cases {
		got := Compare(NewCLIArtifact("cmd", "old", LevelAllowlistedDifference), NewCLIArtifact("cmd", "new", LevelAllowlistedDifference), allow)
		if len(got) != 1 || got[0].Problem == "" {
			t.Fatalf("missing allowlist problem for %#v: %#v", allow, got)
		}
	}
}
