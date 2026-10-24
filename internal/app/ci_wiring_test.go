package app

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/auth"
	cfgusecase "github.com/bnema/gordon/internal/usecase/config"
	"github.com/bnema/gordon/internal/usecase/container"
	proxyusecase "github.com/bnema/gordon/internal/usecase/proxy"
)

func TestBuildProxyConfig_CarriesCIDomain(t *testing.T) {
	cfg := Config{}
	cfg.Server.GordonDomain = "gordon.example.com"
	cfg.Server.CIDomain = "ci.example.com"
	result, err := buildProxyConfig(cfg, zerowrap.Default())
	require.NoError(t, err)
	assert.Equal(t, "ci.example.com", result.proxyConfig.CIDomain)
	assert.Equal(t, "gordon.example.com", result.proxyConfig.RegistryDomain)
}

func TestManagementHosts_IncludeCIDomain(t *testing.T) {
	cfg := Config{}
	cfg.Server.GordonDomain = "gordon.example.com"
	assert.Equal(t, []string{"gordon.example.com"}, managementHosts(cfg))
	cfg.Server.CIDomain = "ci.example.com"
	assert.Equal(t, []string{"gordon.example.com", "ci.example.com"}, managementHosts(cfg))
}

func TestReloadRuntime_ManagementHostsIncludeCIDomain(t *testing.T) {
	ctx := t.Context()
	_, cfg := readSetImageConfig(t, setImageTOML)
	publicTLS := inmocks.NewMockPublicTLSService(t)
	publicTLS.EXPECT().SetAdditionalHosts(mock.Anything, []string{"gordon.example", "ci.example"}).Once()
	v, svc := newTestServicesWithContainer(t, publicTLS)
	require.NoError(t, (&reloadRuntime{v: v, svc: svc, log: zerowrap.Default()}).Apply(ctx, cfg))
}

func TestInstallationImagePolicy_AliasesCIDomain(t *testing.T) {
	cfg := Config{}
	cfg.Server.CIDomain = "ci.example.com"
	policy := installationImagePolicy(cfg, "gordon.example.com")
	assert.True(t, policy.IsInstallationImage("ci.example.com/web@sha256:"+zeros64))
	assert.True(t, policy.IsInstallationImage("gordon.example.com/web@sha256:"+zeros64))
}

const zeros64 = "0000000000000000000000000000000000000000000000000000000000000000"

func tlsState() *tls.ConnectionState { return &tls.ConnectionState{} }

// newTestServicesWithContainer returns reload-ready services whose public
// TLS service records the management hosts.
func newTestServicesWithContainer(t *testing.T, publicTLS *inmocks.MockPublicTLSService) (*viper.Viper, *services) {
	t.Helper()
	v := viper.New()
	v.Set("server.gordon_domain", "gordon.example")
	v.Set("server.registry_port", 5000)
	configSvc := cfgusecase.NewService(v, nil)
	require.NoError(t, configSvc.Load(t.Context()))
	return v, newTestServices(&services{
		configSvc:       configSvc,
		containerSvc:    container.NewService(nil, nil, nil, container.Config{}),
		internalRegUser: "gordon",
		internalRegPass: "secret",
		publicTLSSvc:    publicTLS,
	})
}

// ciEdgeServices builds services with a real auth service and the CI
// collaborators the edge wires.
func ciEdgeServices(t *testing.T, cfg Config) *services {
	t.Helper()
	log := zerowrap.Default()
	authSvc := auth.NewService(auth.Config{Enabled: true, AuthType: domain.AuthTypeToken, TokenSecret: []byte("0123456789abcdef0123456789abcdef")},
		outmocks.NewMockTokenStore(t), log)
	configSvc := inmocks.NewMockConfigService(t)
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{}).Maybe()
	proxyCfg, err := buildProxyConfig(cfg, log)
	require.NoError(t, err)
	svc := &services{
		authSvc:             authSvc,
		proxySvc:            proxyusecase.NewService(configSvc, proxyCfg.proxyConfig),
		workloadIdentitySvc: inmocks.NewMockWorkloadIdentityService(t),
		appSvc:              inmocks.NewMockAppService(t),
	}
	snapshot, err := buildSetImageSnapshot(cfg)
	require.NoError(t, err)
	svc.setImageSnapshot.Store(&snapshot)
	return svc
}

