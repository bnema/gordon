package acmelego

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/go-acme/lego/v4/acme"
	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

func TestNewIssuerValidatesEmail(t *testing.T) {
	_, err := NewIssuer(Config{})
	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrACMEEmailRequired)
}

func TestNewIssuerValidatesStore(t *testing.T) {
	_, err := NewIssuer(Config{Email: "test@example.com"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "certificate store is required")
}

func TestNewIssuerValidatesCloudflareToken(t *testing.T) {
	_, err := NewIssuer(Config{
		Email:     "test@example.com",
		Challenge: domain.ACMEChallengeCloudflareDNS01,
		Store:     &mockStore{},
	})
	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrCloudflareTokenMissing)
}

func TestNewIssuerValidWithDefaults(t *testing.T) {
	issuer, err := NewIssuer(Config{
		Email:             "test@example.com",
		Store:             &mockStore{},
		HTTPChallengeSink: &mockHTTPChallengeSink{},
	})
	require.NoError(t, err)
	require.NotNil(t, issuer)
	assert.Equal(t, domain.ACMEChallengeHTTP01, issuer.cfg.Challenge)
}

// mockHTTPChallengeSink implements HTTPChallengeSink for testing.
type mockHTTPChallengeSink struct{}

func (m *mockHTTPChallengeSink) Present(token, keyAuth string) {}
func (m *mockHTTPChallengeSink) CleanUp(token string)          {}

// mockStore implements out.CertificateStore for testing.
type mockStore struct {
	account *out.ACMEAccount
	certs   []out.StoredCertificate
}

func (m *mockStore) LoadAccount(ctx context.Context) (*out.ACMEAccount, error) {
	return m.account, nil
}

func (m *mockStore) SaveAccount(ctx context.Context, account out.ACMEAccount) error {
	m.account = &account
	return nil
}

func (m *mockStore) LoadAll(ctx context.Context) ([]out.StoredCertificate, error) {
	return m.certs, nil
}

func (m *mockStore) Save(ctx context.Context, cert out.StoredCertificate) error {
	return nil
}

func (m *mockStore) Lock(ctx context.Context) (func() error, error) {
	return func() error { return nil }, nil
}

func TestPrivateKeyRoundTrip(t *testing.T) {
	// Generate an ECDSA P-256 private key
	privateKey, err := certcrypto.GeneratePrivateKey(certcrypto.EC256)
	require.NoError(t, err)
	require.NotNil(t, privateKey)

	// PEM encode
	pemBytes := certcrypto.PEMEncode(privateKey)
	require.NotEmpty(t, pemBytes)

	// Parse back
	parsedKey, err := certcrypto.ParsePEMPrivateKey(pemBytes)
	require.NoError(t, err)
	require.NotNil(t, parsedKey)

	// Verify it's the same curve (ECDSA P-256)
	ecKey, ok := parsedKey.(*ecdsa.PrivateKey)
	require.True(t, ok, "expected *ecdsa.PrivateKey")
	assert.Equal(t, elliptic.P256(), ecKey.Curve)

	// Verify we can sign with the original and verify with the parsed
	digest := []byte("test message")
	sig, err := ecdsa.SignASN1(rand.Reader, privateKey.(*ecdsa.PrivateKey), digest[:])
	require.NoError(t, err)
	assert.True(t, ecdsa.VerifyASN1(&ecKey.PublicKey, digest[:], sig))
}

func TestPrivateKeyRSARoundTrip(t *testing.T) {
	// Test RSA private key round-trip via certcrypto
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	pemBytes := certcrypto.PEMEncode(privateKey)
	require.NotEmpty(t, pemBytes)

	parsedKey, err := certcrypto.ParsePEMPrivateKey(pemBytes)
	require.NoError(t, err)
	require.NotNil(t, parsedKey)

	_, ok := parsedKey.(*rsa.PrivateKey)
	require.True(t, ok)
}

