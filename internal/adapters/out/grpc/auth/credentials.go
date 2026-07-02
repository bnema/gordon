package auth

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc/credentials"
)

var errEmptyBearerToken = errors.New("bearer token must not be empty")

type bearerTokenCredentials struct {
	token string
}

// NewBearerTokenCredentials returns per-RPC credentials that attach a bearer token.
func NewBearerTokenCredentials(token string) (credentials.PerRPCCredentials, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errEmptyBearerToken
	}

	return bearerTokenCredentials{token: token}, nil
}

func (c bearerTokenCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{
		"authorization": "Bearer " + c.token,
	}, nil
}

func (c bearerTokenCredentials) RequireTransportSecurity() bool {
	return true
}
