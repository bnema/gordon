package workloadauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

const (
	testIssuer   = "https://issuer.example"
	testAudience = "https://gordon.example"
	testJWKSURI  = "https://issuer.example/keys"
)

// providerStub is a concurrency-safe out.OIDCProvider fake.
type providerStub struct {
	mu          sync.Mutex
	raw         []byte
	err         error
	fetches     int
	discoveries int
	jwksURIs    []string
	block       chan struct{} // when set, FetchJWKS waits for it to close
}

func (p *providerStub) Discover(context.Context, domain.TrustedIssuer) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.discoveries++
	return "https://issuer.example/discovered-keys", p.err
}

func (p *providerStub) FetchJWKS(_ context.Context, _ domain.TrustedIssuer, uri string) ([]byte, error) {
	p.mu.Lock()
	block := p.block
	p.mu.Unlock()
	if block != nil {
		<-block
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fetches++
	p.jwksURIs = append(p.jwksURIs, uri)
	return append([]byte(nil), p.raw...), p.err
}

func (p *providerStub) set(raw []byte, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.raw, p.err = raw, err
}

func (p *providerStub) fetchCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fetches
}

// clock is a concurrency-safe manual clock.
type clock struct{ unix atomic.Int64 }

func (c *clock) now() time.Time          { return time.Unix(c.unix.Load(), 0) }
func (c *clock) advance(d time.Duration) { c.unix.Add(int64(d / time.Second)) }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func rsaJWK(key *rsa.PrivateKey, kid string) map[string]any {
	return map[string]any{"kty": "RSA", "kid": kid, "alg": "RS256", "use": "sig",
		"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes())}
}

func ecJWK(key *ecdsa.PrivateKey, kid string) map[string]any {
	raw, err := key.PublicKey.Bytes() // 0x04 || X || Y
	if err != nil {
		panic(err)
	}
	return map[string]any{"kty": "EC", "kid": kid, "alg": "ES256", "use": "sig", "crv": "P-256",
		"x": b64(raw[1:33]), "y": b64(raw[33:65])}
}

func keySet(t *testing.T, keys ...map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"keys": keys})
	require.NoError(t, err)
	return raw
}

func sign(t *testing.T, method jwt.SigningMethod, key any, claims jwt.MapClaims, kid any) string {
	t.Helper()
	token := jwt.NewWithClaims(method, claims)
	if kid != nil {
		token.Header["kid"] = kid
	}
	raw, err := token.SignedString(key)
	require.NoError(t, err)
	return raw
}

func baseClaims(now time.Time) jwt.MapClaims {
	return jwt.MapClaims{"iss": testIssuer, "sub": "repo:acme/web:environment:production", "aud": testAudience,
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()}
}

func testPolicy() domain.TrustedIssuer {
	return domain.TrustedIssuer{Issuer: testIssuer, Audience: testAudience, JWKSURI: testJWKSURI,
		MaxTokenLifetime: time.Hour, ClockSkew: time.Minute}
}

// newFixture builds a service for policy and waits for the background warm-up
// so fetch counts are deterministic.
func newFixture(t *testing.T, raw []byte, fetchErr error, policy domain.TrustedIssuer) (*Service, *providerStub, *clock) {
	t.Helper()
	clk := &clock{}
	clk.unix.Store(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC).Unix())
	p := &providerStub{raw: raw, err: fetchErr}
	s := NewService(t.Context(), p, clk.now)
	s.SetIssuers([]domain.TrustedIssuer{policy})
	require.Eventually(t, func() bool { return p.fetchCount() == 1 }, time.Second, time.Millisecond)
	waitIdle(s, policy.Issuer)
	return s, p, clk
}

// waitIdle blocks until no refresh is running for issuer.
func waitIdle(s *Service, issuer string) {
	s.mu.RLock()
	state := s.issuers[issuer]
	s.mu.RUnlock()
	state.mu.Lock()
	state.mu.Unlock() //nolint:staticcheck // empty critical section is the point
}

