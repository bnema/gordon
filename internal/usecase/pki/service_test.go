package pki_test

import (
	"context"
	"crypto/tls"
	"testing"

	pkiadapter "github.com/bnema/gordon/internal/adapters/out/pki"
	"github.com/bnema/gordon/internal/domain"
	pkiusecase "github.com/bnema/gordon/internal/usecase/pki"
	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubConfigService satisfies pkiusecase.RouteChecker
type stubConfigService struct {
	routes         []domain.Route
	externalRoutes map[string]string
}

func (s *stubConfigService) GetRoutes(_ context.Context) []domain.Route { return s.routes }
func (s *stubConfigService) GetExternalRoutes() map[string]string       { return s.externalRoutes }

func testLogger() zerowrap.Logger { return zerowrap.Default() }

func TestService_GetCertificate_KnownDomain(t *testing.T) {
	dir := t.TempDir()
	ca, err := pkiadapter.NewCA(dir, testLogger())
	require.NoError(t, err)

	cfg := &stubConfigService{
		routes: []domain.Route{{Domain: "app.example.com"}},
	}

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

	cfg := &stubConfigService{
		routes: []domain.Route{{Domain: "app.example.com"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := pkiusecase.NewService(ctx, ca, cfg, testLogger())
	defer svc.Stop()

	hello := &tls.ClientHelloInfo{ServerName: "unknown.example.com"}
	cert, err := svc.GetCertificate(hello)
	assert.Error(t, err)
	assert.Nil(t, cert)
}

func TestService_GetCertificate_Caching(t *testing.T) {
	dir := t.TempDir()
	ca, err := pkiadapter.NewCA(dir, testLogger())
	require.NoError(t, err)

	cfg := &stubConfigService{
		routes: []domain.Route{{Domain: "cached.example.com"}},
	}

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
	assert.Equal(t, cert1, cert2, "second call should return cached cert")
}
