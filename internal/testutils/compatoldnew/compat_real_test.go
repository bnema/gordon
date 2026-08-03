package compatoldnew

import (
	"os"
	"testing"
)

func realCompatibilityRunEnabled() bool {
	return os.Getenv("GORDON_COMPAT_RUN_REAL") == "1"
}

func requireRealCompatibilityRun(t *testing.T) {
	t.Helper()
	if !realCompatibilityRunEnabled() {
		t.Skip("set GORDON_COMPAT_RUN_REAL=1 to run real old/new compatibility scenarios")
	}
}

func TestRealCompatibilityRunEnabledRequiresExplicitOne(t *testing.T) {
	t.Setenv("GORDON_COMPAT_RUN_REAL", "")
	if realCompatibilityRunEnabled() {
		t.Fatal("unset real-run gate must not run compatibility scenarios")
	}

	t.Setenv("GORDON_COMPAT_RUN_REAL", "true")
	if realCompatibilityRunEnabled() {
		t.Fatal("only an explicit value of 1 may enable compatibility scenarios")
	}

	t.Setenv("GORDON_COMPAT_RUN_REAL", "1")
	if !realCompatibilityRunEnabled() {
		t.Fatal("explicit real-run gate must enable compatibility scenarios")
	}
}
