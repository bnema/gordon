package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetInternalCredentialsFindsRuntimeFile(t *testing.T) {
	// Simulate daemon writing to XDG_RUNTIME_DIR/gordon/ while
	// CLI has no XDG_RUNTIME_DIR set.
	tmpRuntime := t.TempDir()
	gordonRuntime := filepath.Join(tmpRuntime, "gordon")
	if err := os.MkdirAll(gordonRuntime, 0700); err != nil {
		t.Fatal(err)
	}

	// Write "live" creds to the runtime dir (as daemon would)
	liveCreds := `{"username":"gordon-internal","password":"livepassword"}`
	if err := os.WriteFile(filepath.Join(gordonRuntime, "internal-creds.json"), []byte(liveCreds), 0600); err != nil {
		t.Fatal(err)
	}

	// Also write a stale creds file to home fallback
	tmpHome := t.TempDir()
	gordonHome := filepath.Join(tmpHome, ".gordon", "run")
	if err := os.MkdirAll(gordonHome, 0700); err != nil {
		t.Fatal(err)
	}
	staleCreds := `{"username":"gordon-internal","password":"stalepassword"}`
	if err := os.WriteFile(filepath.Join(gordonHome, "internal-creds.json"), []byte(staleCreds), 0600); err != nil {
		t.Fatal(err)
	}

	creds, err := GetInternalCredentialsFromCandidates([]string{
		filepath.Join(gordonRuntime, "internal-creds.json"),
		filepath.Join(gordonHome, "internal-creds.json"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.Password != "livepassword" {
		t.Errorf("got password %q, want %q", creds.Password, "livepassword")
	}
}

func TestGetInternalCredentialsFallsBackToStale(t *testing.T) {
	// If only the stale/home file exists, it should still work
	tmpHome := t.TempDir()
	gordonHome := filepath.Join(tmpHome, ".gordon", "run")
	if err := os.MkdirAll(gordonHome, 0700); err != nil {
		t.Fatal(err)
	}
	staleCreds := `{"username":"gordon-internal","password":"stalepassword"}`
	if err := os.WriteFile(filepath.Join(gordonHome, "internal-creds.json"), []byte(staleCreds), 0600); err != nil {
		t.Fatal(err)
	}

	creds, err := GetInternalCredentialsFromCandidates([]string{
		filepath.Join(t.TempDir(), "gordon", "internal-creds.json"), // doesn't exist
		filepath.Join(gordonHome, "internal-creds.json"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.Password != "stalepassword" {
		t.Errorf("got password %q, want %q", creds.Password, "stalepassword")
	}
}

func TestGetInternalCredentialsNoCandidatesReturnsError(t *testing.T) {
	_, err := GetInternalCredentialsFromCandidates([]string{
		filepath.Join(t.TempDir(), "nonexistent", "internal-creds.json"),
	})
	if err == nil {
		t.Fatal("expected error when no credentials found, got nil")
	}
}
