package compatoldnew

import (
	"context"
	"os"
	"testing"
)

func TestPodmanAvailableReportsMissingBinaryActionably(t *testing.T) {
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", "")
	err := PodmanAvailable(context.Background())
	if err == nil {
		t.Fatal("PodmanAvailable unexpectedly succeeded with empty PATH")
	}
	if got := err.Error(); got == "" || !containsAll(got, "podman unavailable", "binary not found") {
		t.Fatalf("unactionable error: %v", err)
	}
	t.Setenv("PATH", oldPath)
}

func TestPodmanCleanupRejectsEmptyRunID(t *testing.T) {
	if err := CleanupRunResources(context.Background(), "!!!"); err == nil {
		t.Fatal("CleanupRunResources accepted empty sanitized runID")
	}
}

func TestPodmanHarnessResourceRequiresLabelsAndPrefix(t *testing.T) {
	runID := "test-run"
	good := PodmanResource{Name: ContainerPrefix(runID, SideOld) + "-web", Labels: ResourceLabels(runID, SideOld, "web")}
	if !isHarnessResource(good, runID) {
		t.Fatal("expected harness resource")
	}
	badLabel := good
	badLabel.Labels = map[string]string{LabelRun: runID, LabelSide: SideOld}
	if isHarnessResource(badLabel, runID) {
		t.Fatal("resource without fixture label must not be cleaned")
	}
	badPrefix := good
	badPrefix.Name = "unrelated-" + good.Name
	if isHarnessResource(badPrefix, runID) {
		t.Fatal("resource without harness prefix must not be cleaned")
	}
}

func TestPodmanSmoke(t *testing.T) {
	if os.Getenv("GORDON_COMPAT_PODMAN") == "" {
		t.Skip("Podman smoke test skipped: set GORDON_COMPAT_PODMAN=1 to enable live podman CLI checks")
	}
	RequirePodman(t)
	ctx := context.Background()
	runID := RunID(t.Name())
	if _, err := InspectContainers(ctx, runID); err != nil {
		t.Fatalf("InspectContainers failed: %v", err)
	}
	if _, err := InspectNetworks(ctx, runID); err != nil {
		t.Fatalf("InspectNetworks failed: %v", err)
	}
	if _, err := InspectVolumes(ctx, runID); err != nil {
		t.Fatalf("InspectVolumes failed: %v", err)
	}
	if err := CleanupRunResources(ctx, runID); err != nil {
		t.Fatalf("CleanupRunResources failed: %v", err)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if !stringsContains(s, part) {
			return false
		}
	}
	return true
}

func stringsContains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return substr == ""
}
