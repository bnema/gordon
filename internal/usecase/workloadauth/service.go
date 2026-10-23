// Package workloadauth verifies workload identity tokens (OIDC JWTs) against
// configured trusted issuers. Network access goes through out.OIDCProvider.
package workloadauth

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/golang-jwt/jwt/v5"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

const (
	// CacheTTL is how long fetched keys are used before a background refresh.
	CacheTTL = time.Hour
	// RefreshInterval bounds key fetches per issuer: at most one fetch starts per
	// interval, whatever triggered it (startup or reload warm-up, TTL expiry, an
	// unknown kid, a retry after failure). A key rotated within this interval
	// after a previous fetch is therefore picked up up to RefreshInterval late.
	RefreshInterval = time.Minute
	// StaleLimit is how long keys stay usable while refreshes keep failing.
	StaleLimit = 24 * time.Hour
	// refreshTimeout bounds one discovery + JWKS fetch (two adapter requests).
	refreshTimeout = 20 * time.Second
)

var allowedAlgs = []string{"RS256", "ES256"}

// issuerState is one issuer's key cache. mu guards the mutable fields and is
// never held during network I/O.
type issuerState struct {
	policy     domain.TrustedIssuer // immutable after creation
	mu         sync.Mutex
	keys       []verificationKey
	fetched    time.Time     // start of the last successful fetch
	attempted  time.Time     // start of the last fetch, successful or not
	refreshing chan struct{} // non-nil while a fetch runs; closed when it ends
	waiters    int           // Verify calls blocked on refreshing (observability, tests)
}

// Service implements in.WorkloadIdentityService. Each issuer has its own key
// cache; a changed issuer policy starts from an empty cache.
type Service struct {
	provider out.OIDCProvider
	now      func() time.Time
	ctx      context.Context
	mu       sync.RWMutex
	issuers  map[string]*issuerState
}

// NewService builds the verifier. ctx is the service lifetime: key fetches run
// on it (never on a caller's context) and stop when it is cancelled. now must
// be safe for concurrent use.
func NewService(ctx context.Context, provider out.OIDCProvider, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{ctx: ctx, provider: provider, now: now, issuers: make(map[string]*issuerState)}
}

// SetIssuers atomically replaces the trusted issuers and starts background key
// fetches; it never blocks on the network. Policies come from validated
// configuration with defaults applied (an explicit zero clock skew is kept).
func (s *Service) SetIssuers(issuers []domain.TrustedIssuer) {
	next := make(map[string]*issuerState, len(issuers))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, policy := range issuers {
		if err := checkPolicy(policy); err != nil {
			log := zerowrap.FromCtx(s.ctx)
			log.Warn().Err(err).
				Str(zerowrap.FieldLayer, "usecase").
				Str("issuer", policy.Issuer).
				Msg("trusted issuer ignored")
			continue
		}
		state := s.issuers[policy.Issuer]
		if state == nil || state.policy != policy {
			state = &issuerState{policy: policy}
		}
		next[policy.Issuer] = state
	}
	s.issuers = next
	for _, state := range next {
		state.mu.Lock()
		s.startRefreshLocked(state, false)
		state.mu.Unlock()
	}
}

func checkPolicy(policy domain.TrustedIssuer) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if policy.MaxTokenLifetime <= 0 {
		return fmt.Errorf("max token lifetime must be positive")
	}
	return nil
}

// Verify returns the token principal. Invalid or untrusted tokens yield
// domain.ErrInvalidToken; domain.ErrIdentityProviderUnavailable means no usable
// keys exist for the issuer (empty cache, keys older than StaleLimit, or ctx
// ended while waiting for a fetch).
func (s *Service) Verify(ctx context.Context, raw string) (domain.WorkloadPrincipal, error) {
	var unverified jwt.RegisteredClaims
	token, _, err := jwt.NewParser().ParseUnverified(raw, &unverified)
	if err != nil || !slices.Contains(allowedAlgs, token.Method.Alg()) {
		return domain.WorkloadPrincipal{}, domain.ErrInvalidToken
	}
	s.mu.RLock()
	state := s.issuers[unverified.Issuer]
	s.mu.RUnlock()
	if state == nil {
		return domain.WorkloadPrincipal{}, domain.ErrInvalidToken
	}
	key, err := s.key(ctx, state, token)
	if err != nil {
		return domain.WorkloadPrincipal{}, err
	}
	return s.verifySigned(raw, state.policy, key)
}

func (s *Service) verifySigned(raw string, p domain.TrustedIssuer, key any) (domain.WorkloadPrincipal, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(raw, &claims, func(*jwt.Token) (any, error) { return key, nil },
		jwt.WithValidMethods(allowedAlgs), jwt.WithIssuer(p.Issuer), jwt.WithAudience(p.Audience),
		jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(p.ClockSkew), jwt.WithTimeFunc(s.now))
	if err != nil || claims.Subject == "" || claims.IssuedAt == nil || claims.ExpiresAt == nil {
		return domain.WorkloadPrincipal{}, domain.ErrInvalidToken
	}
	if lifetime := claims.ExpiresAt.Sub(claims.IssuedAt.Time); lifetime <= 0 || lifetime > p.MaxTokenLifetime {
		return domain.WorkloadPrincipal{}, domain.ErrInvalidToken
	}
	return domain.WorkloadPrincipal{Issuer: p.Issuer, Subject: claims.Subject}, nil
}

