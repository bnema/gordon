package publictls

import (
	"context"
	"sync"

	"github.com/bnema/gordon/internal/domain"
)

// HTTP01Challenges stores ACME HTTP-01 challenge tokens and their key
// authorizations. It is safe for concurrent use.
type HTTP01Challenges struct {
	mu   sync.RWMutex
	data map[string]string
}

// NewHTTP01Challenges returns an initialized HTTP01Challenges.
func NewHTTP01Challenges() *HTTP01Challenges {
	return &HTTP01Challenges{
		data: make(map[string]string),
	}
}

// Present stores the key authorization for the given token. It silently
// ignores unsafe tokens and empty keyAuth values.
func (c *HTTP01Challenges) Present(token, keyAuth string) {
	if !domain.IsValidHTTP01Token(token) || keyAuth == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[token] = keyAuth
}

// CleanUp removes the stored key authorization for the given token. It silently
// ignores unsafe tokens.
func (c *HTTP01Challenges) CleanUp(token string) {
	if !domain.IsValidHTTP01Token(token) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, token)
}

// Get returns the key authorization for the given token, and a boolean
// indicating whether it was found. It returns false for unsafe tokens.
func (c *HTTP01Challenges) Get(_ context.Context, token string) (string, bool) {
	if !domain.IsValidHTTP01Token(token) {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	keyAuth, ok := c.data[token]
	return keyAuth, ok
}
