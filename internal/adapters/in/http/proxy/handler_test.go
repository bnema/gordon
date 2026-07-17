package proxy

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

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

	handler := NewHandler(proxySvc, nil, testLogger())

	// Simulate an in-flight connection.
	handler.activeConns.Add(1)
	defer handler.activeConns.Add(-1)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestHandler_RoutesToRegistrySnapshotTarget(t *testing.T) {
	var upstreamPort string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/", r.URL.Path)
		assert.Equal(t, "registry.internal:"+upstreamPort, r.Host)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	upstreamPort = upstreamURL.Port()
	registryPort, err := strconv.Atoi(upstreamPort)
	require.NoError(t, err)

	proxySvc := inmocks.NewMockProxyService(t)
	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{})
	proxySvc.EXPECT().GetTarget(mock.Anything, "registry.example.com").Return(&domain.ProxyTarget{
		Host: "127.0.0.1", Port: registryPort, Scheme: "http", OriginalHost: "registry.internal", Registry: true,
	}, nil)
	proxySvc.EXPECT().TrackRegistryRequest().Return()
	proxySvc.EXPECT().ReleaseRegistryRequest().Return()

	handler := NewHandler(proxySvc, nil, testLogger())
	req := httptest.NewRequest(http.MethodGet, "http://registry.example.com/v2/", nil)
	req.Host = "registry.example.com"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestHandler_NormalizesRequestHostForLookup(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		wantHost string
	}{
		{name: "mixed case", host: "App.Example.com", wantHost: "app.example.com"},
		{name: "explicit port", host: "App.Example.com:8080", wantHost: "app.example.com"},
		{name: "trailing dot", host: "App.Example.com.", wantHost: "app.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxySvc := inmocks.NewMockProxyService(t)
			proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{})
			proxySvc.EXPECT().GetTarget(mock.Anything, tt.wantHost).Return(nil, domain.ErrNoTargetAvailable)

			handler := NewHandler(proxySvc, nil, testLogger())
			srv := httptest.NewServer(handler)
			defer srv.Close()
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
			require.NoError(t, err)
			req.Host = tt.host
			resp, err := srv.Client().Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		})
	}
}

func TestHandler_RejectsInvalidRequestHost(t *testing.T) {
	proxySvc := inmocks.NewMockProxyService(t)
	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{})

	handler := NewHandler(proxySvc, nil, testLogger())
	srv := httptest.NewServer(handler)
	defer srv.Close()
	for _, host := range []string{"app.example.com:bad", "app.example.com:0", "app.example.com:99999"} {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
		require.NoError(t, err)
		req.Host = host
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	}
}

func TestHandler_Returns404WhenNoTarget(t *testing.T) {
	proxySvc := inmocks.NewMockProxyService(t)

	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{})
	proxySvc.EXPECT().GetTarget(mock.Anything, "unknown.example.com").Return(nil, domain.ErrNoTargetAvailable)

	handler := NewHandler(proxySvc, nil, testLogger())

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

	backendPort := backend.Listener.Addr().(*net.TCPAddr).Port

	target := &domain.ProxyTarget{
		Host:        "127.0.0.1",
		Port:        backendPort,
		ContainerID: "c-1",
		Scheme:      "http",
	}
	proxySvc.EXPECT().GetTarget(mock.Anything, "app.example.com").Return(target, nil)
	proxySvc.EXPECT().TrackInFlight("c-1").Return(func() {})

	handler := NewHandler(proxySvc, nil, testLogger())

	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.Host = "app.example.com"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ok", w.Body.String())
}

func TestHandler_ReadsMaxBodySizeConfig(t *testing.T) {
	readResult := make(chan struct {
		bytesRead int
		err       error
	}, 1)
	// Backend that reads the full request body, triggering MaxBytesReader.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		readResult <- struct {
			bytesRead int
			err       error
		}{bytesRead: len(body), err: err}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	proxySvc := inmocks.NewMockProxyService(t)
	backendPort := backend.Listener.Addr().(*net.TCPAddr).Port

	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{
		MaxBodySize: 1024,
	})
	proxySvc.EXPECT().GetTarget(mock.Anything, "app.example.com").Return(&domain.ProxyTarget{
		Host:        "127.0.0.1",
		Port:        backendPort,
		ContainerID: "c-1",
		Scheme:      "http",
	}, nil)
	proxySvc.EXPECT().TrackInFlight("c-1").Return(func() {})

	handler := NewHandler(proxySvc, nil, testLogger())

	// Send a body larger than the 1024-byte limit.
	largeBody := bytes.NewReader(make([]byte, 2048))
	req := httptest.NewRequest(http.MethodPost, "http://app.example.com/test", largeBody)
	req.Host = "app.example.com"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	var result struct {
		bytesRead int
		err       error
	}
	select {
	case result = <-readResult:
	case <-time.After(time.Second):
		t.Fatal("backend did not report request body read")
	}
	assert.Equal(t, 1024, result.bytesRead)
	assert.Error(t, result.err)
}

func TestHandler_UpdatedConfigReflected(t *testing.T) {
	proxySvc := inmocks.NewMockProxyService(t)

	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{
		MaxConcurrentConns: 0,
	}).Once()
	proxySvc.EXPECT().GetTarget(mock.Anything, "app.example.com").Return(nil, domain.ErrNoTargetAvailable).Once()

	handler := NewHandler(proxySvc, nil, testLogger())

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
	proxySvc.EXPECT().GetTarget(mock.Anything, "app.example.com").Return(&domain.ProxyTarget{
		Host:        "127.0.0.1",
		Port:        backendPort,
		ContainerID: "c-1",
		Scheme:      "http",
	}, nil)
	proxySvc.EXPECT().TrackInFlight("c-1").Return(func() {})

	handler := NewHandler(proxySvc, nil, testLogger())

	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.Host = "app.example.com"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}
