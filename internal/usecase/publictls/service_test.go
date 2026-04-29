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
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeRoutes implements RouteSource for testing.
type fakeRoutes struct {
	mu       sync.Mutex
	routes   []domain.Route
	external map[string]string
}

func (f *fakeRoutes) GetRoutes(_ context.Context) []domain.Route {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Route, len(f.routes))
	copy(out, f.routes)
	return out
}

func (f *fakeRoutes) GetExternalRoutes() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.external))
	for k, v := range f.external {
		out[k] = v
	}
	return out
}

// fakeIssuer implements out.PublicCertificateIssuer for testing.
type fakeIssuer struct {
	mu      sync.Mutex
	orders  []out.CertificateOrder // recorded orders
	obtain  func(ctx context.Context, order out.CertificateOrder) (*out.StoredCertificate, error)
	renewFn func(ctx context.Context, cert out.StoredCertificate) (*out.StoredCertificate, error)
}

func (f *fakeIssuer) Obtain(ctx context.Context, order out.CertificateOrder) (*out.StoredCertificate, error) {
	f.mu.Lock()
	f.orders = append(f.orders, order)
	f.mu.Unlock()
	if f.obtain != nil {
		return f.obtain(ctx, order)
	}
	// Default: return a valid certificate.
	return defaultStoredCert(order)
}

func (f *fakeIssuer) Renew(ctx context.Context, cert out.StoredCertificate) (*out.StoredCertificate, error) {
	if f.renewFn != nil {
		return f.renewFn(ctx, cert)
	}
	return &cert, nil
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

// fakeStore implements out.CertificateStore for testing.
type fakeStore struct {
	mu       sync.Mutex
	lockMu   sync.Mutex // separate lock for Lock()/unlock to avoid deadlock with mu
	account  *out.ACMEAccount
	certs    []out.StoredCertificate
	lockHold bool
}

func (f *fakeStore) LoadAccount(_ context.Context) (*out.ACMEAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.account == nil {
		return nil, nil
	}
	acct := *f.account
	return &acct, nil
}

func (f *fakeStore) SaveAccount(_ context.Context, account out.ACMEAccount) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	acct := account
	f.account = &acct
	return nil
}

func (f *fakeStore) LoadAll(_ context.Context) ([]out.StoredCertificate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]out.StoredCertificate, len(f.certs))
	copy(out, f.certs)
	return out, nil
}

func (f *fakeStore) Save(_ context.Context, cert out.StoredCertificate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.certs {
		if c.ID == cert.ID {
			f.certs[i] = cert
			return nil
		}
	}
	f.certs = append(f.certs, cert)
	return nil
}

func (f *fakeStore) Lock(_ context.Context) (func() error, error) {
	f.lockMu.Lock()
	f.lockHold = true
	return func() error {
		f.lockHold = false
		f.lockMu.Unlock()
		return nil
	}, nil
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
	issuer := &fakeIssuer{}
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

	// Load (nothing stored yet).
	err := svc.Load(ctx)
	require.NoError(t, err)

	// Reconcile — should obtain one certificate for app.example.com.
	err = svc.Reconcile(ctx)
	require.NoError(t, err)

	// Verify the issuer received exactly one order.
	issuer.mu.Lock()
	orders := make([]out.CertificateOrder, len(issuer.orders))
	copy(orders, issuer.orders)
	issuer.mu.Unlock()

	require.Len(t, orders, 1, "expected one order to be placed")
	assert.Equal(t, "http01-app.example.com", orders[0].ID)
	assert.Equal(t, []string{"app.example.com"}, orders[0].Names)
	assert.Equal(t, domain.ACMEChallengeHTTP01, orders[0].Challenge)

	// Verify the store has the saved certificate.
	stored, err := store.LoadAll(ctx)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, "http01-app.example.com", stored[0].ID)
	assert.Equal(t, []string{"app.example.com"}, stored[0].Names)
	assert.False(t, stored[0].NotAfter.IsZero())
	assert.NotEmpty(t, stored[0].Certificate.Certificate)
}

