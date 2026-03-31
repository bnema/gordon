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
	"github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	pkiusecase "github.com/bnema/gordon/internal/usecase/pki"
)

func testLogger() zerowrap.Logger { return zerowrap.Default() }

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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := pkiusecase.NewService(ctx, ca, cfg, testLogger())
	defer svc.Stop()

	hello := &tls.ClientHelloInfo{ServerName: "app.example.com"}
	cert, err := svc.GetCertificate(hello)
	require.NoError(t, err)
	require.NotNil(t, cert)
}

func TestService_GetCertificate_UnknownDomain(t *testing.T) {
	dir := t.TempDir()
	ca, err := pkiadapter.NewCA(dir, testLogger())
	require.NoError(t, err)

	cfg := newRouteCheckerMock(t, "app.example.com")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := pkiusecase.NewService(ctx, ca, cfg, testLogger())
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := pkiusecase.NewService(ctx, ca, cfg, testLogger())
	defer svc.Stop()

	hello := &tls.ClientHelloInfo{ServerName: "cached.example.com"}
	cert1, err := svc.GetCertificate(hello)
	require.NoError(t, err)

	cert2, err := svc.GetCertificate(hello)
	require.NoError(t, err)

	// Same pointer = served from cache
	assert.Same(t, cert1, cert2, "second call should return cached cert")
}
