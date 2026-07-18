package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/bnema/gordon/internal/adapters/in/http/httphelper"
	"github.com/bnema/gordon/internal/adapters/in/http/middleware"
	httpproxy "github.com/bnema/gordon/internal/adapters/in/http/proxy"
	trafficadapter "github.com/bnema/gordon/internal/adapters/in/traffic"
	"github.com/bnema/gordon/internal/adapters/out/accesslog"
	grpcauth "github.com/bnema/gordon/internal/adapters/out/grpc/auth"
	edgesnapshotclient "github.com/bnema/gordon/internal/adapters/out/grpc/edgesnapshot"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/proxy"
	"github.com/bnema/gordon/pkg/bytesize"
)

// edgeRoleDependencies contains the entire edge role surface. Its deliberate
// absence of runtime/socket/config-service constructors is an ownership guard:
// an edge consumes snapshots and proxies requests, nothing more.
type edgeRoleDependencies struct {
	listen            func(network, address string) (net.Listener, error)
	dialSnapshot      func(context.Context, EdgeControlConfig) (*edgesnapshotclient.Client, *grpc.ClientConn, error)
	newHTTPServer     func(string, http.Handler) *http.Server
	newTrafficManager func() edgeTrafficManager
}

func productionEdgeRoleDependencies() edgeRoleDependencies {
	return edgeRoleDependencies{
		listen:            net.Listen,
		dialSnapshot:      newEdgeSnapshotClient,
		newTrafficManager: func() edgeTrafficManager { return trafficadapter.NewManager() },
		newHTTPServer: func(address string, handler http.Handler) *http.Server {
			return &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
		},
	}
}

// runEdgeImpl starts the Phase 4 public edge. It uses an edge-only config
// contract: local certificate files terminate TLS, while external termination
// only permits plaintext from explicitly trusted proxy CIDRs.
func runEdgeImpl(ctx context.Context, configPath string) error {
	return runEdgeWithDependencies(ctx, configPath, productionEdgeRoleDependencies())
}

func runEdgeWithDependencies(ctx context.Context, configPath string, deps edgeRoleDependencies) error {
	cfg, err := initEdgeConfig(configPath)
	if err != nil {
		return err
	}
	log, cleanup, err := initEdgeLogger()
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	ctx = zerowrap.WithCtx(ctx, log)

	accessWriter, closeAccessLog, err := openEdgeAccessLog(cfg, log)
	if err != nil {
		return err
	}
	defer closeAccessLog()

	routes, connection, stopRoutes, err := startEdgeSnapshotClient(ctx, cfg.Control, deps, log)
	if err != nil {
		return err
	}
	defer stopRoutes()
	graphs := edgesnapshotclient.NewTrafficGraphClient(connection)
	if err := graphs.Start(ctx); err != nil {
		return log.WrapErr(err, "start edge traffic graph client")
	}
	defer graphs.Stop()
	err = runEdgeTraffic(ctx, cfg, deps, log, accessWriter, routes, graphs)
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	return err
}

func initEdgeLogger() (zerowrap.Logger, func(), error) {
	return zerowrap.New(zerowrap.Config{Level: "info", Format: "console"}), func() {}, nil
}

