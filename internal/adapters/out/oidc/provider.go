// Package oidc implements out.OIDCProvider: bounded, issuer-scoped HTTPS access
// to OIDC discovery documents and JWKS endpoints with SSRF protection.
package oidc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

const (
	// MaxBodySize caps discovery and JWKS responses.
	MaxBodySize = 256 << 10
	// RequestTimeout bounds one discovery or JWKS request end to end.
	RequestTimeout = 10 * time.Second
)

// ErrDestinationNotPublic is returned when a dialed address is not public and
// the issuer does not set AllowPrivateNetwork.
var ErrDestinationNotPublic = errors.New("OIDC destination is not a public address")

// Resolver resolves host names. *net.Resolver implements it.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Option customises a Provider. Options cannot disable TLS verification, the
// address policy, the redirect refusal, the timeout or the body cap.
type Option func(*Provider)

// WithResolver replaces the DNS resolver (tests map names to chosen addresses).
func WithResolver(r Resolver) Option { return func(p *Provider) { p.resolver = r } }

// WithRootCAs replaces the trusted TLS roots (tests trust an httptest server).
func WithRootCAs(pool *x509.CertPool) Option { return func(p *Provider) { p.rootCAs = pool } }

// Provider implements out.OIDCProvider.
type Provider struct {
	resolver Resolver
	rootCAs  *x509.CertPool // nil = system roots
}

// NewProvider returns a Provider with production defaults.
func NewProvider(opts ...Option) *Provider {
	p := &Provider{resolver: net.DefaultResolver}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Discover fetches <issuer>/.well-known/openid-configuration and returns its
// jwks_uri. The document issuer must equal the configured issuer exactly.
func (p *Provider) Discover(ctx context.Context, issuer domain.TrustedIssuer) (string, error) {
	if err := domain.ValidateIssuerURL(issuer.Issuer); err != nil {
		return "", fmt.Errorf("issuer: %w", err)
	}
	raw, err := p.get(ctx, issuer, strings.TrimSuffix(issuer.Issuer, "/")+"/.well-known/openid-configuration")
	if err != nil {
		return "", err
	}
	var doc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("invalid OIDC discovery document: %w", err)
	}
	if doc.Issuer != issuer.Issuer {
		return "", fmt.Errorf("OIDC discovery issuer %q does not match configured issuer", doc.Issuer)
	}
	if err := domain.ValidateIssuerURL(doc.JWKSURI); err != nil {
		return "", fmt.Errorf("discovered jwks_uri: %w", err)
	}
	return doc.JWKSURI, nil
}

// FetchJWKS returns the raw JWKS document at jwksURI.
func (p *Provider) FetchJWKS(ctx context.Context, issuer domain.TrustedIssuer, jwksURI string) ([]byte, error) {
	return p.get(ctx, issuer, jwksURI)
}

func (p *Provider) get(ctx context.Context, issuer domain.TrustedIssuer, uri string) ([]byte, error) {
	if err := domain.ValidateIssuerURL(uri); err != nil {
		return nil, fmt.Errorf("OIDC URL: %w", err)
	}
	client, closeIdle := p.client(issuer.AllowPrivateNetwork)
	defer closeIdle()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OIDC request to %s failed: %w", uri, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OIDC request to %s: status %d", uri, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodySize+1))
	if err != nil {
		return nil, fmt.Errorf("read OIDC response: %w", err)
	}
	if len(raw) > MaxBodySize {
		return nil, fmt.Errorf("OIDC response from %s exceeds %d bytes", uri, MaxBodySize)
	}
	return raw, nil
}

// client builds a single-use client so pooled connections never cross issuer
// network policies.
func (p *Provider) client(allowPrivate bool) (*http.Client, func()) {
	transport := &http.Transport{
		Proxy: nil, // address checks must apply to the real destination
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return p.dial(ctx, network, address, allowPrivate)
		},
		TLSClientConfig:       &tls.Config{RootCAs: p.rootCAs, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   RequestTimeout,
		ResponseHeaderTimeout: RequestTimeout,
		ForceAttemptHTTP2:     true,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   RequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("OIDC redirects are refused")
		},
	}
	return client, transport.CloseIdleConnections
}

// dial resolves the host, refuses non-public addresses unless allowed, and
// connects to a checked IP literal so DNS rebinding cannot bypass the check.
func (p *Provider) dial(ctx context.Context, network, address string, allowPrivate bool) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addrs, err := p.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("OIDC host %s has no addresses", host)
	}
	for _, a := range addrs {
		if !allowPrivate && !IsPublicIP(a.IP) {
			return nil, fmt.Errorf("%w: %s resolves to %s", ErrDestinationNotPublic, host, a.IP)
		}
	}
	dialer := &net.Dialer{Timeout: RequestTimeout}
	var lastErr error
	for _, a := range addrs {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),     // "this" network
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),   // reserved, broadcast
	netip.MustParsePrefix("64:ff9b::/96"),  // NAT64 can reach IPv4 private ranges
}

// IsPublicIP reports whether ip is a globally routable unicast address. It
// rejects loopback, link-local (incl. 169.254.169.254), RFC 1918, 100.64/10,
// ULA, unspecified, multicast and other reserved ranges.
func IsPublicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}