func generateKeys(t *testing.T) (*rsa.PrivateKey, *ecdsa.PrivateKey) {
	t.Helper()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return rsaKey, ecKey
}

func TestVerifyClaims(t *testing.T) {
	rsaKey, ecKey := generateKeys(t)
	algorithms := []struct {
		name   string
		method jwt.SigningMethod
		key    any
		jwk    map[string]any
	}{
		{"RS256", jwt.SigningMethodRS256, rsaKey, rsaJWK(rsaKey, "k1")},
		{"ES256", jwt.SigningMethodES256, ecKey, ecJWK(ecKey, "k1")},
	}
	for _, alg := range algorithms {
		t.Run(alg.name, func(t *testing.T) {
			s, _, clk := newFixture(t, keySet(t, alg.jwk), nil, testPolicy())
			now := clk.now()
			cases := []struct {
				name   string
				change func(jwt.MapClaims)
				valid  bool
			}{
				{"valid", func(jwt.MapClaims) {}, true},
				{"issuer with trailing slash", func(c jwt.MapClaims) { c["iss"] = testIssuer + "/" }, false},
				{"issuer different case", func(c jwt.MapClaims) { c["iss"] = "https://ISSUER.example" }, false},
				{"issuer untrusted", func(c jwt.MapClaims) { c["iss"] = "https://other.example" }, false},
				{"issuer missing", func(c jwt.MapClaims) { delete(c, "iss") }, false},
				{"audience missing", func(c jwt.MapClaims) { delete(c, "aud") }, false},
				{"audience wrong", func(c jwt.MapClaims) { c["aud"] = "https://evil.example" }, false},
				{"audience with trailing slash", func(c jwt.MapClaims) { c["aud"] = testAudience + "/" }, false},
				{"audience list contains", func(c jwt.MapClaims) { c["aud"] = []string{"other", testAudience} }, true},
				{"exp missing", func(c jwt.MapClaims) { delete(c, "exp") }, false},
				{"iat missing", func(c jwt.MapClaims) { delete(c, "iat") }, false},
				{"sub empty", func(c jwt.MapClaims) { c["sub"] = "" }, false},
				{"sub missing", func(c jwt.MapClaims) { delete(c, "sub") }, false},
				{"expired within skew", func(c jwt.MapClaims) {
					c["iat"], c["exp"] = now.Add(-30*time.Minute).Unix(), now.Add(-59*time.Second).Unix()
				}, true},
				{"expired beyond skew", func(c jwt.MapClaims) {
					c["iat"], c["exp"] = now.Add(-30*time.Minute).Unix(), now.Add(-61*time.Second).Unix()
				}, false},
				{"nbf within skew", func(c jwt.MapClaims) { c["nbf"] = now.Add(59 * time.Second).Unix() }, true},
				{"nbf beyond skew", func(c jwt.MapClaims) { c["nbf"] = now.Add(61 * time.Second).Unix() }, false},
				{"iat within skew", func(c jwt.MapClaims) { c["iat"] = now.Add(59 * time.Second).Unix() }, true},
				{"iat beyond skew", func(c jwt.MapClaims) { c["iat"] = now.Add(61 * time.Second).Unix() }, false},
				{"lifetime at cap", func(c jwt.MapClaims) { c["exp"] = now.Add(time.Hour).Unix() }, true},
				{"lifetime above cap", func(c jwt.MapClaims) { c["exp"] = now.Add(time.Hour + time.Second).Unix() }, false},
				{"exp before iat", func(c jwt.MapClaims) {
					c["iat"], c["exp"] = now.Unix(), now.Add(-time.Second).Unix()
				}, false},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					claims := baseClaims(now)
					tc.change(claims)
					principal, err := s.Verify(t.Context(), sign(t, alg.method, alg.key, claims, "k1"))
					if !tc.valid {
						require.ErrorIs(t, err, domain.ErrInvalidToken)
						return
					}
					require.NoError(t, err)
					require.Equal(t, domain.WorkloadPrincipal{Issuer: testIssuer, Subject: "repo:acme/web:environment:production"}, principal)
				})
			}
		})
	}
}