func openEdgeAccessLog(cfg EdgeConfig, log zerowrap.Logger) (out.AccessLogWriter, func(), error) {
	if !cfg.Logging.AccessLog.Enabled {
		return nil, func() {}, nil
	}
	writer, err := accesslog.New(accesslog.Config{
		Format:           cfg.Logging.AccessLog.Format,
		Output:           cfg.Logging.AccessLog.Output,
		FilePath:         cfg.Logging.AccessLog.FilePath,
		MaxSize:          cfg.Logging.AccessLog.MaxSize,
		MaxBackups:       cfg.Logging.AccessLog.MaxBackups,
		MaxAge:           cfg.Logging.AccessLog.MaxAge,
		SyslogIdentifier: cfg.Logging.AccessLog.SyslogIdentifier,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("initialize edge access log: %w", err)
	}
	log.Info().Str("format", cfg.Logging.AccessLog.Format).Str("output", cfg.Logging.AccessLog.Output).Msg("edge access log enabled")
	return writer, func() { _ = writer.Close() }, nil
}

func startEdgeSnapshotClient(ctx context.Context, control EdgeControlConfig, deps edgeRoleDependencies, log zerowrap.Logger) (*edgesnapshotclient.Client, *grpc.ClientConn, func(), error) {
	snapshotClient, connection, err := deps.dialSnapshot(ctx, control)
	if err != nil {
		return nil, nil, nil, log.WrapErr(err, "create edge route snapshot client")
	}
	if snapshotClient == nil || connection == nil {
		return nil, nil, nil, fmt.Errorf("edge route snapshot client and connection are required")
	}
	if err := snapshotClient.Start(ctx); err != nil {
		_ = connection.Close()
		return nil, nil, nil, log.WrapErr(err, "start edge route snapshot client")
	}
	return snapshotClient, connection, func() {
		snapshotClient.Stop()
		_ = connection.Close()
	}, nil
}

func listenEdge(cfg EdgeConfig, deps edgeRoleDependencies) (net.Listener, error) {
	listener, err := deps.listen("tcp", cfg.Edge.ListenAddress)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := edgeTLSConfig(cfg)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	if tlsConfig != nil {
		return tls.NewListener(listener, tlsConfig), nil
	}
	return listener, nil
}

func buildEdgeProxyConfig(cfg EdgeConfig) (proxy.Config, error) {
	bodySize, err := edgeByteSize(cfg.Edge.MaxProxyBodySize, 512<<20, "edge.max_proxy_body_size")
	if err != nil {
		return proxy.Config{}, err
	}
	responseSize, err := edgeByteSize(cfg.Edge.MaxProxyResponseSize, 1<<30, "edge.max_proxy_response_size")
	if err != nil {
		return proxy.Config{}, err
	}
	maxConnections := cfg.Edge.MaxConcurrentConns
	if maxConnections < 0 {
		maxConnections = 10000
	}
	return proxy.Config{
		RegistryDomain:     cfg.Edge.RegistryDomain,
		MaxBodySize:        bodySize,
		MaxResponseSize:    responseSize,
		MaxConcurrentConns: maxConnections,
	}, nil
}

func edgeByteSize(raw string, defaultSize int64, name string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultSize, nil
	}
	size, err := bytesize.Parse(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	return size, nil
}

// edgeHTTPHandler returns the unwrapped edge routes for focused handler tests.
// Production serving always uses edgeHTTPHandlerWithMiddleware.
func edgeHTTPHandler(proxyHandler http.Handler, snapshots *edgesnapshotclient.Client) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		if snapshots != nil && snapshots.SnapshotHealth().Healthy {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "edge snapshot unhealthy", http.StatusServiceUnavailable)
	})
	mux.Handle("/", proxyHandler)
	return mux
}

func edgeHTTPHandlerWithMiddleware(proxyHandler http.Handler, snapshots *edgesnapshotclient.Client, cfg EdgeConfig, log zerowrap.Logger, accessWriter out.AccessLogWriter) http.Handler {
	trustedNets := httphelper.ParseTrustedProxies(cfg.Edge.TrustedProxyCIDRs)
	middlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log, trustedNets),
		middleware.SecurityHeaders,
	}
	// External TLS termination has no safe direct plaintext path. The same
	// CIDRs that make forwarded headers trustworthy are the only permitted peers.
	// Unlike registry access, loopback is not implicitly trusted: operators must
	// explicitly list it when their terminating proxy connects from localhost.
	if strings.EqualFold(cfg.Edge.TLS.Mode, edgeTLSModeExternal) {
		middlewares = append(middlewares, middleware.StrictDirectPeerCIDRAllowlist(trustedNets, log))
	}
	handler := otelhttp.NewHandler(middleware.Chain(middlewares...)(edgeHTTPHandler(proxyHandler, snapshots)), "gordon.edge")
	if accessWriter != nil {
		handler = middleware.AccessLogger(accessWriter, cfg.Logging.AccessLog.ExcludeHealthChecks, log, trustedNets)(handler)
	}
	return handler
}

