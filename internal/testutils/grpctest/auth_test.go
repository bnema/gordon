package grpctest_test

import (
	"context"
	"testing"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/testutils/grpctest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

func TestAuthFixtureValidatesLocalTokenIdentityAndScopes(t *testing.T) {
	fixture := grpctest.NewAuthFixture(
		"runtime-local",
		domain.ComponentRoleRuntime,
		domain.ComponentScopeRuntimeStatus,
		domain.ComponentScopeRuntimeLogs,
	)

	identity, err := fixture.ValidateToken(context.Background(), grpctest.LocalComponentToken, domain.ComponentScopeRuntimeLogs)
	require.NoError(t, err)
	assert.Equal(t, "runtime-local", identity.Name)
	assert.Equal(t, domain.ComponentRoleRuntime, identity.Role)
	assert.Equal(t, []domain.ComponentScope{domain.ComponentScopeRuntimeStatus, domain.ComponentScopeRuntimeLogs}, identity.Scopes)
	assert.NotEmpty(t, identity.KeyID)
}

func TestAuthFixtureRejectsUnknownTokenAndMissingScope(t *testing.T) {
	fixture := grpctest.NewAuthFixture("edge-local", domain.ComponentRoleEdge, domain.ComponentScopeEdgeDrain)

	_, err := fixture.ValidateToken(context.Background(), "not-the-local-token", domain.ComponentScopeEdgeDrain)
	require.ErrorIs(t, err, domain.ErrInvalidToken)

	_, err = fixture.ValidateToken(context.Background(), grpctest.LocalComponentToken, domain.ComponentScopeRoutesWatch)
	require.ErrorIs(t, err, domain.ErrInsufficientScope)
}

func TestAuthFixtureAddsOutgoingBearerMetadata(t *testing.T) {
	ctx := grpctest.AuthenticatedContext(context.Background(), grpctest.LocalComponentToken)

	md, ok := metadata.FromOutgoingContext(ctx)
	require.True(t, ok)
	assert.Equal(t, []string{"Bearer " + grpctest.LocalComponentToken}, md.Get("authorization"))
}
