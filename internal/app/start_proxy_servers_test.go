package app

import (
	"context"
	"crypto/tls"
	"errors"
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
	traffic "github.com/bnema/gordon/internal/usecase/traffic"
)

func TestWaitForServerReady_NilReadyReturnsImmediately(t *testing.T) {
	errChan := make(chan error)
	done := make(chan error, 1)

	go func() {
		done <- waitForServerReady(nil, errChan)
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("nil ready channel blocked startup readiness")
	}
}

func TestWaitForServerReady_NonNilReadyPreservesErrorBehavior(t *testing.T) {
	expected := errors.New("listen failed")
	errChan := make(chan error, 1)
	errChan <- expected

	err := waitForServerReady(make(chan struct{}), errChan)
	require.ErrorIs(t, err, expected)
}

func TestStartProxyServers_DoesNotStartLegacyHTTPListenerFromServerPort(t *testing.T) {
	cfg := Config{}
	cfg.Server.Port = freeTCPPort(t)

	manager := trafficadapter.NewManager()
	defer func() { require.NoError(t, manager.Shutdown(context.Background())) }()

	httpSrv, httpReady, tlsSrv, tlsReady, err := startProxyServers(
		cfg,
		http.NotFoundHandler(),
		http.NotFoundHandler(),
		newTestPKIService(t),
		nil,
		manager,
		zerowrap.Default(),
	)
	require.NoError(t, err)
	require.Nil(t, httpSrv)
	require.Nil(t, httpReady)
	require.Nil(t, tlsSrv)
	require.Nil(t, tlsReady)
	assertTCPPortClosed(t, fmt.Sprintf("127.0.0.1:%d", cfg.Server.Port))
}

func TestStartProxyServers_DoesNotTreatLegacyTLSPortAsTLSCapable(t *testing.T) {
	cfg := Config{}
	cfg.Server.TLSPort = freeTCPPort(t)

	manager := trafficadapter.NewManager()
	defer func() { require.NoError(t, manager.Shutdown(context.Background())) }()

	httpSrv, httpReady, tlsSrv, tlsReady, err := startProxyServers(
		cfg,
		http.NotFoundHandler(),
		http.NotFoundHandler(),
		newTestPKIService(t),
		nil,
		manager,
		zerowrap.Default(),
	)
	require.NoError(t, err)
	require.Nil(t, httpSrv)
	require.Nil(t, httpReady)
	require.Nil(t, tlsSrv)
	require.Nil(t, tlsReady)
	assertTCPPortClosed(t, fmt.Sprintf("127.0.0.1:%d", cfg.Server.TLSPort))
}

func TestStartProxyServers_ConfiguresSmartTCPHTTPAndHTTPSRoutesWithoutPublicHTTPListener(t *testing.T) {
	cfg := Config{}
	cfg.Server.Port = freeTCPPort(t)
	cfg.EntryPoints = map[string]traffic.EntryPointConfig{
		traffic.DefaultEdgeEntryPointName: {
			Address:  freeTCPAddress(t),
			Protocol: domain.EntryPointProtocolSmartTCP,
		},
	}

	manager := trafficadapter.NewManager()
	defer func() { require.NoError(t, manager.Shutdown(context.Background())) }()
	pkiSvc := newTestPKIService(t)

	httpSrv, httpReady, tlsSrv, tlsReady, err := startProxyServers(
		cfg,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("cleartext " + r.Proto)) }),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secure")) }),
		pkiSvc,
		nil,
		manager,
		zerowrap.Default(),
	)
	require.NoError(t, err)
	require.Nil(t, httpSrv)
	require.Nil(t, httpReady)
	require.Nil(t, tlsSrv)
	require.Nil(t, tlsReady)
	assertTCPPortClosed(t, fmt.Sprintf("127.0.0.1:%d", cfg.Server.Port))

	configSvc := inmocks.NewMockConfigService(t)
	configSvc.EXPECT().GetRoutes(context.Background()).Return([]domain.Route{{Domain: "app.example.com", HTTPS: true}})
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{})
	require.NoError(t, applyTrafficRuntimeConfig(context.Background(), manager, cfg, configSvc))

	_, smartPortText, err := net.SplitHostPort(cfg.EntryPoints[traffic.DefaultEdgeEntryPointName].Address)
	require.NoError(t, err)
	smartPort, err := strconv.Atoi(smartPortText)
	require.NoError(t, err)
	require.Contains(t, httpGetForTest(t, smartPort, "app.example.com"), "cleartext HTTP/1.1")
	require.Contains(t, httpsGetForTest(t, smartPort, "app.example.com"), "secure")
}

