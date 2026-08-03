package compatoldnew

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"time"

	"github.com/bnema/zerowrap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	edgev1 "github.com/bnema/gordon/api/gordon/edge/v1"
	runtimev1 "github.com/bnema/gordon/api/gordon/runtime/v1"
	edgesnapshotgrpc "github.com/bnema/gordon/internal/adapters/in/grpc/edgesnapshot"
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	runtimegrpc "github.com/bnema/gordon/internal/adapters/in/grpc/runtime"
	httpproxy "github.com/bnema/gordon/internal/adapters/in/http/proxy"
	grpcauth "github.com/bnema/gordon/internal/adapters/out/grpc/auth"
	edgesnapshotclient "github.com/bnema/gordon/internal/adapters/out/grpc/edgesnapshot"
	runtimeclient "github.com/bnema/gordon/internal/adapters/out/grpc/runtime"
	"github.com/bnema/gordon/internal/adapters/out/tokenstore"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/componentauth"
	"github.com/bnema/gordon/internal/usecase/container"
	"github.com/bnema/gordon/internal/usecase/edgesnapshot"
	proxyusecase "github.com/bnema/gordon/internal/usecase/proxy"
)

const distributedDrainScenarioName = "proxy/distributed-drain-protocol"

// RunCompatibilityDistributedDrainProtocol exercises the production split drain
// path over TCP gRPC and HTTP. Its report intentionally persists contract
// booleans only; endpoint, token, and private runtime identities stay in memory.
func RunCompatibilityDistributedDrainProtocol(ctx context.Context, artifactDir string) (Report, error) {
	if artifactDir == "" {
		return Report{}, fmt.Errorf("distributed drain compatibility: report artifact directory is required")
	}
	fixture, err := newDistributedDrainFixture(ctx)
	if err != nil {
		return Report{}, err
	}
	defer fixture.close()

	observation, err := fixture.exercise(ctx)
	if err != nil {
		return Report{}, err
	}
	return writeCurrentDistributedDrainReport(artifactDir, observation)
}

type distributedDrainFixture struct {
	root            string
	runtimeListener net.Listener
	controlListener net.Listener
	runtimeServer   *grpc.Server
	controlServer   *grpc.Server
	runtimeConn     *grpc.ClientConn
	controlConn     *grpc.ClientConn
	hub             *edgesnapshot.SnapshotHub
	coordinator     *edgesnapshot.DrainCoordinator
	registry        *container.RuntimeDrainRegistry
	receiver        *distributedDrainAckReceiver
	snapshotClient  *edgesnapshotclient.Client
	proxyService    *proxyusecase.Service
	public          *httptest.Server
	oldBackend      *httptest.Server
	newBackend      *httptest.Server
	oldStarted      chan struct{}
	releaseOld      chan struct{}
	oldState        domain.RuntimeRouteState
	oldEntry        domain.RouteTargetEntry
	newEntry        domain.RouteTargetEntry
	controlAckTime  time.Time
}

