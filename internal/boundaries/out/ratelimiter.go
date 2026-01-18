package out

import "context"

// RateLimiter defines the contract for rate limiting operations.
// Implementations may use different backends (memory, Redis).
type RateLimiter interface {
	// Allow checks if a request identified by key is allowed.
	// Returns true if allowed, false if rate limited.
	// Key is typically "global" or "ip:<address>".
	Allow(ctx context.Context, key string) bool

	// AllowN checks if n requests identified by key are allowed.
	AllowN(ctx context.Context, key string, n int) bool
}
