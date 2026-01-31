package remote

import (
	"context"
	"fmt"

	gordon "github.com/bnema/gordon/internal/grpc"
)

// PasswordRequest represents the request body for authentication.
type PasswordRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// PasswordResponse represents the response from password authentication.
type PasswordResponse struct {
	Token     string `json:"token"`
	ExpiresIn int    `json:"expires_in"`
	IssuedAt  string `json:"issued_at"`
}

// Authenticate issues a token using username and password.
// This method does NOT require an existing token since it's used to obtain one.
func (c *Client) Authenticate(ctx context.Context, username, password string) (*PasswordResponse, error) {
	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}

	resp, err := c.admin.AuthenticatePassword(ctx, &gordon.AuthenticatePasswordRequest{
		Username: username,
		Password: password,
	})
	if err != nil {
		return nil, err
	}

	return &PasswordResponse{
		Token:     resp.Token,
		ExpiresIn: int(resp.ExpiresIn),
		IssuedAt:  resp.IssuedAt,
	}, nil
}

// UpdateRemoteToken updates the token for a named remote.
func UpdateRemoteToken(name, token string) error {
	config, err := LoadRemotes("")
	if err != nil {
		return err
	}

	remote, ok := config.Remotes[name]
	if !ok {
		return fmt.Errorf("remote '%s' not found", name)
	}

	remote.Token = token
	remote.TokenEnv = ""
	config.Remotes[name] = remote

	return SaveRemotes("", config)
}