func TestVerifyZeroClockSkew(t *testing.T) {
	rsaKey, _ := generateKeys(t)
	policy := testPolicy()
	policy.ClockSkew = 0
	s, _, clk := newFixture(t, keySet(t, rsaJWK(rsaKey, "k1")), nil, policy)
	claims := baseClaims(clk.now())
	claims["nbf"] = clk.now().Add(time.Second).Unix()
	_, err := s.Verify(t.Context(), sign(t, jwt.SigningMethodRS256, rsaKey, claims, "k1"))
	require.ErrorIs(t, err, domain.ErrInvalidToken)
}

func TestVerifyRejectsAlgorithmAttacks(t *testing.T) {
	rsaKey, ecKey := generateKeys(t)
	s, p, clk := newFixture(t, keySet(t, rsaJWK(rsaKey, "rsa"), ecJWK(ecKey, "ec")), nil, testPolicy())
	claims := baseClaims(clk.now())
	publicDER, err := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	require.NoError(t, err)
	attacks := map[string]string{
		"none":                    sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, claims, "rsa"),
		"HS256 with public key":   sign(t, jwt.SigningMethodHS256, publicDER, claims, "rsa"),
		"HS256 with modulus":      sign(t, jwt.SigningMethodHS256, rsaKey.N.Bytes(), claims, "rsa"),
		"ES256 against RSA kid":   sign(t, jwt.SigningMethodES256, ecKey, claims, "rsa"),
		"RS256 against EC kid":    sign(t, jwt.SigningMethodRS256, rsaKey, claims, "ec"),
		"RS384 not allowed":       sign(t, jwt.SigningMethodRS384, rsaKey, claims, "rsa"),
		"PS256 not allowed":       sign(t, jwt.SigningMethodPS256, rsaKey, claims, "rsa"),
		"missing kid, two keys":   sign(t, jwt.SigningMethodRS256, rsaKey, claims, nil),
		"non-string kid":          sign(t, jwt.SigningMethodRS256, rsaKey, claims, 7),
		"empty kid":               sign(t, jwt.SigningMethodRS256, rsaKey, claims, ""),
		"signed by another key":   sign(t, jwt.SigningMethodRS256, mustRSA(t), claims, "rsa"),
		"malformed":               "not.a.jwt",
		"empty":                   "",
		"tampered payload":        tamper(sign(t, jwt.SigningMethodRS256, rsaKey, claims, "rsa")),
		"ES256 with RSA kid swap": sign(t, jwt.SigningMethodES256, ecKey, claims, "rsa"),
	}
	for name, raw := range attacks {
		t.Run(name, func(t *testing.T) {
			_, err := s.Verify(t.Context(), raw)
			require.ErrorIs(t, err, domain.ErrInvalidToken)
		})
	}
	require.Equal(t, 1, p.fetchCount(), "known-kid and kid-less failures must not refresh")
}

func mustRSA(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key
}

func tamper(raw string) string {
	b := []byte(raw)
	i := len(b) / 2
	if b[i] == 'A' {
		b[i] = 'B'
	} else {
		b[i] = 'A'
	}
	return string(b)
}

func TestVerifyKidRules(t *testing.T) {
	rsaKey, ecKey := generateKeys(t)
	t.Run("single key without kid", func(t *testing.T) {
		s, _, clk := newFixture(t, keySet(t, rsaJWK(rsaKey, "only")), nil, testPolicy())
		_, err := s.Verify(t.Context(), sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(clk.now()), nil))
		require.NoError(t, err)
	})
	t.Run("two keys select by kid", func(t *testing.T) {
		s, _, clk := newFixture(t, keySet(t, rsaJWK(rsaKey, "rsa"), ecJWK(ecKey, "ec")), nil, testPolicy())
		_, err := s.Verify(t.Context(), sign(t, jwt.SigningMethodES256, ecKey, baseClaims(clk.now()), "ec"))
		require.NoError(t, err)
		_, err = s.Verify(t.Context(), sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(clk.now()), "rsa"))
		require.NoError(t, err)
	})
}

