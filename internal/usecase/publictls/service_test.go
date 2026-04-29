package publictls

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"maps"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeRoutes models a stateful RouteSource using an internal mutex and
// copy-on-read semantics. Generated mocks cannot easily replicate this
// pattern because the interface methods return slices/maps by value and
// callers may modify the returned data; a hand-rolled fake ensures each
// GetRoutes/GetExternalRoutes call returns a defensive copy.
type fakeRoutes struct {
	mu       sync.Mutex
	routes   []domain.Route
	external map[string]string
}

func (f *fakeRoutes) GetRoutes(_ context.Context) []domain.Route {
	f.mu.Lock()
	defer f.mu.Unlock()
	routesCopy := make([]domain.Route, len(f.routes))
	copy(routesCopy, f.routes)
	return routesCopy
}

func (f *fakeRoutes) GetExternalRoutes() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	externalCopy := make(map[string]string, len(f.external))
	maps.Copy(externalCopy, f.external)
	return externalCopy
}

type mockIssuerRecorder struct {
	mu     sync.Mutex
	orders []out.CertificateOrder
}

func (r *mockIssuerRecorder) record(order out.CertificateOrder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.orders = append(r.orders, order)
}

func (r *mockIssuerRecorder) Orders() []out.CertificateOrder {
	r.mu.Lock()
	defer r.mu.Unlock()
	orders := make([]out.CertificateOrder, len(r.orders))
	copy(orders, r.orders)
	return orders
}

func newMockPublicCertificateIssuer(
	t *testing.T,
	obtain func(context.Context, out.CertificateOrder) (*out.StoredCertificate, error),
	renew func(context.Context, out.StoredCertificate) (*out.StoredCertificate, error),
) (*outmocks.MockPublicCertificateIssuer, *mockIssuerRecorder) {
	t.Helper()
	recorder := &mockIssuerRecorder{}
	issuer := outmocks.NewMockPublicCertificateIssuer(t)

	obtainCall := issuer.EXPECT().Obtain(mock.Anything, mock.Anything).RunAndReturn(
		func(ctx context.Context, order out.CertificateOrder) (*out.StoredCertificate, error) {
			recorder.record(order)
			if obtain != nil {
				return obtain(ctx, order)
			}
			return defaultStoredCert(order)
		},
	)
	obtainCall.Maybe()

	renewCall := issuer.EXPECT().Renew(mock.Anything, mock.Anything).RunAndReturn(
		func(ctx context.Context, cert out.StoredCertificate) (*out.StoredCertificate, error) {
			if renew != nil {
				return renew(ctx, cert)
			}
			return &cert, nil
		},
	)
	renewCall.Maybe()

	return issuer, recorder
}

// defaultStoredCert creates a StoredCertificate with a self-signed TLS cert
// for the given order.
func defaultStoredCert(order out.CertificateOrder) (*out.StoredCertificate, error) {
	certPEM, keyPEM, err := generateTestCertPEM(order.Names)
	if err != nil {
		return nil, err
	}
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &out.StoredCertificate{
		ID:            order.ID,
		Names:         order.Names,
		Challenge:     order.Challenge,
		Certificate:   tlsCert,
		FullchainPEM:  certPEM,
		PrivateKeyPEM: keyPEM,
		NotAfter:      time.Now().Add(90 * 24 * time.Hour),
	}, nil
}

type mockCertificateStoreState struct {
	mu      sync.Mutex
	account *out.ACMEAccount
	certs   []out.StoredCertificate
}

func (s *mockCertificateStoreState) All() []out.StoredCertificate {
	s.mu.Lock()
	defer s.mu.Unlock()
	certs := make([]out.StoredCertificate, len(s.certs))
	copy(certs, s.certs)
	return certs
}

