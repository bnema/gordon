package traffic

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestClientHelloTooLargeUsesDomainSentinel(t *testing.T) {
	client, server := net.Pipe()
	go func() {
		_, _ = client.Write(clientHelloBytes(t, "large.example.com"))
		_ = client.Close()
	}()
	_, _, err := peekClientHelloSNIWithLimit(server, 1)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrClientHelloTooLarge))
	_ = server.Close()
}

func TestClientHelloPeek(t *testing.T) {
	t.Run("valid sni returns replayable bytes", func(t *testing.T) {
		sni, replayed := peekHelloFromTLSClient(t, "raw.example.com")
		assert.Equal(t, "raw.example.com", sni)
		buf := make([]byte, 5)
		n, err := replayed.Read(buf)
		require.NoError(t, err)
		assert.Equal(t, 5, n)
		assert.Equal(t, byte(22), buf[0])
	})

	t.Run("no sni", func(t *testing.T) {
		sni, _ := peekHelloFromTLSClient(t, "")
		assert.Empty(t, sni)
	})

	t.Run("malformed", func(t *testing.T) {
		client, server := net.Pipe()
		go func() {
			_, _ = client.Write([]byte("not tls"))
			_ = client.Close()
		}()
		_, _, err := peekClientHelloSNI(server)
		require.Error(t, err)
	})

	t.Run("fragmented", func(t *testing.T) {
		hello := clientHelloBytes(t, "fragmented.example.com")
		client, server := net.Pipe()
		go func() {
			for _, b := range hello {
				_, _ = client.Write([]byte{b})
			}
			_ = client.Close()
		}()
		sni, replayed, err := peekClientHelloSNI(server)
		require.NoError(t, err)
		assert.Equal(t, "fragmented.example.com", sni)
		buf := make([]byte, len(hello))
		_, err = io.ReadFull(replayed, buf)
		require.NoError(t, err)
		assert.Equal(t, hello, buf)
	})

	t.Run("client_hello_across_multiple_tls_records", func(t *testing.T) {
		hello := splitClientHelloAcrossRecords(t, clientHelloBytes(t, "multi-record.example.com"), 10)
		client, server := net.Pipe()
		go func() {
			_, _ = client.Write(hello)
			_ = client.Close()
		}()
		sni, replayed, err := peekClientHelloSNI(server)
		require.NoError(t, err)
		assert.Equal(t, "multi-record.example.com", sni)
		buf := make([]byte, len(hello))
		_, err = io.ReadFull(replayed, buf)
		require.NoError(t, err)
		assert.Equal(t, hello, buf)
	})

	t.Run("client_hello_header_across_multiple_tls_records", func(t *testing.T) {
		hello := splitClientHelloAcrossRecords(t, clientHelloBytes(t, "split-header.example.com"), 2)
		client, server := net.Pipe()
		go func() {
			_, _ = client.Write(hello)
			_ = client.Close()
		}()
		sni, replayed, err := peekClientHelloSNI(server)
		require.NoError(t, err)
		assert.Equal(t, "split-header.example.com", sni)
		buf := make([]byte, len(hello))
		_, err = io.ReadFull(replayed, buf)
		require.NoError(t, err)
		assert.Equal(t, hello, buf)
	})
}

func splitClientHelloAcrossRecords(t *testing.T, hello []byte, firstPayloadLen int) []byte {
	t.Helper()
	require.GreaterOrEqual(t, len(hello), 15)
	require.Equal(t, byte(22), hello[0])
	recordLen := int(hello[3])<<8 | int(hello[4])
	require.Greater(t, recordLen, firstPayloadLen)
	first := append([]byte{}, hello[:5]...)
	first[3] = byte(firstPayloadLen >> 8)
	first[4] = byte(firstPayloadLen)
	first = append(first, hello[5:5+firstPayloadLen]...)
	remainingPayloadLen := recordLen - firstPayloadLen
	second := append([]byte{22, hello[1], hello[2], byte(remainingPayloadLen >> 8), byte(remainingPayloadLen)}, hello[5+firstPayloadLen:5+recordLen]...)
	out := append(first, second...)
	out = append(out, hello[5+recordLen:]...)
	return out
}