//nolint:gocyclo // The fixture deliberately exposes each independently failing transport setup step.
func newDistributedDrainFixture(ctx context.Context) (*distributedDrainFixture, error) {
	root, err := os.MkdirTemp("", "gordon-compat-distributed-drain-*")
	if err != nil {
		return nil, fmt.Errorf("distributed drain token directory: %w", err)
	}
	cleanup := func(err error) (*distributedDrainFixture, error) {
		_ = os.RemoveAll(root)
		return nil, err
	}
	store, err := tokenstore.NewUnsafeStore(root, distributedDrainLog())
	if err != nil {
		return cleanup(fmt.Errorf("distributed drain token store: %w", err))
	}
	auth := componentauth.NewService(store, distributedDrainLog(), componentauth.Config{})
	edgeToken, err := auth.CreateToken(ctx, componentauth.CreateRequest{
		Name: "gordon-edge", Role: domain.ComponentRoleEdge,
		Scopes: []domain.ComponentScope{domain.ComponentScopeRoutesWatch, domain.ComponentScopeEdgeDrain},
	})
	if err != nil {
		return cleanup(fmt.Errorf("distributed drain edge token: %w", err))
	}
	controlToken, err := auth.CreateToken(ctx, componentauth.CreateRequest{
		Name: "gordon-control", Role: domain.ComponentRoleControl,
		Scopes: []domain.ComponentScope{domain.ComponentScopeRuntimeDrainAck},
	})
	if err != nil {
		return cleanup(fmt.Errorf("distributed drain control token: %w", err))
	}

	backendHost, err := distributedDrainNonLoopbackIPv4()
	if err != nil {
		return cleanup(err)
	}
	fixture := &distributedDrainFixture{
		root: root, oldStarted: make(chan struct{}), releaseOld: make(chan struct{}),
		controlAckTime: time.Unix(1_700_000_000, 0).UTC(),
	}
	fixture.oldBackend, err = distributedDrainBackend(backendHost, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(fixture.oldStarted)
		<-fixture.releaseOld
		_, _ = io.WriteString(w, "old-backend")
	}))
	if err != nil {
		fixture.close()
		return nil, fmt.Errorf("distributed drain old backend: %w", err)
	}
	fixture.newBackend, err = distributedDrainBackend(backendHost, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "new-backend")
	}))
	if err != nil {
		fixture.close()
		return nil, fmt.Errorf("distributed drain new backend: %w", err)
	}
	oldPort, err := distributedDrainServerPort(fixture.oldBackend)
	if err != nil {
		fixture.close()
		return nil, err
	}
	newPort, err := distributedDrainServerPort(fixture.newBackend)
	if err != nil {
		fixture.close()
		return nil, err
	}
	fixture.oldState = domain.RuntimeRouteState{
		Domain: "app.example.test", Generation: 1, RouteVersion: "route-v1",
		ContainerAlias: "gordon-target-app-example-test", EdgeTargetAlias: backendHost,
		TargetPort: oldPort, Scheme: "http", Protocol: domain.RouteTargetProtocolHTTP1,
		Status: domain.RouteTargetStatusReady, BackingContainerName: "private-old-backing",
	}
	fixture.oldEntry, err = domain.NewManagedReadyRouteTargetEntry("app.example.test", backendHost, oldPort, "http", domain.RouteTargetProtocolHTTP1, 1, fixture.oldState.BackingContainerName+"\x00"+fixture.oldState.RouteVersion)
	if err != nil {
		fixture.close()
		return nil, fmt.Errorf("distributed drain old route: %w", err)
	}
	fixture.newEntry, err = domain.NewManagedReadyRouteTargetEntry("app.example.test", backendHost, newPort, "http", domain.RouteTargetProtocolHTTP1, 2, "private-new-backing\x00route-v2")
	if err != nil {
		fixture.close()
		return nil, fmt.Errorf("distributed drain new route: %w", err)
	}

	fixture.registry = container.NewRuntimeDrainRegistry(func(containerID string) (domain.RuntimeRouteState, bool) {
		return fixture.oldState, containerID == "old-container"
	})
	fixture.receiver = &distributedDrainAckReceiver{next: fixture.registry}
	fixture.runtimeListener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fixture.close()
		return nil, fmt.Errorf("distributed drain runtime listener: %w", err)
	}
	fixture.runtimeServer = grpc.NewServer(grpc.UnaryInterceptor(interceptors.ComponentAuthUnaryInterceptor(auth, runtimegrpc.MethodScopes(), runtimegrpc.MethodRoles())))
	runtimev1.RegisterRuntimeServiceServer(fixture.runtimeServer, runtimegrpc.NewServerWithRouteDrainAckReceiver(nil, fixture.receiver, "gordon-runtime"))
	go func() { _ = fixture.runtimeServer.Serve(fixture.runtimeListener) }()

	controlCredentials, err := grpcauth.NewInsecureBearerTokenCredentials(controlToken.Token)
	if err != nil {
		fixture.close()
		return nil, err
	}
	fixture.runtimeConn, err = grpc.NewClient(fixture.runtimeListener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithPerRPCCredentials(controlCredentials))
	if err != nil {
		fixture.close()
		return nil, fmt.Errorf("distributed drain runtime connection: %w", err)
	}
	fixture.hub = edgesnapshot.NewSnapshotHub()
	fixture.coordinator, err = edgesnapshot.NewDrainCoordinator(fixture.hub, edgesnapshot.DrainCoordinatorOptions{
		Runtime: runtimeclient.NewClient(fixture.runtimeConn), Now: func() time.Time { return fixture.controlAckTime },
	})
	if err != nil {
		fixture.close()
		return nil, fmt.Errorf("distributed drain coordinator: %w", err)
	}
	if err := fixture.hub.Publish(domain.RouteTargetSnapshot{Generation: 1, Entries: []domain.RouteTargetEntry{fixture.oldEntry}}); err != nil {
		fixture.close()
		return nil, fmt.Errorf("distributed drain initial snapshot: %w", err)
	}

	fixture.controlListener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fixture.close()
		return nil, fmt.Errorf("distributed drain control listener: %w", err)
	}
	fixture.controlServer = grpc.NewServer(
		grpc.UnaryInterceptor(interceptors.ComponentAuthUnaryInterceptor(auth, edgesnapshotgrpc.MethodScopes(), edgesnapshotgrpc.MethodRoles())),
		grpc.StreamInterceptor(interceptors.ComponentAuthStreamInterceptor(auth, edgesnapshotgrpc.MethodScopes(), edgesnapshotgrpc.MethodRoles())),
	)
	edgev1.RegisterEdgeServiceServer(fixture.controlServer, edgesnapshotgrpc.NewServerWithDrainStateReceiver(fixture.hub, fixture.coordinator))
	go func() { _ = fixture.controlServer.Serve(fixture.controlListener) }()
	edgeCredentials, err := grpcauth.NewInsecureBearerTokenCredentials(edgeToken.Token)
	if err != nil {
		fixture.close()
		return nil, err
	}
	fixture.controlConn, err = grpc.NewClient(fixture.controlListener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithPerRPCCredentials(edgeCredentials))
	if err != nil {
		fixture.close()
		return nil, fmt.Errorf("distributed drain control connection: %w", err)
	}
	fixture.snapshotClient = edgesnapshotclient.NewClient(fixture.controlConn, edgesnapshotclient.WithReconnectBackoff(time.Millisecond, 5*time.Millisecond))
	fixture.proxyService = proxyusecase.NewSnapshotService(fixture.snapshotClient, proxyusecase.Config{EdgeDrainTimeout: time.Second}, fixture.snapshotClient)
	fixture.snapshotClient.SetSnapshotAcceptanceObserver(fixture.proxyService)
	if err := fixture.snapshotClient.Start(ctx); err != nil {
		fixture.close()
		return nil, fmt.Errorf("distributed drain snapshot client: %w", err)
	}
	fixture.public = httptest.NewServer(httpproxy.NewHandler(fixture.proxyService, nil, distributedDrainLog()))
	return fixture, nil
}

