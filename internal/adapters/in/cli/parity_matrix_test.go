package cli

import (
	"os"
	"strings"
	"testing"
)

func TestLocalParityMatrix(t *testing.T) {
	checks := []struct {
		file       string
		legacyText string
		command    string
	}{
		{file: "push.go", legacyText: "push requires remote mode", command: "gordon push"},
		{file: "rollback.go", legacyText: "rollback requires remote mode", command: "gordon rollback"},
		{file: "backup.go", legacyText: "backup commands require a configured remote target", command: "gordon backups"},
		{file: "routes.go", legacyText: "status command requires --remote flag or GORDON_REMOTE env var", command: "gordon status"},
		{file: "restart.go", legacyText: "local restart does not support --with-attachments; use --remote", command: "gordon restart --with-attachments"},
	}

	for _, check := range checks {
		content, err := os.ReadFile(check.file)
		if err != nil {
			t.Fatalf("failed to read %s: %v", check.file, err)
		}
		if strings.Contains(string(content), check.legacyText) {
			t.Fatalf("local parity gap: %s still blocks local mode (%q found in %s)", check.command, check.legacyText, check.file)
		}
	}
}
