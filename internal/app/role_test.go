package app

import (
	"context"
	"errors"
	"fmt"
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

func TestRunDispatchesByRole(t *testing.T) {
	tests := []struct {
		name      string
		roleValue string
		want      Role
	}{
		{name: "no role defaults to monolith", want: RoleMonolith},
		{name: "explicit monolith", roleValue: "monolith", want: RoleMonolith},
		{name: "control", roleValue: "control", want: RoleControl},
		{name: "runtime", roleValue: "runtime", want: RoleRuntime},
		{name: "edge", roleValue: "edge", want: RoleEdge},
		{name: "registry", roleValue: "registry", want: RoleRegistry},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := stubRoleRunners(t)

			require.NoError(t, RunWithRole(context.Background(), "config.toml", tt.roleValue))
			assert.Equal(t, []Role{tt.want}, *calls)
		})
	}
}

func TestRunWithRoleInvalidRoleFailsBeforeRunner(t *testing.T) {
	calls := stubRoleRunners(t)

	err := RunWithRole(context.Background(), "config.toml", "worker")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role")
	assert.Empty(t, *calls)
}

func TestRoleNotImplemented(t *testing.T) {
	tests := []struct {
		role Role
		run  func(context.Context, string) error
	}{
		{role: RoleControl, run: runControlImpl},
		{role: RoleRuntime, run: runRuntimeImpl},
		{role: RoleEdge, run: runEdgeImpl},
		{role: RoleRegistry, run: runRegistryImpl},
	}

	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			err := tt.run(context.Background(), "config.toml")
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrRoleNotImplemented))
			assert.Contains(t, err.Error(), string(tt.role))
			assert.Contains(t, err.Error(), "not implemented")
		})
	}
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

func stubRoleRunners(t *testing.T) *[]Role {
	t.Helper()

	oldRunMonolith := runMonolith
	oldRunControl := runControl
	oldRunRuntime := runRuntime
	oldRunEdge := runEdge
	oldRunRegistry := runRegistry
	t.Cleanup(func() {
		runMonolith = oldRunMonolith
		runControl = oldRunControl
		runRuntime = oldRunRuntime
		runEdge = oldRunEdge
		runRegistry = oldRunRegistry
	})

	var calls []Role
	stub := func(role Role) func(context.Context, string) error {
		return func(ctx context.Context, configPath string) error {
			calls = append(calls, role)
			if configPath != "config.toml" {
				return fmt.Errorf("unexpected config path: %s", configPath)
			}
			return nil
		}
	}

	runMonolith = stub(RoleMonolith)
	runControl = stub(RoleControl)
	runRuntime = stub(RoleRuntime)
	runEdge = stub(RoleEdge)
	runRegistry = stub(RoleRegistry)

	return &calls
}
