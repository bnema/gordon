package proxy

import (
	"context"
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

	gordonhttp "github.com/bnema/gordon/internal/adapters/in/http/httphelper"
	"github.com/bnema/gordon/internal/boundaries/in"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

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
	assert.Equal(t, net.JoinHostPort("upstream.example.com", strconv.Itoa(backendPort)), receivedHost)
}

func TestForwardToTarget_RouteHostPreservedForManagedTarget(t *testing.T) {
	var receivedHost string
	var receivedForwardedHost string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHost = r.Host
		receivedForwardedHost = r.Header.Get("X-Forwarded-Host")
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
		RouteHost:   "app.example.com",
	}, nil)
	proxySvc.EXPECT().TrackInFlight("c-1").Return(func() {})

	handler := NewHandler(proxySvc, nil, zerowrap.Default())

	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.Host = "App.Example.com:443"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "app.example.com", receivedHost)
	assert.Equal(t, "app.example.com", receivedForwardedHost)
}

func TestForwardToTarget_ContextCanceled_Returns499(t *testing.T) {
	proxySvc := inmocks.NewMockProxyService(t)

	proxySvc.EXPECT().ProxyConfig().Return(in.ProxyServiceConfig{})
	proxySvc.EXPECT().IsRegistryDomain("app.example.com").Return(false)
	proxySvc.EXPECT().GetTarget(mock.Anything, "app.example.com").Return(&domain.ProxyTarget{
		Host:        "127.0.0.1",
		Port:        8080,
		ContainerID: "c-sse",
		Scheme:      "http",
	}, nil)
	proxySvc.EXPECT().TrackInFlight("c-sse").Return(func() {})

	handler := NewHandler(proxySvc, nil, zerowrap.Default())
	handler.appTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.Canceled
	})

	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/api/notifications/sent-events", nil)
	req.Host = "app.example.com"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, statusClientClosedRequest, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "default-src 'none'; frame-ancestors 'none'", w.Header().Get("Content-Security-Policy"))
	assert.Empty(t, w.Body.String())
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
	trustedNets := gordonhttp.ParseTrustedProxies([]string{"10.0.0.0/8"})

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
		{
			name:          "bare IPv4 no port trusted source preserves proto",
			remoteAddr:    "10.0.0.1",
			incomingProto: "https",
			wantProto:     "https",
			nets:          trustedNets,
		},
		{
			name:          "bare IPv6 with brackets trusted source preserves proto",
			remoteAddr:    "[::1]",
			incomingProto: "https",
			wantProto:     "https",
			nets:          gordonhttp.ParseTrustedProxies([]string{"::1/128"}),
		},
		{
			name:          "bare IPv4 no port untrusted source strips proto",
			remoteAddr:    "1.2.3.4",
			incomingProto: "https",
			wantProto:     "http",
			nets:          trustedNets,
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

			proxy := newReverseProxy(reverseProxyOptions{
				targetURL:   backendURL,
				transport:   http.DefaultTransport,
				incomingReq: incomingReq,
				trustedNets: tt.nets,
				errorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
					http.Error(w, err.Error(), http.StatusBadGateway)
				},
			})

			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, incomingReq)

			assert.Equal(t, tt.wantProto, gotProto)
		})
	}
}

