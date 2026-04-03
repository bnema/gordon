package proxy

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/http/middleware"
	"github.com/bnema/gordon/internal/boundaries/in"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestForwardToTarget_H2CEndToEnd(t *testing.T) {
	var h2cProtos http.Protocols
	h2cProtos.SetUnencryptedHTTP2(true)

	h2cBackend := &http.Server{
		Protocols: &h2cProtos,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Proto", r.Proto)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "proto=%s", r.Proto)
		}),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	go func() {
		_ = h2cBackend.Serve(ln)
	}()
	defer h2cBackend.Close()

	_, portStr, splitErr := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, splitErr)
	port, atoiErr := strconv.Atoi(portStr)
	require.NoError(t, atoiErr)

	proxySvc := inmocks.NewMockProxyService(t)

	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{})
	proxySvc.EXPECT().IsRegistryDomain("grpc.example.com").Return(false)
	proxySvc.EXPECT().GetTarget(mock.Anything, "grpc.example.com").Return(&domain.ProxyTarget{
		Host:        "127.0.0.1",
		Port:        port,
		ContainerID: "grpc-1",
		Scheme:      "http",
		Protocol:    "h2c",
	}, nil)
	proxySvc.EXPECT().TrackInFlight("grpc-1").Return(func() {})

	handler := NewHandler(proxySvc, nil, zerowrap.Default())

	req := httptest.NewRequest(http.MethodGet, "http://grpc.example.com/test", nil)
	req.Host = "grpc.example.com"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "HTTP/2")
}

func TestForwardToTarget_HTTP1StillWorks(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "proto=%s", r.Proto)
	}))
	defer backend.Close()

	backendPort := backend.Listener.Addr().(*net.TCPAddr).Port

	proxySvc := inmocks.NewMockProxyService(t)

	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{})
	proxySvc.EXPECT().IsRegistryDomain("web.example.com").Return(false)
	proxySvc.EXPECT().GetTarget(mock.Anything, "web.example.com").Return(&domain.ProxyTarget{
		Host:        "127.0.0.1",
		Port:        backendPort,
		ContainerID: "web-1",
		Scheme:      "http",
	}, nil)
	proxySvc.EXPECT().TrackInFlight("web-1").Return(func() {})

	handler := NewHandler(proxySvc, nil, zerowrap.Default())

	req := httptest.NewRequest(http.MethodGet, "http://web.example.com/", nil)
	req.Host = "web.example.com"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "HTTP/1.1")
}

func TestTransportForTarget_SelectsCorrectTransport(t *testing.T) {
	handler := NewHandler(inmocks.NewMockProxyService(t), nil, zerowrap.Default())

	h2cTarget := &domain.ProxyTarget{Protocol: "h2c"}
	httpTarget := &domain.ProxyTarget{Protocol: ""}

	assert.Equal(t, handler.h2cTransport, handler.transportForTarget(h2cTarget))
	assert.Equal(t, handler.appTransport, handler.transportForTarget(httpTarget))
}

func TestForwardToTarget_OriginalHostPreserved(t *testing.T) {
	var receivedHost string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	backendPort := backend.Listener.Addr().(*net.TCPAddr).Port

	proxySvc := inmocks.NewMockProxyService(t)

	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{})
	proxySvc.EXPECT().IsRegistryDomain("external.example.com").Return(false)
	proxySvc.EXPECT().GetTarget(mock.Anything, "external.example.com").Return(&domain.ProxyTarget{
		Host:         "127.0.0.1",
		Port:         backendPort,
		ContainerID:  "",
		Scheme:       "http",
		OriginalHost: "upstream.example.com",
	}, nil)
	proxySvc.EXPECT().TrackInFlight("").Return(func() {})

	handler := NewHandler(proxySvc, nil, zerowrap.Default())

	req := httptest.NewRequest(http.MethodGet, "http://external.example.com/", nil)
	req.Host = "external.example.com"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, receivedHost, "upstream.example.com")
}

func TestForwardToTarget_BackendDown_Returns503(t *testing.T) {
	proxySvc := inmocks.NewMockProxyService(t)

	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{})
	proxySvc.EXPECT().IsRegistryDomain("down.example.com").Return(false)
	proxySvc.EXPECT().GetTarget(mock.Anything, "down.example.com").Return(&domain.ProxyTarget{
		Host:        "127.0.0.1",
		Port:        1,
		ContainerID: "c-down",
		Scheme:      "http",
	}, nil)
	proxySvc.EXPECT().TrackInFlight("c-down").Return(func() {})

	handler := NewHandler(proxySvc, nil, zerowrap.Default())

	req := httptest.NewRequest(http.MethodGet, "http://down.example.com/", nil)
	req.Host = "down.example.com"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestForwardToTarget_ProxyHeaderSet(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	backendPort := backend.Listener.Addr().(*net.TCPAddr).Port

	proxySvc := inmocks.NewMockProxyService(t)

	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{})
	proxySvc.EXPECT().IsRegistryDomain("app.example.com").Return(false)
	proxySvc.EXPECT().GetTarget(mock.Anything, "app.example.com").Return(&domain.ProxyTarget{
		Host:        "127.0.0.1",
		Port:        backendPort,
		ContainerID: "c-1",
		Scheme:      "http",
	}, nil)
	proxySvc.EXPECT().TrackInFlight("c-1").Return(func() {})

	handler := NewHandler(proxySvc, nil, zerowrap.Default())

	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.Host = "app.example.com"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}
func TestNewReverseProxy_XForwardedProto(t *testing.T) {
	trustedNets := middleware.ParseTrustedProxies([]string{"10.0.0.0/8"})

	tests := []struct {
		name          string
		remoteAddr    string
		incomingProto string // X-Forwarded-Proto on incoming request, empty = not set
		wantProto     string // expected X-Forwarded-Proto at backend
		nets          []*net.IPNet
	}{
		{
			name:          "trusted proxy with https preserved",
			remoteAddr:    "10.0.0.1:12345",
			incomingProto: "https",
			wantProto:     "https",
			nets:          trustedNets,
		},
		{
			name:          "untrusted source proto overwritten",
			remoteAddr:    "1.2.3.4:12345",
			incomingProto: "https",
			wantProto:     "http",
			nets:          trustedNets,
		},
		{
			name:          "trusted proxy without proto set normally",
			remoteAddr:    "10.0.0.1:12345",
			incomingProto: "",
			wantProto:     "http",
			nets:          trustedNets,
		},
		{
			name:          "no trusted nets proto not preserved",
			remoteAddr:    "10.0.0.1:12345",
			incomingProto: "https",
			wantProto:     "http",
			nets:          nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotProto string
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotProto = r.Header.Get("X-Forwarded-Proto")
				w.WriteHeader(http.StatusOK)
			}))
			defer backend.Close()

			backendURL, err := url.Parse(backend.URL)
			require.NoError(t, err)

			// Build incoming request (http scheme, no TLS)
			incomingReq := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
			incomingReq.RemoteAddr = tt.remoteAddr
			if tt.incomingProto != "" {
				incomingReq.Header.Set("X-Forwarded-Proto", tt.incomingProto)
			}

			proxy := newReverseProxy(backendURL, "", http.DefaultTransport, incomingReq, tt.nets,
				func(w http.ResponseWriter, _ *http.Request, err error) {
					http.Error(w, err.Error(), http.StatusBadGateway)
				},
				nil,
			)

			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, incomingReq)

			assert.Equal(t, tt.wantProto, gotProto)
		})
	}
}