func newMockCertificateStore(t *testing.T, initial ...out.StoredCertificate) (*outmocks.MockCertificateStore, *mockCertificateStoreState) {
	t.Helper()
	state := &mockCertificateStoreState{certs: append([]out.StoredCertificate(nil), initial...)}
	store := outmocks.NewMockCertificateStore(t)

	loadAccountCall := store.EXPECT().LoadAccount(mock.Anything).RunAndReturn(func(context.Context) (*out.ACMEAccount, error) {
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.account == nil {
			return nil, nil
		}
		account := *state.account
		return &account, nil
	})
	loadAccountCall.Maybe()

	saveAccountCall := store.EXPECT().SaveAccount(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, account out.ACMEAccount) error {
		state.mu.Lock()
		defer state.mu.Unlock()
		accountCopy := account
		state.account = &accountCopy
		return nil
	})
	saveAccountCall.Maybe()

	loadAllCall := store.EXPECT().LoadAll(mock.Anything).RunAndReturn(func(context.Context) ([]out.StoredCertificate, error) {
		return state.All(), nil
	})
	loadAllCall.Maybe()

	saveCall := store.EXPECT().Save(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, cert out.StoredCertificate) error {
		state.mu.Lock()
		defer state.mu.Unlock()
		for i, existing := range state.certs {
			if existing.ID == cert.ID {
				state.certs[i] = cert
				return nil
			}
		}
		state.certs = append(state.certs, cert)
		return nil
	})
	saveCall.Maybe()

	lockCall := store.EXPECT().Lock(mock.Anything).Return(func() error { return nil }, nil)
	lockCall.Maybe()

	return store, state
}

// ---------------------------------------------------------------------------
// Test certificate helpers
// ---------------------------------------------------------------------------

// generateTestCertPEM generates a self-signed certificate for the given names.
// Returns PEM-encoded fullchain and private key.
func generateTestCertPEM(names []string) (certPEM []byte, keyPEM []byte, err error) {
	if len(names) == 0 {
		names = []string{"localhost"}
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: names[0],
		},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.Add(90 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              names,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if certPEM == nil {
		return nil, nil, fmt.Errorf("encode cert PEM")
	}

	keyDER := x509.MarshalPKCS1PrivateKey(key)
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER})
	if keyPEM == nil {
		return nil, nil, fmt.Errorf("encode key PEM")
	}

	return certPEM, keyPEM, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestServiceReconcileObtainsMissingHTTP01Cert(t *testing.T) {
	ctx := context.Background()

	routes := &fakeRoutes{
		routes: []domain.Route{
			{Domain: "app.example.com"},
		},
	}
	issuer, recorder := newMockPublicCertificateIssuer(t, nil, nil)
	store, storeState := newMockCertificateStore(t)

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

	// Load (nothing stored yet).
	err := svc.Load(ctx)
	require.NoError(t, err)

	// Reconcile — should obtain one certificate for app.example.com.
	err = svc.Reconcile(ctx)
	require.NoError(t, err)

	// Verify the issuer received exactly one order.
	orders := recorder.Orders()

	require.Len(t, orders, 1, "expected one order to be placed")
	assert.Equal(t, "http01-app.example.com", orders[0].ID)
	assert.Equal(t, []string{"app.example.com"}, orders[0].Names)
	assert.Equal(t, domain.ACMEChallengeHTTP01, orders[0].Challenge)

	// Verify the store has the saved certificate.
	stored := storeState.All()
	require.Len(t, stored, 1)
	assert.Equal(t, "http01-app.example.com", stored[0].ID)
	assert.Equal(t, []string{"app.example.com"}, stored[0].Names)
	assert.False(t, stored[0].NotAfter.IsZero())
	assert.NotEmpty(t, stored[0].Certificate.Certificate)
}

func TestServiceReconcileResolvesMissingEffectiveChallenge(t *testing.T) {
	ctx := context.Background()
	routes := &fakeRoutes{routes: []domain.Route{{Domain: "app.example.com"}}}
	issuer, _ := newMockPublicCertificateIssuer(t, nil, nil)
	store, _ := newMockCertificateStore(t)
	cfg := Config{
		Enabled:   true,
		Email:     "admin@example.com",
		Challenge: "http-01",
		HTTPPort:  8088,
		// Missing TLSPort should be caught by ResolveEffectiveChallenge rather
		// than silently defaulting an effective mode.
	}

	svc := NewService(cfg, ServiceDeps{
		Config: cfg,
		Routes: routes,
		Issuer: issuer,
		Store:  store,
	})

	err := svc.Reconcile(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrACMEChallengeInvalid)
}

