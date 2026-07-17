package app

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
	proxyusecase "github.com/bnema/gordon/internal/usecase/proxy"
)

func TestCreateHTTPHandlers_ProxiedRequestIsAccessLogged(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("proxied"))
	}))
	defer backend.Close()

	proxySvc := proxyusecase.NewService(nil, nil, nil, proxyusecase.Config{})
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
