package ci

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/http/httphelper"
	"github.com/bnema/gordon/internal/boundaries/in"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/auth"
)

const (
	testDomain  = "ci.example.com"
	testIssuer  = "https://issuer.example"
	testDigest  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testUpload  = "0b7f5c4e-8a0e-4d2b-9a8f-2f6d1e3c4b5a"
	trustedPeer = "10.0.0.5"
)

var (
	webPrincipal   = domain.WorkloadPrincipal{Issuer: testIssuer, Subject: "repo:acme/web:ref:refs/heads/main"}
	otherPrincipal = domain.WorkloadPrincipal{Issuer: testIssuer, Subject: "repo:acme/other:ref:refs/heads/main"}
)

// fakeIdentity maps raw JWTs to principals. A JWT absent from the map is
// invalid; "unavailable" reports an unreachable identity provider. block,
// when set, runs instead and lets a test hold Verify open.
type fakeIdentity struct {
	mu     sync.Mutex
	tokens map[string]domain.WorkloadPrincipal
	calls  atomic.Int32
	block  func(ctx context.Context) error
}

func (f *fakeIdentity) Verify(ctx context.Context, raw string) (domain.WorkloadPrincipal, error) {
	f.calls.Add(1)
	if f.block != nil {
		return domain.WorkloadPrincipal{}, f.block(ctx)
	}
	if raw == "unavailable" {
		return domain.WorkloadPrincipal{}, domain.ErrIdentityProviderUnavailable
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.tokens[raw]; ok {
		return p, nil
	}
	return domain.WorkloadPrincipal{}, domain.ErrInvalidToken
}

func (*fakeIdentity) SetIssuers([]domain.TrustedIssuer) {}

// spyRegistry stands in for the raw registry handler.
type spyRegistry struct {
	mu    sync.Mutex
	calls []string
}

func (s *spyRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.calls = append(s.calls, r.Method+" "+r.URL.RequestURI())
	s.mu.Unlock()
	w.WriteHeader(http.StatusAccepted)
}

func (s *spyRegistry) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

type harness struct {
	t        *testing.T
	auth     *auth.Service
	identity *fakeIdentity
	apps     *inmocks.MockAppService
	registry *spyRegistry
	policies atomic.Pointer[domain.PromotionPolicySet]
	logs     *syncBuffer
	handler  http.Handler
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

func webPolicies(t *testing.T) domain.PromotionPolicySet {
	t.Helper()
	set, err := domain.NewPromotionPolicySet([]domain.PromotionPolicy{
		{Issuer: testIssuer, Subjects: []string{webPrincipal.Subject}, App: "web", Service: "web", Repository: "web"},
		{Issuer: testIssuer, Subjects: []string{webPrincipal.Subject}, App: "web", Service: "worker", Repository: "acme/worker"},
		{Issuer: testIssuer, Subjects: []string{otherPrincipal.Subject}, App: "other", Service: "api", Repository: "other"},
	})
	require.NoError(t, err)
	return set
}

func newHarness(t *testing.T, opts ...func(*Deps)) *harness {
	t.Helper()
	h := &harness{
		t:        t,
		identity: &fakeIdentity{tokens: map[string]domain.WorkloadPrincipal{"jwt-web": webPrincipal, "jwt-other": otherPrincipal, "jwt-nopolicy": {Issuer: testIssuer, Subject: "repo:acme/none"}}},
		apps:     inmocks.NewMockAppService(t),
		registry: &spyRegistry{},
		logs:     &syncBuffer{},
	}
	log := zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: h.logs})
	h.auth = auth.NewService(auth.Config{Enabled: true, AuthType: domain.AuthTypeToken, TokenSecret: []byte("0123456789abcdef0123456789abcdef")}, nil, log)
	policies := webPolicies(t)
	h.policies.Store(&policies)
	deps := Deps{
		Auth:        h.auth,
		Identity:    h.identity,
		Apps:        h.apps,
		Policies:    func() domain.PromotionPolicySet { return *h.policies.Load() },
		Domain:      func() string { return testDomain },
		Registry:    h.registry,
		TrustedNets: httphelper.ParseTrustedProxies([]string{trustedPeer}),
		Log:         log,
	}
	for _, opt := range opts {
		opt(&deps)
	}
	h.handler = NewHandler(deps)
	return h
}

// ciToken mints a real ci-access token for principal with scopes.
func (h *harness) ciToken(principal domain.WorkloadPrincipal, scopes ...string) string {
	h.t.Helper()
	token, err := h.auth.GenerateCIAccessToken(context.Background(), principal, scopes, 0)
	require.NoError(h.t, err)
	return token
}

// request builds a TLS request to the CI host.
func request(method, target string, body []byte) *http.Request {
	req := httptest.NewRequest(method, "https://"+testDomain+target, bytes.NewReader(body))
	req.TLS = &tls.ConnectionState{}
	req.RemoteAddr = "203.0.113.7:51000"
	return req
}

