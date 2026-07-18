package compatoldnew

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"sync"
	"time"

	trafficadapter "github.com/bnema/gordon/internal/adapters/in/traffic"
	"github.com/bnema/gordon/internal/domain"
)

// TrafficProtocolArtifact is intentionally safe to upload: it records only a
// protocol, boolean result, and status. Addresses, certificates, and runtime
// identities are never retained by this compatibility matrix.
type TrafficProtocolArtifact struct {
	Protocol string `json:"protocol"`
	Passed   bool   `json:"passed"`
	Status   string `json:"status"`
}

// TrafficProtocolMatrix is the safe result of the monolith and split listener
// ownership exercises.
type TrafficProtocolMatrix struct {
	Checks []TrafficProtocolArtifact `json:"checks"`
}

// RunTrafficProtocolMatrix exercises every edge-owned protocol with real local
// sockets. The first pass is the monolith traffic graph. The second pass applies
// a sanitized, split-reachable snapshot to a fresh manager, which is the same
// production listener owner used by the edge role after its graph stream is
// accepted. No fixed or privileged ports are used.
func RunTrafficProtocolMatrix(ctx context.Context) (TrafficProtocolMatrix, error) {
	backends, err := startTrafficProtocolBackends()
	if err != nil {
		return TrafficProtocolMatrix{}, err
	}
	defer backends.close()

	addresses, err := reserveTrafficProtocolAddresses()
	if err != nil {
		return TrafficProtocolMatrix{}, err
	}
	cert, err := generatedTrafficTestCertificate()
	if err != nil {
		return TrafficProtocolMatrix{}, err
	}

	monolith := trafficadapter.NewManager()
	configureTrafficProtocolHandlers(monolith, cert)
	graph := trafficProtocolGraph(addresses, backends)
	if err := monolith.Apply(ctx, &graph); err != nil {
		return TrafficProtocolMatrix{}, fmt.Errorf("apply monolith traffic graph: %w", err)
	}

	checks := make([]TrafficProtocolArtifact, 0, 7)
	for _, check := range []struct {
		protocol string
		probe    func() error
	}{
		{"http", func() error { return probeTrafficHTTP(addresses.smart, false, "smart http") }},
		{"smart_tcp_https_fallback", func() error { return probeTrafficHTTP(addresses.smart, true, "smart https") }},
		{"tls_mux_https_termination", func() error { return probeTrafficHTTP(addresses.mux, true, "mux https") }},
		{"tls_passthrough", func() error { return probeTLSPassthrough(addresses.mux) }},
		{"tcp", func() error { return probeTrafficTCPEcho(addresses.raw) }},
		{"udp", func() error { return probeTrafficUDPEcho(addresses.udp) }},
	} {
		if err := check.probe(); err != nil {
			return TrafficProtocolMatrix{}, fmt.Errorf("%s protocol probe: %w", check.protocol, err)
		}
		checks = append(checks, TrafficProtocolArtifact{Protocol: check.protocol, Passed: true, Status: "ok"})
	}

	// Release the monolith's ephemeral listeners before the edge claims the
	// exact same graph addresses. This catches accidental listener ownership
	// drift without relying on fixed ports.
	shutdownTrafficManager(monolith)

	// A split edge must not get loopback backends. Its listener graph is a
	// separate snapshot and is deliberately backend-free, because listener
	// ownership is independent of control's route/runtime configuration.
	splitGraph := domain.TrafficGraph{EntryPoints: graph.EntryPoints}
	split := domain.TrafficGraphSnapshot{Generation: 1, Graph: splitGraph}.Clone()
	if err := split.ValidateSplitReachability(); err != nil {
		return TrafficProtocolMatrix{}, fmt.Errorf("validate split traffic snapshot: %w", err)
	}
	edge := trafficadapter.NewManager()
	if err := edge.Apply(ctx, &split.Graph); err != nil {
		return TrafficProtocolMatrix{}, fmt.Errorf("apply split edge traffic graph: %w", err)
	}
	if !allTrafficEntryPointsActive(edge.Status(), 4) {
		shutdownTrafficManager(edge)
		return TrafficProtocolMatrix{}, fmt.Errorf("split edge did not own every streamed traffic listener")
	}
	shutdownTrafficManager(edge)
	if err := verifyTrafficListenerRelease(addresses); err != nil {
		return TrafficProtocolMatrix{}, err
	}
	checks = append(checks, TrafficProtocolArtifact{Protocol: "split_edge_listener_ownership", Passed: true, Status: "ok"})
	return TrafficProtocolMatrix{Checks: checks}, nil
}

