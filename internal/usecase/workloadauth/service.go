// Package workloadauth verifies workload identity tokens (OIDC JWTs) against
// configured trusted issuers. Network access goes through out.OIDCProvider.
package workloadauth

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/golang-jwt/jwt/v5"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

const (
	// CacheTTL is how long fetched keys are used before a regular refresh.
	CacheTTL = time.Hour
	// RefreshInterval bounds network refreshes (forced or failed) per issuer.
	RefreshInterval = time.Minute
	// StaleLimit is how long keys stay usable while refreshes keep failing.
	StaleLimit = 24 * time.Hour
)

var allowedAlgs = []string{"RS256", "ES256"}

type issuerState struct {
	policy    domain.TrustedIssuer // immutable after creation
	mu        sync.Mutex           // serialises refreshes and guards the fields below
	keys      []verificationKey
	fetched   time.Time // last successful fetch
	attempted time.Time // last fetch attempt, successful or not
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

// NewService builds the verifier. ctx bounds background cache warm-ups and
// should live as long as the daemon. now must be safe for concurrent use.
func NewService(ctx context.Context, provider out.OIDCProvider, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{ctx: ctx, provider: provider, now: now, issuers: make(map[string]*issuerState)}
}

// SetIssuers atomically replaces the trusted issuers and warms their caches in
// the background; it never blocks on the network. Policies come from validated
// configuration with defaults applied (an explicit zero clock skew is kept).
func (s *Service) SetIssuers(issuers []domain.TrustedIssuer) {
	next := make(map[string]*issuerState, len(issuers))
	s.mu.Lock()
	for _, policy := range issuers {
		if policy.Validate() != nil || policy.MaxTokenLifetime <= 0 {
			continue
		}
		state := s.issuers[policy.Issuer]
		if state == nil || state.policy != policy {
			state = &issuerState{policy: policy}
		}
		next[policy.Issuer] = state
	}
	s.issuers = next
	s.mu.Unlock()
	for _, state := range next {
		go s.warm(state)
	}
}

func (s *Service) warm(state *issuerState) {
	state.mu.Lock()
	defer state.mu.Unlock()
	_ = s.refreshLocked(s.ctx, state, false)
}

// Verify returns the token principal. Invalid or untrusted tokens yield
// domain.ErrInvalidToken; domain.ErrIdentityProviderUnavailable means no usable
// keys exist for the issuer (empty cache or keys older than StaleLimit).
func (s *Service) Verify(ctx context.Context, raw string) (domain.WorkloadPrincipal, error) {
	var unverified jwt.RegisteredClaims
	token, _, err := jwt.NewParser(jwt.WithValidMethods(allowedAlgs)).ParseUnverified(raw, &unverified)
	if err != nil || (token.Method.Alg() != "RS256" && token.Method.Alg() != "ES256") {
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

// key returns the verification key for token, refreshing the cache when it is
// older than CacheTTL or, at most once per RefreshInterval, for an unknown kid.
func (s *Service) key(ctx context.Context, state *issuerState, token *jwt.Token) (any, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	refreshErr := s.refreshLocked(ctx, state, false)
	if !s.usableLocked(state) {
		return nil, domain.ErrIdentityProviderUnavailable
	}
	key, known := selectKey(state.keys, token)
	if key != nil {
		return key, nil
	}
	if known || refreshErr != nil {
		return nil, domain.ErrInvalidToken
	}
	// An explicit kid absent from the cache may be a rotated key.
	if err := s.refreshLocked(ctx, state, true); err != nil && !s.usableLocked(state) {
		return nil, domain.ErrIdentityProviderUnavailable
	}
	if key, _ := selectKey(state.keys, token); key != nil {
		return key, nil
	}
	return nil, domain.ErrInvalidToken
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

// refreshLocked fetches the issuer keys when the cache is older than CacheTTL
// (or always when force is set), at most once per RefreshInterval.
func (s *Service) refreshLocked(ctx context.Context, state *issuerState, force bool) error {
	now := s.now()
	if !force && len(state.keys) > 0 && now.Sub(state.fetched) < CacheTTL {
		return nil
	}
	if !state.attempted.IsZero() && now.Sub(state.attempted) < RefreshInterval {
		return errRefreshThrottled
	}
	state.attempted = now
	keys, err := s.fetch(ctx, state.policy)
	if err != nil {
		log := zerowrap.FromCtx(ctx)
		log.Warn().Err(err).
			Str(zerowrap.FieldLayer, "usecase").
			Str("issuer", state.policy.Issuer).
			Msg("workload identity key refresh failed")
		return domain.ErrIdentityProviderUnavailable
	}
	state.keys, state.fetched = keys, now
	return nil
}

var errRefreshThrottled = errors.New("key refresh throttled")

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
