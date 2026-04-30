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
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	pkiadapter "github.com/bnema/gordon/internal/adapters/out/pki"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	pkiusecase "github.com/bnema/gordon/internal/usecase/pki"
	"github.com/bnema/zerowrap"
)

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
	selector := &certificateSelector{
		staticCerts: prepareStaticTLSCertificates([]tls.Certificate{staticCert}),
		publicTLS:   inmocks.NewMockPublicTLSService(t),
	}

	got, err := selector.GetCertificate(&tls.ClientHelloInfo{ServerName: "static.example.com"})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, staticCert.Certificate[0], got.Certificate[0],
		"static cert should be returned, not public TLS")
}

func TestCertificateSelector_PublicTLSErrorFallsThroughWithoutLocalPKI(t *testing.T) {
	publicTLS := inmocks.NewMockPublicTLSService(t)
	publicTLS.EXPECT().GetCertificateForHost("acme-required.example.com").Return(nil, domain.ErrTLSRouteNotCovered)
	selector := &certificateSelector{publicTLS: publicTLS}

	got, err := selector.GetCertificate(&tls.ClientHelloInfo{ServerName: "acme-required.example.com"})
	require.NoError(t, err)
	assert.Nil(t, got, "public TLS error with no local PKI returns nil,nil")
}

// TestCertificateSelector_PublicTLSNilNil tests that when public TLS returns
// nil,nil and no local PKI, the selector returns nil,nil.
func TestCertificateSelector_PublicTLSErrorFallsThroughToLocalPKI(t *testing.T) {
	publicTLS := inmocks.NewMockPublicTLSService(t)
	publicTLS.EXPECT().GetCertificateForHost("acme-required.example.com").Return(nil, domain.ErrTLSRouteNotCovered)

	routes := outmocks.NewMockRouteChecker(t)
	routes.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{{Domain: "acme-required.example.com"}}).Maybe()
	routes.EXPECT().GetExternalRoutes().Return(map[string]string{}).Maybe()
	ca, err := pkiadapter.NewCA(t.TempDir(), zerowrap.Default())
	require.NoError(t, err)
	pkiSvc := pkiusecase.NewService(context.Background(), ca, routes, zerowrap.Default())
	defer pkiSvc.Stop()

	selector := &certificateSelector{publicTLS: publicTLS, localPKI: pkiSvc}
	got, err := selector.GetCertificate(&tls.ClientHelloInfo{ServerName: "acme-required.example.com"})
	require.NoError(t, err)
	require.NotNil(t, got, "public TLS error should fall through to local PKI")
}

func TestCertificateSelector_PublicTLSNilNil(t *testing.T) {
	publicTLS := inmocks.NewMockPublicTLSService(t)
	publicTLS.EXPECT().GetCertificateForHost("non-acme.example.com").Return(nil, nil)
	selector := &certificateSelector{publicTLS: publicTLS}

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

	// Empty server name returns the first configured static certificate.
	got = matchingStaticCert(certs, "")
	require.NotNil(t, got)
	assert.Equal(t, certA.Certificate[0], got.Certificate[0])

	// Empty cert list
	got = matchingStaticCert(nil, "app.example.com")
	assert.Nil(t, got)
}
