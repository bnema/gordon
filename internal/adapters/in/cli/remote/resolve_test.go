package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolve_FlagNameLookup(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")
	t.Setenv("GORDON_INSECURE", "")

	config := &ClientConfig{
		Remotes: map[string]RemoteEntry{
			"prod": {URL: "https://gordon.example.com", Token: "tok123"},
		},
	}
	require.NoError(t, SaveRemotes("", config))

	resolved, ok := Resolve("prod", "", false)
	require.True(t, ok)
	assert.Equal(t, "prod", resolved.Name)
	assert.Equal(t, "https://gordon.example.com", resolved.URL)
	assert.Equal(t, "tok123", resolved.Token)
	assert.False(t, resolved.InsecureTLS)
}

func TestResolve_FlagURLPassthrough(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")

	resolved, ok := Resolve("https://one-off.example.com", "mytoken", false)
	require.True(t, ok)
	assert.Equal(t, "", resolved.Name)
	assert.Equal(t, "https://one-off.example.com", resolved.URL)
	assert.Equal(t, "mytoken", resolved.Token)
}

func TestResolve_ActiveRemoteFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")

	config := &ClientConfig{
		Active: "staging",
		Remotes: map[string]RemoteEntry{
			"staging": {URL: "https://staging.example.com", Token: "stok"},
		},
	}
	require.NoError(t, SaveRemotes("", config))

	resolved, ok := Resolve("", "", false)
	require.True(t, ok)
	assert.Equal(t, "staging", resolved.Name)
	assert.Equal(t, "https://staging.example.com", resolved.URL)
	assert.Equal(t, "stok", resolved.Token)
}

func TestResolve_NoRemote_LocalMode(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")

	resolved, ok := Resolve("", "", false)
	assert.False(t, ok)
	assert.Nil(t, resolved)
}

func TestResolve_EnvRemoteName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GORDON_REMOTE", "prod")
	t.Setenv("GORDON_TOKEN", "")

	config := &ClientConfig{
		Remotes: map[string]RemoteEntry{
			"prod": {URL: "https://gordon.example.com", Token: "tok"},
		},
	}
	require.NoError(t, SaveRemotes("", config))

	resolved, ok := Resolve("", "", false)
	require.True(t, ok)
	assert.Equal(t, "prod", resolved.Name)
	assert.Equal(t, "https://gordon.example.com", resolved.URL)
}

func TestResolve_EnvRemoteURL(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GORDON_REMOTE", "https://env.example.com")
	t.Setenv("GORDON_TOKEN", "envtok")

	resolved, ok := Resolve("", "", false)
	require.True(t, ok)
	assert.Equal(t, "", resolved.Name)
	assert.Equal(t, "https://env.example.com", resolved.URL)
	assert.Equal(t, "envtok", resolved.Token)
}

func TestResolve_FlagTokenOverridesStored(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")

	config := &ClientConfig{
		Active: "prod",
		Remotes: map[string]RemoteEntry{
			"prod": {URL: "https://gordon.example.com", Token: "stored"},
		},
	}
	require.NoError(t, SaveRemotes("", config))

	resolved, ok := Resolve("", "override", false)
	require.True(t, ok)
	assert.Equal(t, "override", resolved.Token)
}

func TestResolve_EnvTokenOverridesStored(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "envtoken")

	config := &ClientConfig{
		Active: "prod",
		Remotes: map[string]RemoteEntry{
			"prod": {URL: "https://gordon.example.com", Token: "stored"},
		},
	}
	require.NoError(t, SaveRemotes("", config))

	resolved, ok := Resolve("", "", false)
	require.True(t, ok)
	assert.Equal(t, "envtoken", resolved.Token)
}

func TestResolve_InsecureTLSFlag(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")
	t.Setenv("GORDON_INSECURE", "")

	config := &ClientConfig{
		Active: "prod",
		Remotes: map[string]RemoteEntry{
			"prod": {URL: "https://gordon.example.com"},
		},
	}
	require.NoError(t, SaveRemotes("", config))

	resolved, ok := Resolve("", "", true)
	require.True(t, ok)
	assert.True(t, resolved.InsecureTLS)
}

func TestResolve_InsecureTLSFromEntry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")
	t.Setenv("GORDON_INSECURE", "")

	config := &ClientConfig{
		Active: "prod",
		Remotes: map[string]RemoteEntry{
			"prod": {URL: "https://gordon.example.com", InsecureTLS: true},
		},
	}
	require.NoError(t, SaveRemotes("", config))

	resolved, ok := Resolve("", "", false)
	require.True(t, ok)
	assert.True(t, resolved.InsecureTLS)
}

func TestResolve_TokenEnvVar(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")
	t.Setenv("MY_TOKEN", "fromenv")

	config := &ClientConfig{
		Active: "prod",
		Remotes: map[string]RemoteEntry{
			"prod": {URL: "https://gordon.example.com", TokenEnv: "MY_TOKEN"},
		},
	}
	require.NoError(t, SaveRemotes("", config))

	resolved, ok := Resolve("", "", false)
	require.True(t, ok)
	assert.Equal(t, "fromenv", resolved.Token)
}

func TestResolve_UnknownNameReturnsNotFound(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")

	resolved, ok := Resolve("nonexistent", "", false)
	assert.False(t, ok)
	assert.Nil(t, resolved)
}
