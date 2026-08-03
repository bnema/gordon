package auth

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc/credentials"
)

var errEmptyBearerToken = errors.New("bearer token must not be empty")

type bearerTokenCredentials struct {
	token                    string
	requireTransportSecurity bool
}

// NewBearerTokenCredentials returns per-RPC credentials that attach a bearer token over a secure transport.
func NewBearerTokenCredentials(token string) (credentials.PerRPCCredentials, error) {
	return newBearerTokenCredentials(token, true)
}

// NewInsecureBearerTokenCredentials returns per-RPC credentials for an explicitly configured private plaintext transport.
func NewInsecureBearerTokenCredentials(token string) (credentials.PerRPCCredentials, error) {
	return newBearerTokenCredentials(token, false)
}

func newBearerTokenCredentials(token string, requireTransportSecurity bool) (credentials.PerRPCCredentials, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errEmptyBearerToken
	}

	return bearerTokenCredentials{token: token, requireTransportSecurity: requireTransportSecurity}, nil
}

func (c bearerTokenCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{
		"authorization": "Bearer " + c.token,
	}, nil
}

func (c bearerTokenCredentials) RequireTransportSecurity() bool {
	return c.requireTransportSecurity
}
