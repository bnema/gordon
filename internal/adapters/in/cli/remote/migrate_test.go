package remote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateClientConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	gordonToml := filepath.Join(tmpDir, "gordon", "gordon.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(gordonToml), 0o700))
	require.NoError(t, os.WriteFile(gordonToml, []byte(`
[client]
mode = "remote"
remote = "https://old.example.com"
token = "oldtok"
insecure_tls = true
`), 0o600))

	config, err := LoadRemotes("")
	require.NoError(t, err)

	entry, ok := config.Remotes["default"]
	require.True(t, ok, "expected 'default' remote entry from migration")
	assert.Equal(t, "https://old.example.com", entry.URL)
	assert.Equal(t, "oldtok", entry.Token)
	assert.True(t, entry.InsecureTLS)
	assert.Equal(t, "default", config.Active)
}

func TestMigrateClientConfig_NoOverwrite(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	gordonToml := filepath.Join(tmpDir, "gordon", "gordon.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(gordonToml), 0o700))
	require.NoError(t, os.WriteFile(gordonToml, []byte(`
[client]
mode = "remote"
remote = "https://old.example.com"
token = "oldtok"
`), 0o600))

	existing := &ClientConfig{
		Active: "prod",
		Remotes: map[string]RemoteEntry{
			"prod":    {URL: "https://prod.example.com"},
			"default": {URL: "https://existing-default.example.com"},
		},
	}
	require.NoError(t, SaveRemotes("", existing))

	config, err := LoadRemotes("")
	require.NoError(t, err)
	assert.Equal(t, "https://existing-default.example.com", config.Remotes["default"].URL)
	assert.Equal(t, "prod", config.Active)
}

func TestMigrateClientConfig_SkipsLocalMode(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	gordonToml := filepath.Join(tmpDir, "gordon", "gordon.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(gordonToml), 0o700))
	require.NoError(t, os.WriteFile(gordonToml, []byte(`
[client]
mode = "local"
remote = "https://ignored.example.com"
`), 0o600))

	config, err := LoadRemotes("")
	require.NoError(t, err)
	_, ok := config.Remotes["default"]
	assert.False(t, ok)
}
