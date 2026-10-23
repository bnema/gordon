package oidc

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

var _ out.OIDCProvider = (*Provider)(nil)

// staticResolver maps every host name to fixed addresses.
type staticResolver struct {
	addrs []string
	hosts []string
}

func (r *staticResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	r.hosts = append(r.hosts, host)
	var out []net.IPAddr
	for _, a := range r.addrs {
		out = append(out, net.IPAddr{IP: net.ParseIP(a)})
	}
	return out, nil
}

// issuerServer is a loopback TLS issuer. Its certificate is valid for
// example.com, so tests address it as https://example.com:<port> and map
// that name to 127.0.0.1 with an injected resolver.
type issuerServer struct {
	*httptest.Server
	base string // https://example.com:<port>
	mux  *http.ServeMux
}

func newIssuerServer(t *testing.T) *issuerServer {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)
	return &issuerServer{Server: srv, base: "https://example.com:" + port, mux: mux}
}

func (s *issuerServer) provider(addrs ...string) *Provider {
	pool := x509.NewCertPool()
	pool.AddCert(s.Certificate())
	if len(addrs) == 0 {
		addrs = []string{"127.0.0.1"}
	}
	return NewProvider(WithRootCAs(pool), WithResolver(&staticResolver{addrs: addrs}))
}

