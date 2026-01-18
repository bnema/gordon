package ratelimit

import (
	"fmt"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
)

// NewStore creates a RateLimiter based on the configured backend.
func NewStore(backend string, rps float64, burst int, log zerowrap.Logger) (out.RateLimiter, error) {
	switch backend {
	case "memory", "":
		return NewMemoryStore(rps, burst, log), nil
	case "redis":
		return nil, fmt.Errorf("redis backend not yet implemented")
	default:
		return nil, fmt.Errorf("unknown rate limit backend: %s", backend)
	}
}
