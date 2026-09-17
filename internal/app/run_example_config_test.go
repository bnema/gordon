package app

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInitConfigAcceptsShippedExampleConfig guards the example shipped in
// the image: it must load cleanly, so no retired key may reappear as an
// active table. Viper reports a table that only holds comments as present
// in the config, so an empty [routes]-style header fails startup.
func TestInitConfigAcceptsShippedExampleConfig(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "gordon.toml.example"))
	require.NoError(t, err)

	_, _, err = initConfig(path)

	require.NoError(t, err)
}
