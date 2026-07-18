package compatoldnew

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	edgev1 "github.com/bnema/gordon/api/gordon/edge/v1"
	edgesnapshotgrpc "github.com/bnema/gordon/internal/adapters/in/grpc/edgesnapshot"
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	trafficadapter "github.com/bnema/gordon/internal/adapters/in/traffic"
	grpcauth "github.com/bnema/gordon/internal/adapters/out/grpc/auth"
	edgesnapshotclient "github.com/bnema/gordon/internal/adapters/out/grpc/edgesnapshot"
	"github.com/bnema/gordon/internal/adapters/out/tokenstore"
	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/componentauth"
	"github.com/bnema/gordon/internal/usecase/edgesnapshot"
)

// RunTrafficGraphStreamMatrix proves that the edge listener manager is driven
// by the authenticated, sanitized traffic stream. In particular the manager is
// never handed a graph by the test: the only Apply calls are made by the same
// accepted-snapshot callback that production edge wiring installs.
func RunTrafficGraphStreamMatrix(ctx context.Context) (TrafficProtocolMatrix, error) {
	fixture, err := newTrafficGraphStreamFixture(ctx)
	if err != nil {
		return TrafficProtocolMatrix{}, err
	}
	defer func() { _ = fixture.close() }()

	if err := fixture.publish(1, fixture.initial); err != nil {
		return TrafficProtocolMatrix{}, err
	}
	if err := fixture.waitGeneration(ctx, 1); err != nil {
		return TrafficProtocolMatrix{}, err
	}
	if err := fixture.probe(fixture.initialAddresses); err != nil {
		return TrafficProtocolMatrix{}, fmt.Errorf("probe streamed initial graph: %w", err)
	}

	if err := fixture.publish(2, fixture.updated); err != nil {
		return TrafficProtocolMatrix{}, err
	}
	if err := fixture.waitGeneration(ctx, 2); err != nil {
		return TrafficProtocolMatrix{}, err
	}
	if err := fixture.probe(fixture.updatedAddresses); err != nil {
		return TrafficProtocolMatrix{}, fmt.Errorf("probe streamed update graph: %w", err)
	}
	if err := verifyTrafficListenerRelease(fixture.initialAddresses); err != nil {
		return TrafficProtocolMatrix{}, fmt.Errorf("updated stream graph retained old listener: %w", err)
	}
	if err := fixture.close(); err != nil {
		return TrafficProtocolMatrix{}, fmt.Errorf("cancel production traffic apply loop: %w", err)
	}
	if err := verifyTrafficListenerRelease(fixture.updatedAddresses); err != nil {
		return TrafficProtocolMatrix{}, fmt.Errorf("production traffic apply loop retained listener after cancellation: %w", err)
	}

	return TrafficProtocolMatrix{Checks: []TrafficProtocolArtifact{
		{Protocol: "authenticated_traffic_graph_stream", Passed: true, Status: "ok"},
		{Protocol: "http", Passed: true, Status: "ok"},
		{Protocol: "smart_tcp_https_fallback", Passed: true, Status: "ok"},
		{Protocol: "tls_mux_https_termination", Passed: true, Status: "ok"},
		{Protocol: "tls_passthrough", Passed: true, Status: "ok"},
		{Protocol: "tcp", Passed: true, Status: "ok"},
		{Protocol: "udp", Passed: true, Status: "ok"},
	}}, nil
}

type trafficGraphStreamFixture struct {
	root             string
	listener         net.Listener
	server           *grpc.Server
	connection       *grpc.ClientConn
	client           *edgesnapshotclient.TrafficGraphClient
	manager          *trafficadapter.Manager
	backends         *trafficProtocolBackends
	initialAddresses trafficProtocolAddresses
	updatedAddresses trafficProtocolAddresses
	initial          domain.TrafficGraph
	updated          domain.TrafficGraph
	hub              *edgesnapshot.TrafficGraphHub
	loopCancel       context.CancelFunc
	loopDone         chan error
	closeOnce        sync.Once
	closeErr         error
}