func TestTLSPassthrough(t *testing.T) {
	backendA := startTLSGreetingServer(t, "backend-a", "backend-a\n")
	backendB := startTLSGreetingServer(t, "backend-b", "backend-b\n")
	backendExact := startTLSGreetingServer(t, "backend-exact", "backend-exact\n")

	graph := tlsGraph(t, freeTCPAddress(t), []tlsRoute{
		{name: "raw", sni: "raw.example.com", service: "raw", backend: backendA.address},
		{name: "wild", sni: "*.example.com", service: "wild", backend: backendB.address},
		{name: "exact", sni: "api.example.com", service: "exact", backend: backendExact.address},
	})
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	assertTLSGreeting(t, graph.EntryPoints[0].Address, "raw.example.com", "backend-a\n")
	assertTLSGreeting(t, graph.EntryPoints[0].Address, "shop.example.com", "backend-b\n")
	assertTLSGreeting(t, graph.EntryPoints[0].Address, "api.example.com", "backend-exact\n")
}

func TestTLSMuxUnknownClosesWithoutHTTPSRoute(t *testing.T) {
	backend := startTLSGreetingServer(t, "backend", "backend\n")
	graph := tlsGraph(t, freeTCPAddress(t), []tlsRoute{{name: "raw", sni: "raw.example.com", service: "raw", backend: backend.address}})
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	_, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", graph.EntryPoints[0].Address, &tls.Config{ServerName: "unknown.example.com", InsecureSkipVerify: true})
	require.Error(t, err)
	assert.Eventually(t, func() bool { return manager.Status().Counters.TotalRefused == 1 }, time.Second, 10*time.Millisecond)
}

func TestTLSMuxIncompleteClientHelloDoesNotHangShutdown(t *testing.T) {
	graph := domain.TrafficGraph{
		Options:     domain.TrafficOptions{TCP: domain.TCPOptions{DialTimeout: time.Second, IdleTimeout: time.Minute, DrainTimeout: 50 * time.Millisecond}},
		EntryPoints: []domain.EntryPoint{{Name: "websecure", Address: freeTCPAddress(t), Protocol: domain.EntryPointProtocolTLSMux}},
	}
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))

	conn := dialTCP(t, graph.EntryPoints[0].Address)
	_, err := conn.Write([]byte{22})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	started := time.Now()
	require.NoError(t, manager.Shutdown(ctx))
	assert.Less(t, time.Since(started), 250*time.Millisecond)
	_ = conn.Close()
}

func TestTLSMuxHTTPServerRefreshesActiveRuntime(t *testing.T) {
	graph := tlsGraph(t, freeTCPAddress(t), nil)
	manager := NewManager()
	manager.SetTLSHTTPServer("websecure", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("before"))
	}), testTLSConfig(t, "app.example.com"))
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)
	assertHTTPSBody(t, graph.EntryPoints[0].Address, "app.example.com", "before")

	manager.SetTLSHTTPServer("websecure", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("after"))
	}), testTLSConfig(t, "app.example.com"))

	assertHTTPSBody(t, graph.EntryPoints[0].Address, "app.example.com", "after")
}

func TestTLSHTTPListenerServeRejectsAfterClose(t *testing.T) {
	for range 1000 {
		addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:443")
		require.NoError(t, err)
		listener := newTLSHTTPListener(addr)
		require.NoError(t, listener.Close())
		client, server := net.Pipe()
		accepted := listener.serve(server)
		_ = client.Close()
		_ = server.Close()
		require.False(t, accepted)
	}
}

func TestTLSMuxHTTPSRoute(t *testing.T) {
	backend := startTLSGreetingServer(t, "raw-backend", "raw\n")
	graph := tlsGraph(t, freeTCPAddress(t), []tlsRoute{{name: "raw", sni: "raw.example.com", service: "raw", backend: backend.address}})
	manager := NewManager()
	manager.SetTLSHTTPServer("websecure", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("https route"))
	}), testTLSConfig(t, "app.example.com"))
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	assertTLSGreeting(t, graph.EntryPoints[0].Address, "raw.example.com", "raw\n")
	assertHTTPSBody(t, graph.EntryPoints[0].Address, "app.example.com", "https route")
	assertHTTPSBodyNoSNI(t, graph.EntryPoints[0].Address, "https route")

	conn := dialTCP(t, graph.EntryPoints[0].Address)
	_, err := conn.Write([]byte("malformed"))
	require.NoError(t, err)
	_ = conn.Close()
	assert.Eventually(t, func() bool { return manager.Status().Counters.TotalErrors >= 1 }, time.Second, 10*time.Millisecond)
}

func TestTLSMuxHTTP2Route(t *testing.T) {
	graph := tlsGraph(t, freeTCPAddress(t), nil)
	manager := NewManager()
	tlsConfig := testTLSConfig(t, "app.example.com")
	tlsConfig.NextProtos = []string{"h2", "http/1.1"}
	manager.SetTLSHTTPServer("websecure", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Proto))
	}), tlsConfig)
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	assertHTTPSBody(t, graph.EntryPoints[0].Address, "app.example.com", "HTTP/2.0")
}

