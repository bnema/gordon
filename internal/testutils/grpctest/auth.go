package grpctest

import (
	"context"

	"google.golang.org/grpc/metadata"

	"github.com/bnema/gordon/internal/domain"
)

const (
	// LocalComponentToken is fake token material reserved for in-process tests.
	LocalComponentToken = "gordon-test-local-component-token"
	localComponentKeyID = "local-test-key"
)

// AuthFixture is a local component-token validator with a deterministic identity.
type AuthFixture struct {
	identity domain.ComponentIdentity
}

// NewAuthFixture returns a validator for LocalComponentToken and the supplied identity.
func NewAuthFixture(name string, role domain.ComponentRole, scopes ...domain.ComponentScope) *AuthFixture {
	return &AuthFixture{identity: domain.ComponentIdentity{
		KeyID:  localComponentKeyID,
		Name:   name,
		Role:   role,
		Scopes: append([]domain.ComponentScope(nil), scopes...),
	}}
}

// ValidateToken validates the deterministic local token and required scope.
func (f *AuthFixture) ValidateToken(_ context.Context, token string, required domain.ComponentScope) (*domain.ComponentIdentity, error) {
	if token != LocalComponentToken {
		return nil, domain.ErrInvalidToken
	}
	if !domain.ComponentScopesContain(f.identity.Scopes, required) {
		return nil, domain.ErrInsufficientScope
	}

	identity := f.identity
	identity.Scopes = append([]domain.ComponentScope(nil), f.identity.Scopes...)
	return &identity, nil
}

// Identity returns an independent copy of the fixture identity.
func (f *AuthFixture) Identity() *domain.ComponentIdentity {
	identity := f.identity
	identity.Scopes = append([]domain.ComponentScope(nil), f.identity.Scopes...)
	return &identity
}

// AuthenticatedContext adds bearer metadata for a local authenticated RPC.
func AuthenticatedContext(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

type localBearerCredentials struct {
	token string
}

func (c localBearerCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + c.token}, nil
}

func (localBearerCredentials) RequireTransportSecurity() bool {
	return false
}