//nolint:gocyclo // Each independent transport setup step needs immediate, bounded cleanup.
func newTrafficGraphStreamFixture(ctx context.Context) (*trafficGraphStreamFixture, error) {
	root, err := osMkdirTemp("gordon-compat-traffic-stream-*")
	if err != nil {
		return nil, err
	}
	cleanup := func(err error, f *trafficGraphStreamFixture) (*trafficGraphStreamFixture, error) {
		if f != nil {
			_ = f.close()
		} else {
			_ = osRemoveAll(root)
		}
		return nil, err
	}

	store, err := tokenstore.NewUnsafeStore(root, distributedDrainLog())
	if err != nil {
		return cleanup(fmt.Errorf("traffic stream token store: %w", err), nil)
	}
	auth := componentauth.NewService(store, distributedDrainLog(), componentauth.Config{})
	token, err := auth.CreateToken(ctx, componentauth.CreateRequest{Name: "gordon-edge-traffic", Role: domain.ComponentRoleEdge, Scopes: []domain.ComponentScope{domain.ComponentScopeTrafficWatch}})
	if err != nil {
		return cleanup(fmt.Errorf("traffic stream edge token: %w", err), nil)
	}

	backendHost, err := distributedDrainNonLoopbackIPv4()
	if err != nil {
		return cleanup(err, nil)
	}
	backends, err := startTrafficProtocolBackendsOn(backendHost)
	if err != nil {
		return cleanup(fmt.Errorf("traffic stream backends: %w", err), nil)
	}
	initialAddresses, err := reserveTrafficProtocolAddresses()
	if err != nil {
		backends.close()
		return cleanup(err, nil)
	}
	updatedAddresses, err := reserveTrafficProtocolAddresses()
	if err != nil {
		backends.close()
		return cleanup(err, nil)
	}
	certFile, keyFile, err := writeTrafficGraphStreamCertificate(root)
	if err != nil {
		backends.close()
		return cleanup(err, nil)
	}
	f := &trafficGraphStreamFixture{root: root, backends: backends, initialAddresses: initialAddresses, updatedAddresses: updatedAddresses, hub: edgesnapshot.NewTrafficGraphHub(), manager: trafficadapter.NewManager()}
	f.initial, f.updated = trafficProtocolGraph(initialAddresses, backends), trafficProtocolGraph(updatedAddresses, backends)
	streamOptions := domain.TrafficOptions{
		TCP: domain.TCPOptions{DialTimeout: time.Second, IdleTimeout: time.Second, DrainTimeout: 100 * time.Millisecond},
		UDP: domain.UDPOptions{IdleTimeout: time.Second, DrainTimeout: 100 * time.Millisecond},
	}
	f.initial.Options, f.updated.Options = streamOptions, streamOptions
	f.loopDone = make(chan error, 1)
	loopCtx, loopCancel := context.WithCancel(ctx)
	f.loopCancel = loopCancel

	f.listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return cleanup(fmt.Errorf("traffic stream listener: %w", err), f)
	}
	f.server = grpc.NewServer(grpc.StreamInterceptor(interceptors.ComponentAuthStreamInterceptor(auth, edgesnapshotgrpc.MethodScopes(), edgesnapshotgrpc.MethodRoles())))
	edgev1.RegisterEdgeServiceServer(f.server, edgesnapshotgrpc.NewServerWithTrafficGraphSource(nil, f.hub))
	go func() { _ = f.server.Serve(f.listener) }()
	credentials, err := grpcauth.NewInsecureBearerTokenCredentials(token.Token)
	if err != nil {
		return cleanup(err, f)
	}
	f.connection, err = grpc.NewClient(f.listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithPerRPCCredentials(credentials))
	if err != nil {
		return cleanup(fmt.Errorf("traffic stream client connection: %w", err), f)
	}
	f.client = edgesnapshotclient.NewTrafficGraphClient(f.connection, edgesnapshotclient.WithReconnectBackoff(time.Millisecond, 5*time.Millisecond))
	if err := f.client.Start(loopCtx); err != nil {
		return cleanup(fmt.Errorf("start authenticated traffic stream: %w", err), f)
	}
	cfg := app.EdgeConfig{Edge: app.EdgeServingConfig{TLS: app.EdgeTLSConfig{Mode: "files", CertFile: certFile, KeyFile: keyFile}}}
	handler := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { _, _ = response.Write([]byte("edge traffic")) })
	go func() { f.loopDone <- app.RunEdgeTrafficApplyLoop(loopCtx, cfg, handler, f.client, f.manager) }()
	return f, nil
}

func (f *trafficGraphStreamFixture) publish(generation domain.TrafficGraphGeneration, graph domain.TrafficGraph) error {
	if err := f.hub.Publish(domain.TrafficGraphSnapshot{Generation: generation, Graph: graph}); err != nil {
		return fmt.Errorf("publish traffic graph %d: %w", generation, err)
	}
	return nil
}

