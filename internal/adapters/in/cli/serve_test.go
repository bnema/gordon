package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServeCommandFlags(t *testing.T) {
	cmd := newServeCmd()

	roleFlag := cmd.Flags().Lookup("role")
	require.NotNil(t, roleFlag)
	assert.Equal(t, "", roleFlag.DefValue)

	configFlag := cmd.Flags().Lookup("config")
	require.NotNil(t, configFlag)
	assert.Equal(t, "c", configFlag.Shorthand)

	assert.Nil(t, cmd.Flags().Lookup("component"))
}

func TestRootCommandServeExposesRoleFlag(t *testing.T) {
	root := NewRootCmd()
	serve, _, err := root.Find([]string{"serve"})
	require.NoError(t, err)
	require.NotNil(t, serve)

	assert.NotNil(t, serve.Flags().Lookup("role"))
	assert.Nil(t, serve.Flags().Lookup("component"))
}
