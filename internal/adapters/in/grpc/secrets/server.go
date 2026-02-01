// Package grpcsecrets implements the gRPC server for the secrets component.
// This server wraps existing TokenStore and SecretProvider implementations.
package grpcsecrets

import (
	"context"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	gordon "github.com/bnema/gordon/internal/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements the SecretsService gRPC interface.
type Server struct {
	gordon.UnimplementedSecretsServiceServer
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
func (s *Server) GetSecret(ctx context.Context, req *gordon.GetSecretRequest) (*gordon.GetSecretResponse, error) {
	if req.Provider == "" {
		return nil, status.Error(codes.InvalidArgument, "provider is required")
	}
	if req.Path == "" {
		return nil, status.Error(codes.InvalidArgument, "path is required")
	}

	provider, ok := s.providers[req.Provider]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "provider %s not available", req.Provider)
	}

	value, err := provider.GetSecret(ctx, req.Path)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "secret not found at path %s: %v", req.Path, err)
	}

	return &gordon.GetSecretResponse{
		Value: value,
		Found: true,
	}, nil
}

// SaveToken stores a token.
func (s *Server) SaveToken(ctx context.Context, req *gordon.SaveTokenRequest) (*gordon.SaveTokenResponse, error) {
	token := protoToDomainToken(req.Token)
	if err := s.tokenStore.SaveToken(ctx, token, req.Jwt); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to save token: %v", err)
	}
	return &gordon.SaveTokenResponse{Success: true}, nil
}

// GetToken retrieves a token by subject.
func (s *Server) GetToken(ctx context.Context, req *gordon.GetTokenRequest) (*gordon.GetTokenResponse, error) {
	if req.Subject == "" {
		return nil, status.Error(codes.InvalidArgument, "subject is required")
	}

	jwt, token, err := s.tokenStore.GetToken(ctx, req.Subject)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "token not found for subject %s: %v", req.Subject, err)
	}

	return &gordon.GetTokenResponse{
		Jwt:   jwt,
		Token: domainToProtoToken(token),
		Found: true,
	}, nil
}

// ListTokens returns all stored tokens.
func (s *Server) ListTokens(ctx context.Context, _ *gordon.ListTokensRequest) (*gordon.ListTokensResponse, error) {
	tokens, err := s.tokenStore.ListTokens(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list tokens: %v", err)
	}

	protoTokens := make([]*gordon.Token, len(tokens))
	for i, t := range tokens {
		protoTokens[i] = domainToProtoToken(&t)
	}

	return &gordon.ListTokensResponse{Tokens: protoTokens}, nil
}

// RevokeToken revokes a token.
func (s *Server) RevokeToken(ctx context.Context, req *gordon.RevokeTokenRequest) (*gordon.RevokeTokenResponse, error) {
	if err := s.tokenStore.Revoke(ctx, req.TokenId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to revoke token: %v", err)
	}
	return &gordon.RevokeTokenResponse{Success: true}, nil
}

// IsRevoked checks if a token is revoked.
func (s *Server) IsRevoked(ctx context.Context, req *gordon.IsRevokedRequest) (*gordon.IsRevokedResponse, error) {
	revoked, err := s.tokenStore.IsRevoked(ctx, req.TokenId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check revocation: %v", err)
	}
	return &gordon.IsRevokedResponse{Revoked: revoked}, nil
}

// DeleteToken removes a token.
func (s *Server) DeleteToken(ctx context.Context, req *gordon.DeleteTokenRequest) (*gordon.DeleteTokenResponse, error) {
	if err := s.tokenStore.DeleteToken(ctx, req.Subject); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete token: %v", err)
	}
	return &gordon.DeleteTokenResponse{Success: true}, nil
}

// domainToProtoToken converts a domain.Token to protobuf Token.
func domainToProtoToken(t *domain.Token) *gordon.Token {
	if t == nil {
		return nil
	}

	protoToken := &gordon.Token{
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
func protoToDomainToken(t *gordon.Token) *domain.Token {
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
