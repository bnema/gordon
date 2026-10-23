package workloadauth

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// The coordination tests below run in a synctest bubble: the service, its
// refresh goroutines and the gated provider share the bubble's fake clock, and
// synctest.Wait returns once every goroutine is blocked on a gate, a waiter
// channel or a timer. An elapsed fake time of zero proves nothing waited for a
// timeout.

// WA-1: a fetch started by a request whose context is already cancelled still
// completes on the service context, publishes keys and uses the refresh slot.
func TestRefreshIgnoresCallerContext(t *testing.T) {
	oldKey, _ := generateKeys(t)
	newKey := mustRSA(t)
	synctest.Test(t, func(t *testing.T) {
		s, p := newBubbleFixture(t, keySet(t, rsaJWK(oldKey, "old")))
		gate := make(chan struct{})
		p.setGate(gate)
		p.set(keySet(t, rsaJWK(newKey, "new")), nil)
		time.Sleep(RefreshInterval)

		cancelled, cancel := context.WithCancel(t.Context())
		cancel()
		token := sign(t, jwt.SigningMethodRS256, newKey, baseClaims(time.Now()), "new")
		_, err := s.Verify(cancelled, token)
		require.ErrorIs(t, err, domain.ErrIdentityProviderUnavailable)
		require.ErrorIs(t, err, context.Canceled)
		synctest.Wait()
		require.Equal(t, 2, p.fetchCount(), "the caller started a fetch before giving up")

		close(gate)
		synctest.Wait()
		p.mu.Lock()
		require.NoError(t, p.fetchCtxErr, "the fetch must not run on the caller context")
		p.mu.Unlock()

		_, err = s.Verify(t.Context(), token)
		require.NoError(t, err, "keys fetched for the cancelled caller are published")
		require.Equal(t, 2, p.fetchCount(), "no second fetch was needed")
	})
}

// WA-2: a cached key answers while a refresh is blocked on the network.
func TestKnownKidServedDuringBlockedRefresh(t *testing.T) {
	rsaKey, _ := generateKeys(t)
	synctest.Test(t, func(t *testing.T) {
		s, p := newBubbleFixture(t, keySet(t, rsaJWK(rsaKey, "k1")))
		gate := make(chan struct{})
		defer close(gate)
		p.setGate(gate)
		time.Sleep(CacheTTL) // the next Verify starts a background refresh that blocks

		// Waiting on the gated fetch would only end at refreshTimeout.
		start := time.Now()
		_, err := s.Verify(t.Context(), sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(time.Now()), "k1"))
		require.NoError(t, err)
		require.Zero(t, time.Since(start), "Verify of a cached kid waited for the network")
		synctest.Wait()
		require.Equal(t, 2, p.fetchCount(), "the TTL refresh is in flight")

		// A second known-kid request during the same blocked fetch also returns.
		_, err = s.Verify(t.Context(), sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(time.Now()), "k1"))
		require.NoError(t, err)
		require.Zero(t, time.Since(start))
		require.Equal(t, 2, p.fetchCount())
	})
}

// WA-2: a request waiting for a fetch gives up when its own context ends.
func TestWaiterHonoursContext(t *testing.T) {
	rsaKey, _ := generateKeys(t)
	synctest.Test(t, func(t *testing.T) {
		s, p := newBubbleFixture(t, keySet(t, rsaJWK(rsaKey, "k1")))
		gate := make(chan struct{})
		defer close(gate)
		p.setGate(gate)
		time.Sleep(RefreshInterval)

		const timeout = 50 * time.Millisecond
		ctx, cancel := context.WithTimeout(t.Context(), timeout)
		defer cancel()
		start := time.Now()
		_, err := s.Verify(ctx, sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(time.Now()), "unknown"))
		require.ErrorIs(t, err, domain.ErrIdentityProviderUnavailable)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Equal(t, timeout, time.Since(start), "the waiter returns at its own deadline, not the fetch timeout")
	})
}

