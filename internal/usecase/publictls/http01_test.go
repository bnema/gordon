package publictls

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTP01Challenges(t *testing.T) {
	ctx := context.Background()

	t.Run("full lifecycle", func(t *testing.T) {
		ch := NewHTTP01Challenges()
		require.NotNil(t, ch)

		// Present a valid token
		ch.Present("token", "key-auth")
		got, ok := ch.Get(ctx, "token")
		assert.True(t, ok)
		assert.Equal(t, "key-auth", got)

		// Clean up
		ch.CleanUp("token")
		_, ok = ch.Get(ctx, "token")
		assert.False(t, ok)
	})

	t.Run("concurrent access is correct", func(t *testing.T) {
		ch := NewHTTP01Challenges()

		// Pre-populate some tokens.
		ch.Present("existing-token", "existing-keyauth")

		const numGoroutines = 20
		var wg sync.WaitGroup
		errCh := make(chan error, numGoroutines)

		// Spawn concurrent readers and writers.
		for i := range numGoroutines {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				token := fmt.Sprintf("token-%d", id)
				keyAuth := fmt.Sprintf("ka-%d", id)

				// Every goroutine presents its own token.
				ch.Present(token, keyAuth)

				// Every goroutine reads back its own token.
				got, ok := ch.Get(ctx, token)
				if !ok {
					errCh <- fmt.Errorf("token %q not found after Present", token)
					return
				}
				if got != keyAuth {
					errCh <- fmt.Errorf("token %q: got %q, want %q", token, got, keyAuth)
					return
				}

				// Every goroutine reads the pre-populated existing token.
				existing, ok := ch.Get(ctx, "existing-token")
				if !ok {
					errCh <- fmt.Errorf("existing-token not found by goroutine %d", id)
					return
				}
				if existing != "existing-keyauth" {
					errCh <- fmt.Errorf("existing-token: got %q, want existing-keyauth", existing)
					return
				}

				// Every goroutine cleans up its own token.
				ch.CleanUp(token)
				_, ok = ch.Get(ctx, token)
				if ok {
					errCh <- fmt.Errorf("token %q should be gone after CleanUp", token)
					return
				}
			}(i)
		}

		wg.Wait()
		close(errCh)

		// Collect any errors from goroutines.
		for err := range errCh {
			t.Error(err)
		}

		// Verify that pre-populated token is still present after all concurrent access.
		existing, ok := ch.Get(ctx, "existing-token")
		require.True(t, ok, "existing-token should survive concurrent access")
		assert.Equal(t, "existing-keyauth", existing)
	})
}

func TestHTTP01ChallengesRejectUnsafeToken(t *testing.T) {
	ctx := context.Background()

	t.Run("path traversal token ../secret is rejected", func(t *testing.T) {
		ch := NewHTTP01Challenges()

		ch.Present("../secret", "key-auth")
		_, ok := ch.Get(ctx, "../secret")
		assert.False(t, ok, "Present should silently ignore ../secret")
	})

	t.Run("token with slash is rejected", func(t *testing.T) {
		ch := NewHTTP01Challenges()

		ch.Present("a/b", "key-auth")
		_, ok := ch.Get(ctx, "a/b")
		assert.False(t, ok, "token containing / should be rejected")

		// Get also rejects the unsafe token
		_, ok = ch.Get(ctx, "safe/token")
		assert.False(t, ok)
	})

	t.Run("token with backslash is rejected", func(t *testing.T) {
		ch := NewHTTP01Challenges()

		ch.Present("a\\b", "key-auth")
		_, ok := ch.Get(ctx, "a\\b")
		assert.False(t, ok, "token containing backslash should be rejected")

		_, ok = ch.Get(ctx, "safe\\token")
		assert.False(t, ok)
	})

	t.Run("empty token is rejected", func(t *testing.T) {
		ch := NewHTTP01Challenges()

		ch.Present("", "key-auth")
		_, ok := ch.Get(ctx, "")
		assert.False(t, ok, "empty token should be rejected")
	})

	t.Run("'..' token is rejected", func(t *testing.T) {
		ch := NewHTTP01Challenges()

		ch.Present("..", "key-auth")
		_, ok := ch.Get(ctx, "..")
		assert.False(t, ok, "token '..' should be rejected")
	})

	t.Run("empty keyAuth is silently ignored", func(t *testing.T) {
		ch := NewHTTP01Challenges()
		ch.Present("valid-token", "")
		_, ok := ch.Get(ctx, "valid-token")
		assert.False(t, ok, "Present with empty keyAuth should not store anything")
	})

	t.Run("valid tokens are accepted and retrievable", func(t *testing.T) {
		ch := NewHTTP01Challenges()
		ch.Present("valid-token", "key-auth-value")
		got, ok := ch.Get(ctx, "valid-token")
		assert.True(t, ok)
		assert.Equal(t, "key-auth-value", got)
	})
}