func TestServiceStatusReportsCoverage(t *testing.T) {
	ctx := context.Background()

	// Pre-populate the store with a valid certificate.
	certPEM, keyPEM, err := generateTestCertPEM([]string{"app.example.com"})
	require.NoError(t, err)
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)

	store, _ := newMockCertificateStore(t, out.StoredCertificate{
		ID:            "http01-app.example.com",
		Names:         []string{"app.example.com"},
		Challenge:     domain.ACMEChallengeHTTP01,
		Certificate:   tlsCert,
		FullchainPEM:  certPEM,
		PrivateKeyPEM: keyPEM,
		NotAfter:      time.Now().Add(90 * 24 * time.Hour),
	})

	routes := &fakeRoutes{
		routes: []domain.Route{
			{Domain: "app.example.com"},
		},
	}
	cfg := Config{
		Enabled:   true,
		Email:     "admin@example.com",
		Challenge: "http-01",
		HTTPPort:  8088,
		TLSPort:   8443,
	}

	svc := NewService(cfg, ServiceDeps{
		Config: cfg,
		Routes: routes,
		Store:  store,
		Effective: EffectiveChallenge{
			ConfiguredMode: domain.ACMEChallengeHTTP01,
			Mode:           domain.ACMEChallengeHTTP01,
			TokenSource:    domain.ACMETokenSourceNone,
			Reason:         "http-01 challenge selected",
		},
	})

	// Load the cert into the cache.
	err = svc.Load(ctx)
	require.NoError(t, err)

	// Get status.
	status := svc.Status(ctx)

	assert.True(t, status.ACMEEnabled)
	assert.Equal(t, domain.ACMEChallengeHTTP01, status.ConfiguredMode)
	assert.Equal(t, domain.ACMEChallengeHTTP01, status.EffectiveMode)

	require.Len(t, status.Certificates, 1, "expected one managed certificate")
	assert.Equal(t, "http01-app.example.com", status.Certificates[0].ID)
	assert.Equal(t, domain.TLSCertificateStatusValid, status.Certificates[0].Status)

	require.Len(t, status.Routes, 1, "expected one route coverage")
	assert.Equal(t, "app.example.com", status.Routes[0].Domain)
	assert.True(t, status.Routes[0].Covered, "route should be covered")
	assert.Equal(t, "http01-app.example.com", status.Routes[0].CoveredBy)
	assert.True(t, status.Routes[0].RequiredACME)
}

func TestServiceGetCertificateReturnsErrTLSRouteNotCovered(t *testing.T) {
	ctx := context.Background()

	routes := &fakeRoutes{
		routes: []domain.Route{
			{Domain: "app.example.com"},
		},
	}
	// Make Obtain fail so no cert is cached.
	issuer, _ := newMockPublicCertificateIssuer(t, func(_ context.Context, _ out.CertificateOrder) (*out.StoredCertificate, error) {
		return nil, fmt.Errorf("acme provider unavailable")
	}, nil)
	store, _ := newMockCertificateStore(t)

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

	// Load (no certs stored).
	err := svc.Load(ctx)
	require.NoError(t, err)

	// Reconcile — issuer fails, no cert cached but requiredHosts is set.
	err = svc.Reconcile(ctx)
	require.NoError(t, err) // Reconcile swallows per-target obtain errors

	// GetCertificateForHost for app.example.com should return ErrTLSRouteNotCovered.
	_, err = svc.GetCertificateForHost("app.example.com")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrTLSRouteNotCovered)
	assert.Contains(t, err.Error(), "app.example.com")
}

func TestServiceReconcileDNS01BrokenResolverReturnsErrTLSRouteNotCovered(t *testing.T) {
	ctx := context.Background()

	routes := &fakeRoutes{
		routes: []domain.Route{
			{Domain: "app.example.com"},
		},
	}
	issuer, _ := newMockPublicCertificateIssuer(t, nil, nil)
	store, _ := newMockCertificateStore(t)

	// Use generated mock that always returns an error.
	zoneResolver := outmocks.NewMockCloudflareZoneResolver(t)
	zoneResolver.EXPECT().FindZone(mock.Anything, "app.example.com").Return(
		out.CloudflareZone{}, fmt.Errorf("zone resolver unavailable for app.example.com"),
	)

	cfg := Config{
		Enabled:   true,
		Email:     "admin@example.com",
		Challenge: "dns-01",
		HTTPPort:  8088,
		TLSPort:   8443,
	}

	svc := NewService(cfg, ServiceDeps{
		Config:       cfg,
		Routes:       routes,
		Issuer:       issuer,
		Store:        store,
		ZoneResolver: zoneResolver, // generated mock that always fails
		Challenges:   NewHTTP01Challenges(),
		Effective: EffectiveChallenge{
			ConfiguredMode: domain.ACMEChallengeCloudflareDNS01,
			Mode:           domain.ACMEChallengeCloudflareDNS01,
			TokenSource:    domain.ACMETokenSourceNone,
			Reason:         "dns-01 challenge selected",
		},
	})

	// Load (nothing stored).
	err := svc.Load(ctx)
	require.NoError(t, err)

	// Reconcile — zone resolver is broken, so DeriveCertificateTargets fails.
	err = svc.Reconcile(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "derive certificate targets")

	// GetCertificateForHost for the configured route should return ErrTLSRouteNotCovered,
	// not nil,nil, because requiredHosts was set from route hosts before
	// the failed DeriveCertificateTargets call.
	_, err = svc.GetCertificateForHost("app.example.com")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrTLSRouteNotCovered)
	assert.Contains(t, err.Error(), "app.example.com")
}