func TestUnknownKidRefreshesAtMostOncePerMinute(t *testing.T) {
	rsaKey, _ := generateKeys(t)
	s, p, clk := newFixture(t, keySet(t, rsaJWK(rsaKey, "k1")), nil, testPolicy())
	for range 20 {
		_, err := s.Verify(t.Context(), sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(clk.now()), "unknown"))
		require.ErrorIs(t, err, domain.ErrInvalidToken)
	}
	require.Equal(t, 1, p.fetchCount(), "warm-up fetch only: first forced refresh is throttled by the warm-up")
	clk.advance(RefreshInterval)
	for range 20 {
		_, err := s.Verify(t.Context(), sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(clk.now()), "unknown"))
		require.ErrorIs(t, err, domain.ErrInvalidToken)
	}
	require.Equal(t, 2, p.fetchCount(), "exactly one forced refresh per minute")
	clk.advance(RefreshInterval - time.Second)
	_, _ = s.Verify(t.Context(), sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(clk.now()), "unknown"))
	require.Equal(t, 2, p.fetchCount())
	clk.advance(time.Second)
	_, _ = s.Verify(t.Context(), sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(clk.now()), "unknown"))
	require.Equal(t, 3, p.fetchCount())
}

func TestKeyRotation(t *testing.T) {
	oldKey, _ := generateKeys(t)
	newKey := mustRSA(t)
	s, p, clk := newFixture(t, keySet(t, rsaJWK(oldKey, "old")), nil, testPolicy())
	_, err := s.Verify(t.Context(), sign(t, jwt.SigningMethodRS256, oldKey, baseClaims(clk.now()), "old"))
	require.NoError(t, err)

	p.set(keySet(t, rsaJWK(newKey, "new")), nil)
	clk.advance(RefreshInterval)
	_, err = s.Verify(t.Context(), sign(t, jwt.SigningMethodRS256, newKey, baseClaims(clk.now()), "new"))
	require.NoError(t, err, "unknown kid triggers a refresh that finds the rotated key")
	require.Equal(t, 2, p.fetchCount())

	_, err = s.Verify(t.Context(), sign(t, jwt.SigningMethodRS256, oldKey, baseClaims(clk.now()), "old"))
	require.ErrorIs(t, err, domain.ErrInvalidToken, "removed key is no longer trusted")
}

func TestCacheTTLRefresh(t *testing.T) {
	rsaKey, _ := generateKeys(t)
	s, p, clk := newFixture(t, keySet(t, rsaJWK(rsaKey, "k1")), nil, testPolicy())
	verify := func() error {
		_, err := s.Verify(t.Context(), sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(clk.now()), "k1"))
		return err
	}
	clk.advance(CacheTTL - time.Second)
	require.NoError(t, verify())
	require.Equal(t, 1, p.fetchCount())
	clk.advance(time.Second)
	require.NoError(t, verify())
	require.Equal(t, 2, p.fetchCount())
}

