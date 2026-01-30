// Package grpcsecrets implements the gRPC server for the secrets component.
// This server wraps existing TokenStore and SecretProvider implementations.
package grpcsecrets

import (
	"context"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	gordonv1 "github.com/bnema/gordon/internal/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements the SecretsService gRPC interface.
type Server struct {
	gordonv1.UnimplementedSecretsServiceServer
	tokenStore out.TokenStore
	providers  map[string]out.SecretProvider
}

// NewServer creates a new secrets gRPC server.
func NewServer(tokenStore out.TokenStore, providers []out.SecretProvider) *Server {
	providerMap := make(map[string]out.SecretProvider)
	for _, p := range providers {
		if p.IsAvailable() {
			providerMap[p.Name()] = p
		}
	}
	return &Server{
		tokenStore: tokenStore,
		providers:  providerMap,
	}
}

// GetSecret retrieves a secret from the configured backend.
func (s *Server) GetSecret(ctx context.Context, req *gordonv1.GetSecretRequest) (*gordonv1.GetSecretResponse, error) {
	provider, ok := s.providers[req.Provider]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "provider %s not available", req.Provider)
	}

	value, err := provider.GetSecret(ctx, req.Path)
	if err != nil {
		return &gordonv1.GetSecretResponse{Found: false}, nil
	}

	return &gordonv1.GetSecretResponse{
		Value: value,
		Found: true,
	}, nil
}

// SaveToken stores a token.
func (s *Server) SaveToken(ctx context.Context, req *gordonv1.SaveTokenRequest) (*gordonv1.SaveTokenResponse, error) {
	token := protoToDomainToken(req.Token)
	if err := s.tokenStore.SaveToken(ctx, token, req.Jwt); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to save token: %v", err)
	}
	return &gordonv1.SaveTokenResponse{Success: true}, nil
}

// GetToken retrieves a token by subject.
func (s *Server) GetToken(ctx context.Context, req *gordonv1.GetTokenRequest) (*gordonv1.GetTokenResponse, error) {
	jwt, token, err := s.tokenStore.GetToken(ctx, req.Subject)
	if err != nil {
		return &gordonv1.GetTokenResponse{Found: false}, nil
	}

	return &gordonv1.GetTokenResponse{
		Jwt:   jwt,
		Token: domainToProtoToken(token),
		Found: true,
	}, nil
}

// ListTokens returns all stored tokens.
func (s *Server) ListTokens(ctx context.Context, _ *gordonv1.ListTokensRequest) (*gordonv1.ListTokensResponse, error) {
	tokens, err := s.tokenStore.ListTokens(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list tokens: %v", err)
	}

	protoTokens := make([]*gordonv1.Token, len(tokens))
	for i, t := range tokens {
		protoTokens[i] = domainToProtoToken(&t)
	}

	return &gordonv1.ListTokensResponse{Tokens: protoTokens}, nil
}

// RevokeToken revokes a token.
func (s *Server) RevokeToken(ctx context.Context, req *gordonv1.RevokeTokenRequest) (*gordonv1.RevokeTokenResponse, error) {
	if err := s.tokenStore.Revoke(ctx, req.TokenId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to revoke token: %v", err)
	}
	return &gordonv1.RevokeTokenResponse{Success: true}, nil
}

// IsRevoked checks if a token is revoked.
func (s *Server) IsRevoked(ctx context.Context, req *gordonv1.IsRevokedRequest) (*gordonv1.IsRevokedResponse, error) {
	revoked, err := s.tokenStore.IsRevoked(ctx, req.TokenId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check revocation: %v", err)
	}
	return &gordonv1.IsRevokedResponse{Revoked: revoked}, nil
}

// DeleteToken removes a token.
func (s *Server) DeleteToken(ctx context.Context, req *gordonv1.DeleteTokenRequest) (*gordonv1.DeleteTokenResponse, error) {
	if err := s.tokenStore.DeleteToken(ctx, req.Subject); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete token: %v", err)
	}
	return &gordonv1.DeleteTokenResponse{Success: true}, nil
}

// domainToProtoToken converts a domain.Token to protobuf Token.
func domainToProtoToken(t *domain.Token) *gordonv1.Token {
	if t == nil {
		return nil
	}

	protoToken := &gordonv1.Token{
		Id:       t.ID,
		Subject:  t.Subject,
		Scopes:   t.Scopes,
		IssuedAt: t.IssuedAt.Unix(),
		Metadata: make(map[string]string),
	}

	if !t.ExpiresAt.IsZero() {
		protoToken.ExpiresAt = t.ExpiresAt.Unix()
	}

	return protoToken
}

// protoToDomainToken converts a protobuf Token to domain.Token.
func protoToDomainToken(t *gordonv1.Token) *domain.Token {
	if t == nil {
		return nil
	}

	token := &domain.Token{
		ID:       t.Id,
		Subject:  t.Subject,
		Scopes:   t.Scopes,
		IssuedAt: time.Unix(t.IssuedAt, 0),
	}

	if t.ExpiresAt != 0 {
		token.ExpiresAt = time.Unix(t.ExpiresAt, 0)
	}

	return token
}
