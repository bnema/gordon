// Package domain contains pure business types without external dependencies.
package domain

import (
	"fmt"
	"strings"
	"time"
)

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

// Scope action constants for registry operations.
const (
	ScopeActionPull = "pull"
	ScopeActionPush = "push"
	ScopeActionAll  = "*"
)

// Scope represents a Docker registry scope (repository:name:actions).
// Format follows the Docker Registry v2 token scope specification.
type Scope struct {
	Type    string   // "repository" for repository access
	Name    string   // repository name or "*" for all
	Actions []string // ["pull", "push", "*"]
}

// ParseScope parses a Docker v2 scope string into a Scope struct.
// Format: type:name:action1,action2 (e.g., "repository:myrepo:push,pull")
func ParseScope(s string) (*Scope, error) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid scope format: %s", s)
	}

	scope := &Scope{
		Type:    parts[0],
		Name:    parts[1],
		Actions: strings.Split(parts[2], ","),
	}

	// Trim whitespace from actions
	for i, action := range scope.Actions {
		scope.Actions[i] = strings.TrimSpace(action)
	}

	return scope, nil
}

// CanAccess checks if the scope grants the requested action on the repository.
// Supports exact matches and wildcard matching for repository names.
func (s *Scope) CanAccess(repoName string, action string) bool {
	if s.Type != "repository" {
		return false
	}

	// Check repository name match
	if s.Name != "*" && s.Name != repoName {
		// Check prefix match for org-level scopes (e.g., "myorg/*" matches "myorg/app")
		if !strings.HasSuffix(s.Name, "/*") {
			return false
		}
		prefix := strings.TrimSuffix(s.Name, "/*")
		if !strings.HasPrefix(repoName, prefix+"/") {
			return false
		}
	}

	// Check action permission
	for _, a := range s.Actions {
		if a == "*" || a == action {
			return true
		}
	}

	return false
}

// String returns the scope as a string in Docker v2 format.
func (s *Scope) String() string {
	return fmt.Sprintf("%s:%s:%s", s.Type, s.Name, strings.Join(s.Actions, ","))
}