func peekHelloFromTLSClient(t *testing.T, serverName string) (string, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = tls.Client(client, &tls.Config{ServerName: serverName, InsecureSkipVerify: true}).Handshake()
		_ = client.Close()
	}()
	sni, replayed, err := peekClientHelloSNI(server)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = replayed.Close()
		<-done
	})
	return sni, replayed
}

func clientHelloBytes(t *testing.T, serverName string) []byte {
	t.Helper()
	_, replayed := peekHelloFromTLSClient(t, serverName)
	buf := make([]byte, maxClientHelloBytes)
	n, err := replayed.Read(buf)
	require.NoError(t, err)
	return append([]byte{}, buf[:n]...)
}

type tlsRoute struct {
	name    string
	sni     string
	service string
	backend string
}

func tlsGraph(t *testing.T, listenAddress string, routes []tlsRoute) domain.TrafficGraph {
	t.Helper()
	graph := domain.TrafficGraph{
		Options:     domain.TrafficOptions{TCP: domain.TCPOptions{DialTimeout: time.Second, IdleTimeout: time.Minute, DrainTimeout: time.Second}},
		EntryPoints: []domain.EntryPoint{{Name: "websecure", Address: listenAddress, Protocol: domain.EntryPointProtocolTLSMux}},
	}
	for _, route := range routes {
		backend, err := backendFromAddress(route.service+":tls", route.backend)
		require.NoError(t, err)
		ref := serviceRef(route.service, "tls")
		graph.Routers = append(graph.Routers, domain.TrafficRouter{Name: route.name, EntryPoint: "websecure", Protocol: domain.RouterProtocolTLSPassthrough, Rule: domain.TrafficRule{SNI: route.sni}, Service: ref})
		graph.Services = append(graph.Services, domain.TrafficService{Name: ref, Backends: []domain.TrafficBackend{backend}})
	}
	require.NoError(t, graph.Validate())
	return graph
}

func assertTLSGreeting(t *testing.T, address string, serverName string, want string) {
	t.Helper()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, &tls.Config{ServerName: serverName, InsecureSkipVerify: true})
	require.NoError(t, err)
	defer conn.Close()
	line, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, want, line)
}

func assertHTTPSBody(t *testing.T, address string, serverName string, wantBody string) {
	t.Helper()
	transport := &http.Transport{
		TLSClientConfig:   &tls.Config{ServerName: serverName, InsecureSkipVerify: true},
		ForceAttemptHTTP2: true,
	}
	client := &http.Client{Transport: transport, Timeout: time.Second}
	defer transport.CloseIdleConnections()

	req, err := http.NewRequest(http.MethodGet, "https://"+address+"/", nil)
	require.NoError(t, err)
	req.Host = serverName
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), wantBody)
}

func assertHTTPSBodyNoSNI(t *testing.T, address string, wantBody string) {
	t.Helper()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, &tls.Config{InsecureSkipVerify: true})
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: app.example.com\r\nConnection: close\r\n\r\n"))
	require.NoError(t, err)
	body, err := io.ReadAll(conn)
	require.NoError(t, err)
	assert.Contains(t, string(body), wantBody)
}

type tlsGreetingServer struct{ address string }

func startTLSGreetingServer(t *testing.T, commonName string, greeting string) tlsGreetingServer {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", testTLSConfig(t, commonName))
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = conn.Write([]byte(greeting))
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})
	return tlsGreetingServer{address: listener.Addr().String()}
}

func testTLSConfig(t *testing.T, commonName string) *tls.Config {
	t.Helper()
	cert, err := testCertificate(commonName)
	require.NoError(t, err)
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
}

func testCertificate(commonName string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return tls.Certificate{}, err
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		DNSNames:     []string{commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	return tls.X509KeyPair(certPEM, keyPEM)
}

func TestHostMatchesTLSWildcard(t *testing.T) {
	assert.True(t, hostMatchesTLSWildcard("api.example.com", "*.example.com"))
	assert.False(t, hostMatchesTLSWildcard("example.com", "*.example.com"))
	assert.False(t, hostMatchesTLSWildcard("v1.api.example.com", "*.example.com"))
	assert.False(t, hostMatchesTLSWildcard("api.other.com", "*.example.com"))
	assert.True(t, strings.EqualFold(normalizeTLSName("API.EXAMPLE.COM."), "api.example.com"))
}
