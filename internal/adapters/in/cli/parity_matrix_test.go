package cli

import (
	"os"
	"strings"
	"testing"
)

type uiAdoptionExpectation struct {
	family    string
	file      string
	functions []string
}

var uiAdoptionExpectations = []uiAdoptionExpectation{
	{
		family:    "server",
		file:      "serve.go",
		functions: []string{"newStartCmd"},
	},
	{
		family:    "root/server",
		file:      "root.go",
		functions: []string{"newVersionCmd", "runReloadRemote", "runLogsRemote", "streamLogsRemote", "showContainerLogsLocal"},
	},
	{
		family:    "backups",
		file:      "backup.go",
		functions: []string{"newBackupListCmd", "newBackupRunCmd", "newBackupDetectCmd", "newBackupStatusCmd"},
	},
	{
		family:    "images",
		file:      "images.go",
		functions: []string{"runImagesList", "runImagesPrune"},
	},
}

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

func TestUIAdoptionMatrixCoverage(t *testing.T) {
	for _, expect := range uiAdoptionExpectations {
		expect := expect
		t.Run(expect.family, func(t *testing.T) {
			if len(expect.functions) == 0 {
				t.Fatalf("ui adoption matrix for %s is empty", expect.family)
			}

			content, err := os.ReadFile(expect.file)
			if err != nil {
				t.Fatalf("failed to read %s: %v", expect.file, err)
			}

			source := string(content)
			for _, fn := range expect.functions {
				if !strings.Contains(source, "func "+fn+"(") {
					t.Fatalf("ui adoption matrix references missing function %s in %s", fn, expect.file)
				}
			}
		})
	}
}
