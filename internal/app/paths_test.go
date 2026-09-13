package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigureViper_ExplicitPathExtensionIsCaseInsensitive(t *testing.T) {
	tests := []struct {
		name    string
		ext     string
		content string
	}{
		{name: "lowercase yaml", ext: ".yaml", content: "foo: bar\n"},
		{name: "uppercase yaml", ext: ".YAML", content: "foo: bar\n"},
		{name: "mixed case yml", ext: ".YmL", content: "foo: bar\n"},
		{name: "uppercase json", ext: ".JSON", content: "{\"foo\":\"bar\"}\n"},
		{name: "uppercase toml", ext: ".TOML", content: "foo = \"bar\"\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "gordon"+tt.ext)
			require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o600))

			v := viper.New()
			ConfigureViper(v, path)
			require.NoError(t, v.ReadInConfig())
			assert.Equal(t, "bar", v.GetString("foo"))
		})
	}
}

func TestConfigureViper_UnknownExtensionFallsBackToTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gordon.conf")
	require.NoError(t, os.WriteFile(path, []byte("foo = \"bar\"\n"), 0o600))

	v := viper.New()
	ConfigureViper(v, path)
	require.NoError(t, v.ReadInConfig())
	assert.Equal(t, "bar", v.GetString("foo"))
}
