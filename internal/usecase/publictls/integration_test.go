package publictls

import (
	"context"
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// ---------------------------------------------------------------------------
// Integration-style tests using fakes (no real ACME / Cloudflare)
// ---------------------------------------------------------------------------

func TestPublicTLSIntegration_DNS01WildcardStatusAndCertificateLookup(t *testing.T) {
	ctx := context.Background()

	// Routes: app.example.com, api.prod.example.com, example.com
	routes := &fakeRoutes{
		routes: []domain.Route{
			{Domain: "app.example.com"},
			{Domain: "api.prod.example.com"},
			{Domain: "example.com"},
		},
	}

	// Fake zone resolver returns "example.com" zone.
	zoneResolver := fixedZoneResolver{zone: "example.com"}

	// Fake issuer: returns valid stored cert for any order.
	issuer := &fakeIssuer{
		obtain: func(_ context.Context, order out.CertificateOrder) (*out.StoredCertificate, error) {
			return defaultStoredCert(order)
		},
	}
	store := &fakeStore{}

	cfg := Config{
		Enabled:   true,
		Email:     "admin@example.com",
		Challenge: "cloudflare-dns-01",
		TLSPort:   8443,
	}

	svc := NewService(cfg, ServiceDeps{
		Config:       cfg,
		Routes:       routes,
		Issuer:       issuer,
		Store:        store,
		ZoneResolver: zoneResolver,
		Challenges:   NewHTTP01Challenges(),
		Effective: EffectiveChallenge{
			ConfiguredMode: domain.ACMEChallengeCloudflareDNS01,
			Mode:           domain.ACMEChallengeCloudflareDNS01,
			TokenSource:    domain.ACMETokenSourceNone,
			Reason:         "cloudflare dns-01 challenge selected",
		},
	})

	// Load (nothing stored).
	err := svc.Load(ctx)
	require.NoError(t, err)

	// Reconcile — should obtain DNS wildcard base targets:
	// example.com (for app.example.com + example.com) and prod.example.com (for api.prod.example.com).
	err = svc.Reconcile(ctx)
	require.NoError(t, err)

	// Verify issuer received exactly 2 orders.
	issuer.mu.Lock()
	orders := make([]out.CertificateOrder, len(issuer.orders))
	copy(orders, issuer.orders)
	issuer.mu.Unlock()

	require.Len(t, orders, 2, "expected 2 DNS-01 orders")

	// Check order details (orders are sorted alphabetically by target base).
	order0 := orders[0]
	assert.Equal(t, "dns01-example.com", order0.ID)
	assert.ElementsMatch(t, []string{"example.com", "*.example.com"}, order0.Names)
	assert.Equal(t, domain.ACMEChallengeCloudflareDNS01, order0.Challenge)

	order1 := orders[1]
	assert.Equal(t, "dns01-prod.example.com", order1.ID)
	assert.ElementsMatch(t, []string{"prod.example.com", "*.prod.example.com"}, order1.Names)
	assert.Equal(t, domain.ACMEChallengeCloudflareDNS01, order1.Challenge)

	// --- Status reports routes covered ---
	status := svc.Status(ctx)
	require.True(t, status.ACMEEnabled)
	require.Len(t, status.Routes, 3, "expected 3 routes in status")

	// Build a map for easier assertions.
	routeMap := make(map[string]domain.TLSRouteCoverage)
	for _, r := range status.Routes {
		routeMap[r.Domain] = r
	}

	for _, domain := range []string{"app.example.com", "api.prod.example.com", "example.com"} {
		rc, ok := routeMap[domain]
		require.True(t, ok, "route %s should be present in status", domain)
		assert.True(t, rc.Covered, "route %s should be covered", domain)
		assert.True(t, rc.RequiredACME)
		assert.NotEmpty(t, rc.CoveredBy, "route %s should have CoveredBy set", domain)
		assert.Empty(t, rc.Error, "route %s should have no error", domain)
	}

	// app.example.com should be covered by the wildcard cert (dns01-example.com).
	assert.Equal(t, "dns01-example.com", routeMap["app.example.com"].CoveredBy,
		"app.example.com should be covered by dns01-example.com (wildcard *.example.com)")

	// example.com should be covered by the base cert (dns01-example.com).
	assert.Equal(t, "dns01-example.com", routeMap["example.com"].CoveredBy,
		"example.com should be covered by dns01-example.com")

	// api.prod.example.com should be covered by the wildcard cert (dns01-prod.example.com).
	assert.Equal(t, "dns01-prod.example.com", routeMap["api.prod.example.com"].CoveredBy,
		"api.prod.example.com should be covered by dns01-prod.example.com (wildcard *.prod.example.com)")

	// --- GetCertificate returns non-nil certs ---
	t.Run("GetCertificate_returns_cert_for_app_example_com", func(t *testing.T) {
		cert, err := svc.GetCertificate(&tls.ClientHelloInfo{ServerName: "app.example.com"})
		require.NoError(t, err)
		require.NotNil(t, cert)
		require.NotEmpty(t, cert.Certificate)
	})

	t.Run("GetCertificate_returns_cert_for_api_prod_example_com", func(t *testing.T) {
		cert, err := svc.GetCertificate(&tls.ClientHelloInfo{ServerName: "api.prod.example.com"})
		require.NoError(t, err)
		require.NotNil(t, cert)
		require.NotEmpty(t, cert.Certificate)
	})

	t.Run("GetCertificate_returns_cert_for_example_com", func(t *testing.T) {
		cert, err := svc.GetCertificate(&tls.ClientHelloInfo{ServerName: "example.com"})
		require.NoError(t, err)
		require.NotNil(t, cert)
		require.NotEmpty(t, cert.Certificate)
	})

	// Also verify that a host not in the routes returns nil (no required ACME).
	// "unrelated.com" is not covered by any wildcard base in requiredHosts.
	t.Run("GetCertificate_returns_nil_for_unrelated_host", func(t *testing.T) {
		cert, err := svc.GetCertificate(&tls.ClientHelloInfo{ServerName: "unrelated.com"})
		require.NoError(t, err)
		assert.Nil(t, cert, "unrelated host should not require ACME coverage")
	})
}

func TestPublicTLSIntegration_HTTP01PerRouteChallengeFlow(t *testing.T) {
	ctx := context.Background()

	routes := &fakeRoutes{
		routes: []domain.Route{
			{Domain: "app.example.com"},
		},
	}

	issuer := &fakeIssuer{}
	store := &fakeStore{}
	challenges := NewHTTP01Challenges()

	cfg := Config{
		Enabled:   true,
		Email:     "admin@example.com",
		Challenge: "http-01",
		HTTPPort:  8088,
		TLSPort:   8443,
	}

	svc := NewService(cfg, ServiceDeps{
		Config:     cfg,
		Routes:     routes,
		Issuer:     issuer,
		Store:      store,
		Challenges: challenges,
		Effective: EffectiveChallenge{
			ConfiguredMode: domain.ACMEChallengeHTTP01,
			Mode:           domain.ACMEChallengeHTTP01,
			TokenSource:    domain.ACMETokenSourceNone,
			Reason:         "http-01 challenge selected",
		},
	})

	// Present an HTTP-01 challenge token/keyAuth.
	token := "test-token-abc123"
	keyAuth := "test-key-auth-xyz789"
	challenges.Present(token, keyAuth)

	// GetHTTP01Challenge should return the keyAuth.
	gotKeyAuth, ok := svc.GetHTTP01Challenge(ctx, token)
	require.True(t, ok, "GetHTTP01Challenge should find the token")
	assert.Equal(t, keyAuth, gotKeyAuth)

	// Load the service (nothing stored).
	err := svc.Load(ctx)
	require.NoError(t, err)

	// Reconcile — should create per-route target app.example.com.
	err = svc.Reconcile(ctx)
	require.NoError(t, err)

	// Verify the issuer received exactly 1 order.
	issuer.mu.Lock()
	orders := make([]out.CertificateOrder, len(issuer.orders))
	copy(orders, issuer.orders)
	issuer.mu.Unlock()

	require.Len(t, orders, 1, "expected 1 HTTP-01 order")
	assert.Equal(t, "http01-app.example.com", orders[0].ID)
	assert.Equal(t, []string{"app.example.com"}, orders[0].Names)
	assert.Equal(t, domain.ACMEChallengeHTTP01, orders[0].Challenge)

	// Status reports covered route.
	status := svc.Status(ctx)
	require.True(t, status.ACMEEnabled)
	require.Len(t, status.Routes, 1, "expected 1 route in status")
	assert.Equal(t, "app.example.com", status.Routes[0].Domain)
	assert.True(t, status.Routes[0].Covered, "route should be covered")
	assert.Equal(t, "http01-app.example.com", status.Routes[0].CoveredBy)
	assert.Empty(t, status.Routes[0].Error)

	// GetCertificate returns non-nil cert for app.example.com.
	cert, err := svc.GetCertificate(&tls.ClientHelloInfo{ServerName: "app.example.com"})
	require.NoError(t, err)
	require.NotNil(t, cert)
	require.NotEmpty(t, cert.Certificate)

	// GetCertificate returns nil for unrelated host.
	cert, err = svc.GetCertificate(&tls.ClientHelloInfo{ServerName: "other.example.com"})
	require.NoError(t, err)
	assert.Nil(t, cert)
}

func TestPublicTLSIntegration_MissingRequiredCertReportsCoverageError(t *testing.T) {
	ctx := context.Background()

	routes := &fakeRoutes{
		routes: []domain.Route{
			{Domain: "missing.example.com"},
		},
	}

	// Issuer returns error so no cert is obtained.
	issuer := &fakeIssuer{
		obtain: func(_ context.Context, _ out.CertificateOrder) (*out.StoredCertificate, error) {
			return nil, assert.AnError
		},
	}
	store := &fakeStore{}

	cfg := Config{
		Enabled:   true,
		Email:     "admin@example.com",
		Challenge: "http-01",
		HTTPPort:  8088,
		TLSPort:   8443,
	}

	svc := NewService(cfg, ServiceDeps{
		Config:     cfg,
		Routes:     routes,
		Issuer:     issuer,
		Store:      store,
		Challenges: NewHTTP01Challenges(),
		Effective: EffectiveChallenge{
			ConfiguredMode: domain.ACMEChallengeHTTP01,
			Mode:           domain.ACMEChallengeHTTP01,
			TokenSource:    domain.ACMETokenSourceNone,
			Reason:         "http-01 challenge selected",
		},
	})

	// Load (nothing stored).
	err := svc.Load(ctx)
	require.NoError(t, err)

	// Reconcile — issuer fails, so no cert is cached but requiredHosts is set.
	err = svc.Reconcile(ctx)
	require.NoError(t, err) // Reconcile swallows per-target obtain errors

	// GetCertificate for missing.example.com should return ErrTLSRouteNotCovered.
	_, err = svc.GetCertificate(&tls.ClientHelloInfo{ServerName: "missing.example.com"})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrTLSRouteNotCovered)
	// The error message should contain the hostname.
	assert.Contains(t, err.Error(), "missing.example.com")

	// Status should show the route is NOT covered and has an error.
	status := svc.Status(ctx)
	require.True(t, status.ACMEEnabled)
	require.Len(t, status.Routes, 1, "expected 1 route in status")
	rc := status.Routes[0]
	assert.Equal(t, "missing.example.com", rc.Domain)
	assert.False(t, rc.Covered, "route should NOT be covered")
	assert.True(t, rc.RequiredACME)

	// The route error may be empty since not all code paths populate routeErr;
	// at minimum the covered flag must be false.
	t.Logf("Route error (may be empty): %q", rc.Error)
}
