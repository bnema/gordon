package compatoldnew

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
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
	defer fixture.close()

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
	generation       domain.TrafficGraphGeneration
	applied          chan domain.TrafficGraphGeneration
	applyErr         error
	mu               sync.Mutex
}

//nolint:gocyclo // Each independent transport setup step needs immediate, bounded cleanup.
func newTrafficGraphStreamFixture(ctx context.Context) (*trafficGraphStreamFixture, error) {
	root, err := osMkdirTemp("gordon-compat-traffic-stream-*")
	if err != nil {
		return nil, err
	}
	cleanup := func(err error, f *trafficGraphStreamFixture) (*trafficGraphStreamFixture, error) {
		if f != nil {
			f.close()
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
	cert, err := generatedTrafficTestCertificate()
	if err != nil {
		backends.close()
		return cleanup(err, nil)
	}
	f := &trafficGraphStreamFixture{root: root, backends: backends, initialAddresses: initialAddresses, updatedAddresses: updatedAddresses, hub: edgesnapshot.NewTrafficGraphHub(), manager: trafficadapter.NewManager(), applied: make(chan domain.TrafficGraphGeneration, 2)}
	f.initial, f.updated = trafficProtocolGraph(initialAddresses, backends), trafficProtocolGraph(updatedAddresses, backends)
	streamOptions := domain.TrafficOptions{
		TCP: domain.TCPOptions{DialTimeout: time.Second, IdleTimeout: time.Second, DrainTimeout: 100 * time.Millisecond},
		UDP: domain.UDPOptions{IdleTimeout: time.Second, DrainTimeout: 100 * time.Millisecond},
	}
	f.initial.Options, f.updated.Options = streamOptions, streamOptions
	configureTrafficProtocolHandlers(f.manager, cert)

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
	f.client.SetTrafficGraphAcceptanceObserver(func(snapshot domain.TrafficGraphSnapshot) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.applyErr == nil {
			f.applyErr = f.manager.Apply(context.Background(), &snapshot.Graph)
		}
		if f.applyErr == nil {
			f.generation = snapshot.Generation
			select {
			case f.applied <- snapshot.Generation:
			default:
			}
		}
	})
	if err := f.client.Start(ctx); err != nil {
		return cleanup(fmt.Errorf("start authenticated traffic stream: %w", err), f)
	}
	return f, nil
}

func (f *trafficGraphStreamFixture) publish(generation domain.TrafficGraphGeneration, graph domain.TrafficGraph) error {
	if err := f.hub.Publish(domain.TrafficGraphSnapshot{Generation: generation, Graph: graph}); err != nil {
		return fmt.Errorf("publish traffic graph %d: %w", generation, err)
	}
	return nil
}

func (f *trafficGraphStreamFixture) waitGeneration(ctx context.Context, want domain.TrafficGraphGeneration) error {
	for {
		f.mu.Lock()
		got, applyErr := f.generation, f.applyErr
		f.mu.Unlock()
		if applyErr != nil {
			return fmt.Errorf("apply streamed graph: %w", applyErr)
		}
		if got >= want {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for streamed generation %d (got %d, health %+v): %w", want, got, f.client.TrafficGraphHealth(), ctx.Err())
		case <-f.applied:
		}
	}
}

func (f *trafficGraphStreamFixture) probe(addresses trafficProtocolAddresses) error {
	for _, probe := range []func() error{
		func() error { return probeTrafficHTTP(addresses.smart, false, "smart http") },
		func() error { return probeTrafficHTTP(addresses.smart, true, "smart https") },
		func() error { return probeTrafficHTTP(addresses.mux, true, "mux https") },
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

func (f *trafficGraphStreamFixture) close() {
	if f == nil {
		return
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
	if f.manager != nil {
		shutdownTrafficManager(f.manager)
	}
	if f.backends != nil {
		f.backends.close()
	}
	if f.root != "" {
		_ = osRemoveAll(f.root)
	}
}

// Kept as tiny variables so the stream fixture's cleanup behavior is easy to
// exercise without retaining paths or token material in compatibility reports.
var osMkdirTemp = func(pattern string) (string, error) { return os.MkdirTemp("", pattern) }
var osRemoveAll = os.RemoveAll

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