func TestResourceConversionToStoredCertificate(t *testing.T) {
	// Generate a self-signed certificate for testing
	certPEM, keyPEM, err := generateSelfSignedCert("example.com")
	require.NoError(t, err)

	// Generate a fake issuer certificate
	issuerPEM, _, err := generateSelfSignedCert("fake-ca.example.com")
	require.NoError(t, err)

	resource := &certificate.Resource{
		Domain:            "example.com",
		CertURL:           "https://acme-staging-v02.api.letsencrypt.org/acme/cert/123",
		CertStableURL:     "https://acme-staging-v02.api.letsencrypt.org/acme/cert/123",
		PrivateKey:        keyPEM,
		Certificate:       certPEM,
		IssuerCertificate: issuerPEM,
	}

	order := out.CertificateOrder{
		ID:        "test-cert",
		Names:     []string{"example.com"},
		Challenge: domain.ACMEChallengeHTTP01,
	}

	stored, err := resourceToStored(order, resource)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "test-cert", stored.ID)
	assert.Equal(t, []string{"example.com"}, stored.Names)
	assert.Equal(t, domain.ACMEChallengeHTTP01, stored.Challenge)
	assert.Equal(t, certPEM, stored.CertPEM)
	assert.Equal(t, issuerPEM, stored.ChainPEM)

	// FullchainPEM should be cert + issuer
	expectedFullchain := append(append([]byte{}, certPEM...), issuerPEM...)
	assert.Equal(t, expectedFullchain, stored.FullchainPEM)
	assert.Equal(t, keyPEM, stored.PrivateKeyPEM)

	// NotAfter should be set
	assert.False(t, stored.NotAfter.IsZero(), "NotAfter should be set")

	// Verify tls.Certificate is populated from the PEM data
	assert.NotEmpty(t, stored.Certificate.Certificate, "tls.Certificate should be populated from PEM")
	assert.NotNil(t, stored.Certificate.PrivateKey, "private key should be populated")
}

func TestResourceConversionNoIssuer(t *testing.T) {
	certPEM, keyPEM, err := generateSelfSignedCert("example.com")
	require.NoError(t, err)

	resource := &certificate.Resource{
		Domain:      "example.com",
		PrivateKey:  keyPEM,
		Certificate: certPEM,
	}

	order := out.CertificateOrder{
		ID:    "test-cert-no-issuer",
		Names: []string{"example.com"},
	}

	stored, err := resourceToStored(order, resource)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, certPEM, stored.FullchainPEM, "fullchain should equal cert when no issuer")
	assert.Empty(t, stored.ChainPEM)
}

func TestAccountJSONRoundTrip(t *testing.T) {
	// Simulate an ACME account with BodyJSON containing the serialized acme.Account
	body := acme.Account{
		Status:               "valid",
		Contact:              []string{"mailto:test@example.com"},
		TermsOfServiceAgreed: true,
		Orders:               "https://acme-v02.api.letsencrypt.org/acme/orders/abc123",
	}

	bodyJSON, err := json.Marshal(body)
	require.NoError(t, err)

	var restored acme.Account
	err = json.Unmarshal(bodyJSON, &restored)
	require.NoError(t, err)
	assert.Equal(t, body.Status, restored.Status)
	assert.Equal(t, body.Contact, restored.Contact)
	assert.Equal(t, body.TermsOfServiceAgreed, restored.TermsOfServiceAgreed)
	assert.Equal(t, body.Orders, restored.Orders)
}

// generateSelfSignedCert creates a self-signed certificate and its private key
// for testing purposes.
func generateSelfSignedCert(domain string) (certPEM, keyPEM []byte, err error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   domain,
			Organization: []string{"Test"},
		},
		DNSNames:              []string{domain},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if certPEM == nil {
		return nil, nil, errors.New("pem encode certificate returned nil")
	}

	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, nil, err
	}

	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if keyPEM == nil {
		return nil, nil, errors.New("pem encode private key returned nil")
	}

	return certPEM, keyPEM, nil
}

// Ensure TestResourceConversion also validates the tls.X509KeyPair parse works.
func TestParsedCertificateNotAfter(t *testing.T) {
	certPEM, keyPEM, err := generateSelfSignedCert("test.example.com")
	require.NoError(t, err)

	resource := &certificate.Resource{
		Domain:      "test.example.com",
		PrivateKey:  keyPEM,
		Certificate: certPEM,
	}

	order := out.CertificateOrder{
		ID:    "parse-test",
		Names: []string{"test.example.com"},
	}

	stored, err := resourceToStored(order, resource)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.False(t, stored.NotAfter.IsZero())
	assert.True(t, stored.NotAfter.After(time.Now()))

	// Confirm tls.X509KeyPair succeeds on the stored PEMs
	_, parseErr := tls.X509KeyPair(stored.FullchainPEM, stored.PrivateKeyPEM)
	assert.NoError(t, parseErr, "tls.X509KeyPair should succeed")
}