func TestNewReverseProxy_ClientIPHeaders(t *testing.T) {
	trustedNets := gordonhttp.ParseTrustedProxies([]string{"10.0.0.0/8", "fd00::/8", "173.245.48.0/20"})

	tests := []struct {
		name       string
		remoteAddr string
		headers    http.Header
		nets       []*net.IPNet
		wantIP     string // expected X-Real-IP and X-Forwarded-For, empty = absent
		wantCF     string // expected Cf-Connecting-Ip, empty = absent
	}{
		{
			name:       "untrusted IPv4 peer forged headers replaced",
			remoteAddr: "203.0.113.7:4321",
			headers: http.Header{
				"X-Real-IP":        {"1.2.3.4"},
				"X-Forwarded-For":  {"1.2.3.4"},
				"Cf-Connecting-Ip": {"1.2.3.4"}},
			nets:   trustedNets,
			wantIP: "203.0.113.7",
		},
		{
			name:       "untrusted IPv6 peer forged X-Real-IP replaced",
			remoteAddr: "[2001:db8::7]:4321",
			headers:    http.Header{"X-Real-IP": {"1.2.3.4"}},
			nets:       trustedNets,
			wantIP:     "2001:db8::7",
		},
		{
			name:       "untrusted IPv4-mapped IPv6 peer normalized to IPv4",
			remoteAddr: "[::ffff:203.0.113.7]:4321",
			headers:    http.Header{"X-Real-IP": {"1.2.3.4"}},
			nets:       trustedNets,
			wantIP:     "203.0.113.7",
		},
		{
			name:       "no trusted nets ignores proxy headers",
			remoteAddr: "10.0.0.1:4321",
			headers:    http.Header{"X-Forwarded-For": {"198.51.100.9"}},
			nets:       nil,
			wantIP:     "10.0.0.1",
		},
		{
			name:       "trusted Cloudflare peer uses Cf-Connecting-Ip",
			remoteAddr: "173.245.48.10:4321",
			headers: http.Header{
				"Cf-Connecting-Ip": {"198.51.100.9"},
				"X-Real-IP":        {"1.2.3.4"},
				"X-Forwarded-For":  {"1.2.3.4"}},
			nets:   trustedNets,
			wantIP: "198.51.100.9",
			wantCF: "198.51.100.9",
		},
		{
			name:       "trusted Cloudflare peer with IPv6 client",
			remoteAddr: "173.245.48.10:4321",
			headers:    http.Header{"Cf-Connecting-Ip": {"2001:db8::9"}},
			nets:       trustedNets,
			wantIP:     "2001:db8::9",
			wantCF:     "2001:db8::9",
		},
		{
			name:       "trusted non-Cloudflare proxy uses right-most untrusted XFF hop",
			remoteAddr: "10.0.0.1:4321",
			headers: http.Header{
				"X-Forwarded-For":  {"1.2.3.4, 198.51.100.9, 10.0.0.2"},
				"Cf-Connecting-Ip": {"1.2.3.4"}},
			nets:   trustedNets,
			wantIP: "198.51.100.9",
		},
		{
			name:       "trusted IPv6 proxy with IPv4-mapped XFF hop",
			remoteAddr: "[fd00::1]:4321",
			headers:    http.Header{"X-Forwarded-For": {"::ffff:198.51.100.9"}},
			nets:       trustedNets,
			wantIP:     "198.51.100.9",
		},
		{
			name:       "trusted proxy X-Real-IP honored without XFF",
			remoteAddr: "10.0.0.1:4321",
			headers:    http.Header{"X-Real-IP": {"2001:db8::9"}},
			nets:       trustedNets,
			wantIP:     "2001:db8::9",
		},
		{
			name:       "trusted proxy adding a new XFF line uses that line",
			remoteAddr: "10.0.0.1:4321",
			headers:    http.Header{"X-Forwarded-For": {"1.2.3.4", "198.51.100.9"}},
			nets:       trustedNets,
			wantIP:     "198.51.100.9",
		},
		{
			name:       "trusted proxy repeated X-Real-IP ignored",
			remoteAddr: "10.0.0.1:4321",
			headers:    http.Header{"X-Real-IP": {"1.2.3.4", "198.51.100.9"}},
			nets:       trustedNets,
			wantIP:     "10.0.0.1",
		},
		{
			name:       "underscore header variants removed",
			remoteAddr: "203.0.113.7:4321",
			headers: http.Header{
				"X_Real_IP":        {"1.2.3.4"},
				"X_Forwarded_For":  {"1.2.3.4"},
				"Cf_Connecting_Ip": {"1.2.3.4"},
			},
			nets:   trustedNets,
			wantIP: "203.0.113.7",
		},
		{
			name:       "Cloudflare peer not in trusted proxies strips Cf-Connecting-Ip",
			remoteAddr: "173.245.48.10:4321",
			headers:    http.Header{"Cf-Connecting-Ip": {"198.51.100.9"}},
			nets:       gordonhttp.ParseTrustedProxies([]string{"10.0.0.0/8"}),
			wantIP:     "173.245.48.10",
		},
		{
			name:       "IPv4-mapped trusted Cloudflare peer uses Cf-Connecting-Ip",
			remoteAddr: "[::ffff:173.245.48.10]:4321",
			headers:    http.Header{"Cf-Connecting-Ip": {"198.51.100.9"}},
			nets:       trustedNets,
			wantIP:     "198.51.100.9",
			wantCF:     "198.51.100.9",
		},
		{
			name:       "trusted Cloudflare peer with invalid Cf-Connecting-Ip falls back",
			remoteAddr: "173.245.48.10:4321",
			headers: http.Header{
				"Cf-Connecting-Ip": {"not-an-ip"},
				"X-Forwarded-For":  {"198.51.100.9"},
			},
			nets:   trustedNets,
			wantIP: "198.51.100.9",
		},
		{
			name:       "unparsable peer sends no client IP",
			remoteAddr: "@",
			headers:    http.Header{"X-Real-IP": {"1.2.3.4"}, "X-Forwarded-For": {"1.2.3.4"}},
			nets:       trustedNets,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got http.Header
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Clone()
				w.WriteHeader(http.StatusOK)
			}))
			defer backend.Close()

			backendURL, err := url.Parse(backend.URL)
			require.NoError(t, err)

			incomingReq := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
			incomingReq.RemoteAddr = tt.remoteAddr
			for k, v := range tt.headers {
				incomingReq.Header[http.CanonicalHeaderKey(k)] = v
			}

			proxy := newReverseProxy(reverseProxyOptions{
				targetURL:   backendURL,
				transport:   http.DefaultTransport,
				incomingReq: incomingReq,
				trustedNets: tt.nets,
				errorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
					http.Error(w, err.Error(), http.StatusBadGateway)
				},
			})

			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, incomingReq)
			require.Equal(t, http.StatusOK, rec.Code)

			assert.Equal(t, tt.wantIP, got.Get("X-Real-IP"))
			assert.Equal(t, tt.wantIP, got.Get("X-Forwarded-For"))
			assert.Equal(t, tt.wantCF, got.Get("Cf-Connecting-Ip"))
			for key := range got {
				assert.NotContains(t, key, "_", "underscore header %q forwarded", key)
			}
		})
	}
}
