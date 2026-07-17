package app

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	proxyusecase "github.com/bnema/gordon/internal/usecase/proxy"
)

func TestNewMonolithProxyService_ResolvesManagedRouteFromLocalSnapshot(t *testing.T) {
	ctx := context.Background()
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{{Domain: "app.example.com", Image: "app:v1"}}).Once()
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{}).Once()
	containerSvc.EXPECT().Get(mock.Anything, "app.example.com").Return(&domain.Container{
		ID:     "container-1",
		Image:  "app:v1",
		Status: string(domain.ContainerStatusRunning),
	}, true).Once()
	runtime.EXPECT().GetImageLabels(mock.Anything, "app:v1").Return(map[string]string{domain.LabelProxyPort: "8080"}, nil).Once()
	runtime.EXPECT().GetContainerPort(mock.Anything, "container-1", 8080).Return(18080, nil).Maybe()

	proxySvc := newMonolithProxyService(runtime, containerSvc, configSvc, proxyusecase.Config{})
	target, err := proxySvc.GetTarget(ctx, "app.example.com")

	require.NoError(t, err)
	assert.Equal(t, "app.example.com", target.RouteHost)
	if target.Host == "localhost" {
		assert.Equal(t, 18080, target.Port)
	} else {
		assert.Equal(t, "gordon-target-app-example-com", target.Host)
		assert.Equal(t, 8080, target.Port)
	}
}

func TestNewMonolithProxyServiceWithDrainWaiter_UsesSnapshotAdapter(t *testing.T) {
	service, waiter := newMonolithProxyServiceWithDrainWaiter(nil, nil, nil, proxyusecase.Config{})
	require.NotNil(t, service)
	assert.IsType(t, &proxyusecase.LocalSnapshotDrainWaiter{}, waiter)
}

func TestCreateHTTPHandlers_ProxiedRequestIsAccessLogged(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("proxied"))
	}))
	defer backend.Close()

	proxySvc := proxyusecase.NewSnapshotService(nil, proxyusecase.Config{})
	backendPort := backend.Listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, proxySvc.RegisterTarget(context.Background(), "app.example.com", &domain.ProxyTarget{
		Host:        "127.0.0.1",
		Port:        backendPort,
		ContainerID: "proxy-under-test",
		Scheme:      "http",
		RouteHost:   "app.example.com",
	}))

	accessWriter := &testAccessLogWriter{}
	_, proxyHandler, _ := createHTTPHandlers(&services{proxySvc: proxySvc}, Config{}, zerowrap.Default(), accessWriter)
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/orders?status=open", nil)
	req.Host = "app.example.com"
	req.RemoteAddr = "198.51.100.8:12345"
	rec := httptest.NewRecorder()

	proxyHandler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "proxied", rec.Body.String())
	entries := accessWriter.snapshot()
	require.Len(t, entries, 1)
	assert.Equal(t, "198.51.100.8", entries[0].ClientIP)
	assert.Equal(t, http.MethodGet, entries[0].Method)
	assert.Equal(t, "app.example.com", entries[0].Host)
	assert.Equal(t, "/orders", entries[0].Path)
	assert.Equal(t, "status=open", entries[0].Query)
	assert.Equal(t, http.StatusCreated, entries[0].Status)
	assert.Equal(t, len("proxied"), entries[0].BytesSent)
	assert.NotEmpty(t, entries[0].RequestID)
}