func TestCreateHTTPHandlers_CIHostServedByCIRouter(t *testing.T) {
	_, cfg := readSetImageConfig(t, setImageTOML)
	cfg.API.RateLimit.TrustedProxies = []string{"10.0.0.5"}
	svc := ciEdgeServices(t, cfg)
	registryOut, httpOut, httpsOut := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	// HTTPS edge: the CI host challenges for its own token endpoint.
	req := httptest.NewRequest(http.MethodGet, "https://ci.example/v2/", nil)
	req.TLS = tlsState()
	rec := httptest.NewRecorder()
	httpsOut.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, `Bearer realm="https://ci.example/auth/token",service="ci.example"`, rec.Header().Get("WWW-Authenticate"))

	// The CI host never serves admin or registry reads.
	for _, path := range []string{"/admin/status", "/v2/_catalog", "/v2/web/tags/list", "/.well-known/gordon/ca.crt"} {
		req := httptest.NewRequest(http.MethodGet, "https://ci.example"+path, nil)
		req.TLS = tlsState()
		rec := httptest.NewRecorder()
		httpsOut.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code, path)
	}

	// HTTP edge behind the trusted proxy with X-Forwarded-Proto: https.
	req = httptest.NewRequest(http.MethodGet, "http://ci.example/v2/", nil)
	req.RemoteAddr = net.JoinHostPort("10.0.0.5", "4000")
	req.Header.Set("X-Forwarded-Proto", "https")
	rec = httptest.NewRecorder()
	httpOut.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Header().Get("WWW-Authenticate"), `realm="https://ci.example/auth/token"`)

	// Plain HTTP from the trusted proxy without https: 404.
	req = httptest.NewRequest(http.MethodGet, "http://ci.example/v2/", nil)
	req.RemoteAddr = net.JoinHostPort("10.0.0.5", "4000")
	rec = httptest.NewRecorder()
	httpOut.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// The registry listener does not know the CI router: a ci-access token
	// is refused there.
	principal := domain.WorkloadPrincipal{Issuer: "https://issuer.example", Subject: "a"}
	token, err := svc.authSvc.GenerateCIAccessToken(context.Background(), principal, []string{"repository:web:push,pull"}, time.Minute)
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	registryOut.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestCreateHTTPHandlers_CIRouterAbsentWithoutCIDomain(t *testing.T) {
	_, cfg := readSetImageConfig(t, setImageTOML)
	cfg.Server.CIDomain = ""
	svc := ciEdgeServices(t, cfg)
	_, _, httpsOut := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	req := httptest.NewRequest(http.MethodGet, "https://ci.example/v2/", nil)
	req.TLS = tlsState()
	rec := httptest.NewRecorder()
	httpsOut.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Empty(t, rec.Header().Get("WWW-Authenticate"))
}

func TestCreateHTTPHandlers_CITokenEndpointRateLimited(t *testing.T) {
	_, cfg := readSetImageConfig(t, setImageTOML)
	svc := ciEdgeServices(t, cfg)
	identity := svc.workloadIdentitySvc.(*inmocks.MockWorkloadIdentityService)
	identity.EXPECT().Verify(mock.Anything, mock.Anything).Return(domain.WorkloadPrincipal{}, domain.ErrInvalidToken)
	_, _, httpsOut := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	limited := 0
	for range 30 {
		req := httptest.NewRequest(http.MethodGet, "https://ci.example/auth/token", nil)
		req.TLS = tlsState()
		req.RemoteAddr = "198.51.100.9:5000"
		req.SetBasicAuth("oidc", "forged")
		rec := httptest.NewRecorder()
		httpsOut.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			limited++
			var body dto.RegistryErrorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		}
	}
	assert.Positive(t, limited, "the default auth limiter (burst 10 per IP) caps token requests")
}