// newEdgeSnapshotClient dials control using TLS with normal hostname
// verification by default. Plaintext and insecure bearer credentials are only
// selected by the explicit control.insecure_tls opt-in.
func newEdgeSnapshotClient(_ context.Context, cfg EdgeControlConfig) (*edgesnapshotclient.Client, *grpc.ClientConn, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return nil, nil, fmt.Errorf("control.endpoint is required for edge role")
	}
	token := controlToken(cfg)
	if token == "" {
		return nil, nil, fmt.Errorf("control token is required for edge role")
	}
	transport := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	newBearer := grpcauth.NewBearerTokenCredentials
	if cfg.InsecureTLS {
		transport = insecure.NewCredentials()
		newBearer = grpcauth.NewInsecureBearerTokenCredentials
	}
	bearer, err := newBearer(token)
	if err != nil {
		return nil, nil, fmt.Errorf("create control bearer credentials: %w", err)
	}
	connection, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(transport), grpc.WithPerRPCCredentials(bearer))
	if err != nil {
		return nil, nil, fmt.Errorf("dial control snapshot service: %w", err)
	}
	return edgesnapshotclient.NewClient(connection), connection, nil
}

// controlToken keeps token precedence consistent with the runtime client:
// explicit config wins, then the explicitly named environment variable. The
// value is never included in edge logs.
func controlToken(cfg EdgeControlConfig) string {
	if token := strings.TrimSpace(cfg.Token); token != "" {
		return token
	}
	if envKey := strings.TrimSpace(cfg.TokenEnv); envKey != "" {
		return strings.TrimSpace(os.Getenv(envKey))
	}
	return ""
}

// runEdgeTraffic owns every split public traffic entrypoint. Control only
// supplies validated snapshots; no control configuration or runtime state is
// retained by this process.
func runEdgeTraffic(ctx context.Context, cfg EdgeConfig, deps edgeRoleDependencies, log zerowrap.Logger, accessWriter out.AccessLogWriter, routes *edgesnapshotclient.Client, graphs *edgesnapshotclient.TrafficGraphClient) error {
	handler, closeProxy, err := newEdgeTrafficProxy(cfg, log, accessWriter, routes)
	if err != nil {
		return err
	}
	defer closeProxy()
	if _, err := waitForInitialEdgeRouteSnapshot(ctx, routes); err != nil {
		return err
	}
	newManager := deps.newTrafficManager
	if newManager == nil {
		newManager = func() edgeTrafficManager { return trafficadapter.NewManager() }
	}
	manager := newManager()
	if manager == nil {
		return fmt.Errorf("edge traffic manager is required")
	}
	return runEdgeTrafficApplyLoop(ctx, cfg, deps, log, handler, graphs, manager)
}

// edgeTrafficGraphProvider is deliberately limited to the sanitized graph
// stream. The edge apply loop cannot obtain control configuration through it.
type edgeTrafficGraphProvider interface {
	CurrentTrafficGraph(context.Context) (domain.TrafficGraphSnapshot, error)
	SetTrafficGraphAcceptanceObserver(func(domain.TrafficGraphSnapshot))
	TrafficGraphHealth() edgesnapshotclient.TrafficGraphHealth
}

// edgeTrafficManager is the production listener manager surface used by the
// edge apply loop. Keeping this boundary here makes the loop testable without
// substituting its listener implementation.
type edgeTrafficManager interface {
	Apply(context.Context, *domain.TrafficGraph) error
	Shutdown(context.Context) error
	SetTLSHTTPServer(string, http.Handler, *tls.Config)
	SetSmartTCPHTTPServer(string, http.Handler, *http.Protocols)
	SetSmartTCPTLSServer(string, http.Handler, *tls.Config)
}

// runEdgeTrafficApplyLoop is the production split-edge traffic owner. It
// waits for the authenticated initial graph, installs smart/TLS HTTP fallbacks,
// applies subsequent accepted graphs, and drains listeners on shutdown.
func runEdgeTrafficApplyLoop(ctx context.Context, cfg EdgeConfig, deps edgeRoleDependencies, log zerowrap.Logger, handler http.Handler, graphs edgeTrafficGraphProvider, manager edgeTrafficManager) error {
	if graphs == nil || manager == nil {
		return fmt.Errorf("edge traffic graph provider and manager are required")
	}
	shutdownManager := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			log.Warn().Err(err).Msg("edge traffic manager shutdown error")
		}
	}
	defer shutdownManager()

	updates := edgeTrafficUpdateChannel(graphs)
	initialGraph, err := waitForInitialEdgeTrafficGraph(ctx, graphs)
	if err != nil {
		return err
	}
	if edgeDedicatedHTTPEnabled(cfg) && edgeGraphAddressConflict(cfg.Edge.ListenAddress, initialGraph.Graph.EntryPoints) {
		return fmt.Errorf("edge.listen_address conflicts with a streamed traffic entrypoint")
	}
	tlsConfig, err := edgeTLSConfig(cfg)
	if err != nil {
		return err
	}
	handlers, err := configureEdgeTrafficHandlers(manager, initialGraph.Graph, cfg, handler, tlsConfig, edgeTrafficHandlers{})
	if err == nil {
		err = manager.Apply(ctx, &initialGraph.Graph)
	}
	if err != nil {
		return fmt.Errorf("apply initial edge traffic graph: %w", err)
	}
	log.Info().Uint64("generation", uint64(initialGraph.Generation)).Bool("healthy", graphs.TrafficGraphHealth().Healthy).Msg("edge traffic graph applied")

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	go applyEdgeTrafficUpdates(runCtx, updates, manager, cfg, handler, tlsConfig, handlers, initialGraph.Generation, log)

	server, errCh, err := startEdgeDedicatedHTTP(cfg, deps, handler, log)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return shutdownEdgeHTTP(server)
	case serveErr := <-errCh:
		if serveErr == nil || serveErr == http.ErrServerClosed {
			return nil
		}
		return log.WrapErr(serveErr, "edge HTTP server stopped")
	}
}

