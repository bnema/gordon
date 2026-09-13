package version

import "testing"

func TestSetStoresAllBuildInfo(t *testing.T) {
	previousVersion, previousCommit, previousDate, previousDirty := Version(), Commit(), BuildDate(), Dirty()
	t.Cleanup(func() { Set(previousVersion, previousCommit, previousDate, previousDirty) })

	Set("v1.2.3", "abc1234", "2026-09-13", "true")

	if got := Version(); got != "v1.2.3" {
		t.Errorf("Version() = %q, want %q", got, "v1.2.3")
	}
	if got := Commit(); got != "abc1234" {
		t.Errorf("Commit() = %q, want %q", got, "abc1234")
	}
	if got := BuildDate(); got != "2026-09-13" {
		t.Errorf("BuildDate() = %q, want %q", got, "2026-09-13")
	}
	if got := Dirty(); got != "true" {
		t.Errorf("Dirty() = %q, want %q", got, "true")
	}
}
