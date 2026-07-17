package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

func TestRootRegistersComponentTokenCommands(t *testing.T) {
	cmd, _, err := NewRootCmd().Find([]string{"auth", "component-token", "create"})
	require.NoError(t, err)
	assert.Equal(t, "create", cmd.Name())

	cmd, _, err = NewRootCmd().Find([]string{"auth", "component-token", "list"})
	require.NoError(t, err)
	assert.Equal(t, "list", cmd.Name())

	cmd, _, err = NewRootCmd().Find([]string{"auth", "component-token", "revoke"})
	require.NoError(t, err)
	assert.Equal(t, "revoke", cmd.Name())
}

func TestComponentTokenCommands_CreateListRevoke(t *testing.T) {
	dataDir := t.TempDir()
	configPath := writeComponentTokenCLIConfig(t, dataDir)

	var createOut bytes.Buffer
	create := newComponentTokenCmd()
	create.SetArgs([]string{"create", "--config", configPath, "--name", "runtime-a", "--role", "runtime", "--scope", "runtime:deploy,runtime:logs", "--scope", "runtime:status"})
	create.SetOut(&createOut)
	require.NoError(t, create.ExecuteContext(context.Background()))

	output := createOut.String()
	require.Contains(t, output, "gordon_component.")
	assert.Equal(t, 1, strings.Count(output, "gordon_component."), "plaintext token must be printed once")
	assert.Contains(t, output, "cannot be retrieved again")
	assert.NotContains(t, output, "token_hash")

	var listOut bytes.Buffer
	list := newComponentTokenCmd()
	list.SetArgs([]string{"list", "--config", configPath, "--json"})
	list.SetOut(&listOut)
	require.NoError(t, list.ExecuteContext(context.Background()))

	var listed []map[string]any
	require.NoError(t, json.Unmarshal(listOut.Bytes(), &listed))
	require.Len(t, listed, 1)
	assert.Equal(t, "runtime-a", listed[0]["name"])
	assert.NotContains(t, listOut.String(), "gordon_component.")
	assert.NotContains(t, listOut.String(), "token_hash")
	keyID, ok := listed[0]["key_id"].(string)
	require.True(t, ok)

	var revokeOut bytes.Buffer
	revoke := newComponentTokenCmd()
	revoke.SetArgs([]string{"revoke", "--config", configPath, keyID})
	revoke.SetOut(&revokeOut)
	require.NoError(t, revoke.ExecuteContext(context.Background()))
	assert.Contains(t, revokeOut.String(), keyID)

	var textListOut bytes.Buffer
	textList := newComponentTokenCmd()
	textList.SetArgs([]string{"list", "--config", configPath})
	textList.SetOut(&textListOut)
	require.NoError(t, textList.ExecuteContext(context.Background()))
	assert.Contains(t, textListOut.String(), "Revoked:")
	assert.NotContains(t, textListOut.String(), "gordon_component.")
	assert.NotContains(t, textListOut.String(), "token_hash")
}

func TestComponentTokenCreate_JSONDoesNotExposeHash(t *testing.T) {
	configPath := writeComponentTokenCLIConfig(t, t.TempDir())
	var out bytes.Buffer
	cmd := newComponentTokenCmd()
	cmd.SetArgs([]string{"create", "--config", configPath, "--name", "edge-a", "--role", "edge", "--json"})
	cmd.SetOut(&out)

	require.NoError(t, cmd.ExecuteContext(context.Background()))
	assert.Equal(t, 1, strings.Count(out.String(), "gordon_component."))
	assert.Contains(t, out.String(), "cannot be retrieved again")
	assert.NotContains(t, out.String(), "token_hash")
}

func TestComponentTokenCreate_RejectsInvalidArguments(t *testing.T) {
	configPath := writeComponentTokenCLIConfig(t, t.TempDir())
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "role", args: []string{"create", "--config", configPath, "--name", "x", "--role", "unknown"}, want: "unknown component role"},
		{name: "scope", args: []string{"create", "--config", configPath, "--name", "x", "--role", "edge", "--scope", "runtime:deploy"}, want: "not allowed"},
		{name: "expiry", args: []string{"create", "--config", configPath, "--name", "x", "--role", "edge", "--expiry", "-1h"}, want: "expiry must be positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newComponentTokenCmd()
			cmd.SetArgs(tc.args)
			assert.ErrorContains(t, cmd.ExecuteContext(context.Background()), tc.want)
		})
	}
}

func TestComponentTokenCommands_ReturnBackendErrors(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(dataDir, []byte("x"), 0600))
	configPath := writeComponentTokenCLIConfig(t, dataDir)
	cmd := newComponentTokenCmd()
	cmd.SetArgs([]string{"create", "--config", configPath, "--name", "edge-a", "--role", "edge"})

	assert.ErrorContains(t, cmd.ExecuteContext(context.Background()), "create component token")
}

func writeComponentTokenCLIConfig(t *testing.T, dataDir string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "gordon.yaml")
	contents := "server:\n  data_dir: " + dataDir + "\nauth:\n  secrets_backend: unsafe\n"
	require.NoError(t, os.WriteFile(configPath, []byte(contents), 0600))
	return configPath
}

func TestRunAuthLoginWithToken_StoresToken(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PATH", filepath.Join("/nonexistent", "pass-disabled"))
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	config := &remote.ClientConfig{
		Active: "prod",
		Remotes: map[string]remote.RemoteEntry{
			"prod": {URL: server.URL},
		},
	}
	assert.NoError(t, remote.SaveRemotes(remote.DefaultRemotesPath(), config))

	// Set global flag to target "prod" remote
	origRemote := remoteFlag
	remoteFlag = "prod"
	t.Cleanup(func() { remoteFlag = origRemote })

	var out bytes.Buffer
	err := runAuthLoginWithToken(context.Background(), "token123", &out)
	assert.NoError(t, err)

	loaded, err := remote.LoadRemotes("")
	assert.NoError(t, err)
	assert.Equal(t, "token123", loaded.Remotes["prod"].Token)
}
