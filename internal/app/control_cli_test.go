package app_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/cli"
	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/testutils/remotemanagement"
)

// TestControlRoleRemoteManagementCommands is the authoritative Cobra gate for
// the remote management inventory. Unlike the lightweight cli package smoke,
// every command below talks to the production split-control HTTP listener.
func TestControlRoleRemoteManagementCommands(t *testing.T) {
	harness := app.StartControlCLIHarness(t)
	t.Cleanup(func() { harness.Close(t) })

	// Pin requires a fully qualified route image; provision it through the
	// production route mutation rather than weakening its image preflight.
	_, err := executeRemote(t, harness, "routes", "add", "pin.example.com", "registry.example/pin:v1")
	require.NoError(t, err)

	// One executable command for each family registered by
	// TestRemoteManagementFamilyInventory. Stable 501/503 responses identify
	// deliberately absent split-runtime capabilities rather than a fake handler.
	tests := []struct {
		family string
		args   []string
		output string
		err    string
	}{
		{"attachments", []string{"attachments", "list", "--json"}, "", ""},
		{"autoroute", []string{"autoroute", "allow", "list", "--json"}, `"domains"`, ""},
		{"backups", []string{"backups", "list", "--json"}, "", "503"},
		{"bootstrap", []string{"bootstrap", "new.example.com", "registry.example/app:v1"}, "", "400"},
		{"config", []string{"config", "show", "--json"}, `"routes"`, ""},
		{"deploy", []string{"deploy", "app.example.com", "--json"}, `"domain"`, ""},
		{"images", []string{"images", "list", "--json"}, "", "503"},
		{"logs", []string{"logs", "app.example.com", "--lines", "2"}, "", ""},
		{"networks", []string{"networks", "list", "--json"}, `[]`, ""},
		{"pin", []string{"pin", "list", "pin.example.com", "--json"}, "", "503"},
		{"preview", []string{"preview", "list", "--json"}, "", "503"},
		{"push", []string{"push", "--domain", "pin.example.com", "--tag", "v1", "--no-deploy"}, "", ""},
		{"reload", []string{"reload"}, "Configuration reloaded successfully", ""},
		{"restart", []string{"restart", "app.example.com", "--with-attachments"}, "Restarted", ""},
		{"routes", []string{"routes", "list", "--json"}, `"app.example.com"`, ""},
		{"secrets", []string{"secrets", "list", "app.example.com", "--json"}, `"keys"`, ""},
		{"status", []string{"status", "--json"}, `"routes"`, ""},
		{"tls", []string{"tls", "status", "--json"}, `"acme_enabled"`, ""},
		{"traffic", []string{"traffic", "status", "--json"}, `"last_reload_status"`, ""},
		{"volumes", []string{"volumes", "list", "--json"}, "", "503"},
	}

	executed := make(map[string]struct{}, len(tests))
	for _, test := range tests {
		executed[test.family] = struct{}{}
		t.Run(test.family, func(t *testing.T) {
			out, err := executeRemote(t, harness, test.args...)
			if test.err != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), test.err)
				return
			}
			// Push intentionally uses its non-mutating local-image preflight;
			// all other successful commands must render their actual response.
			if test.family == "push" {
				require.Error(t, err)
				require.Contains(t, err.Error(), "local image registry.example/pin not found")
				return
			}
			require.NoError(t, err)
			if test.output != "" {
				require.Contains(t, out, test.output)
			}
		})
	}
	for _, family := range remotemanagement.Families {
		_, ok := executed[family]
		require.Truef(t, ok, "missing production Cobra case for remote family %q", family)
	}
	harness.WaitForRuntimeDeploy(t, 1) // deploy traverses the runtime command port.
}

// TestControlRoleRemoteManagementMutations proves Cobra mutations persist in
// the real control config and that intentionally unsupported mutation endpoints
// retain explicit public statuses.
func TestControlRoleRemoteManagementMutations(t *testing.T) {
	harness := app.StartControlCLIHarness(t)
	t.Cleanup(func() { harness.Close(t) })

	for _, args := range [][]string{
		{"routes", "add", "cli.example.com", "registry.example/cli:v1"},
		{"routes", "add", "pin.example.com", "registry.example/pin:v1"},
		{"attachments", "add", "cli.example.com", "redis:7"},
		{"secrets", "set", "cli.example.com", "TOKEN=value"},
	} {
		out, err := executeRemote(t, harness, args...)
		require.NoError(t, err, out)
	}

	out, err := executeRemote(t, harness, "routes", "list", "--json")
	require.NoError(t, err)
	require.Contains(t, out, "registry.example/cli:v1")
	out, err = executeRemote(t, harness, "attachments", "list", "cli.example.com", "--json")
	require.NoError(t, err)
	require.Contains(t, out, "redis:7")
	out, err = executeRemote(t, harness, "secrets", "list", "cli.example.com", "--json")
	require.NoError(t, err)
	require.Contains(t, out, "TOKEN")

	for _, args := range [][]string{
		{"volumes", "prune", "--dry-run"},
		{"images", "prune", "--dry-run"},
	} {
		_, err := executeRemote(t, harness, args...)
		require.Error(t, err)
		require.True(t, strings.Contains(err.Error(), "501") || strings.Contains(err.Error(), "503"), err)
	}

	for _, args := range [][]string{
		{"secrets", "remove", "cli.example.com", "TOKEN", "--force"},
		{"attachments", "remove", "cli.example.com", "redis:7", "--force"},
		{"routes", "remove", "cli.example.com", "--force"},
	} {
		out, err := executeRemote(t, harness, args...)
		require.NoError(t, err, out)
	}
}

func executeRemote(t *testing.T, harness *app.ControlCLIHarness, args ...string) (string, error) {
	t.Helper()
	root := cli.NewRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"--remote", harness.URL, "--token", harness.Token}, args...))
	err := root.Execute()
	return out.String() + errOut.String(), err
}
