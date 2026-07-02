package interceptors

import (
	"context"
	"testing"

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestComponentIdentityContextRoundTrip(t *testing.T) {
	identity := &domain.ComponentIdentity{
		KeyID:  "key-1",
		Name:   "runtime-1",
		Role:   domain.ComponentRoleRuntime,
		Scopes: []domain.ComponentScope{domain.ComponentScopeRuntimeDeploy},
	}

	ctx := ContextWithComponentIdentity(context.Background(), identity)

	got, ok := ComponentIdentityFromContext(ctx)
	require.True(t, ok)
	require.Same(t, identity, got)
}

func TestComponentIdentityContextMissingIdentity(t *testing.T) {
	_, ok := ComponentIdentityFromContext(context.Background())
	require.False(t, ok)

	_, err := RequireComponentRole(context.Background(), domain.ComponentRoleRuntime)
	require.Error(t, err)
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	_, err = RequireScope(context.Background(), domain.ComponentScopeRuntimeDeploy)
	require.Error(t, err)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestComponentIdentityContextRole(t *testing.T) {
	identity := &domain.ComponentIdentity{Role: domain.ComponentRoleRuntime}
	ctx := ContextWithComponentIdentity(context.Background(), identity)

	got, err := RequireComponentRole(ctx, domain.ComponentRoleRuntime)
	require.NoError(t, err)
	require.Same(t, identity, got)

	got, err = RequireComponentRole(ctx, domain.ComponentRoleRegistry)
	require.Nil(t, got)
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestComponentIdentityContextScope(t *testing.T) {
	identity := &domain.ComponentIdentity{
		Scopes: []domain.ComponentScope{domain.ComponentScopeRuntimeDeploy},
	}
	ctx := ContextWithComponentIdentity(context.Background(), identity)

	got, err := RequireScope(ctx, domain.ComponentScopeRuntimeDeploy)
	require.NoError(t, err)
	require.Same(t, identity, got)

	got, err = RequireScope(ctx, domain.ComponentScopeRuntimeLogs)
	require.Nil(t, got)
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
