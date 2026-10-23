package ci

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRouter_RefusesHostOtherThanCurrentCIDomain pins the reload window: the
// proxy chose the CI router from one config generation, then ci_domain was
// removed (or changed) before the router ran. The router re-checks the Host
// against the generation it reads and never serves, or challenges with an
// empty realm, for a host that is no longer the CI domain.
func TestRouter_RefusesHostOtherThanCurrentCIDomain(t *testing.T) {
	var domainValue atomic.Value
	domainValue.Store("")
	h := newHarness(t, func(d *Deps) { d.Domain = func() string { return domainValue.Load().(string) } })
	token := h.ciToken(webPrincipal, "repository:web:push,pull")

	for _, current := range []string{"", "other.example.com"} {
		domainValue.Store(current)
		for _, req := range []*http.Request{
			request(http.MethodGet, "/v2/", nil),
			withBearer(request(http.MethodPost, "/v2/web/blobs/uploads/", nil), token),
			tokenRequest("oidc", "jwt-web", "?scope=repository:web:push"),
		} {
			rec := h.serve(req)
			assert.Equal(t, http.StatusNotFound, rec.Code, "ci_domain=%q %s %s", current, req.Method, req.URL.Path)
			assert.Empty(t, rec.Header().Get("WWW-Authenticate"))
		}
	}
	assert.Empty(t, h.registry.Calls())
	assert.Zero(t, h.identity.calls.Load())

	domainValue.Store(testDomain)
	for _, host := range []string{"ci.example.com", "CI.Example.com", "ci.example.com.", "ci.example.com:8443"} {
		req := request(http.MethodGet, "/v2/", nil)
		req.Host = host
		rec := h.serve(req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, host)
		assert.Equal(t, `Bearer realm="https://ci.example.com/auth/token",service="ci.example.com"`, rec.Header().Get("WWW-Authenticate"), host)
	}
}

func TestRouter_ForwardedProtoMustBeSingleHTTPS(t *testing.T) {
	h := newHarness(t)
	forwarded := func(peer string, values ...string) int {
		req := httptest.NewRequest(http.MethodGet, "http://"+testDomain+"/v2/", nil)
		req.RemoteAddr = net.JoinHostPort(peer, "40000")
		for _, v := range values {
			req.Header.Add("X-Forwarded-Proto", v)
		}
		return h.serve(req).Code
	}
	assert.Equal(t, http.StatusNotFound, forwarded(trustedPeer, "https", "https"), "two header lines")
	assert.Equal(t, http.StatusNotFound, forwarded(trustedPeer, "https", "http"), "conflicting header lines")
	assert.Equal(t, http.StatusNotFound, forwarded(trustedPeer, "https,http"), "comma-joined list")
	assert.Equal(t, http.StatusNotFound, forwarded(trustedPeer, "http,https"), "comma-joined list, https last")
	assert.Equal(t, http.StatusNotFound, forwarded("203.0.113.9", "https"), "untrusted peer")
	assert.Equal(t, http.StatusUnauthorized, forwarded(trustedPeer, "https"), "control: one https line from the trusted proxy")
	assert.Empty(t, h.registry.Calls())
}

// TestRouter_MethodOverrideHeadersAreIgnored proves the router and the
// registry only ever see the real request method.
func TestRouter_MethodOverrideHeadersAreIgnored(t *testing.T) {
	h := newHarness(t)
	token := h.ciToken(webPrincipal, "repository:web:push,pull")
	cases := []struct {
		method, path, header, value string
		status                      int
	}{
		{http.MethodGet, "/v2/web/manifests/v1", "X-HTTP-Method-Override", "PUT", http.StatusNotFound},
		{http.MethodGet, "/v2/web/blobs/" + testDigest, "X-HTTP-Method-Override", "HEAD", http.StatusNotFound},
		{http.MethodPost, "/v2/web/manifests/v1", "X-HTTP-Method", "PUT", http.StatusNotFound},
		{http.MethodGet, "/v2/web/tags/list", "X-Method-Override", "HEAD", http.StatusNotFound},
		{http.MethodPut, "/v2/web/manifests/v1", "X-HTTP-Method-Override", "GET", http.StatusAccepted},
		{http.MethodHead, "/v2/web/manifests/v1", "X-HTTP-Method", "DELETE", http.StatusAccepted},
	}
	for _, tc := range cases {
		req := withBearer(request(tc.method, tc.path, nil), token)
		req.Header.Set(tc.header, tc.value)
		rec := h.serve(req)
		assert.Equal(t, tc.status, rec.Code, "%s %s with %s: %s", tc.method, tc.path, tc.header, tc.value)
	}
	require.Equal(t, []string{"PUT /v2/web/manifests/v1", "HEAD /v2/web/manifests/v1"}, h.registry.Calls(),
		"the registry sees the real methods only")
}