// key returns the verification key for token. It waits for an in-flight fetch
// only when the cache cannot answer (no usable keys, or an unknown kid); ctx
// bounds that wait, never the fetch itself.
func (s *Service) key(ctx context.Context, state *issuerState, token *jwt.Token) (any, error) {
	state.mu.Lock()
	key, wait, err := s.lookupLocked(state, token, true)
	state.mu.Unlock()
	if wait == nil {
		return key, err
	}
	err = s.wait(ctx, state, wait)
	state.mu.Lock()
	defer state.mu.Unlock()
	state.waiters--
	if err != nil {
		return nil, err
	}
	key, _, err = s.lookupLocked(state, token, false)
	return key, err
}

// wait blocks until the in-flight fetch ends, the caller gives up or the
// service shuts down. The caller must decrement state.waiters afterwards.
func (s *Service) wait(ctx context.Context, state *issuerState, done <-chan struct{}) error {
	state.mu.Lock()
	state.waiters++
	state.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", domain.ErrIdentityProviderUnavailable, ctx.Err())
	case <-s.ctx.Done():
	}
	if err := s.ctx.Err(); err != nil {
		return fmt.Errorf("%w: service stopping: %w", domain.ErrIdentityProviderUnavailable, err)
	}
	return nil
}

// lookupLocked answers from the cache. With mayRefresh it starts a fetch when
// the cache is older than CacheTTL (served meanwhile) or the kid is unknown,
// and returns a channel to wait on when only that fetch can answer.
func (s *Service) lookupLocked(state *issuerState, token *jwt.Token, mayRefresh bool) (any, <-chan struct{}, error) {
	var inflight chan struct{}
	if mayRefresh {
		inflight = s.startRefreshLocked(state, false)
	}
	if !s.usableLocked(state) {
		if inflight != nil {
			return nil, inflight, nil
		}
		return nil, nil, domain.ErrIdentityProviderUnavailable
	}
	key, known := selectKey(state.keys, token)
	if key != nil {
		return key, nil, nil
	}
	if !known && mayRefresh {
		// An explicit kid absent from the cache may be a rotated key.
		if inflight = s.startRefreshLocked(state, true); inflight != nil {
			return nil, inflight, nil
		}
	}
	return nil, nil, domain.ErrInvalidToken
}

func (s *Service) usableLocked(state *issuerState) bool {
	return len(state.keys) > 0 && s.now().Sub(state.fetched) <= StaleLimit
}

// selectKey returns the matching key. known reports that the token cannot match
// any key a refresh could add: no kid (or a malformed one), or a kid already
// cached with another algorithm.
func selectKey(keys []verificationKey, token *jwt.Token) (key any, known bool) {
	alg := token.Method.Alg()
	rawKid, hasKid := token.Header["kid"]
	if !hasKid {
		if len(keys) == 1 && keys[0].alg == alg {
			return keys[0].public, true
		}
		return nil, true // kid is required when the set has several keys
	}
	kid, ok := rawKid.(string)
	if !ok || kid == "" {
		return nil, true
	}
	for _, k := range keys {
		if k.kid == kid {
			if k.alg != alg {
				return nil, true
			}
			return k.public, true
		}
	}
	return nil, false
}

// startRefreshLocked returns the in-flight fetch, or starts one when the cache
// is older than CacheTTL (always when force is set) and RefreshInterval has
// passed since the last attempt. It returns nil when no fetch runs.
func (s *Service) startRefreshLocked(state *issuerState, force bool) chan struct{} {
	if state.refreshing != nil {
		return state.refreshing
	}
	now := s.now()
	if !force && len(state.keys) > 0 && now.Sub(state.fetched) < CacheTTL {
		return nil
	}
	if !state.attempted.IsZero() && now.Sub(state.attempted) < RefreshInterval {
		return nil
	}
	state.attempted = now
	done := make(chan struct{})
	state.refreshing = done
	go s.refresh(state, now, done)
	return done
}

// refresh fetches keys on a context detached from any caller, bounded by the
// service lifetime and refreshTimeout, then publishes them and closes done.
func (s *Service) refresh(state *issuerState, started time.Time, done chan struct{}) {
	ctx, cancel := context.WithTimeout(s.ctx, refreshTimeout)
	defer cancel()
	keys, err := s.fetch(ctx, state.policy)
	if err != nil {
		log := zerowrap.FromCtx(s.ctx)
		log.Warn().Err(err).
			Str(zerowrap.FieldLayer, "usecase").
			Str("issuer", state.policy.Issuer).
			Msg("workload identity key refresh failed")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err == nil {
		state.keys, state.fetched = keys, started
	}
	state.refreshing = nil
	close(done)
}

func (s *Service) fetch(ctx context.Context, policy domain.TrustedIssuer) ([]verificationKey, error) {
	uri := policy.JWKSURI
	if uri == "" {
		discovered, err := s.provider.Discover(ctx, policy)
		if err != nil {
			return nil, err
		}
		uri = discovered
	}
	raw, err := s.provider.FetchJWKS(ctx, policy, uri)
	if err != nil {
		return nil, err
	}
	return parseJWKS(raw)
}