func withBearer(req *http.Request, token string) *http.Request {
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func (h *harness) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func registryErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body dto.RegistryErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	require.NotEmpty(t, body.Errors)
	return body.Errors[0].Code
}

func TestRouter_DeniedRoutesAre404AndNeverReachRegistryOrAdmin(t *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodGet, "/v2/web/manifests/v1"},
		{http.MethodGet, "/v2/web/manifests/" + testDigest},
		{http.MethodGet, "/v2/web/blobs/" + testDigest},
		{http.MethodGet, "/v2/web/tags/list"},
		{http.MethodGet, "/v2/_catalog"},
		{http.MethodDelete, "/v2/web/manifests/" + testDigest},
		{http.MethodDelete, "/v2/web/blobs/" + testDigest},
		{http.MethodDelete, "/v2/web/blobs/uploads/" + testUpload},
		{http.MethodPost, "/v2/web/blobs/uploads/?mount=" + testDigest + "&from=other"},
		{http.MethodPost, "/v2/web/blobs/uploads/?from=other"},
		{http.MethodPost, "/v2/web/blobs/uploads/?mount=" + testDigest},
		{http.MethodPut, "/v2/web/blobs/uploads/"},
		{http.MethodGet, "/v2/web/blobs/uploads/" + testUpload},
		{http.MethodPost, "/v2/web/manifests/v1"},
		{http.MethodHead, "/v2/"},
		{http.MethodGet, "/v2"},
		{http.MethodGet, "/v2/BAD/manifests/v1"},
		{http.MethodGet, "/admin/apps"},
		{http.MethodPost, "/admin/apps/web/promote"},
		{http.MethodGet, "/admin/auth/verify"},
		{http.MethodGet, "/.well-known/gordon/"},
		{http.MethodGet, "/.well-known/gordon/ca.crt"},
		{http.MethodPost, "/auth/token"},
		{http.MethodGet, "/auth/password"},
		{http.MethodGet, "/-/ci/apps/web/promote"},
		{http.MethodPost, "/-/ci/apps/web/operations/by-key/key-1"},
		{http.MethodPost, "/-/ci/apps/Bad_App/promote"},
		{http.MethodGet, "/-/ci/apps/web/operations/by-key/"},
		{http.MethodGet, "/-/ci/apps/web/operations/by-key/a/b"},
		{http.MethodGet, "/-/ci/apps/web"},
		{http.MethodGet, "/"},
	}
	h := newHarness(t)
	token := h.ciToken(webPrincipal, "repository:web:push,pull")
	for _, rt := range routes {
		for _, authed := range []bool{false, true} {
			t.Run(rt.method+" "+rt.path, func(t *testing.T) {
				req := request(rt.method, rt.path, nil)
				if authed {
					withBearer(req, token)
				}
				rec := h.serve(req)
				assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
				assert.Empty(t, rec.Header().Get("WWW-Authenticate"), "a denied route never challenges")
				if strings.HasPrefix(rt.path, "/v2") && rt.method != http.MethodHead {
					assert.Equal(t, "NOT_FOUND", registryErrorCode(t, rec))
					assert.Equal(t, "registry/2.0", rec.Header().Get("Docker-Distribution-API-Version"))
				}
			})
		}
	}
	assert.Empty(t, h.registry.Calls(), "no denied route reached the registry")
	assert.Zero(t, h.identity.calls.Load(), "no denied route reached the identity provider")
}

func TestRouter_AllowedRegistryRoutesReachRawRegistry(t *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodGet, "/v2/"},
		{http.MethodHead, "/v2/web/blobs/" + testDigest},
		{http.MethodPost, "/v2/web/blobs/uploads/"},
		{http.MethodPatch, "/v2/web/blobs/uploads/" + testUpload},
		{http.MethodPut, "/v2/web/blobs/uploads/" + testUpload + "?digest=" + testDigest},
		{http.MethodPut, "/v2/web/manifests/v1"},
		{http.MethodHead, "/v2/web/manifests/v1"},
		{http.MethodHead, "/v2/acme/worker/manifests/" + testDigest},
	}
	h := newHarness(t)
	token := h.ciToken(webPrincipal, "repository:web:push,pull", "repository:acme/worker:pull")
	var want []string
	for _, rt := range routes {
		rec := h.serve(withBearer(request(rt.method, rt.path, nil), token))
		assert.Equal(t, http.StatusAccepted, rec.Code, "%s %s: %s", rt.method, rt.path, rec.Body.String())
		want = append(want, rt.method+" "+rt.path)
	}
	assert.Equal(t, want, h.registry.Calls())
}

func TestRouter_RepositoryOutsidePolicyDenied(t *testing.T) {
	h := newHarness(t)
	// The other principal's token names its own repository; a forged scope
	// for another repository is impossible, so check the policy boundary
	// directly with a token minted for a repository the principal no
	// longer (or never) may push.
	token := h.ciToken(webPrincipal, "repository:web:push,pull", "repository:other:push,pull")

	rec := h.serve(withBearer(request(http.MethodPost, "/v2/other/blobs/uploads/", nil), token))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "DENIED", registryErrorCode(t, rec))

	// A reload that removes the web policy applies to tokens already issued.
	empty := domain.PromotionPolicySet{}
	h.policies.Store(&empty)
	rec = h.serve(withBearer(request(http.MethodPost, "/v2/web/blobs/uploads/", nil), token))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, h.registry.Calls())
}