// ValidateTrafficProtocolFailClosed preserves the graph validation boundaries
// the edge relies on when consuming an untrusted traffic stream.
func ValidateTrafficProtocolFailClosed() error {
	loopback := domain.TrafficGraphSnapshot{Generation: 1, Graph: domain.TrafficGraph{
		Services: []domain.TrafficService{{Name: "service:echo:tcp", Backends: []domain.TrafficBackend{{Host: "127.0.0.1", Port: 1, Protocol: domain.NetworkProtocolTCP}}}},
	}}
	if err := loopback.ValidateSplitReachability(); err == nil {
		return fmt.Errorf("split traffic graph accepted a loopback backend")
	}
	malformedCIDR := domain.TrafficGraph{EntryPoints: []domain.EntryPoint{{Name: "bad", Address: "127.0.0.1:0", Protocol: domain.EntryPointProtocolTCP, TrustedCIDRs: []string{"not-a-cidr"}}}}
	if err := malformedCIDR.Validate(); err == nil {
		return fmt.Errorf("traffic graph accepted malformed CIDR")
	}
	unknownProtocol := domain.TrafficGraph{EntryPoints: []domain.EntryPoint{{Name: "bad", Address: "127.0.0.1:0", Protocol: "unknown"}}}
	if err := unknownProtocol.Validate(); err == nil {
		return fmt.Errorf("traffic graph accepted unknown protocol")
	}
	return nil
}

type trafficProtocolAddresses struct{ smart, mux, raw, udp string }

type trafficProtocolBackends struct {
	tcp, udp, tls string
	closers       []io.Closer
	wg            sync.WaitGroup
}

func reserveTrafficProtocolAddresses() (trafficProtocolAddresses, error) {
	tcpAddress := func() (string, error) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", err
		}
		address := listener.Addr().String()
		return address, listener.Close()
	}
	smart, err := tcpAddress()
	if err != nil {
		return trafficProtocolAddresses{}, err
	}
	mux, err := tcpAddress()
	if err != nil {
		return trafficProtocolAddresses{}, err
	}
	raw, err := tcpAddress()
	if err != nil {
		return trafficProtocolAddresses{}, err
	}
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return trafficProtocolAddresses{}, err
	}
	udp := packet.LocalAddr().String()
	if err := packet.Close(); err != nil {
		return trafficProtocolAddresses{}, err
	}
	return trafficProtocolAddresses{smart: smart, mux: mux, raw: raw, udp: udp}, nil
}

func startTrafficProtocolBackends() (*trafficProtocolBackends, error) {
	result := &trafficProtocolBackends{}
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	result.tcp, result.closers = tcpListener.Addr().String(), append(result.closers, tcpListener)
	result.wg.Add(1)
	go serveTrafficTCPEcho(tcpListener, &result.wg)

	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		result.close()
		return nil, err
	}
	result.udp, result.closers = packet.LocalAddr().String(), append(result.closers, packet)
	result.wg.Add(1)
	go serveTrafficUDPEcho(packet, &result.wg)

	cert, err := generatedTrafficTestCertificate()
	if err != nil {
		result.close()
		return nil, err
	}
	tlsListener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		result.close()
		return nil, err
	}
	result.tls, result.closers = tlsListener.Addr().String(), append(result.closers, tlsListener)
	result.wg.Add(1)
	go serveTrafficTLSPassthrough(tlsListener, &result.wg)
	return result, nil
}

func (b *trafficProtocolBackends) close() {
	for _, closer := range b.closers {
		_ = closer.Close()
	}
	b.wg.Wait()
}

func serveTrafficTCPEcho(listener net.Listener, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
	}
}

func serveTrafficUDPEcho(packet net.PacketConn, wg *sync.WaitGroup) {
	defer wg.Done()
	buffer := make([]byte, 64<<10)
	for {
		n, address, err := packet.ReadFrom(buffer)
		if err != nil {
			return
		}
		_, _ = packet.WriteTo(buffer[:n], address)
	}
}

func serveTrafficTLSPassthrough(listener net.Listener, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			buffer := make([]byte, 32)
			if _, err := conn.Read(buffer); err == nil {
				_, _ = conn.Write([]byte("tls passthrough"))
			}
		}()
	}
}