func (f *trafficGraphStreamFixture) waitGeneration(ctx context.Context, want domain.TrafficGraphGeneration) error {
	graph := f.initial
	if want >= 2 {
		graph = f.updated
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		health := f.client.TrafficGraphHealth()
		if health.Healthy && health.LastAcceptedGeneration >= want && trafficGraphActive(f.manager, graph) {
			return nil
		}
		select {
		case err := <-f.loopDone:
			return fmt.Errorf("production traffic apply loop stopped: %w", err)
		default:
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for streamed generation %d (health %+v): %w", want, health, ctx.Err())
		case <-ticker.C:
		}
	}
}

func trafficGraphActive(manager *trafficadapter.Manager, graph domain.TrafficGraph) bool {
	status := manager.Status()
	if status.LastReloadError != "" || len(status.EntryPoints) != len(graph.EntryPoints) {
		return false
	}
	for _, expected := range graph.EntryPoints {
		found := false
		for _, actual := range status.EntryPoints {
			if actual.Name == expected.Name && actual.Address == expected.Address && actual.Protocol == expected.Protocol && actual.Active {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (f *trafficGraphStreamFixture) probe(addresses trafficProtocolAddresses) error {
	for _, probe := range []func() error{
		func() error { return probeTrafficHTTP(addresses.smart, false, "edge traffic") },
		func() error { return probeTrafficHTTP(addresses.smart, true, "edge traffic") },
		func() error { return probeTrafficHTTP(addresses.mux, true, "edge traffic") },
		func() error { return probeTLSPassthrough(addresses.mux) },
		func() error { return probeTrafficTCPEcho(addresses.raw) },
		func() error { return probeTrafficUDPEcho(addresses.udp) },
	} {
		if err := probe(); err != nil {
			return err
		}
	}
	return nil
}

func (f *trafficGraphStreamFixture) close() error {
	if f == nil {
		return nil
	}
	f.closeOnce.Do(func() {
		if f.loopCancel != nil {
			f.loopCancel()
		}
		if f.loopDone != nil {
			select {
			case err := <-f.loopDone:
				if err != nil {
					f.closeErr = fmt.Errorf("production traffic apply loop: %w", err)
				}
			case <-time.After(5 * time.Second):
				f.closeErr = fmt.Errorf("production traffic apply loop did not stop")
			}
		}
		if f.client != nil {
			f.client.Stop()
		}
		if f.connection != nil {
			_ = f.connection.Close()
		}
		if f.server != nil {
			f.server.Stop()
		}
		if f.listener != nil {
			_ = f.listener.Close()
		}
		if f.backends != nil {
			f.backends.close()
		}
		if f.root != "" {
			_ = osRemoveAll(f.root)
		}
	})
	return f.closeErr
}

// Kept as tiny variables so the stream fixture's cleanup behavior is easy to
// exercise without retaining paths or token material in compatibility reports.
var osMkdirTemp = func(pattern string) (string, error) { return os.MkdirTemp("", pattern) }
var osRemoveAll = os.RemoveAll

// writeTrafficGraphStreamCertificate gives the production edge loop ordinary
// cert/key paths; it must load TLS through EdgeConfig rather than inherit an
// in-memory test TLS configuration.
func writeTrafficGraphStreamCertificate(root string) (string, string, error) {
	certificate, err := generatedTrafficTestCertificate()
	if err != nil {
		return "", "", err
	}
	if len(certificate.Certificate) == 0 {
		return "", "", fmt.Errorf("generated traffic certificate has no leaf")
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		return "", "", fmt.Errorf("marshal traffic certificate key: %w", err)
	}
	certFile, keyFile := filepath.Join(root, "edge-cert.pem"), filepath.Join(root, "edge-key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}), 0600); err != nil {
		return "", "", fmt.Errorf("write traffic certificate: %w", err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		return "", "", fmt.Errorf("write traffic certificate key: %w", err)
	}
	return certFile, keyFile, nil
}

func startTrafficProtocolBackendsOn(host string) (*trafficProtocolBackends, error) {
	result := &trafficProtocolBackends{}
	tcpListener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return nil, err
	}
	result.tcp, result.closers = tcpListener.Addr().String(), append(result.closers, tcpListener)
	result.wg.Add(1)
	go serveTrafficTCPEcho(tcpListener, &result.wg)
	packet, err := net.ListenPacket("udp", net.JoinHostPort(host, "0"))
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
	tlsListener, err := tls.Listen("tcp", net.JoinHostPort(host, "0"), &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		result.close()
		return nil, err
	}
	result.tls, result.closers = tlsListener.Addr().String(), append(result.closers, tlsListener)
	result.wg.Add(1)
	go serveTrafficTLSPassthrough(tlsListener, &result.wg)
	return result, nil
}
