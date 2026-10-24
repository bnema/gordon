package app

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
	proxyusecase "github.com/bnema/gordon/internal/usecase/proxy"
)

const ciTestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// registryListenerSpy stands in for the registry listener (registry API and
// admin API): the edge proxy forwards registry-domain traffic to it. A CI
// host request must never arrive here.
func registryListenerSpy(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// ciEdge wires the real edge (createHTTPHandlers) for a CI domain with the
// registry listener replaced by a spy. The registry service is left nil: a
// request that reaches a registry operation past the CI allowlist panics
// there and answers 500, which every test below forbids.
func ciEdge(t *testing.T) (http.Handler, http.Handler, *atomic.Int32, *services) {
	t.Helper()
	spy, hits := registryListenerSpy(t)
	_, cfg := readSetImageConfig(t, setImageTOML)
	cfg.Server.RegistryPort = spy.Listener.Addr().(*net.TCPAddr).Port
	cfg.API.RateLimit.TrustedProxies = []string{"10.0.0.5"}
	svc := ciEdgeServices(t, cfg)
	_, httpOut, httpsOut := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)
	return httpOut, httpsOut, hits, svc
}

type rawResponse struct {
	status   int
	location string
	auth     string
}

// rawRequest writes one request line byte for byte over TLS, so path
// encodings and double slashes reach the server exactly as written. It
// runs against httptest over real sockets, outside synctest.
func rawRequest(t *testing.T, addr, method, target, host string, headers ...string) rawResponse {
	t.Helper()
	//nolint:gosec // G402: the httptest certificate is self-signed.
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	require.NoError(t, err)
	defer conn.Close()
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n", method, target, host)
	for _, h := range headers {
		b.WriteString(h + "\r\n")
	}
	b.WriteString("\r\n")
	_, err = conn.Write([]byte(b.String()))
	require.NoError(t, err)
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: method})
	require.NoError(t, err)
	defer resp.Body.Close()
	return rawResponse{status: resp.StatusCode, location: resp.Header.Get("Location"), auth: resp.Header.Get("WWW-Authenticate")}
}

// TestCIEdge_AdversarialPathsNeverReachRegistryOrAdmin drives the wired
// HTTPS edge (proxy handler → CI router). Every request is either refused
// with 404, or redirected by the edge mux to its cleaned path on the same
// host, which is then refused too. None reaches the registry operations or
// the registry/admin listener.
func TestCIEdge_AdversarialPathsNeverReachRegistryOrAdmin(t *testing.T) {
	_, httpsOut, hits, svc := ciEdge(t)
	srv := httptest.NewTLSServer(httpsOut)
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	token := ciTokenFor(t, svc)
	bearer := "Authorization: Bearer " + token

	cases := []struct {
		method, target string
		headers        []string
	}{
		{"GET", "/v2/%2e%2e/admin", nil},
		{"GET", "/%61dmin", nil},
		{"GET", "/%61dmin/apps", []string{bearer}},
		{"GET", "/v2/web/manifests/..%2f..%2fadmin", []string{bearer}},
		{"GET", "/v2/web/manifests/..%2F..%2Fadmin%2Fstatus", []string{bearer}},
		{"GET", "/v2/../admin/status", []string{bearer}},
		{"GET", "/v2/./_catalog", []string{bearer}},
		{"GET", "/v2/web/../_catalog", []string{bearer}},
		{"GET", "//v2/web/manifests/x", []string{bearer}},
		{"GET", "/v2//_catalog", []string{bearer}},
		{"GET", "/v2/web//manifests/x", []string{bearer}},
		{"GET", "/v2/web/manifests/x", []string{bearer, "X-HTTP-Method-Override: PUT"}},
		{"GET", "/v2/web/blobs/" + ciTestDigest, []string{bearer}},
		{"GET", "/v2/web/blobs/" + ciTestDigest, []string{bearer, "X-HTTP-Method-Override: HEAD"}},
		{"POST", "/v2/web/manifests/x", []string{bearer, "X-HTTP-Method: PUT", "Content-Length: 0"}},
		{"DELETE", "/v2/web/manifests/" + ciTestDigest, []string{bearer}},
		{"GET", "/v2/web/tags/list", []string{bearer, "X-HTTP-Method: HEAD"}},
		{"GET", "/admin/status", []string{bearer}},
		{"GET", "/.well-known/gordon/ca.crt", nil},
		{"GET", "/%2e%77ell-known/gordon/", nil},
	}
	for _, host := range []string{"ci.example", "CI.Example", "ci.example.", "ci.example:443", "CI.EXAMPLE.:8443"} {
		for _, tc := range cases {
			name := host + " " + tc.method + " " + tc.target
			resp := rawRequest(t, addr, tc.method, tc.target, host, tc.headers...)
			if resp.status == http.StatusMovedPermanently || resp.status == http.StatusTemporaryRedirect || resp.status == http.StatusPermanentRedirect {
				require.True(t, strings.HasPrefix(resp.location, "/"), "%s: redirect stays on the host: %q", name, resp.location)
				resp = rawRequest(t, addr, tc.method, resp.location, host, tc.headers...)
			}
			assert.Equal(t, http.StatusNotFound, resp.status, name)
			assert.Empty(t, resp.auth, "%s: refused routes never challenge", name)
		}
	}
	assert.Zero(t, hits.Load(), "no CI host request reached the registry/admin listener")
}