func trafficProtocolGraph(addresses trafficProtocolAddresses, backends *trafficProtocolBackends) domain.TrafficGraph {
	tcpHost, tcpPort := trafficBackendAddress(backends.tcp)
	udpHost, udpPort := trafficBackendAddress(backends.udp)
	tlsHost, tlsPort := trafficBackendAddress(backends.tls)
	return domain.TrafficGraph{
		EntryPoints: []domain.EntryPoint{
			{Name: "smart", Address: addresses.smart, Protocol: domain.EntryPointProtocolSmartTCP},
			{Name: "mux", Address: addresses.mux, Protocol: domain.EntryPointProtocolTLSMux},
			{Name: "raw", Address: addresses.raw, Protocol: domain.EntryPointProtocolTCP},
			{Name: "udp", Address: addresses.udp, Protocol: domain.EntryPointProtocolUDP},
		},
		Routers: []domain.TrafficRouter{
			{Name: "smart-http", EntryPoint: "smart", Protocol: domain.RouterProtocolHTTP, Rule: domain.TrafficRule{Host: "smart.test"}, Service: "route:smart.test"},
			{Name: "mux-http", EntryPoint: "mux", Protocol: domain.RouterProtocolHTTP, Rule: domain.TrafficRule{Host: "mux.test"}, Service: "route:mux.test"},
			{Name: "mux-tls", EntryPoint: "mux", Protocol: domain.RouterProtocolTLSPassthrough, Rule: domain.TrafficRule{SNI: "passthrough.test"}, Service: "service:tls:tcp"},
			{Name: "raw", EntryPoint: "raw", Protocol: domain.RouterProtocolTCP, Service: "service:raw:tcp"},
			{Name: "udp", EntryPoint: "udp", Protocol: domain.RouterProtocolUDP, Service: "service:udp:udp"},
		},
		Services: []domain.TrafficService{
			{Name: "route:smart.test"}, {Name: "route:mux.test"},
			{Name: "service:tls:tcp", Backends: []domain.TrafficBackend{{Host: tlsHost, Port: tlsPort, Protocol: domain.NetworkProtocolTCP}}},
			{Name: "service:raw:tcp", Backends: []domain.TrafficBackend{{Host: tcpHost, Port: tcpPort, Protocol: domain.NetworkProtocolTCP}}},
			{Name: "service:udp:udp", Backends: []domain.TrafficBackend{{Host: udpHost, Port: udpPort, Protocol: domain.NetworkProtocolUDP}}},
		},
	}
}

func trafficBackendAddress(address string) (string, int) {
	host, port, _ := net.SplitHostPort(address)
	var value int
	_, _ = fmt.Sscanf(port, "%d", &value)
	return host, value
}

func configureTrafficProtocolHandlers(manager *trafficadapter.Manager, cert tls.Certificate) {
	handler := func(marker string) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { _, _ = response.Write([]byte(marker)) })
	}
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	manager.SetSmartTCPHTTPServer("smart", handler("smart http"), &protocols)
	manager.SetSmartTCPTLSServer("smart", handler("smart https"), &tls.Config{Certificates: []tls.Certificate{cert}})
	manager.SetTLSHTTPServer("mux", handler("mux https"), &tls.Config{Certificates: []tls.Certificate{cert}})
}

func generatedTrafficTestCertificate() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	template := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "gordon traffic test"}, DNSNames: []string{"smart.test", "mux.test", "passthrough.test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

func probeTrafficHTTP(address string, secure bool, want string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	scheme := "http"
	if secure {
		scheme = "https"
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	} // #nosec G402 -- generated test certificate.
	response, err := client.Get(scheme + "://" + address)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if string(body) != want {
		return fmt.Errorf("got %q, want %q", body, want)
	}
	return nil
}

func probeTLSPassthrough(address string) error {
	conn, err := tls.Dial("tcp", address, &tls.Config{ServerName: "passthrough.test", InsecureSkipVerify: true}) // #nosec G402 -- generated test certificate.
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("test")); err != nil {
		return err
	}
	response := make([]byte, len("tls passthrough"))
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}
	if string(response) != "tls passthrough" {
		return fmt.Errorf("unexpected passthrough response")
	}
	return nil
}

func probeTrafficTCPEcho(address string) error {
	conn, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.Write([]byte("tcp echo")); err != nil {
		return err
	}
	response := make([]byte, len("tcp echo"))
	if _, err = io.ReadFull(conn, response); err != nil {
		return err
	}
	if string(response) != "tcp echo" {
		return fmt.Errorf("unexpected tcp echo")
	}
	return nil
}

func probeTrafficUDPEcho(address string) error {
	conn, err := net.DialTimeout("udp", address, 3*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.Write([]byte("udp echo")); err != nil {
		return err
	}
	if err = conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	response := make([]byte, len("udp echo"))
	if _, err = io.ReadFull(conn, response); err != nil {
		return err
	}
	if string(response) != "udp echo" {
		return fmt.Errorf("unexpected udp echo")
	}
	return nil
}

func verifyTrafficListenerRelease(addresses trafficProtocolAddresses) error {
	for _, address := range []string{addresses.smart, addresses.mux, addresses.raw} {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return fmt.Errorf("traffic TCP listener leaked: %w", err)
		}
		if err := listener.Close(); err != nil {
			return err
		}
	}
	packet, err := net.ListenPacket("udp", addresses.udp)
	if err != nil {
		return fmt.Errorf("traffic UDP listener leaked: %w", err)
	}
	return packet.Close()
}

func allTrafficEntryPointsActive(status domain.TrafficStatus, want int) bool {
	if len(status.EntryPoints) != want {
		return false
	}
	for _, entry := range status.EntryPoints {
		if !entry.Active {
			return false
		}
	}
	return true
}

func shutdownTrafficManager(manager *trafficadapter.Manager) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(ctx)
}
