// Package ratelimit provides rate limiter implementations.
package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/bnema/zerowrap"
	"golang.org/x/time/rate"

	"github.com/bnema/gordon/internal/boundaries/out"
)

// Ensure MemoryStore implements out.RateLimiter.
var _ out.RateLimiter = (*MemoryStore)(nil)

// MemoryStore is an in-memory rate limiter implementation using golang.org/x/time/rate.
// Each unique key gets its own independent rate limiter.
type MemoryStore struct {
	limiters map[string]*rate.Limiter
	mu       sync.RWMutex
	rps      float64
	burst    int
	log      zerowrap.Logger
}

// NewMemoryStore creates a new in-memory rate limiter store.
func NewMemoryStore(rps float64, burst int, log zerowrap.Logger) *MemoryStore {
	return &MemoryStore{
		limiters: make(map[string]*rate.Limiter),
		rps:      rps,
		burst:    burst,
		log:      log,
	}
}

// Allow checks if a request identified by key is allowed.
// Returns true if allowed, false if rate limited.
func (s *MemoryStore) Allow(_ context.Context, key string) bool {
	return s.getLimiter(key).Allow()
}

// AllowN checks if n requests identified by key are allowed.
func (s *MemoryStore) AllowN(_ context.Context, key string, n int) bool {
	return s.getLimiter(key).AllowN(time.Now(), n)
}

// getLimiter returns the rate limiter for the given key, creating one if it doesn't exist.
func (s *MemoryStore) getLimiter(key string) *rate.Limiter {
	s.mu.RLock()
	limiter, exists := s.limiters[key]
	s.mu.RUnlock()

	if exists {
		return limiter
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check after acquiring write lock
	if limiter, exists = s.limiters[key]; exists {
		return limiter
	}

	limiter = rate.NewLimiter(rate.Limit(s.rps), s.burst)
	s.limiters[key] = limiter
	return limiter
}
