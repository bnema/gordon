// Package ratelimit provides rate limiter implementations.
package ratelimit

import (
	"container/list"
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
	key      string
	limiter  *rate.Limiter
	lastSeen time.Time
}

const (
	maxLimiterEntries = 10000
	limiterIdleTTL    = 10 * time.Minute
)

type MemoryStore struct {
	limiters map[string]*list.Element
	lru      *list.List
	mu       sync.Mutex
	rps      float64
	burst    int
	log      zerowrap.Logger
}

// NewMemoryStore creates a new in-memory rate limiter store.
func NewMemoryStore(rps float64, burst int, log zerowrap.Logger) *MemoryStore {
	return &MemoryStore{
		limiters: make(map[string]*list.Element),
		lru:      list.New(),
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

	if element, exists := s.limiters[key]; exists {
		entry := element.Value.(*limiterEntry)
		entry.lastSeen = now
		s.lru.MoveToFront(element)
		return entry.limiter
	}

	s.evictExpiredOrOldest(now)
	limiter := rate.NewLimiter(rate.Limit(s.rps), s.burst)
	entry := &limiterEntry{key: key, limiter: limiter, lastSeen: now}
	s.limiters[key] = s.lru.PushFront(entry)
	return limiter
}

func (s *MemoryStore) evictExpiredOrOldest(now time.Time) {
	for element := s.lru.Back(); element != nil; {
		entry := element.Value.(*limiterEntry)
		if now.Sub(entry.lastSeen) <= limiterIdleTTL {
			break
		}
		previous := element.Prev()
		s.removeElement(element)
		element = previous
	}
	if len(s.limiters) >= maxLimiterEntries {
		s.removeElement(s.lru.Back())
	}
}

func (s *MemoryStore) removeElement(element *list.Element) {
	if element == nil {
		return
	}
	entry := element.Value.(*limiterEntry)
	delete(s.limiters, entry.key)
	s.lru.Remove(element)
}
