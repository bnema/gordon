package proxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/bnema/gordon/internal/boundaries/in"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func TestHandler_ConcurrentConnectionLimit_503WhenFull(t *testing.T) {
	proxySvc := inmocks.NewMockProxyService(t)

	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{
		MaxConcurrentConns: 1,
	})

	handler := NewHandler(proxySvc, testLogger())

	// Simulate an in-flight connection.
	handler.activeConns.Add(1)
	defer handler.activeConns.Add(-1)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestHandler_RoutesToRegistry(t *testing.T) {
	proxySvc := inmocks.NewMockProxyService(t)

	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{
		RegistryPort: 5000,
	})
	proxySvc.EXPECT().IsRegistryDomain("registry.example.com").Return(true)
	proxySvc.EXPECT().TrackRegistryRequest().Return()
	proxySvc.EXPECT().ReleaseRegistryRequest().Return()

	handler := NewHandler(proxySvc, testLogger())

	req := httptest.NewRequest(http.MethodGet, "http://registry.example.com/v2/", nil)
	req.Host = "registry.example.com"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.True(t, w.Code == http.StatusBadGateway || w.Code == http.StatusServiceUnavailable)
}

func TestHandler_Returns404WhenNoTarget(t *testing.T) {
	proxySvc := inmocks.NewMockProxyService(t)

	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{})
	proxySvc.EXPECT().IsRegistryDomain("unknown.example.com").Return(false)
	proxySvc.EXPECT().GetTarget(mock.Anything, "unknown.example.com").Return(nil, domain.ErrNoTargetAvailable)

	handler := NewHandler(proxySvc, testLogger())

	req := httptest.NewRequest(http.MethodGet, "http://unknown.example.com/", nil)
	req.Host = "unknown.example.com"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandler_ProxiesToTarget(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	proxySvc := inmocks.NewMockProxyService(t)

	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{})
	proxySvc.EXPECT().IsRegistryDomain("app.example.com").Return(false)

	backendPort := backend.Listener.Addr().(*net.TCPAddr).Port

	target := &domain.ProxyTarget{
		Host:        "127.0.0.1",
		Port:        backendPort,
		ContainerID: "c-1",
		Scheme:      "http",
	}
	proxySvc.EXPECT().GetTarget(mock.Anything, "app.example.com").Return(target, nil)
	proxySvc.EXPECT().TrackInFlight("c-1").Return(func() {})

	handler := NewHandler(proxySvc, testLogger())

	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.Host = "app.example.com"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ok", w.Body.String())
}

func TestHandler_MaxBodySize_Applied(t *testing.T) {
	proxySvc := inmocks.NewMockProxyService(t)

	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{
		MaxBodySize: 1024,
	})
	proxySvc.EXPECT().IsRegistryDomain("app.example.com").Return(false)
	proxySvc.EXPECT().GetTarget(mock.Anything, "app.example.com").Return(nil, domain.ErrNoTargetAvailable)

	handler := NewHandler(proxySvc, testLogger())

	req := httptest.NewRequest(http.MethodPost, "http://app.example.com/test", nil)
	req.Host = "app.example.com"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandler_UpdatedConfigReflected(t *testing.T) {
	proxySvc := inmocks.NewMockProxyService(t)

	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{
		MaxConcurrentConns: 0,
	}).Once()
	proxySvc.EXPECT().IsRegistryDomain("app.example.com").Return(false).Once()
	proxySvc.EXPECT().GetTarget(mock.Anything, "app.example.com").Return(nil, domain.ErrNoTargetAvailable).Once()

	handler := NewHandler(proxySvc, testLogger())

	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.Host = "app.example.com"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{
		MaxConcurrentConns: 1,
	}).Once()

	handler.activeConns.Add(1)
	defer handler.activeConns.Add(-1)

	req2 := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req2.Host = "app.example.com"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusServiceUnavailable, rec2.Code)
}

func TestHandler_NoConcurrencyLimitWhenZero(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	proxySvc := inmocks.NewMockProxyService(t)
	backendPort := backend.Listener.Addr().(*net.TCPAddr).Port

	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{
		MaxConcurrentConns: 0,
	})
	proxySvc.EXPECT().IsRegistryDomain("app.example.com").Return(false)
	proxySvc.EXPECT().GetTarget(mock.Anything, "app.example.com").Return(&domain.ProxyTarget{
		Host:        "127.0.0.1",
		Port:        backendPort,
		ContainerID: "c-1",
		Scheme:      "http",
	}, nil)
	proxySvc.EXPECT().TrackInFlight("c-1").Return(func() {})

	handler := NewHandler(proxySvc, testLogger())

	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.Host = "app.example.com"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}
