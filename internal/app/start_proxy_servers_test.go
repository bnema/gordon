package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	trafficadapter "github.com/bnema/gordon/internal/adapters/in/traffic"
	pkiadapter "github.com/bnema/gordon/internal/adapters/out/pki"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	pkiusecase "github.com/bnema/gordon/internal/usecase/pki"
)

func TestStartProxyServers_ConfiguresTrafficManagerHTTPSRoute(t *testing.T) {
	cfg := Config{}
	cfg.Server.Port = freeTCPPort(t)
	cfg.Server.TLSPort = freeTCPPort(t)

	manager := trafficadapter.NewManager()
	defer func() { require.NoError(t, manager.Shutdown(context.Background())) }()
	pkiSvc := newTestPKIService(t)
	errChan := make(chan error, 4)

	httpSrv, httpReady, tlsSrv, tlsReady, err := startProxyServers(
		cfg,
		http.NotFoundHandler(),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secure")) }),
		pkiSvc,
		nil,
		manager,
		errChan,
		zerowrap.Default(),
	)
	require.NoError(t, err)
	defer shutdownHTTPServer(t, httpSrv)
	require.Nil(t, tlsSrv)
	require.Nil(t, tlsReady)
	waitReadyForTest(t, httpReady, errChan)

	configSvc := inmocks.NewMockConfigService(t)
	configSvc.EXPECT().GetRoutes(context.Background()).Return([]domain.Route{{Domain: "app.example.com", HTTPS: true}})
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{})
	require.NoError(t, applyTrafficRuntimeConfig(context.Background(), manager, cfg, configSvc))

	body := httpsGetForTest(t, cfg.Server.TLSPort, "app.example.com")
	require.Contains(t, body, "secure")
}

func TestStartProxyServers_TLSRequiresTrafficManager(t *testing.T) {
	cfg := Config{}
	cfg.Server.Port = freeTCPPort(t)
	cfg.Server.TLSPort = freeTCPPort(t)

	httpSrv, _, tlsSrv, _, err := startProxyServers(
		cfg,
		http.NotFoundHandler(),
		http.NotFoundHandler(),
		newTestPKIService(t),
		nil,
		nil,
		make(chan error, 2),
		zerowrap.Default(),
	)
	if httpSrv != nil {
		shutdownHTTPServer(t, httpSrv)
	}
	if tlsSrv != nil {
		shutdownHTTPServer(t, tlsSrv)
	}
	require.ErrorContains(t, err, "traffic manager is required")
}

func newTestPKIService(t *testing.T) *pkiusecase.Service {
	t.Helper()
	routes := outmocks.NewMockRouteChecker(t)
	routes.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{{Domain: "app.example.com", HTTPS: true}}).Maybe()
	routes.EXPECT().GetExternalRoutes().Return(map[string]string{}).Maybe()
	ca, err := pkiadapter.NewCA(t.TempDir(), zerowrap.Default())
	require.NoError(t, err)
	pkiSvc := pkiusecase.NewService(context.Background(), ca, routes, zerowrap.Default())
	t.Cleanup(pkiSvc.Stop)
	return pkiSvc
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	_, portValue, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portValue)
	require.NoError(t, err)
	return port
}

func waitReadyForTest(t *testing.T, ready <-chan struct{}, errChan <-chan error) {
	t.Helper()
	select {
	case <-ready:
	case err := <-errChan:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("server did not become ready")
	}
}

func shutdownHTTPServer(t *testing.T, srv *http.Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))
}

func httpsGetForTest(t *testing.T, port int, serverName string) string {
	t.Helper()
	transport := &http.Transport{
		TLSClientConfig:   &tls.Config{ServerName: serverName, InsecureSkipVerify: true},
		ForceAttemptHTTP2: true,
	}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	defer transport.CloseIdleConnections()

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("https://127.0.0.1:%d/", port), nil)
	require.NoError(t, err)
	req.Host = serverName
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}