func TestStaleWindowThenFailClosed(t *testing.T) {
	rsaKey, _ := generateKeys(t)
	s, p, clk := newFixture(t, keySet(t, rsaJWK(rsaKey, "k1")), nil, testPolicy())
	verify := func() error {
		_, err := s.Verify(t.Context(), sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(clk.now()), "k1"))
		return err
	}
	p.set(nil, errors.New("issuer offline"))
	clk.advance(CacheTTL)
	require.NoError(t, verify(), "stale keys stay usable while refresh fails")
	fetches := p.fetchCount()
	for range 5 {
		require.NoError(t, verify())
	}
	require.Equal(t, fetches, p.fetchCount(), "failed refreshes are throttled")

	clk.advance(StaleLimit - CacheTTL)
	require.NoError(t, verify(), "exactly StaleLimit old is still usable")
	clk.advance(time.Second)
	require.ErrorIs(t, verify(), domain.ErrIdentityProviderUnavailable)

	p.set(keySet(t, rsaJWK(rsaKey, "k1")), nil)
	clk.advance(RefreshInterval)
	require.NoError(t, verify(), "recovers once the issuer is reachable")
}

func TestUnavailableVersusInvalid(t *testing.T) {
	rsaKey, _ := generateKeys(t)
	s, p, clk := newFixture(t, nil, errors.New("issuer offline"), testPolicy())
	valid := sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(clk.now()), "k1")

	_, err := s.Verify(t.Context(), valid)
	require.ErrorIs(t, err, domain.ErrIdentityProviderUnavailable, "no keys yet")

	untrusted := baseClaims(clk.now())
	untrusted["iss"] = "https://other.example"
	_, err = s.Verify(t.Context(), sign(t, jwt.SigningMethodRS256, rsaKey, untrusted, "k1"))
	require.ErrorIs(t, err, domain.ErrInvalidToken, "untrusted issuer never contacts a provider")

	_, err = s.Verify(t.Context(), "garbage")
	require.ErrorIs(t, err, domain.ErrInvalidToken)

	p.set([]byte(`{"keys":[{"kty":"RSA","use":"sig","n":"AQAB","e":"AQAB"}]}`), nil)
	clk.advance(RefreshInterval)
	_, err = s.Verify(t.Context(), valid)
	require.ErrorIs(t, err, domain.ErrIdentityProviderUnavailable, "a JWKS without usable keys is an outage")

	p.set(keySet(t, rsaJWK(rsaKey, "k1")), nil)
	clk.advance(RefreshInterval)
	_, err = s.Verify(t.Context(), valid)
	require.NoError(t, err)
}

func TestDiscoveryOnlyWithoutJWKSURI(t *testing.T) {
	rsaKey, _ := generateKeys(t)
	_, p, _ := newFixture(t, keySet(t, rsaJWK(rsaKey, "k1")), nil, testPolicy())
	require.Zero(t, p.discoveries)
	require.Equal(t, []string{testJWKSURI}, p.jwksURIs)

	policy := testPolicy()
	policy.JWKSURI = ""
	_, p, _ = newFixture(t, keySet(t, rsaJWK(rsaKey, "k1")), nil, policy)
	require.Equal(t, 1, p.discoveries)
	require.Equal(t, []string{"https://issuer.example/discovered-keys"}, p.jwksURIs)
}

func TestSetIssuersNeverBlocksAndReplacesTrust(t *testing.T) {
	rsaKey, _ := generateKeys(t)
	p := &providerStub{raw: keySet(t, rsaJWK(rsaKey, "k1")), block: make(chan struct{})}
	clk := &clock{}
	clk.unix.Store(time.Now().Unix())
	s := NewService(t.Context(), p, clk.now)

	done := make(chan struct{})
	go func() {
		s.SetIssuers([]domain.TrustedIssuer{testPolicy()})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SetIssuers blocked on the network")
	}
	close(p.block)
	require.Eventually(t, func() bool { return p.fetchCount() == 1 }, time.Second, time.Millisecond)

	token := sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(clk.now()), "k1")
	_, err := s.Verify(t.Context(), token)
	require.NoError(t, err)

	s.SetIssuers([]domain.TrustedIssuer{testPolicy()})
	_, err = s.Verify(t.Context(), token)
	require.NoError(t, err, "an unchanged issuer keeps its cache")
	waitIdle(s, testIssuer)
	require.Equal(t, 1, p.fetchCount())

	changed := testPolicy()
	changed.Audience = "https://new.example"
	s.SetIssuers([]domain.TrustedIssuer{changed})
	_, err = s.Verify(t.Context(), token)
	require.ErrorIs(t, err, domain.ErrInvalidToken, "new audience applies immediately")

	invalid := testPolicy()
	invalid.Issuer = "http://insecure.example"
	s.SetIssuers([]domain.TrustedIssuer{invalid})
	_, err = s.Verify(t.Context(), token)
	require.ErrorIs(t, err, domain.ErrInvalidToken, "removed issuer is no longer trusted")
}

