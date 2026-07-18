package app_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/cli"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
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
	// TestRemoteManagementFamilyInventory. A success has an exact JSON schema
	// (or rendered response); an unavailable capability has a typed HTTP status.
	tests := []struct {
		family      string
		args        []string
		output      string
		jsonFields  []string
		errorStatus int
	}{
		{"attachments", []string{"attachments", "list", "--json"}, "", []string{"attachments"}, 0},
		{"autoroute", []string{"autoroute", "allow", "list", "--json"}, "", []string{"domains"}, 0},
		{"backups", []string{"backups", "list", "--json"}, "", nil, 503},
		{"bootstrap", []string{"bootstrap", "new.example.com", "registry.example/app:v1"}, "", nil, 400},
		{"config", []string{"config", "show", "--json"}, "", []string{"routes", "server"}, 0},
		{"deploy", []string{"deploy", "app.example.com", "--json"}, "", []string{"domain", "status"}, 0},
		{"images", []string{"images", "list", "--json"}, "", nil, 503},
		{"logs", []string{"logs", "app.example.com", "--lines", "2"}, "", nil, 0},
		{"networks", []string{"networks", "list", "--json"}, "", []string{"[]"}, 0},
		{"pin", []string{"pin", "list", "pin.example.com", "--json"}, "", nil, 503},
		{"preview", []string{"preview", "list", "--json"}, "", nil, 503},
		// Image build/tag/upload is an intentional client-local workflow. Its
		// Docker/registry credential and manifest boundary is tested in cli;
		// this control test pins the no-local-image preflight contract.
		{"push", []string{"push", "--domain", "pin.example.com", "--tag", "v1", "--no-deploy"}, "", nil, 0},
		{"reload", []string{"reload"}, "Configuration reloaded successfully", nil, 0},
		{"restart", []string{"restart", "app.example.com", "--with-attachments"}, "Restarted", nil, 0},
		{"routes", []string{"routes", "list", "--json"}, "", []string{"[]", "routes", "kind"}, 0},
		{"secrets", []string{"secrets", "list", "app.example.com", "--json"}, "", []string{"keys", "attachments"}, 0},
		{"status", []string{"status", "--json"}, "", []string{"routes", "container_status"}, 0},
		{"tls", []string{"tls", "status", "--json"}, "", []string{"acme_enabled"}, 0},
		{"traffic", []string{"traffic", "status", "--json"}, "", []string{"last_reload_status"}, 0},
		{"volumes", []string{"volumes", "list", "--json"}, "", nil, 503},
	}

	executed := make(map[string]struct{}, len(tests))
	for _, test := range tests {
		executed[test.family] = struct{}{}
		t.Run(test.family, func(t *testing.T) {
			out, err := executeRemote(t, harness, test.args...)
			if test.errorStatus != 0 {
				requireRemoteCLIStatus(t, err, test.errorStatus)
				return
			}
			if test.family == "push" {
				require.Error(t, err)
				require.Contains(t, err.Error(), "local image registry.example/pin not found")
				return
			}
			require.NoError(t, err)
			if len(test.jsonFields) > 0 {
				assertRemoteCLIJSONFields(t, out, test.jsonFields...)
			}
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

// TestControlRoleRemoteManagementMutations proves every supported Cobra
// mutation persists in production config or reaches the runtime. Unsupported
// mutations must return their stable typed capability status without mutation.
func TestControlRoleRemoteManagementMutations(t *testing.T) {
	harness := app.StartControlCLIHarness(t)
	t.Cleanup(func() { harness.Close(t) })

	for _, args := range [][]string{
		{"routes", "add", "cli.example.com", "registry.example/cli:v1"},
		{"routes", "add", "pin.example.com", "registry.example/pin:v1"},
		{"attachments", "add", "cli.example.com", "redis:7"},
		{"secrets", "set", "cli.example.com", "TOKEN=value"},
		{"autoroute", "allow", "add", "*.cli.example.com"},
	} {
		out, err := executeRemote(t, harness, args...)
		require.NoError(t, err, out)
	}

	out, err := executeRemote(t, harness, "routes", "list", "--json")
	require.NoError(t, err)
	assertRemoteCLIJSONFields(t, out, "[]", "routes", "kind")
	require.Contains(t, out, "registry.example/cli:v1")
	out, err = executeRemote(t, harness, "attachments", "list", "cli.example.com", "--json")
	require.NoError(t, err)
	assertRemoteCLIJSONFields(t, out, "target", "images")
	require.Contains(t, out, "redis:7")
	out, err = executeRemote(t, harness, "secrets", "list", "cli.example.com", "--json")
	require.NoError(t, err)
	assertRemoteCLIJSONFields(t, out, "keys", "attachments")
	require.Contains(t, out, "TOKEN")
	out, err = executeRemote(t, harness, "autoroute", "allow", "list", "--json")
	require.NoError(t, err)
	assertRemoteCLIJSONFields(t, out, "domains")
	require.Contains(t, out, "*.cli.example.com")

	beforeRestart := harness.RuntimeRestartCalls()
	out, err = executeRemote(t, harness, "restart", "cli.example.com", "--with-attachments")
	require.NoError(t, err, out)
	harness.WaitForRuntimeRestart(t, beforeRestart+1, true)

	for _, test := range []struct {
		args   []string
		status int
	}{
		{[]string{"volumes", "prune", "--dry-run"}, 503},
		{[]string{"images", "prune", "--dry-run"}, 503},
		{[]string{"attachments", "prune", "--stop"}, 503},
		{[]string{"backups", "run", "app.example.com"}, 503},
		{[]string{"preview", "delete", "preview.cli.example.com"}, 503},
		{[]string{"bootstrap", "new.example.com", "registry.example/app:v1"}, 400},
	} {
		_, err := executeRemote(t, harness, test.args...)
		requireRemoteCLIStatus(t, err, test.status)
	}

	for _, args := range [][]string{
		{"autoroute", "allow", "remove", "*.cli.example.com"},
		{"secrets", "remove", "cli.example.com", "TOKEN", "--force"},
		{"attachments", "remove", "cli.example.com", "redis:7", "--force"},
		{"routes", "remove", "cli.example.com", "--force"},
	} {
		out, err := executeRemote(t, harness, args...)
		require.NoError(t, err, out)
	}
	out, err = executeRemote(t, harness, "autoroute", "allow", "list", "--json")
	require.NoError(t, err)
	require.NotContains(t, out, "*.cli.example.com")
	out, err = executeRemote(t, harness, "routes", "list", "--json")
	require.NoError(t, err)
	require.NotContains(t, out, "cli.example.com")
}

func requireRemoteCLIStatus(t *testing.T, err error, want int) {
	t.Helper()
	var responseErr *remote.HTTPError
	require.ErrorAs(t, err, &responseErr)
	require.Equal(t, want, responseErr.StatusCode)
	require.NotEmpty(t, responseErr.Body)
}

func assertRemoteCLIJSONFields(t *testing.T, output string, fields ...string) {
	t.Helper()
	if len(fields) > 0 && fields[0] == "[]" {
		var value []map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(output), &value), output)
		fields = fields[1:]
		if len(fields) == 0 {
			return
		}
		require.NotEmpty(t, value, output)
		for _, field := range fields {
			raw, ok := value[0][field]
			require.Truef(t, ok, "missing JSON field %q in %s", field, output)
			require.NotEqualf(t, "null", string(raw), "JSON field %q must not be null", field)
		}
		return
	}
	var value map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(output), &value), output)
	for _, field := range fields {
		raw, ok := value[field]
		require.Truef(t, ok, "missing JSON field %q in %s", field, output)
		require.NotEqualf(t, "null", string(raw), "JSON field %q must not be null", field)
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
