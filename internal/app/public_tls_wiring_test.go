package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// stubPublicTLS implements in.PublicTLSService for testing the certificate selector.
type stubPublicTLS struct {
	getCertFunc func(hello *tls.ClientHelloInfo) (*tls.Certificate, error)
}

func (s *stubPublicTLS) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if s.getCertFunc != nil {
		return s.getCertFunc(hello)
	}
	return nil, nil
}

func (s *stubPublicTLS) GetHTTP01Challenge(ctx context.Context, token string) (string, bool) {
	return "", false
}

func (s *stubPublicTLS) Status(ctx context.Context) domain.PublicTLSStatus {
	return domain.PublicTLSStatus{}
}

func (s *stubPublicTLS) Reconcile(ctx context.Context) error {
	return nil
}

func (s *stubPublicTLS) Stop(ctx context.Context) error {
	return nil
}

// generateTestCert creates a self-signed certificate for the given hostnames.
func generateTestCert(t *testing.T, hosts ...string) tls.Certificate {
	t.Helper()

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, host)
		}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privKey.PublicKey, privKey)
	require.NoError(t, err)

	return tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  privKey,
	}
}

// TestCertificateSelector_StaticCertWins tests that a static certificate matching
// the server name is returned before public TLS.
func TestCertificateSelector_StaticCertWins(t *testing.T) {
	staticCert := generateTestCert(t, "static.example.com")
	publicCert := generateTestCert(t, "static.example.com")

	selector := &certificateSelector{
		staticCerts: []tls.Certificate{staticCert},
		publicTLS: &stubPublicTLS{
			getCertFunc: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
				return &publicCert, nil
			},
		},
	}

	got, err := selector.GetCertificate(&tls.ClientHelloInfo{ServerName: "static.example.com"})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, staticCert.Certificate[0], got.Certificate[0],
		"static cert should be returned, not public TLS")
}

// TestCertificateSelector_PublicTLSErrorPropagates tests that if public TLS
// returns an error (e.g. ErrTLSRouteNotCovered), the error propagates.
func TestCertificateSelector_PublicTLSErrorPropagates(t *testing.T) {
	selector := &certificateSelector{
		publicTLS: &stubPublicTLS{
			getCertFunc: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
				return nil, domain.ErrTLSRouteNotCovered
			},
		},
	}

	got, err := selector.GetCertificate(&tls.ClientHelloInfo{ServerName: "acme-required.example.com"})
	assert.ErrorIs(t, err, domain.ErrTLSRouteNotCovered,
		"public TLS error should propagate")
	assert.Nil(t, got, "no certificate should be returned on error")
}

// TestCertificateSelector_PublicTLSNilNil tests that when public TLS returns
// nil,nil and no local PKI, the selector returns nil,nil.
func TestCertificateSelector_PublicTLSNilNil(t *testing.T) {
	selector := &certificateSelector{
		publicTLS: &stubPublicTLS{
			getCertFunc: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
				return nil, nil
			},
		},
	}

	got, err := selector.GetCertificate(&tls.ClientHelloInfo{ServerName: "non-acme.example.com"})
	require.NoError(t, err)
	assert.Nil(t, got, "nil,nil from public TLS with no local PKI returns nil,nil")
}

// TestCertificateSelector_NoSource tests that when no source has a matching cert,
// nil, nil is returned.
func TestCertificateSelector_NoSource(t *testing.T) {
	selector := &certificateSelector{}

	got, err := selector.GetCertificate(&tls.ClientHelloInfo{ServerName: "unknown.example.com"})
	assert.NoError(t, err)
	assert.Nil(t, got)
}

// TestMatchingStaticCert tests the matchingStaticCert helper.
func TestMatchingStaticCert(t *testing.T) {
	certA := generateTestCert(t, "app.example.com")
	certB := generateTestCert(t, "api.example.com")

	certs := []tls.Certificate{certA, certB}

	// Exact match
	got := matchingStaticCert(certs, "app.example.com")
	require.NotNil(t, got)
	assert.Equal(t, certA.Certificate[0], got.Certificate[0])

	// Second cert match
	got = matchingStaticCert(certs, "api.example.com")
	require.NotNil(t, got)
	assert.Equal(t, certB.Certificate[0], got.Certificate[0])

	// No match
	got = matchingStaticCert(certs, "other.example.com")
	assert.Nil(t, got)

	// Empty server name
	got = matchingStaticCert(certs, "")
	assert.Nil(t, got)

	// Empty cert list
	got = matchingStaticCert(nil, "app.example.com")
	assert.Nil(t, got)
}
