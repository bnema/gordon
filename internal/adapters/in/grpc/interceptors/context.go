package interceptors

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bnema/gordon/internal/domain"
)

type componentIdentityContextKey struct{}

// ContextWithComponentIdentity returns a child context carrying the authenticated component identity.
func ContextWithComponentIdentity(ctx context.Context, identity *domain.ComponentIdentity) context.Context {
	return context.WithValue(ctx, componentIdentityContextKey{}, identity)
}

// ComponentIdentityFromContext returns the authenticated component identity from ctx, if present.
func ComponentIdentityFromContext(ctx context.Context) (*domain.ComponentIdentity, bool) {
	identity, ok := ctx.Value(componentIdentityContextKey{}).(*domain.ComponentIdentity)
	return identity, ok && identity != nil
}

// RequireComponentRole returns the authenticated component identity if it has the required role.
func RequireComponentRole(ctx context.Context, role domain.ComponentRole) (*domain.ComponentIdentity, error) {
	identity, ok := ComponentIdentityFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "component identity required")
	}
	if identity.Role != role {
		return nil, status.Error(codes.PermissionDenied, "component role not permitted")
	}
	return identity, nil
}

// RequireScope returns the authenticated component identity if it has the required scope.
func RequireScope(ctx context.Context, scope domain.ComponentScope) (*domain.ComponentIdentity, error) {
	identity, ok := ComponentIdentityFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "component identity required")
	}
	if !domain.ComponentScopesContain(identity.Scopes, scope) {
		return nil, status.Error(codes.PermissionDenied, "component scope not permitted")
	}
	return identity, nil
}