func newEdgeTrafficProxy(cfg EdgeConfig, log zerowrap.Logger, accessWriter out.AccessLogWriter, routes *edgesnapshotclient.Client) (http.Handler, func(), error) {
	proxyCfg, err := buildEdgeProxyConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	proxyService := proxy.NewSnapshotService(routes, proxyCfg, routes)
	routes.SetSnapshotAcceptanceObserver(proxyService)
	handler := edgeHTTPHandlerWithMiddleware(httpproxy.NewHandler(proxyService, nil, log), routes, cfg, log, accessWriter)
	return handler, proxyService.Close, nil
}

func edgeTrafficUpdateChannel(graphs edgeTrafficGraphProvider) <-chan domain.TrafficGraphSnapshot {
	updates := make(chan domain.TrafficGraphSnapshot, 1)
	graphs.SetTrafficGraphAcceptanceObserver(func(snapshot domain.TrafficGraphSnapshot) {
		select {
		case updates <- snapshot:
		default:
			select {
			case <-updates:
			default:
			}
			select {
			case updates <- snapshot:
			default:
			}
		}
	})
	return updates
}

func startEdgeDedicatedHTTP(cfg EdgeConfig, deps edgeRoleDependencies, handler http.Handler, log zerowrap.Logger) (*http.Server, <-chan error, error) {
	if !edgeDedicatedHTTPEnabled(cfg) {
		return nil, nil, nil
	}
	listener, err := listenEdge(cfg, deps)
	if err != nil {
		return nil, nil, log.WrapErr(err, "listen for externally terminated edge HTTP")
	}
	server := deps.newHTTPServer(listener.Addr().String(), handler)
	errs := make(chan error, 1)
	go func() { errs <- server.Serve(listener) }()
	log.Info().Str("addr", listener.Addr().String()).Msg("gordon-edge external HTTP listener started")
	return server, errs, nil
}