func TestServiceStatusReportsCoverage(t *testing.T) {
	ctx := context.Background()

	// Pre-populate the store with a valid certificate.
	certPEM, keyPEM, err := generateTestCertPEM([]string{"app.example.com"})
	require.NoError(t, err)
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)

	store := &fakeStore{
		certs: []out.StoredCertificate{
			{
				ID:            "http01-app.example.com",
				Names:         []string{"app.example.com"},
				Challenge:     domain.ACMEChallengeHTTP01,
				Certificate:   tlsCert,
				FullchainPEM:  certPEM,
				PrivateKeyPEM: keyPEM,
				NotAfter:      time.Now().Add(90 * 24 * time.Hour),
			},
		},
	}

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
	issuer := &fakeIssuer{
		// Make Obtain fail so no cert is cached.
		obtain: func(_ context.Context, _ out.CertificateOrder) (*out.StoredCertificate, error) {
			return nil, fmt.Errorf("acme provider unavailable")
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

	// Load (no certs stored).
	err := svc.Load(ctx)
	require.NoError(t, err)

	// Reconcile — issuer fails, no cert cached but requiredHosts is set.
	err = svc.Reconcile(ctx)
	require.NoError(t, err) // Reconcile swallows per-target obtain errors

	// GetCertificate for app.example.com should return ErrTLSRouteNotCovered.
	_, err = svc.GetCertificate(&tls.ClientHelloInfo{ServerName: "app.example.com"})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrTLSRouteNotCovered)
	assert.Contains(t, err.Error(), "app.example.com")
}

// brokenZoneResolver always returns an error.
type brokenZoneResolver struct{}

func (brokenZoneResolver) FindZone(_ context.Context, host string) (out.CloudflareZone, error) {
	return out.CloudflareZone{}, fmt.Errorf("zone resolver unavailable for %s", host)
}

func TestServiceReconcileDNS01BrokenResolverReturnsErrTLSRouteNotCovered(t *testing.T) {
	ctx := context.Background()

	routes := &fakeRoutes{
		routes: []domain.Route{
			{Domain: "app.example.com"},
		},
	}
	issuer := &fakeIssuer{}
	store := &fakeStore{}

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
		ZoneResolver: brokenZoneResolver{}, // broken: always fails
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

	// GetCertificate for the configured route should return ErrTLSRouteNotCovered,
	// not nil,nil, because requiredHosts was set from route hosts before
	// the failed DeriveCertificateTargets call.
	_, err = svc.GetCertificate(&tls.ClientHelloInfo{ServerName: "app.example.com"})
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
	store := &fakeStore{
		certs: []out.StoredCertificate{
			{
				ID:            "http01-test.example.com",
				Names:         []string{"test.example.com"},
				Challenge:     domain.ACMEChallengeHTTP01,
				FullchainPEM:  certPEM,
				PrivateKeyPEM: keyPEM,
				NotAfter:      time.Now().Add(90 * 24 * time.Hour),
				// Certificate field is zero — will be populated by Load.
			},
		},
	}

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

	// Check that the cached cert has a valid tls.Certificate.
	//nolint:unused // accessing the cert through the map is sufficient
	svc.mu.Lock()
	cached, ok := svc.certs["http01-test.example.com"]
	svc.mu.Unlock()

	require.True(t, ok, "cert should be cached")
	require.NotNil(t, cached)
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

	store := &fakeStore{
		certs: []out.StoredCertificate{
			{
				ID:            "http01-app.example.com",
				Names:         []string{"app.example.com"},
				Challenge:     domain.ACMEChallengeHTTP01,
				Certificate:   tlsCert,
				FullchainPEM:  certPEM,
				PrivateKeyPEM: keyPEM,
				NotAfter:      time.Now().Add(90 * 24 * time.Hour),
				LastError:     "failed to obtain: token=sk-secret-goes-here provider said invalid",
			},
		},
	}

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
