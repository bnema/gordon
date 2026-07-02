package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoleDefaultIsMonolith(t *testing.T) {
	role, err := ParseRole("")
	require.NoError(t, err)
	assert.Equal(t, RoleMonolith, role)
}

func TestRoleValidStrings(t *testing.T) {
	tests := map[string]Role{
		"monolith": RoleMonolith,
		"control":  RoleControl,
		"runtime":  RoleRuntime,
		"edge":     RoleEdge,
		"registry": RoleRegistry,
	}

	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			role, err := ParseRole(input)
			require.NoError(t, err)
			assert.Equal(t, want, role)
		})
	}
}

func TestRoleInvalidInputListsAcceptedValues(t *testing.T) {
	_, err := ParseRole("worker")
	require.Error(t, err)
	for _, value := range []string{"monolith", "control", "runtime", "edge", "registry"} {
		assert.Contains(t, err.Error(), value)
	}
}

func TestRoleFromFlagEnvPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		flagValue string
		envValue  string
		want      Role
	}{
		{name: "empty flag and env default monolith", want: RoleMonolith},
		{name: "env used when flag empty", envValue: "runtime", want: RoleRuntime},
		{name: "flag wins over env", flagValue: "control", envValue: "runtime", want: RoleControl},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			role, err := ResolveRole(tt.flagValue, tt.envValue)
			require.NoError(t, err)
			assert.Equal(t, tt.want, role)
		})
	}
}

func TestRunWithRoleNonMonolithReturnsNotImplemented(t *testing.T) {
	err := RunWithRole(context.Background(), "", "control")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRoleNotImplemented))
	assert.Contains(t, err.Error(), "control")
}

func TestRunDefaultsToMonolithPath(t *testing.T) {
	oldRunMonolith := runMonolith
	t.Cleanup(func() { runMonolith = oldRunMonolith })

	called := false
	runMonolith = func(ctx context.Context, configPath string) error {
		called = true
		assert.Equal(t, "config.toml", configPath)
		return nil
	}

	t.Setenv("GORDON_ROLE", "runtime")
	require.NoError(t, Run(context.Background(), "config.toml"))
	assert.True(t, called)
}
