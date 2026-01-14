// Package domain contains pure business types without external dependencies.
package domain

import "time"

// AuthType represents the type of authentication configured for the registry.
type AuthType string

const (
	// AuthTypePassword uses bcrypt-hashed password authentication.
	AuthTypePassword AuthType = "password"
	// AuthTypeToken uses JWT token-based authentication.
	AuthTypeToken AuthType = "token"
)

// Token represents a generated authentication token stored in the secrets backend.
type Token struct {
	ID        string
	Subject   string
	Scopes    []string
	IssuedAt  time.Time
	ExpiresAt time.Time // Zero value means never expires
	Revoked   bool
}

// TokenClaims represents the JWT claims for a token.
type TokenClaims struct {
	ID        string   `json:"jti"`
	Subject   string   `json:"sub"`
	Scopes    []string `json:"scopes"`
	IssuedAt  int64    `json:"iat"`
	ExpiresAt int64    `json:"exp,omitempty"` // 0 means never expires
	Issuer    string   `json:"iss"`
}

// IsExpired checks if the token has expired.
func (t *Token) IsExpired() bool {
	if t.ExpiresAt.IsZero() {
		return false // Never expires
	}
	return time.Now().After(t.ExpiresAt)
}

// SecretsBackend represents the type of secrets storage backend.
type SecretsBackend string

const (
	// SecretsBackendPass uses the pass password manager.
	SecretsBackendPass SecretsBackend = "pass"
	// SecretsBackendSops uses SOPS for encrypted secrets.
	SecretsBackendSops SecretsBackend = "sops"
	// SecretsBackendUnsafe stores secrets in plain text (development only).
	SecretsBackendUnsafe SecretsBackend = "unsafe"
)