//nolint:gocyclo // The protocol assertions are deliberately linear so each safety property stays explicit.
func (f *distributedDrainFixture) exercise(ctx context.Context) (map[string]bool, error) {
	if err := distributedDrainEventually(ctx, func() bool {
		snapshot, getErr := f.snapshotClient.CurrentSnapshot(context.Background())
		return getErr == nil && snapshot.Generation == 1
	}); err != nil {
		return nil, fmt.Errorf("distributed drain initial edge snapshot: %w", err)
	}
	f.registry.PrepareDrain("old-container")
	waitCtx, cancelWait := context.WithTimeout(ctx, 3*time.Second)
	defer cancelWait()
	waiter := make(chan bool, 1)
	go func() { waiter <- f.registry.WaitForNoInFlight(waitCtx, "old-container", 3*time.Second) }()

	oldResponse := make(chan string, 1)
	oldError := make(chan error, 1)
	go func() {
		body, err := f.request(ctx)
		if err != nil {
			oldError <- err
			return
		}
		oldResponse <- body
	}()
	select {
	case <-f.oldStarted:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := f.hub.Publish(domain.RouteTargetSnapshot{Generation: 2, Entries: []domain.RouteTargetEntry{f.newEntry}}); err != nil {
		return nil, fmt.Errorf("distributed drain replacement snapshot: %w", err)
	}
	if err := distributedDrainEventually(ctx, func() bool {
		snapshot, getErr := f.snapshotClient.CurrentSnapshot(context.Background())
		return getErr == nil && snapshot.Generation == 2
	}); err != nil {
		return nil, fmt.Errorf("distributed drain replacement edge snapshot: %w", err)
	}
	freshBody, err := f.request(ctx)
	if err != nil {
		return nil, fmt.Errorf("distributed drain fresh request: %w", err)
	}
	wrongRejected := f.snapshotClient.ReportDrainState(ctx, domain.RouteDrainState{CanonicalDomain: "app.example.test", TransitionGeneration: 2, OldTargetKey: f.newEntry.TargetKey}) != nil
	staleRejected := f.snapshotClient.ReportDrainState(ctx, domain.RouteDrainState{CanonicalDomain: "app.example.test", TransitionGeneration: 1, OldTargetKey: f.oldEntry.TargetKey}) != nil
	waiterBlocked := distributedDrainStillBlocked(waiter)

	close(f.releaseOld)
	var oldBody string
	select {
	case err := <-oldError:
		return nil, fmt.Errorf("distributed drain old request: %w", err)
	case oldBody = <-oldResponse:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var waiterReleased bool
	select {
	case waiterReleased = <-waiter:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	ack, acked := f.receiver.acknowledgement()
	duplicateAccepted := f.snapshotClient.ReportDrainState(ctx, domain.RouteDrainState{
		CanonicalDomain: "app.example.test", TransitionGeneration: 2, OldTargetKey: f.oldEntry.TargetKey,
		AcknowledgedAt: time.Unix(1, 0),
	}) == nil
	return map[string]bool{
		"controlRelayedOnce":     f.receiver.count() == 1,
		"edgeAckUsedOpaqueKey":   acked && ack.OldTargetKey == f.oldEntry.TargetKey,
		"freshRequestReachedNew": freshBody == "new-backend",
		"oldRequestReachedOld":   oldBody == "old-backend",
		"oldWaiterBlocked":       waiterBlocked,
		"oldWaiterReleased":      waiterReleased,
		"serverObservedAckTime":  acked && ack.AcknowledgedAt.Equal(f.controlAckTime),
		"staleReportRejected":    staleRejected,
		"wrongKeyReportRejected": wrongRejected,
		"duplicateAckIdempotent": duplicateAccepted && f.receiver.count() == 1,
	}, nil
}

func (f *distributedDrainFixture) request(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.public.URL+"/", nil)
	if err != nil {
		return "", err
	}
	req.Host = "app.example.test"
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected public status %d", response.StatusCode)
	}
	return string(body), nil
}

func (f *distributedDrainFixture) close() {
	if f.public != nil {
		f.public.Close()
	}
	if f.proxyService != nil {
		f.proxyService.Close()
	}
	if f.snapshotClient != nil {
		f.snapshotClient.Stop()
	}
	if f.controlConn != nil {
		_ = f.controlConn.Close()
	}
	if f.runtimeConn != nil {
		_ = f.runtimeConn.Close()
	}
	if f.controlServer != nil {
		f.controlServer.Stop()
	}
	if f.runtimeServer != nil {
		f.runtimeServer.Stop()
	}
	if f.controlListener != nil {
		_ = f.controlListener.Close()
	}
	if f.runtimeListener != nil {
		_ = f.runtimeListener.Close()
	}
	if f.coordinator != nil {
		f.coordinator.Close()
	}
	if f.registry != nil {
		f.registry.Close()
	}
	if f.oldBackend != nil {
		f.oldBackend.Close()
	}
	if f.newBackend != nil {
		f.newBackend.Close()
	}
	if f.root != "" {
		_ = os.RemoveAll(f.root)
	}
}

type distributedDrainAckReceiver struct {
	next out.RouteDrainAckReceiver
	mu   sync.Mutex
	acks []domain.RouteDrainAck
}

func (r *distributedDrainAckReceiver) PrepareRouteDrain(ctx context.Context, canonicalDomain string, generation domain.RouteTargetGeneration, key domain.RouteTargetKey) error {
	registrar, ok := r.next.(out.RouteDrainRegistrar)
	if !ok {
		return fmt.Errorf("route drain registrar not configured")
	}
	return registrar.PrepareRouteDrain(ctx, canonicalDomain, generation, key)
}

func (r *distributedDrainAckReceiver) AcknowledgeRouteDrain(ctx context.Context, acknowledgement domain.RouteDrainAck) error {
	r.mu.Lock()
	r.acks = append(r.acks, acknowledgement)
	r.mu.Unlock()
	return r.next.AcknowledgeRouteDrain(ctx, acknowledgement)
}

func (r *distributedDrainAckReceiver) acknowledgement() (domain.RouteDrainAck, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.acks) == 0 {
		return domain.RouteDrainAck{}, false
	}
	return r.acks[0], true
}

