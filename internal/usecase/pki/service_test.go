package pki_test

import (
	"context"
	"crypto/tls"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	pkiadapter "github.com/bnema/gordon/internal/adapters/out/pki"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	pkiusecase "github.com/bnema/gordon/internal/usecase/pki"
)

func testLogger() zerowrap.Logger { return zerowrap.Default() }

type blockingCertificateAuthority struct {
	out.CertificateAuthority
	started chan struct{}
	release chan struct{}
}

func (b *blockingCertificateAuthority) IssueCertificate(domain string) (*tls.Certificate, error) {
	close(b.started)
	<-b.release
	return b.CertificateAuthority.IssueCertificate(domain)
}

func newRouteCheckerMock(t *testing.T, domains ...string) *mocks.MockRouteChecker {
	m := mocks.NewMockRouteChecker(t)
	routes := make([]domain.Route, len(domains))
	for i, d := range domains {
		routes[i] = domain.Route{Domain: d}
	}
	m.EXPECT().GetRoutes(mock.Anything).Return(routes).Maybe()
	m.EXPECT().GetExternalRoutes().Return(nil).Maybe()
	return m
}

func TestService_GetCertificate_KnownDomain(t *testing.T) {
	dir := t.TempDir()
	ca, err := pkiadapter.NewCA(dir, testLogger())
	require.NoError(t, err)

	cfg := newRouteCheckerMock(t, "app.example.com")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	svc := pkiusecase.NewService(ctx, ca, cfg, nil, testLogger())
	defer svc.Stop()

	hello := &tls.ClientHelloInfo{ServerName: "app.example.com"}
	cert, err := svc.GetCertificate(hello)
	require.NoError(t, err)
	require.NotNil(t, cert)
}

func TestService_GetCertificate_AdditionalDomain(t *testing.T) {
	dir := t.TempDir()
	ca, err := pkiadapter.NewCA(dir, testLogger())
	require.NoError(t, err)

	cfg := newRouteCheckerMock(t, "app.example.com")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	svc := pkiusecase.NewService(ctx, ca, cfg, []string{"gordon.example.com"}, testLogger())
	defer svc.Stop()

	hello := &tls.ClientHelloInfo{ServerName: "GORDON.EXAMPLE.COM."}
	cert, err := svc.GetCertificate(hello)
	require.NoError(t, err)
	require.NotNil(t, cert)
}

func TestService_SetAdditionalDomains_ReplacesAuthorization(t *testing.T) {
	dir := t.TempDir()
	ca, err := pkiadapter.NewCA(dir, testLogger())
	require.NoError(t, err)

	cfg := newRouteCheckerMock(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	svc := pkiusecase.NewService(ctx, ca, cfg, []string{"old.example.com"}, testLogger())
	defer svc.Stop()

	oldHello := &tls.ClientHelloInfo{ServerName: "old.example.com"}
	cert, err := svc.GetCertificate(oldHello)
	require.NoError(t, err)
	require.NotNil(t, cert)

	svc.SetAdditionalDomains([]string{"NEW.EXAMPLE.COM."})

	cert, err = svc.GetCertificate(oldHello)
	require.NoError(t, err)
	assert.Nil(t, cert)

	cert, err = svc.GetCertificate(&tls.ClientHelloInfo{ServerName: "new.example.com"})
	require.NoError(t, err)
	require.NotNil(t, cert)
}

func TestService_SetAdditionalDomains_RevokesConcurrentIssuance(t *testing.T) {
	dir := t.TempDir()
	ca, err := pkiadapter.NewCA(dir, testLogger())
	require.NoError(t, err)
	blockingCA := &blockingCertificateAuthority{
		CertificateAuthority: ca,
		started:              make(chan struct{}),
		release:              make(chan struct{}),
	}

	cfg := newRouteCheckerMock(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	svc := pkiusecase.NewService(ctx, blockingCA, cfg, []string{"old.example.com"}, testLogger())
	defer svc.Stop()

	result := make(chan *tls.Certificate, 1)
	errs := make(chan error, 1)
	go func() {
		cert, getErr := svc.GetCertificate(&tls.ClientHelloInfo{ServerName: "old.example.com"})
		result <- cert
		errs <- getErr
	}()

	<-blockingCA.started
	svc.SetAdditionalDomains([]string{"new.example.com"})
	close(blockingCA.release)

	require.NoError(t, <-errs)
	assert.Nil(t, <-result)
	assert.Equal(t, 0, svc.CachedCertCount())
}

func TestService_GetCertificate_UnknownDomain(t *testing.T) {
	dir := t.TempDir()
	ca, err := pkiadapter.NewCA(dir, testLogger())
	require.NoError(t, err)

	cfg := newRouteCheckerMock(t, "app.example.com")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	svc := pkiusecase.NewService(ctx, ca, cfg, nil, testLogger())
	defer svc.Stop()

	hello := &tls.ClientHelloInfo{ServerName: "unknown.example.com"}
	cert, err := svc.GetCertificate(hello)
	assert.NoError(t, err)
	assert.Nil(t, cert)
}

func TestService_GetCertificate_Caching(t *testing.T) {
	dir := t.TempDir()
	ca, err := pkiadapter.NewCA(dir, testLogger())
	require.NoError(t, err)

	cfg := newRouteCheckerMock(t, "cached.example.com")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	svc := pkiusecase.NewService(ctx, ca, cfg, nil, testLogger())
	defer svc.Stop()

	hello := &tls.ClientHelloInfo{ServerName: "cached.example.com"}
	cert1, err := svc.GetCertificate(hello)
	require.NoError(t, err)

	cert2, err := svc.GetCertificate(hello)
	require.NoError(t, err)

	// Same pointer = served from cache
	assert.Same(t, cert1, cert2, "second call should return cached cert")
}