// WA-2: concurrent requests for the same unknown kid share one fetch.
func TestConcurrentUnknownKidSingleFlight(t *testing.T) {
	oldKey, _ := generateKeys(t)
	newKey := mustRSA(t)
	synctest.Test(t, func(t *testing.T) {
		s, p := newBubbleFixture(t, keySet(t, rsaJWK(oldKey, "old")))
		gate := make(chan struct{})
		p.setGate(gate)
		p.set(keySet(t, rsaJWK(newKey, "new")), nil)
		time.Sleep(RefreshInterval)

		token := sign(t, jwt.SigningMethodRS256, newKey, baseClaims(time.Now()), "new")
		const callers = 10
		errs := make(chan error, callers)
		for range callers {
			go func() {
				_, err := s.Verify(t.Context(), token)
				errs <- err
			}()
		}
		synctest.Wait() // every caller is blocked on the one in-flight fetch
		require.Empty(t, errs, "no caller returned before the fetch ended")
		require.Equal(t, 2, p.fetchCount(), "warm-up plus exactly one shared fetch")
		close(gate)
		for range callers {
			require.NoError(t, <-errs)
		}
		require.Equal(t, 2, p.fetchCount())
	})
}

// Cancelling the service context wakes blocked waiters with
// ErrIdentityProviderUnavailable, with or without stale keys in the cache. The
// fetch ignores its context, so only the service context can wake them.
func TestServiceShutdownWakesWaiters(t *testing.T) {
	rsaKey, _ := generateKeys(t)
	for name, cached := range map[string][]byte{"empty cache": nil, "unknown kid": keySet(t, rsaJWK(rsaKey, "k1"))} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				p := &providerStub{raw: cached}
				s := NewService(ctx, p, time.Now)
				s.SetIssuers([]domain.TrustedIssuer{testPolicy()})
				synctest.Wait()
				gate := make(chan struct{})
				defer close(gate)
				p.mu.Lock()
				p.gate, p.ignoreCtx = gate, true // the next fetch hangs past shutdown
				p.mu.Unlock()
				time.Sleep(RefreshInterval)

				const callers = 3
				errs := make(chan error, callers)
				token := sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(time.Now()), "rotated")
				for range callers {
					go func() {
						_, err := s.Verify(t.Context(), token)
						errs <- err
					}()
				}
				synctest.Wait()
				require.Empty(t, errs, "callers wait for the hung fetch")
				require.Equal(t, 2, p.fetchCount())

				cancel()
				synctest.Wait()
				require.Len(t, errs, callers, "waiters not woken by service shutdown")
				for range callers {
					require.ErrorIs(t, <-errs, domain.ErrIdentityProviderUnavailable)
				}
			})
		})
	}
}

// Refresh goroutines end when the service context is cancelled.
func TestRefreshStopsWithServiceContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		p := &providerStub{gate: make(chan struct{})}
		s := NewService(ctx, p, time.Now)
		s.SetIssuers([]domain.TrustedIssuer{testPolicy()})
		synctest.Wait()
		require.Equal(t, 1, p.fetchCount(), "the warm-up fetch is blocked")

		start := time.Now()
		cancel()
		waitIdle(s, testIssuer)
		require.Zero(t, time.Since(start), "the fetch ended with the service, not at refreshTimeout")
		p.mu.Lock()
		defer p.mu.Unlock()
		require.ErrorIs(t, p.fetchCtxErr, context.Canceled)
	})
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// WA-6: an issuer dropped by SetIssuers is reported with its URL and reason.
func TestSetIssuersLogsDroppedIssuer(t *testing.T) {
	logs := &syncBuffer{}
	ctx := zerowrap.WithCtx(t.Context(), zerowrap.New(zerowrap.Config{Level: "warn", Format: "json", Output: logs}))
	p := &providerStub{err: errors.New("offline")}
	s := NewService(ctx, p, time.Now)

	insecure := testPolicy()
	insecure.Issuer = "http://insecure.example"
	noLifetime := testPolicy()
	noLifetime.Issuer = "https://nolifetime.example"
	noLifetime.MaxTokenLifetime = 0
	s.SetIssuers([]domain.TrustedIssuer{insecure, noLifetime})

	out := logs.String()
	require.Contains(t, out, "trusted issuer ignored")
	require.Contains(t, out, "http://insecure.example")
	require.Contains(t, out, "https://nolifetime.example")
	require.Contains(t, out, "max token lifetime must be positive")
}