// TestCIEdge_HostVariantsReachCIRouter proves case, trailing-dot and port
// variants of the CI host are dispatched to the CI router (its challenge),
// while HEAD on a blob is the only blob method allowed.
func TestCIEdge_HostVariantsReachCIRouter(t *testing.T) {
	_, httpsOut, hits, _ := ciEdge(t)
	srv := httptest.NewTLSServer(httpsOut)
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	for _, host := range []string{"ci.example", "CI.Example", "ci.example.", "ci.example:443", "Ci.Example.:8443"} {
		resp := rawRequest(t, addr, "GET", "/v2/", host)
		assert.Equal(t, http.StatusUnauthorized, resp.status, host)
		assert.Equal(t, `Bearer realm="https://ci.example/auth/token",service="ci.example"`, resp.auth, host)

		resp = rawRequest(t, addr, "HEAD", "/v2/web/blobs/"+ciTestDigest, host)
		assert.Equal(t, http.StatusUnauthorized, resp.status, "HEAD blob is allowed, so it is challenged: %s", host)
		resp = rawRequest(t, addr, "GET", "/v2/web/blobs/"+ciTestDigest, host)
		assert.Equal(t, http.StatusNotFound, resp.status, "GET blob is refused: %s", host)
	}
	assert.Zero(t, hits.Load())
}

func ciTokenFor(t *testing.T, svc *services) string {
	t.Helper()
	token, err := svc.authSvc.GenerateCIAccessToken(t.Context(),
		domain.WorkloadPrincipal{Issuer: "https://issuer.example", Subject: "a"}, []string{"repository:web:push,pull"}, 0)
	require.NoError(t, err)
	return token
}

// TestCIEdge_ForwardedProtoOnHTTPEdge covers the plaintext edge behind a
// reverse proxy: only one X-Forwarded-Proto: https line from a trusted
// proxy reaches the CI router.
func TestCIEdge_ForwardedProtoOnHTTPEdge(t *testing.T) {
	httpOut, _, hits, _ := ciEdge(t)
	serve := func(peer string, protos ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://ci.example/v2/", nil)
		req.RemoteAddr = net.JoinHostPort(peer, "4000")
		for _, p := range protos {
			req.Header.Add("X-Forwarded-Proto", p)
		}
		rec := httptest.NewRecorder()
		httpOut.ServeHTTP(rec, req)
		return rec
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"two lines":       serve("10.0.0.5", "https", "https"),
		"mixed lines":     serve("10.0.0.5", "https", "http"),
		"comma-joined":    serve("10.0.0.5", "https,http"),
		"comma https end": serve("10.0.0.5", "http,https"),
		"untrusted peer":  serve("203.0.113.40", "https"),
	} {
		assert.Equal(t, http.StatusNotFound, rec.Code, name)
		assert.Empty(t, rec.Header().Get("WWW-Authenticate"), name)
	}
	assert.Equal(t, http.StatusUnauthorized, serve("10.0.0.5", "https").Code, "control")
	assert.Zero(t, hits.Load())
}