func (r *distributedDrainAckReceiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.acks)
}

func distributedDrainBackend(host string, handler http.Handler) (*httptest.Server, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return nil, err
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	return server, nil
}

func distributedDrainServerPort(server *httptest.Server) (int, error) {
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		return 0, err
	}
	var result int
	if _, err := fmt.Sscanf(port, "%d", &result); err != nil || result < 1 {
		return 0, fmt.Errorf("distributed drain backend port is invalid")
	}
	return result, nil
}

func distributedDrainNonLoopbackIPv4() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, addrErr := iface.Addrs()
		if addrErr != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, parseErr := net.ParseCIDR(address.String())
			if parseErr == nil && ip.To4() != nil && !ip.IsLoopback() {
				return ip.String(), nil
			}
		}
	}
	return "", fmt.Errorf("distributed drain compatibility requires a non-loopback IPv4 address for split-edge routing")
}

func distributedDrainEventually(ctx context.Context, check func() bool) error {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if check() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func distributedDrainStillBlocked(waiter <-chan bool) bool {
	select {
	case <-waiter:
		return false
	case <-time.After(25 * time.Millisecond):
		return true
	}
}

func distributedDrainLog() zerowrap.Logger {
	return zerowrap.New(zerowrap.Config{Level: "disabled", Output: io.Discard})
}

func writeCurrentDistributedDrainReport(artifactDir string, observation map[string]bool) (Report, error) {
	expected := make(map[string]bool, len(observation))
	for key := range observation {
		expected[key] = true
	}
	artifactExpected := NewRuntimeArtifact("distributed drain protocol", expected, LevelSecurityNegative)
	artifactActual := NewRuntimeArtifact("distributed drain protocol", observation, LevelSecurityNegative)
	return CompareSideResultsWithMetadata(
		SideResult{Side: SideOld, Artifact: artifactExpected},
		SideResult{Side: SideNew, Artifact: artifactActual},
		nil, artifactDir,
		ReportMetadata{RerunCommand: "make compat-harness-runtime"},
	)
}
