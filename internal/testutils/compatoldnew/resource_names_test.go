package compatoldnew

import (
	"regexp"
	"strings"
	"testing"
)

func TestResourceNamesRunIDSanitizesAndIsUnique(t *testing.T) {
	id := RunID("Test Fixture/With_UPPER punctuation!!!")
	if !strings.HasPrefix(id, "test-fixture-with-upper-punctuatio") {
		t.Fatalf("RunID prefix = %q", id)
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]+[a-z0-9]$`).MatchString(id) {
		t.Fatalf("RunID has unsafe characters: %q", id)
	}
	if id == RunID("Test Fixture/With_UPPER punctuation!!!") {
		t.Fatalf("RunID should include unique suffix")
	}
}

func TestResourceNamesEmptyFixtureLabelIsSanitized(t *testing.T) {
	labels := ResourceLabels("RUN!!!", "OLD", "!!!")
	if labels[LabelRun] != "run" || labels[LabelSide] != "old" || labels[LabelFixture] != "" {
		t.Fatalf("labels = %#v", labels)
	}
}

func TestResourceNamesPrefixesUsePracticalLimits(t *testing.T) {
	runID := RunID(strings.Repeat("ABC_", 80))
	for _, got := range []string{ContainerPrefix(runID, SideOld), NetworkPrefix(runID, SideNew), VolumePrefix(runID, "SIDE!!!")} {
		if len(got) > maxResourceNameLen {
			t.Fatalf("%q length = %d, want <= %d", got, len(got), maxResourceNameLen)
		}
		if !strings.HasPrefix(got, "gordon-compat-") {
			t.Fatalf("prefix %q missing harness prefix", got)
		}
		if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]+[a-z0-9]$`).MatchString(got) {
			t.Fatalf("prefix has unsafe characters: %q", got)
		}
	}
}