func TestServiceLoadParsesPEMIntoTLSCertificate(t *testing.T) {
	ctx := context.Background()

	// Generate valid PEM cert data.
	certPEM, keyPEM, err := generateTestCertPEM([]string{"test.example.com"})
	require.NoError(t, err)

	// Store a cert with PEM data but empty tls.Certificate.
	store, _ := newMockCertificateStore(t, out.StoredCertificate{
		ID:            "http01-test.example.com",
		Names:         []string{"test.example.com"},
		Challenge:     domain.ACMEChallengeHTTP01,
		FullchainPEM:  certPEM,
		PrivateKeyPEM: keyPEM,
		NotAfter:      time.Now().Add(90 * 24 * time.Hour),
		// Certificate field is zero — will be populated by Load.
	})

	routes := &fakeRoutes{}
	cfg := Config{Enabled: true}

	svc := NewService(cfg, ServiceDeps{
		Config: cfg,
		Routes: routes,
		Store:  store,
	})

	// Load — should parse PEM into tls.Certificate.
	err = svc.Load(ctx)
	require.NoError(t, err)

	// Verify the cached cert has a valid tls.Certificate via the accessor.
	cached := svc.GetStoredCertificate("http01-test.example.com")
	require.NotNil(t, cached, "cert should be cached")
	assert.NotEmpty(t, cached.Certificate.Certificate, "tls.Certificate should be populated")
	assert.NotNil(t, cached.Certificate.PrivateKey, "private key should be populated")

	// Verify it's a valid TLS cert by performing a parse round-trip.
	leaf, err := x509.ParseCertificate(cached.Certificate.Certificate[0])
	require.NoError(t, err)
	assert.Contains(t, leaf.DNSNames, "test.example.com")
}

func TestServiceStatusRedactsSensitiveStrings(t *testing.T) {
	ctx := context.Background()

	// Pre-populate the store with a certificate that has a LastError
	// containing sensitive data.
	certPEM, keyPEM, err := generateTestCertPEM([]string{"app.example.com"})
	require.NoError(t, err)
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)

	store, _ := newMockCertificateStore(t, out.StoredCertificate{
		ID:            "http01-app.example.com",
		Names:         []string{"app.example.com"},
		Challenge:     domain.ACMEChallengeHTTP01,
		Certificate:   tlsCert,
		FullchainPEM:  certPEM,
		PrivateKeyPEM: keyPEM,
		NotAfter:      time.Now().Add(90 * 24 * time.Hour),
		LastError:     "failed to obtain: token=sk-secret-goes-here provider said invalid",
	})

	routes := &fakeRoutes{
		routes: []domain.Route{
			{Domain: "app.example.com"},
		},
	}
	cfg := Config{
		Enabled:   true,
		Email:     "admin@example.com",
		Challenge: "http-01",
		HTTPPort:  8088,
		TLSPort:   8443,
	}

	svc := NewService(cfg, ServiceDeps{
		Config: cfg,
		Routes: routes,
		Store:  store,
		Effective: EffectiveChallenge{
			ConfiguredMode: domain.ACMEChallengeHTTP01,
			Mode:           domain.ACMEChallengeHTTP01,
			TokenSource:    domain.ACMETokenSourceNone,
			Reason:         "http-01 challenge selected",
		},
	})

	// Load the cert into the cache.
	err = svc.Load(ctx)
	require.NoError(t, err)

	// Get status.
	status := svc.Status(ctx)

	require.Len(t, status.Certificates, 1)
	lastErr := status.Certificates[0].LastError
	t.Logf("sanitized LastError: %q", lastErr)

	// The original error contained "token=sk-secret-goes-here".
	// After sanitization, the secret value should be redacted.
	assert.NotContains(t, lastErr, "sk-secret-goes-here",
		"LastError must not contain the raw secret value")
	assert.Contains(t, lastErr, "token=redacted",
		"LastError should contain token=redacted")
}