func TestConcurrentVerifyAndReload(t *testing.T) {
	rsaKey, _ := generateKeys(t)
	s, p, clk := newFixture(t, keySet(t, rsaJWK(rsaKey, "k1")), nil, testPolicy())
	token := sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(clk.now()), "k1")
	unknown := sign(t, jwt.SigningMethodRS256, rsaKey, baseClaims(clk.now()), "unknown")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for range 50 {
				switch i % 3 {
				case 0:
					_, _ = s.Verify(context.Background(), token)
				case 1:
					_, _ = s.Verify(context.Background(), unknown)
				default:
					s.SetIssuers([]domain.TrustedIssuer{testPolicy()})
				}
			}
		})
	}
	wg.Wait()
	require.LessOrEqual(t, p.fetchCount(), 2)
}

func TestParseJWKS(t *testing.T) {
	rsaKey, ecKey := generateKeys(t)
	small, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	rejected := []struct {
		name  string
		key   map[string]any
		field string
		value any
	}{
		{"RSA below 2048 bits", rsaJWK(rsaKey, "r"), "n", b64(small.N.Bytes())},
		{"RSA even exponent", rsaJWK(rsaKey, "r"), "e", b64(big.NewInt(65536).Bytes())},
		{"RSA exponent 1", rsaJWK(rsaKey, "r"), "e", b64([]byte{1})},
		{"RSA huge exponent", rsaJWK(rsaKey, "r"), "e", b64(make([]byte, 9))},
		{"RSA bad base64", rsaJWK(rsaKey, "r"), "n", "!!"},
		{"RSA alg mismatch", rsaJWK(rsaKey, "r"), "alg", "ES256"},
		{"RSA alg RS512", rsaJWK(rsaKey, "r"), "alg", "RS512"},
		{"use enc", rsaJWK(rsaKey, "r"), "use", "enc"},
		{"use missing", rsaJWK(rsaKey, "r"), "use", ""},
		{"EC curve P-384", ecJWK(ecKey, "e"), "crv", "P-384"},
		{"EC point off curve", ecJWK(ecKey, "e"), "x", b64(make([]byte, 32))},
		{"EC short coordinate", ecJWK(ecKey, "e"), "y", b64(make([]byte, 31))},
		{"oct key", map[string]any{"kty": "oct", "use": "sig", "k": "c2VjcmV0"}, "kid", "o"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			tc.key[tc.field] = tc.value
			_, err := parseJWKS(keySet(t, tc.key))
			require.Error(t, err)
		})
	}

	_, err = parseJWKS(keySet(t, rsaJWK(rsaKey, "same"), ecJWK(ecKey, "same")))
	require.Error(t, err, "duplicate kid is ambiguous")
	_, err = parseJWKS([]byte("{"))
	require.Error(t, err)

	noAlg := rsaJWK(rsaKey, "r")
	delete(noAlg, "alg")
	enc := ecJWK(ecKey, "enc")
	enc["use"] = "enc"
	keys, err := parseJWKS(keySet(t, noAlg, enc, ecJWK(ecKey, "e")))
	require.NoError(t, err)
	require.Len(t, keys, 2, "unusable keys are skipped, usable ones kept")
	require.Equal(t, "RS256", keys[0].alg)
	require.Equal(t, "ES256", keys[1].alg)
}
