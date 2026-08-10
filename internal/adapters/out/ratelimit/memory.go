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
type limiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

const (
	maxLimiterEntries = 10000
	limiterIdleTTL    = 10 * time.Minute
)

type MemoryStore struct {
	limiters map[string]limiterEntry
	mu       sync.RWMutex
	rps      float64
	burst    int
	log      zerowrap.Logger
}

// NewMemoryStore creates a new in-memory rate limiter store.
func NewMemoryStore(rps float64, burst int, log zerowrap.Logger) *MemoryStore {
	return &MemoryStore{
		limiters: make(map[string]limiterEntry),
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
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry, exists := s.limiters[key]; exists {
		entry.lastSeen = now
		s.limiters[key] = entry
		return entry.limiter
	}

	s.evictExpiredOrOldest(now)
	limiter := rate.NewLimiter(rate.Limit(s.rps), s.burst)
	s.limiters[key] = limiterEntry{limiter: limiter, lastSeen: now}
	return limiter
}

func (s *MemoryStore) evictExpiredOrOldest(now time.Time) {
	for key, entry := range s.limiters {
		if now.Sub(entry.lastSeen) > limiterIdleTTL {
			delete(s.limiters, key)
		}
	}
	if len(s.limiters) < maxLimiterEntries {
		return
	}

	var oldestKey string
	var oldest time.Time
	found := false
	for key, entry := range s.limiters {
		if !found || entry.lastSeen.Before(oldest) {
			oldestKey = key
			oldest = entry.lastSeen
			found = true
		}
	}
	if found {
		delete(s.limiters, oldestKey)
	}
}
