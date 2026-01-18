package ratelimit

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func TestMemoryStore_Allow_WithinLimit(t *testing.T) {
	store := NewMemoryStore(10, 10, testLogger()) // 10 RPS, burst 10
	ctx := context.Background()

	// Should allow requests up to burst
	for i := 0; i < 10; i++ {
		assert.True(t, store.Allow(ctx, "test"), "request %d should be allowed", i+1)
	}
}

func TestMemoryStore_Allow_ExceedsLimit(t *testing.T) {
	store := NewMemoryStore(1, 1, testLogger()) // 1 RPS, burst 1
	ctx := context.Background()

	// First request should succeed (consumes the burst)
	assert.True(t, store.Allow(ctx, "test"), "first request should be allowed")

	// Second request should be rate limited
	assert.False(t, store.Allow(ctx, "test"), "second request should be rate limited")
}

func TestMemoryStore_Allow_BurstBehavior(t *testing.T) {
	store := NewMemoryStore(10, 5, testLogger()) // 10 RPS, burst 5
	ctx := context.Background()

	// Should allow burst of 5
	for i := 0; i < 5; i++ {
		assert.True(t, store.Allow(ctx, "test"), "burst request %d should be allowed", i+1)
	}

	// 6th request should be rate limited (burst exhausted)
	assert.False(t, store.Allow(ctx, "test"), "request exceeding burst should be rate limited")

	// Wait for token replenishment
	time.Sleep(200 * time.Millisecond)

	// Should be able to make at least one request after waiting
	assert.True(t, store.Allow(ctx, "test"), "request after waiting should be allowed")
}

func TestMemoryStore_Allow_IndependentKeys(t *testing.T) {
	store := NewMemoryStore(1, 1, testLogger()) // 1 RPS, burst 1
	ctx := context.Background()

	// First request for key1 should succeed
	assert.True(t, store.Allow(ctx, "key1"), "first request for key1 should be allowed")

	// Key1 is now rate limited
	assert.False(t, store.Allow(ctx, "key1"), "second request for key1 should be rate limited")

	// Key2 should have its own independent limit
	assert.True(t, store.Allow(ctx, "key2"), "first request for key2 should be allowed")

	// Key2 is now rate limited
	assert.False(t, store.Allow(ctx, "key2"), "second request for key2 should be rate limited")
}

func TestMemoryStore_AllowN_Basic(t *testing.T) {
	store := NewMemoryStore(10, 10, testLogger()) // 10 RPS, burst 10
	ctx := context.Background()

	// Should allow 5 tokens at once
	assert.True(t, store.AllowN(ctx, "test", 5), "AllowN(5) should succeed with burst of 10")

	// Should allow another 5
	assert.True(t, store.AllowN(ctx, "test", 5), "AllowN(5) should succeed with 5 remaining")

	// Should not allow more
	assert.False(t, store.AllowN(ctx, "test", 1), "AllowN(1) should fail with 0 remaining")
}

func TestMemoryStore_AllowN_ExceedsBurst(t *testing.T) {
	store := NewMemoryStore(10, 5, testLogger()) // 10 RPS, burst 5
	ctx := context.Background()

	// Should not allow more than burst
	assert.False(t, store.AllowN(ctx, "test", 10), "AllowN(10) should fail with burst of 5")

	// But smaller request should work
	assert.True(t, store.AllowN(ctx, "test", 3), "AllowN(3) should succeed with burst of 5")
}

func TestMemoryStore_Allow_ConcurrentAccess(t *testing.T) {
	store := NewMemoryStore(1000, 100, testLogger()) // High RPS for concurrent testing
	ctx := context.Background()

	var wg sync.WaitGroup
	results := make(chan bool, 200)

	// Run 20 goroutines, each making 10 requests
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				results <- store.Allow(ctx, "concurrent")
			}
		}()
	}

	wg.Wait()
	close(results)

	// Count successful requests
	allowed := 0
	for result := range results {
		if result {
			allowed++
		}
	}

	// At least burst number should be allowed
	require.GreaterOrEqual(t, allowed, 100, "at least burst number of requests should be allowed")
	// And we shouldn't exceed total requests
	require.LessOrEqual(t, allowed, 200, "should not exceed total requests")
}

func TestMemoryStore_IPKeyFormat(t *testing.T) {
	store := NewMemoryStore(1, 1, testLogger())
	ctx := context.Background()

	// Test typical IP key formats
	assert.True(t, store.Allow(ctx, "ip:192.168.1.1"), "should allow first request for IP")
	assert.False(t, store.Allow(ctx, "ip:192.168.1.1"), "should rate limit same IP")
	assert.True(t, store.Allow(ctx, "ip:192.168.1.2"), "should allow first request for different IP")
}

func TestMemoryStore_GlobalKey(t *testing.T) {
	store := NewMemoryStore(1, 1, testLogger())
	ctx := context.Background()

	// Test global key
	assert.True(t, store.Allow(ctx, "global"), "should allow first global request")
	assert.False(t, store.Allow(ctx, "global"), "should rate limit global requests")
}
