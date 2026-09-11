package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
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
		family:    "root/server",
		file:      "root.go",
		functions: []string{"newVersionCmd", "runProcessLogs", "runContainerLogs"},
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

func TestDaemonCommandResolverCoverage(t *testing.T) {
	checks := []struct {
		command string
		file    string
		call    string
	}{
		{command: "status", file: "status.go", call: "resolveControlPlane("},
		{command: "traffic status", file: "traffic_status.go", call: "trafficResolveControlPlane("},
		{command: "config", file: "config.go", call: "resolveControlPlane("},
		{command: "volumes", file: "volumes.go", call: "resolveControlPlane("},
		{command: "networks", file: "networks.go", call: "resolveControlPlane("},
		{command: "backup", file: "backup.go", call: "resolveControlPlane"},
		{command: "secrets", file: "secrets.go", call: "resolveControlPlaneForDomain("},
		{command: "images list/prune", file: "images.go", call: "resolveDaemonClient("},
		{command: "images tags", file: "images.go", call: "resolveControlPlaneForRepository("},
		{command: "push", file: "push.go", call: "resolveControlPlane("},
	}

	for _, check := range checks {
		t.Run(check.command, func(t *testing.T) {
			content, err := os.ReadFile(check.file)
			if err != nil {
				t.Fatalf("read %s: %v", check.file, err)
			}
			if !strings.Contains(string(content), check.call) {
				t.Fatalf("%s must dispatch through shared daemon resolver %q", check.command, check.call)
			}
		})
	}
}

func TestLocalParityMatrix(t *testing.T) {
	checks := []struct {
		file       string
		legacyText string
		command    string
	}{
		{file: "push.go", legacyText: "push requires remote mode", command: "gordon push"},
		// Guards against reintroducing the old rollback-only remote-mode error text during the command rename.
		{file: "backup.go", legacyText: "backup commands require a configured remote target", command: "gordon backups"},
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
		t.Run(expect.family, func(t *testing.T) {
			if len(expect.functions) == 0 {
				t.Fatalf("ui adoption matrix for %s is empty", expect.family)
			}

			fset := token.NewFileSet()
			fileNode, err := parser.ParseFile(fset, expect.file, nil, parser.AllErrors)
			if err != nil {
				t.Fatalf("failed to parse %s: %v", expect.file, err)
			}

			found := make(map[string]bool, len(expect.functions))
			ast.Inspect(fileNode, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok {
					return true
				}
				found[fn.Name.Name] = true
				return true
			})

			for _, fn := range expect.functions {
				if !found[fn] {
					t.Fatalf("ui adoption matrix references missing function %s in %s", fn, expect.file)
				}
			}
		})
	}
}