// TestCIEdge_SpoofedForwardedForCannotEvadeTokenRateLimit runs in a synctest
// bubble: the token limiter reads time.Now, which stays frozen in the
// bubble, so the burst is exact and no request refills it. Requests go
// straight to the handler with recorders; no socket is involved.
func TestCIEdge_SpoofedForwardedForCannotEvadeTokenRateLimit(t *testing.T) {
	_, cfg := readSetImageConfig(t, setImageTOML)
	cfg.API.RateLimit.TrustedProxies = []string{"10.0.0.5"}
	synctest.Test(t, func(t *testing.T) {
		svc := ciEdgeServices(t, cfg)
		identity := svc.workloadIdentitySvc.(*inmocks.MockWorkloadIdentityService)
		identity.EXPECT().Verify(mock.Anything, mock.Anything).Return(domain.WorkloadPrincipal{}, domain.ErrInvalidToken)
		_, _, httpsOut := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

		send := func(peer, xff string) int {
			req := httptest.NewRequest(http.MethodGet, "https://ci.example/auth/token", nil)
			req.TLS = tlsState()
			req.RemoteAddr = net.JoinHostPort(peer, "5000")
			req.Header.Set("X-Forwarded-For", xff)
			req.Header.Set("X-Real-IP", xff)
			req.SetBasicAuth("oidc", "forged")
			rec := httptest.NewRecorder()
			httpsOut.ServeHTTP(rec, req)
			return rec.Code
		}

		limited := 0
		for i := range 30 {
			if send("198.51.100.9", "192.0.2."+strconv.Itoa(i+1)) == http.StatusTooManyRequests {
				limited++
			}
		}
		assert.Equal(t, 20, limited, "an untrusted peer shares one bucket (burst 10) whatever X-Forwarded-For says")

		// Control: behind the trusted proxy each forwarded client has its own bucket.
		for i := range 30 {
			assert.NotEqual(t, http.StatusTooManyRequests, send("10.0.0.5", "192.0.2."+strconv.Itoa(100+i)))
		}
	})
}

// TestCIEdge_LiveReloadOfCIDomain toggles server.ci_domain through the real
// proxy UpdateConfig path while requests flow. Requests go straight to the
// wired handler with recorders; the only socket is the registry listener
// spy, which no request may reach. Goroutines finish through a WaitGroup
// and phases are separated by it, so no timing is involved.
func TestCIEdge_LiveReloadOfCIDomain(t *testing.T) {
	_, httpsOut, hits, svc := ciEdge(t)
	base := svc.proxySvc.ProxyConfig()
	withCI := proxyusecase.Config{RegistryDomain: base.RegistryDomain, RegistryPort: base.RegistryPort, CIDomain: "ci.example"}
	withoutCI := withCI
	withoutCI.CIDomain = ""

	const ciRealm = `Bearer realm="https://ci.example/auth/token",service="ci.example"`
	probe := func() (int, string) {
		req := httptest.NewRequest(http.MethodGet, "https://ci.example/v2/", nil)
		req.TLS = tlsState()
		rec := httptest.NewRecorder()
		httpsOut.ServeHTTP(rec, req)
		return rec.Code, rec.Header().Get("WWW-Authenticate")
	}
	expectCI := func(phase string) {
		code, auth := probe()
		assert.Equal(t, http.StatusUnauthorized, code, phase)
		assert.Equal(t, ciRealm, auth, phase)
	}
	expectGone := func(phase string) {
		code, auth := probe()
		assert.Equal(t, http.StatusNotFound, code, phase)
		assert.Empty(t, auth, phase)
	}

	expectCI("configured at start")
	svc.proxySvc.UpdateConfig(withoutCI)
	expectGone("after removal")
	svc.proxySvc.UpdateConfig(withCI)
	expectCI("after re-adding")

	// Concurrent: readers see only the two coherent outcomes while a
	// writer flips the config.
	var wg sync.WaitGroup
	var bad atomic.Int32
	for range 8 {
		wg.Go(func() {
			for range 200 {
				code, auth := probe()
				ok := (code == http.StatusUnauthorized && auth == ciRealm) || (code == http.StatusNotFound && auth == "")
				if !ok {
					bad.Add(1)
				}
			}
		})
	}
	wg.Go(func() {
		for i := range 400 {
			if i%2 == 0 {
				svc.proxySvc.UpdateConfig(withoutCI)
			} else {
				svc.proxySvc.UpdateConfig(withCI)
			}
		}
	})
	wg.Wait()
	assert.Zero(t, bad.Load(), "every response was either the CI router or a plain 404")

	svc.proxySvc.UpdateConfig(withoutCI)
	expectGone("final removal")
	assert.Zero(t, hits.Load(), "CI host traffic never reached the registry/admin listener")
}