func waitForInitialEdgeRouteSnapshot(ctx context.Context, routes *edgesnapshotclient.Client) (domain.RouteTargetSnapshot, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot, err := routes.CurrentSnapshot(ctx)
		if err == nil {
			return snapshot, nil
		}
		select {
		case <-ctx.Done():
			return domain.RouteTargetSnapshot{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func waitForInitialEdgeTrafficGraph(ctx context.Context, graphs edgeTrafficGraphProvider) (domain.TrafficGraphSnapshot, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot, err := graphs.CurrentTrafficGraph(ctx)
		if err == nil {
			return snapshot, nil
		}
		select {
		case <-ctx.Done():
			return domain.TrafficGraphSnapshot{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

type edgeTrafficHandlers struct {
	tlsMux map[string]struct{}
	smart  map[string]struct{}
}

func configureEdgeTrafficHandlers(manager edgeTrafficManager, graph domain.TrafficGraph, cfg EdgeConfig, handler http.Handler, tlsConfig *tls.Config, previous edgeTrafficHandlers) (edgeTrafficHandlers, error) {
	if err := validateEdgeTrafficTLSMode(cfg, graph); err != nil {
		return previous, err
	}
	next := edgeTrafficHandlers{tlsMux: map[string]struct{}{}, smart: map[string]struct{}{}}
	for _, entry := range graph.EntryPoints {
		switch entry.Protocol {
		case domain.EntryPointProtocolTLSMux:
			next.tlsMux[entry.Name] = struct{}{}
		case domain.EntryPointProtocolSmartTCP:
			next.smart[entry.Name] = struct{}{}
		}
	}
	for name := range previous.tlsMux {
		if _, ok := next.tlsMux[name]; !ok {
			manager.SetTLSHTTPServer(name, nil, nil)
		}
	}
	for name := range previous.smart {
		if _, ok := next.smart[name]; !ok {
			manager.SetSmartTCPHTTPServer(name, nil, nil)
			manager.SetSmartTCPTLSServer(name, nil, nil)
		}
	}
	var plainProtocols http.Protocols
	plainProtocols.SetHTTP1(true)
	plainProtocols.SetUnencryptedHTTP2(true)
	for name := range next.smart {
		manager.SetSmartTCPHTTPServer(name, handler, &plainProtocols)
		if tlsConfig != nil {
			manager.SetSmartTCPTLSServer(name, handler, tlsConfig)
		} else {
			manager.SetSmartTCPTLSServer(name, nil, nil)
		}
	}
	for name := range next.tlsMux {
		if tlsConfig != nil {
			manager.SetTLSHTTPServer(name, handler, tlsConfig)
		} else {
			manager.SetTLSHTTPServer(name, nil, nil)
		}
	}
	return next, nil
}

func validateEdgeTrafficTLSMode(cfg EdgeConfig, graph domain.TrafficGraph) error {
	if strings.EqualFold(cfg.Edge.TLS.Mode, edgeTLSModeFiles) {
		return nil
	}
	for _, router := range graph.Routers {
		if router.Protocol != domain.RouterProtocolHTTP {
			continue
		}
		for _, entry := range graph.EntryPoints {
			if entry.Name == router.EntryPoint && (entry.Protocol == domain.EntryPointProtocolTLSMux || entry.Protocol == domain.EntryPointProtocolSmartTCP) {
				return fmt.Errorf("edge.tls.mode=external cannot serve HTTP router %q on TLS-capable entrypoint %q without a local certificate; use the dedicated external HTTP listener or TLS passthrough", router.Name, entry.Name)
			}
		}
	}
	return nil
}

func applyEdgeTrafficUpdates(ctx context.Context, updates <-chan domain.TrafficGraphSnapshot, manager edgeTrafficManager, cfg EdgeConfig, handler http.Handler, tlsConfig *tls.Config, handlers edgeTrafficHandlers, lastGeneration domain.TrafficGraphGeneration, log zerowrap.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case snapshot := <-updates:
			if snapshot.Generation <= lastGeneration {
				continue
			}
			if edgeDedicatedHTTPEnabled(cfg) && edgeGraphAddressConflict(cfg.Edge.ListenAddress, snapshot.Graph.EntryPoints) {
				log.Error().Uint64("generation", uint64(snapshot.Generation)).Msg("edge traffic graph update conflicts with dedicated external HTTP listener; retaining last known-good graph")
				continue
			}
			next, err := configureEdgeTrafficHandlers(manager, snapshot.Graph, cfg, handler, tlsConfig, handlers)
			if err == nil {
				err = manager.Apply(ctx, &snapshot.Graph)
			}
			if err != nil {
				log.Error().Err(err).Uint64("generation", uint64(snapshot.Generation)).Msg("edge traffic graph update rejected; retaining last known-good graph")
				continue
			}
			handlers, lastGeneration = next, snapshot.Generation
		}
	}
}

func edgeDedicatedHTTPEnabled(cfg EdgeConfig) bool {
	return strings.EqualFold(cfg.Edge.TLS.Mode, edgeTLSModeExternal)
}

func edgeGraphAddressConflict(address string, entryPoints []domain.EntryPoint) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return true
	}
	for _, entry := range entryPoints {
		if entry.Protocol == domain.EntryPointProtocolUDP {
			continue
		}
		entryHost, entryPort, splitErr := net.SplitHostPort(entry.Address)
		if splitErr != nil || port != entryPort {
			continue
		}
		if host == entryHost || edgeWildcardHost(host) || edgeWildcardHost(entryHost) {
			return true
		}
	}
	return false
}

func edgeWildcardHost(host string) bool { return host == "" || host == "0.0.0.0" || host == "::" }

func shutdownEdgeHTTP(server *http.Server) error {
	if server == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return err
	}
	return nil
}