func TestStartProxyServers_ConfiguresTrafficManagerHTTPSRoute(t *testing.T) {
	cfg := Config{}
	cfg.EntryPoints = map[string]traffic.EntryPointConfig{
		traffic.DefaultEdgeEntryPointName: {
			Address:  freeTCPAddress(t),
			Protocol: domain.EntryPointProtocolTLSMux,
		},
	}

	manager := trafficadapter.NewManager()
	defer func() { require.NoError(t, manager.Shutdown(context.Background())) }()
	pkiSvc := newTestPKIService(t)

	httpSrv, httpReady, tlsSrv, tlsReady, err := startProxyServers(
		cfg,
		http.NotFoundHandler(),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secure")) }),
		pkiSvc,
		nil,
		manager,
		zerowrap.Default(),
	)
	require.NoError(t, err)
	require.Nil(t, httpSrv)
	require.Nil(t, httpReady)
	require.Nil(t, tlsSrv)
	require.Nil(t, tlsReady)

	configSvc := inmocks.NewMockConfigService(t)
	configSvc.EXPECT().GetRoutes(context.Background()).Return([]domain.Route{{Domain: "app.example.com", HTTPS: true}})
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{})
	require.NoError(t, applyTrafficRuntimeConfig(context.Background(), manager, cfg, configSvc))

	_, portText, err := net.SplitHostPort(cfg.EntryPoints[traffic.DefaultEdgeEntryPointName].Address)
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	body := httpsGetForTest(t, port, "app.example.com")
	require.Contains(t, body, "secure")
}

func TestStartProxyServers_ConfiguresCustomTLSMuxHTTPSRoute(t *testing.T) {
	cfg := Config{}
	cfg.Server.Port = freeTCPPort(t)
	cfg.Server.TLSPort = freeTCPPort(t)
	cfg.EntryPoints = map[string]traffic.EntryPointConfig{
		"custom-secure": {Address: fmt.Sprintf("127.0.0.1:%d", freeTCPPort(t)), Protocol: domain.EntryPointProtocolTLSMux},
	}
	cfg.Traffic.TLS.Routers = []traffic.RouterConfig{{Name: "raw", EntryPoint: "custom-secure", SNI: "raw.example.com", Service: "network_service:raw:tls"}}
	cfg.NetworkServices = []traffic.NetworkServiceConfig{{Name: "raw", Ports: []traffic.PortConfig{{Name: "tls", Container: 443, Protocol: domain.NetworkProtocolTCP}}}}

	manager := trafficadapter.NewManager()
	defer func() { require.NoError(t, manager.Shutdown(context.Background())) }()
	pkiSvc := newTestPKIService(t)

	httpSrv, httpReady, tlsSrv, tlsReady, err := startProxyServers(
		cfg,
		http.NotFoundHandler(),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("custom secure")) }),
		pkiSvc,
		nil,
		manager,
		zerowrap.Default(),
	)
	require.NoError(t, err)
	require.Nil(t, httpSrv)
	require.Nil(t, httpReady)
	require.Nil(t, tlsSrv)
	require.Nil(t, tlsReady)

	configSvc := inmocks.NewMockConfigService(t)
	configSvc.EXPECT().GetRoutes(context.Background()).Return(nil)
	configSvc.EXPECT().GetExternalRoutes().Return(nil)
	require.NoError(t, applyTrafficRuntimeConfig(context.Background(), manager, cfg, configSvc))

	_, customPort, err := net.SplitHostPort(cfg.EntryPoints["custom-secure"].Address)
	require.NoError(t, err)
	port, err := strconv.Atoi(customPort)
	require.NoError(t, err)
	body := httpsGetForTest(t, port, "app.example.com")
	require.Contains(t, body, "custom secure")
}

func TestStartProxyServers_TLSSetupFailureStopsHTTPServer(t *testing.T) {
	cfg := Config{}
	cfg.EntryPoints = map[string]traffic.EntryPointConfig{
		traffic.DefaultEdgeEntryPointName: {Address: freeTCPAddress(t), Protocol: domain.EntryPointProtocolSmartTCP},
	}
	cfg.Server.TLSCertFile = "/path/that/does/not/exist.crt"
	cfg.Server.TLSKeyFile = "/path/that/does/not/exist.key"

	httpSrv, httpReady, tlsSrv, tlsReady, err := startProxyServers(
		cfg,
		http.NotFoundHandler(),
		http.NotFoundHandler(),
		newTestPKIService(t),
		nil,
		trafficadapter.NewManager(),
		zerowrap.Default(),
	)
	require.ErrorContains(t, err, "load TLS keypair")
	require.Nil(t, tlsSrv)
	require.Nil(t, tlsReady)
	if httpReady != nil {
		select {
		case <-httpReady:
		case <-time.After(time.Second):
		}
	}
	if httpSrv != nil {
		_, err = net.DialTimeout("tcp", httpSrv.Addr, 100*time.Millisecond)
		require.Error(t, err)
	}
}

func TestStartProxyServers_TLSRequiresTrafficManager(t *testing.T) {
	cfg := Config{}
	cfg.EntryPoints = map[string]traffic.EntryPointConfig{
		traffic.DefaultEdgeEntryPointName: {Address: freeTCPAddress(t), Protocol: domain.EntryPointProtocolSmartTCP},
	}

	httpSrv, _, tlsSrv, _, err := startProxyServers(
		cfg,
		http.NotFoundHandler(),
		http.NotFoundHandler(),
		newTestPKIService(t),
		nil,
		nil,
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

func shutdownHTTPServer(t *testing.T, srv *http.Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))
}

func httpGetForTest(t *testing.T, port int, host string) string {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
	require.NoError(t, err)
	req.Host = host
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

func assertTCPPortClosed(t *testing.T, address string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("expected %s to be closed", address)
	}
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
