package publictls

import (
	"context"
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

	t.Run("concurrent access does not panic", func(t *testing.T) {
		ch := NewHTTP01Challenges()
		done := make(chan struct{})
		go func() {
			ch.Present("t1", "ka1")
			done <- struct{}{}
		}()
		go func() {
			ch.Get(ctx, "t1")
			done <- struct{}{}
		}()
		<-done
		<-done
		// If we reach here without a race, the test passes.
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