func (s *issuerServer) serveDiscovery(issuer, jwksURI string) {
	s.mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"issuer":%q,"jwks_uri":%q}`, issuer, jwksURI)
	})
}

func (s *issuerServer) serveJWKS(body string) {
	s.mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
}

func (s *issuerServer) policy(allowPrivate bool) domain.TrustedIssuer {
	return domain.TrustedIssuer{Issuer: s.base, Audience: "gordon", AllowPrivateNetwork: allowPrivate}
}

func TestAllowPrivateNetworkPermitsLoopbackTLS(t *testing.T) {
	srv := newIssuerServer(t)
	srv.serveDiscovery(srv.base, srv.base+"/keys")
	srv.serveJWKS(`{"keys":[]}`)
	p := srv.provider()

	jwksURI, err := p.Discover(t.Context(), srv.policy(true))
	require.NoError(t, err)
	require.Equal(t, srv.base+"/keys", jwksURI)
	raw, err := p.FetchJWKS(t.Context(), srv.policy(true), jwksURI)
	require.NoError(t, err)
	require.JSONEq(t, `{"keys":[]}`, string(raw))
}

func TestIgnoresEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("https_proxy", "http://127.0.0.1:1")
	srv := newIssuerServer(t)
	srv.serveJWKS(`{"keys":[]}`)
	_, err := srv.provider().FetchJWKS(t.Context(), srv.policy(true), srv.base+"/keys")
	require.NoError(t, err)
}

func TestPrivateDestinationsRefused(t *testing.T) {
	srv := newIssuerServer(t)
	srv.serveDiscovery(srv.base, srv.base+"/keys")
	srv.serveJWKS(`{"keys":[]}`)

	t.Run("loopback literal", func(t *testing.T) {
		policy := domain.TrustedIssuer{Issuer: srv.URL, Audience: "gordon"} // https://127.0.0.1:<port>
		_, err := NewProvider().Discover(t.Context(), policy)
		require.ErrorIs(t, err, ErrDestinationNotPublic)
	})
	t.Run("DNS name resolving to loopback", func(t *testing.T) {
		_, err := srv.provider("127.0.0.1").Discover(t.Context(), srv.policy(false))
		require.ErrorIs(t, err, ErrDestinationNotPublic)
	})
	t.Run("JWKS fetch also checked", func(t *testing.T) {
		_, err := srv.provider("127.0.0.1").FetchJWKS(t.Context(), srv.policy(false), srv.base+"/keys")
		require.ErrorIs(t, err, ErrDestinationNotPublic)
	})
	for _, addr := range []string{"10.0.0.5", "172.16.1.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "fd00::1", "::1", "0.0.0.0", "::"} {
		t.Run("DNS name resolving to "+addr, func(t *testing.T) {
			_, err := srv.provider(addr).Discover(t.Context(), srv.policy(false))
			require.ErrorIs(t, err, ErrDestinationNotPublic)
		})
	}
	t.Run("any private address in the answer refuses", func(t *testing.T) {
		_, err := srv.provider("93.184.215.14", "127.0.0.1").Discover(t.Context(), srv.policy(false))
		require.ErrorIs(t, err, ErrDestinationNotPublic)
	})
}

func TestRedirectRefused(t *testing.T) {
	srv := newIssuerServer(t)
	srv.mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	srv.serveJWKS(`{}`)
	srv.mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/keys", http.StatusMovedPermanently)
	})
	_, err := srv.provider().Discover(t.Context(), srv.policy(true))
	require.ErrorContains(t, err, "redirects are refused")
	_, err = srv.provider().FetchJWKS(t.Context(), srv.policy(true), srv.base+"/moved")
	require.ErrorContains(t, err, "redirects are refused")
}

func TestBodyCap(t *testing.T) {
	srv := newIssuerServer(t)
	srv.mux.HandleFunc("/exact", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", maxBodySize)))
	})
	srv.mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", maxBodySize+1)))
	})
	raw, err := srv.provider().FetchJWKS(t.Context(), srv.policy(true), srv.base+"/exact")
	require.NoError(t, err)
	require.Len(t, raw, maxBodySize)
	_, err = srv.provider().FetchJWKS(t.Context(), srv.policy(true), srv.base+"/big")
	require.ErrorContains(t, err, "exceeds")
}

func TestNonOKStatusFails(t *testing.T) {
	srv := newIssuerServer(t)
	_, err := srv.provider().FetchJWKS(t.Context(), srv.policy(true), srv.base+"/missing")
	require.ErrorContains(t, err, "status 404")
}

func TestDiscoveryValidation(t *testing.T) {
	cases := map[string]struct{ issuer, jwks func(base string) string }{
		"issuer trailing slash": {func(b string) string { return b + "/" }, func(b string) string { return b + "/keys" }},
		"issuer other host":     {func(string) string { return "https://evil.example" }, func(b string) string { return b + "/keys" }},
		"jwks_uri over http":    {func(b string) string { return b }, func(string) string { return "http://example.com/keys" }},
		"jwks_uri with query":   {func(b string) string { return b }, func(b string) string { return b + "/keys?x=1" }},
		"jwks_uri missing":      {func(b string) string { return b }, func(string) string { return "" }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := newIssuerServer(t)
			srv.serveDiscovery(tc.issuer(srv.base), tc.jwks(srv.base))
			_, err := srv.provider().Discover(t.Context(), srv.policy(true))
			require.Error(t, err)
		})
	}
	t.Run("malformed document", func(t *testing.T) {
		srv := newIssuerServer(t)
		srv.mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("{"))
		})
		_, err := srv.provider().Discover(t.Context(), srv.policy(true))
		require.ErrorContains(t, err, "invalid OIDC discovery document")
	})
}

func TestIssuerWithPathDiscovery(t *testing.T) {
	srv := newIssuerServer(t)
	issuer := srv.base + "/tenant"
	srv.mux.HandleFunc("/tenant/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"issuer":%q,"jwks_uri":%q}`, issuer, srv.base+"/keys")
	})
	got, err := srv.provider().Discover(t.Context(), domain.TrustedIssuer{Issuer: issuer, Audience: "a", AllowPrivateNetwork: true})
	require.NoError(t, err)
	require.Equal(t, srv.base+"/keys", got)
}

