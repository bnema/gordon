// Package grpcsecrets implements gRPC clients that satisfy the boundaries/out interfaces.
// These clients connect to the gordon-secrets service via gRPC.
package grpcsecrets

import (
	"context"
	"fmt"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	gordonv1 "github.com/bnema/gordon/internal/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client implements both out.TokenStore and out.SecretProvider interfaces.
// It communicates with the gordon-secrets service via gRPC.
type Client struct {
	conn         *grpc.ClientConn
	client       gordonv1.SecretsServiceClient
	providerName string // For SecretProvider interface
}

// NewClient creates a new gRPC client for the secrets service.
func NewClient(addr string) (*Client, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to secrets service: %w", err)
	}

	return &Client{
		conn:   conn,
		client: gordonv1.NewSecretsServiceClient(conn),
	}, nil
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// WithProvider sets the provider name for the SecretProvider interface.
func (c *Client) WithProvider(name string) *Client {
	c.providerName = name
	return c
}

// ==================== TokenStore Implementation ====================

// SaveToken stores a token via gRPC.
func (c *Client) SaveToken(ctx context.Context, token *domain.Token, jwt string) error {
	_, err := c.client.SaveToken(ctx, &gordonv1.SaveTokenRequest{
		Token: domainToProtoToken(token),
		Jwt:   jwt,
	})
	return err
}

// GetToken retrieves a token by subject via gRPC.
func (c *Client) GetToken(ctx context.Context, subject string) (string, *domain.Token, error) {
	resp, err := c.client.GetToken(ctx, &gordonv1.GetTokenRequest{Subject: subject})
	if err != nil {
		return "", nil, err
	}
	if !resp.Found {
		return "", nil, fmt.Errorf("token not found for subject: %s", subject)
	}
	return resp.Jwt, protoToDomainToken(resp.Token), nil
}

// ListTokens returns all stored tokens via gRPC.
func (c *Client) ListTokens(ctx context.Context) ([]domain.Token, error) {
	resp, err := c.client.ListTokens(ctx, &gordonv1.ListTokensRequest{})
	if err != nil {
		return nil, err
	}

	tokens := make([]domain.Token, len(resp.Tokens))
	for i, t := range resp.Tokens {
		tokens[i] = *protoToDomainToken(t)
	}
	return tokens, nil
}

// Revoke adds token ID to revocation list via gRPC.
func (c *Client) Revoke(ctx context.Context, tokenID string) error {
	_, err := c.client.RevokeToken(ctx, &gordonv1.RevokeTokenRequest{TokenId: tokenID})
	return err
}

// IsRevoked checks if token ID is in revocation list via gRPC.
func (c *Client) IsRevoked(ctx context.Context, tokenID string) (bool, error) {
	resp, err := c.client.IsRevoked(ctx, &gordonv1.IsRevokedRequest{TokenId: tokenID})
	if err != nil {
		return false, err
	}
	return resp.Revoked, nil
}

// DeleteToken removes a token via gRPC.
func (c *Client) DeleteToken(ctx context.Context, subject string) error {
	_, err := c.client.DeleteToken(ctx, &gordonv1.DeleteTokenRequest{Subject: subject})
	return err
}

// ==================== SecretProvider Implementation ====================

// Name returns the provider name.
func (c *Client) Name() string {
	return c.providerName
}

// GetSecret retrieves a secret via gRPC.
func (c *Client) GetSecret(ctx context.Context, key string) (string, error) {
	resp, err := c.client.GetSecret(ctx, &gordonv1.GetSecretRequest{
		Provider: c.providerName,
		Path:     key,
	})
	if err != nil {
		return "", err
	}
	if !resp.Found {
		return "", fmt.Errorf("secret not found: %s", key)
	}
	return resp.Value, nil
}

// IsAvailable checks if the secrets service is reachable.
func (c *Client) IsAvailable() bool {
	// Try a simple operation to check connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := c.client.ListTokens(ctx, &gordonv1.ListTokensRequest{})
	return err == nil
}

// Ensure Client implements the interfaces
var _ out.TokenStore = (*Client)(nil)
var _ out.SecretProvider = (*Client)(nil)

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