func TestRouter_TokenWithoutScopeIsChallengedForIt(t *testing.T) {
	h := newHarness(t)
	pullOnly := h.ciToken(webPrincipal, "repository:web:pull")
	rec := h.serve(withBearer(request(http.MethodPut, "/v2/web/manifests/v1", nil), pullOnly))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, `Bearer realm="https://ci.example.com/auth/token",service="ci.example.com",scope="repository:web:push,pull"`,
		rec.Header().Get("WWW-Authenticate"))
	assert.Equal(t, "UNAUTHORIZED", registryErrorCode(t, rec))

	login := h.ciToken(webPrincipal)
	rec = h.serve(withBearer(request(http.MethodHead, "/v2/web/blobs/"+testDigest, nil), login))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Header().Get("WWW-Authenticate"), `scope="repository:web:pull"`)
	assert.Empty(t, h.registry.Calls())
}

func TestRouter_RejectsMissingAndNonCIAccessTokens(t *testing.T) {
	h := newHarness(t)
	access, err := h.auth.GenerateAccessToken(context.Background(), "deployer", []string{"repository:*:push,pull", "admin:*:*"}, 5*time.Minute)
	require.NoError(t, err)
	stored, err := h.auth.GenerateCIAccessToken(context.Background(), webPrincipal, nil, 0)
	require.NoError(t, err)

	cases := map[string]func(*http.Request){
		"no credentials":     func(*http.Request) {},
		"main access token":  func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+access) },
		"garbage bearer":     func(r *http.Request) { r.Header.Set("Authorization", "Bearer not-a-jwt") },
		"raw OIDC JWT":       func(r *http.Request) { r.Header.Set("Authorization", "Bearer jwt-web") },
		"basic ci-access":    func(r *http.Request) { r.SetBasicAuth("oidc", stored) },
		"basic raw OIDC JWT": func(r *http.Request) { r.SetBasicAuth("oidc", "jwt-web") },
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			for _, target := range []string{"/v2/", "/-/ci/apps/web/operations/by-key/key-1"} {
				req := request(http.MethodGet, target, nil)
				set(req)
				rec := h.serve(req)
				assert.Equal(t, http.StatusUnauthorized, rec.Code, target)
				assert.Equal(t, `Bearer realm="https://ci.example.com/auth/token",service="ci.example.com"`, rec.Header().Get("WWW-Authenticate"))
			}
		})
	}
	assert.Empty(t, h.registry.Calls())
}

func TestRouter_HTTPSOnly(t *testing.T) {
	h := newHarness(t)
	plain := func(peer, proto string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "http://"+testDomain+"/v2/", nil)
		req.RemoteAddr = net.JoinHostPort(peer, "40000")
		if proto != "" {
			req.Header.Set("X-Forwarded-Proto", proto)
		}
		return req
	}
	assert.Equal(t, http.StatusNotFound, h.serve(plain("203.0.113.7", "")).Code, "plain HTTP")
	assert.Equal(t, http.StatusNotFound, h.serve(plain("203.0.113.7", "https")).Code, "spoofed X-Forwarded-Proto")
	assert.Equal(t, http.StatusNotFound, h.serve(plain("127.0.0.1", "https")).Code, "loopback is not a configured proxy")
	assert.Equal(t, http.StatusNotFound, h.serve(plain(trustedPeer, "")).Code, "trusted proxy without https")
	assert.Equal(t, http.StatusNotFound, h.serve(plain(trustedPeer, "http")).Code, "trusted proxy forwarding http")

	rec := h.serve(plain(trustedPeer, "https"))
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "trusted proxy with https reaches the router")
	rec = h.serve(request(http.MethodGet, "/v2/", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "TLS reaches the router")
}

func TestRouter_RealmBehindTrustedProxyUsesConfiguredDomain(t *testing.T) {
	h := newHarness(t)
	req := httptest.NewRequest(http.MethodGet, "http://internal:8080/v2/", nil)
	req.Host = "CI.Example.com:8443"
	req.RemoteAddr = net.JoinHostPort(trustedPeer, "40000")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "evil.example")
	rec := h.serve(req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, `Bearer realm="https://ci.example.com/auth/token",service="ci.example.com"`, rec.Header().Get("WWW-Authenticate"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"), "security headers apply")
}

func TestRouter_PanicIsRecovered(t *testing.T) {
	h := newHarness(t, func(d *Deps) {
		d.Registry = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(errors.New("boom")) })
	})
	token := h.ciToken(webPrincipal)
	rec := h.serve(withBearer(request(http.MethodGet, "/v2/", nil), token))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// ensure the in.AppService mock satisfies the port the router consumes.
var _ in.AppService = (*inmocks.MockAppService)(nil)