func TestNonHTTPSRefusedBeforeDialing(t *testing.T) {
	resolver := &staticResolver{addrs: []string{"127.0.0.1"}}
	p := NewProvider(WithResolver(resolver))
	policy := domain.TrustedIssuer{Issuer: "https://example.com", Audience: "a", AllowPrivateNetwork: true}
	for _, uri := range []string{"http://example.com/keys", "https://user:pw@example.com/keys", "https://example.com/keys#f", "ftp://example.com/keys", "/keys"} {
		_, err := p.FetchJWKS(t.Context(), policy, uri)
		require.Error(t, err, uri)
	}
	_, err := p.Discover(t.Context(), domain.TrustedIssuer{Issuer: "http://example.com", Audience: "a", AllowPrivateNetwork: true})
	require.Error(t, err)
	require.Empty(t, resolver.hosts, "invalid URLs must never be resolved")
}

func TestUntrustedCertificateRefused(t *testing.T) {
	srv := newIssuerServer(t)
	srv.serveJWKS(`{}`)
	p := NewProvider(WithResolver(&staticResolver{addrs: []string{"127.0.0.1"}})) // system roots only
	_, err := p.FetchJWKS(t.Context(), srv.policy(true), srv.base+"/keys")
	var certErr x509.UnknownAuthorityError
	require.True(t, errors.As(err, &certErr), "got %v", err)
}

func TestIsPublicIP(t *testing.T) {
	public := []string{
		"93.184.215.14", "8.8.8.8", "1.1.1.1", "2606:4700::1111", "100.128.0.1", "172.32.0.1",
		// NAT64 well-known prefix embedding a public IPv4 address.
		"64:ff9b::808:808",
		// SIIT (::ffff:0:0:0/96) embedding a public IPv4 address.
		"::ffff:0:808:808",
		// 6to4 embedding a public IPv4 address (bits 16-48).
		"2002:808:808::1",
	}
	private := []string{
		"127.0.0.1", "127.1.2.3", "::1", "10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.0.1",
		"169.254.169.254", "169.254.0.1", "fe80::1", "100.64.0.1", "100.127.255.255", "fc00::1", "fd12::1",
		"0.0.0.0", "::", "224.0.0.1", "ff02::1", "255.255.255.255", "::ffff:127.0.0.1", "::ffff:10.0.0.1",
		"198.18.0.1", "192.0.0.8",
		// NAT64 embedding non-public IPv4 addresses.
		"64:ff9b::a00:1", "64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe",
		// NAT64 local-use prefix is refused whatever it embeds (RFC 6052 layouts vary).
		"64:ff9b:1::a00:1", "64:ff9b:1::6440:1", "64:ff9b:1::808:808", "64:ff9b:1:ffff:ffff:ffff:808:808",
		"64:ff9b:1:0a00:0:100:808:808",
		// SIIT embedding non-public IPv4 addresses.
		"::ffff:0:a00:1", "::ffff:0:7f00:1", "::ffff:0:a9fe:a9fe",
		// IPv4-compatible (deprecated) addresses, public or not.
		"::808:808", "::7f00:1", "::a00:1",
		// Site-local (deprecated).
		"fec0::1", "feff::1",
		// 6to4 embedding non-public IPv4 addresses.
		"2002:7f00:1::1", "2002:a00:1::", "2002:a9fe:a9fe::1", "2002:c0a8:101::1",
		// Teredo is always refused.
		"2001::1", "2001:0:4136:e378:8000:63bf:3fff:fdd2",
	}
	for _, ip := range public {
		require.True(t, isPublicIP(net.ParseIP(ip)), ip)
	}
	for _, ip := range private {
		require.False(t, isPublicIP(net.ParseIP(ip)), ip)
	}
	require.False(t, isPublicIP(nil))
}

func TestNAT64PrivateDestinationRefused(t *testing.T) {
	srv := newIssuerServer(t)
	srv.serveJWKS(`{}`)
	_, err := srv.provider("64:ff9b::7f00:1").FetchJWKS(t.Context(), srv.policy(false), srv.base+"/keys")
	require.ErrorIs(t, err, ErrDestinationNotPublic)
}
