package app

import (
	"context"
	"io"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoleRuntimeOwnershipMatrix(t *testing.T) {
	tests := []struct {
		role Role
		want bool
	}{
		{role: RoleMonolith, want: true},
		{role: RoleRuntime, want: true},
		{role: RoleControl, want: false},
		{role: RoleEdge, want: false},
		{role: RoleRegistry, want: false},
	}

	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			assert.Equal(t, tt.want, roleMayInstantiateRuntimeAdapter(tt.role))
		})
	}
}

func TestRoleRuntimeOwnershipRejectsForbiddenRolesBeforeRuntimeDetection(t *testing.T) {
	log := zerowrap.New(zerowrap.Config{Level: "disabled", Output: io.Discard})
	// An explicit socket is always accepted by docker.DetectRuntimeSocket and would
	// proceed to runtime construction if the ownership guard did not run first.
	runtimeSocket := "/definitely/not/a/real/gordon-runtime.sock"

	for _, role := range []Role{RoleControl, RoleEdge, RoleRegistry} {
		t.Run(string(role), func(t *testing.T) {
			runtime, eventBus, err := createOutputAdapters(context.Background(), log, role, runtimeSocket)

			require.Error(t, err)
			assert.ErrorIs(t, err, ErrRoleRuntimeOwnership)
			assert.Contains(t, err.Error(), string(role))
			assert.NotContains(t, err.Error(), runtimeSocket)
			assert.Nil(t, runtime)
			assert.Nil(t, eventBus)
		})
	}
}
