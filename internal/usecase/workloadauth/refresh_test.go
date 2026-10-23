package workloadauth

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// WA-1: a fetch started by a request whose context is already cancelled still
// completes on the service context, publishes keys and uses the refresh slot.
func TestRefreshIgnoresCallerContext(t *testing.T) {
	oldKey, _ := generateKeys(t)
	newKey := mustRSA(t)
	s, p, clk := newFixture(t, keySet(t, rsaJWK(oldKey, "old")), nil, testPolicy())
	gate := make(chan struct{})
	p.setGate(gate)
	p.set(keySet(t, rsaJWK(newKey, "new")), nil)
	clk.advance(RefreshInterval)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	token := sign(t, jwt.SigningMethodRS256, newKey, baseClaims(clk.now()), "new")
	_, err := s.Verify(cancelled, token)
	require.ErrorIs(t, err, domain.ErrIdentityProviderUnavailable)
	require.ErrorIs(t, err, context.Canceled)

	close(gate)
	waitIdle(t, s, testIssuer)
	p.mu.Lock()
	require.NoError(t, p.fetchCtxErr, "the fetch must not run on the caller context")
	p.mu.Unlock()
	require.Equal(t, 2, p.fetchCount())

	_, err = s.Verify(t.Context(), token)
	require.NoError(t, err, "keys fetched for the cancelled caller are published")
	require.Equal(t, 2, p.fetchCount(), "no second fetch was needed")
}

// WA-2: a cached key answers while a refresh is blocked on the network.
func TestKnownKidServedDuringBlockedRefresh(t *testing.T) {
	rsaKey, _ := generateKeys(t)
	s, p, clk := newFixture(t, keySet(t, rsaJWK(rsaKey, "k1")), nil, testPolicy())
	gate := make(chan struct{})
	defer close(gate)
	p.setGate(gate)
	clk.advance(CacheTTL) // the next Verify starts a background refresh that blocks

	done := make(chan error, 1)
	go func() {
		_, err := s.Verify(t.Context(), sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(clk.now()), "k1"))
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Verify of a cached kid waited for the network")
	}
	require.Eventually(t, func() bool { return p.fetchCount() == 2 }, time.Second, time.Millisecond)

	// A second known-kid request during the same blocked fetch also returns.
	_, err := s.Verify(t.Context(), sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(clk.now()), "k1"))
	require.NoError(t, err)
}

// WA-2: a request waiting for a fetch gives up when its own context ends.
func TestWaiterHonoursContext(t *testing.T) {
	rsaKey, _ := generateKeys(t)
	s, p, clk := newFixture(t, keySet(t, rsaJWK(rsaKey, "k1")), nil, testPolicy())
	gate := make(chan struct{})
	defer close(gate)
	p.setGate(gate)
	clk.advance(RefreshInterval)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Verify(ctx, sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(clk.now()), "unknown"))
	require.ErrorIs(t, err, domain.ErrIdentityProviderUnavailable)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), time.Second)
}

// WA-2: concurrent requests for the same unknown kid share one fetch.
func TestConcurrentUnknownKidSingleFlight(t *testing.T) {
	oldKey, _ := generateKeys(t)
	newKey := mustRSA(t)
	s, p, clk := newFixture(t, keySet(t, rsaJWK(oldKey, "old")), nil, testPolicy())
	gate := make(chan struct{})
	entered := make(chan struct{}, 16)
	p.mu.Lock()
	p.gate, p.entered = gate, entered
	p.mu.Unlock()
	p.set(keySet(t, rsaJWK(newKey, "new")), nil)
	clk.advance(RefreshInterval)

	token := sign(t, jwt.SigningMethodRS256, newKey, baseClaims(clk.now()), "new")
	const callers = 10
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			_, err := s.Verify(t.Context(), token)
			errs <- err
		})
	}
	<-entered
	time.Sleep(50 * time.Millisecond) // let every caller reach the wait
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 2, p.fetchCount(), "warm-up plus exactly one shared fetch")
}

// Refresh goroutines end when the service context is cancelled.
func TestRefreshStopsWithServiceContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{}, 1)
	p := &providerStub{gate: make(chan struct{}), entered: entered}
	s := NewService(ctx, p, time.Now)
	s.SetIssuers([]domain.TrustedIssuer{testPolicy()})
	<-entered
	cancel()
	waitIdle(t, s, testIssuer)
	p.mu.Lock()
	defer p.mu.Unlock()
	require.ErrorIs(t, p.fetchCtxErr, context.Canceled)
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
