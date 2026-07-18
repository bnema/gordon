package app

import (
	"context"
	"crypto/subtle"
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
	"google.golang.org/grpc/status"

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
		newTrafficManager: func() edgeTrafficManager { return trafficadapter.NewSplitManager() },
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
		RegistryDomain:       cfg.Edge.RegistryDomain,
		RegistryForwardToken: strings.TrimSpace(os.Getenv(cfg.Edge.RegistryForwardTokenEnv)),
		MaxBodySize:          bodySize,
		MaxResponseSize:      responseSize,
		MaxConcurrentConns:   maxConnections,
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

func edgeHTTPHandlerWithHealth(proxyHandler http.Handler, snapshots *edgesnapshotclient.Client, health *edgeAggregateHealth) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		if health != nil && health.healthy() || health == nil && snapshots != nil && snapshots.SnapshotHealth().Healthy {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Health probes intentionally receive no transport, apply, or certificate
		// detail that could disclose edge topology or filesystem state.
		http.Error(w, "edge unavailable", http.StatusServiceUnavailable)
	})
	mux.Handle("/", proxyHandler)
	return mux
}

func edgeHTTPHandlerWithMiddleware(proxyHandler http.Handler, snapshots *edgesnapshotclient.Client, cfg EdgeConfig, log zerowrap.Logger, accessWriter out.AccessLogWriter) http.Handler {
	return edgeHTTPHandlerWithMiddlewareAndHealth(proxyHandler, snapshots, nil, cfg, log, accessWriter)
}

func edgeHTTPHandlerWithMiddlewareAndHealth(proxyHandler http.Handler, snapshots *edgesnapshotclient.Client, health *edgeAggregateHealth, cfg EdgeConfig, log zerowrap.Logger, accessWriter out.AccessLogWriter) http.Handler {
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
		middlewares = append(middlewares, migrationProbeOrStrictDirectPeerCIDRAllowlist(cfg, trustedNets, log))
	}
	handler := otelhttp.NewHandler(middleware.Chain(middlewares...)(edgeHTTPHandlerWithHealth(proxyHandler, snapshots, health)), "gordon.edge")
	if accessWriter != nil {
		handler = middleware.AccessLogger(accessWriter, cfg.Logging.AccessLog.ExcludeHealthChecks, log, trustedNets)(handler)
	}
	return handler
}

const migrationProbeHeader = "X-Gordon-Migration-Probe"

// migrationProbeOrStrictDirectPeerCIDRAllowlist keeps the normal external-TLS
// direct-peer boundary intact. A prepared edge may bypass it only for a
// constant-time validated, dedicated migration credential; it still traverses
// all later security and proxy routing middleware. Every missing or invalid
// credential is handled by the existing strict middleware and remains 403.
func migrationProbeOrStrictDirectPeerCIDRAllowlist(cfg EdgeConfig, trustedNets []*net.IPNet, log zerowrap.Logger) func(http.Handler) http.Handler {
	strict := middleware.StrictDirectPeerCIDRAllowlist(trustedNets, log)
	if !cfg.Edge.MigrationProbeEnabled {
		return strict
	}
	token := strings.TrimSpace(os.Getenv(cfg.Edge.MigrationProbeTokenEnv))
	return func(next http.Handler) http.Handler {
		strictNext := strict(next)
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			provided := request.Header.Get(migrationProbeHeader)
			if token != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1 {
				next.ServeHTTP(writer, request)
				return
			}
			strictNext.ServeHTTP(writer, request)
		})
	}
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
	if _, err := waitForInitialEdgeRouteSnapshot(ctx, routes); err != nil {
		return err
	}
	newManager := deps.newTrafficManager
	if newManager == nil {
		newManager = func() edgeTrafficManager { return trafficadapter.NewSplitManager() }
	}
	manager := newManager()
	if manager == nil {
		return fmt.Errorf("edge traffic manager is required")
	}
	tlsConfig, certificateHealth, err := edgeTLSConfigWithReloader(cfg)
	if err != nil {
		return err
	}
	health := newEdgeAggregateHealth(routes, graphs, manager, certificateHealth)
	handler, closeProxy, err := newEdgeTrafficProxy(cfg, log, accessWriter, routes, health)
	if err != nil {
		return err
	}
	defer closeProxy()
	return runEdgeTrafficApplyLoop(ctx, cfg, deps, log, handler, routes, graphs, manager, tlsConfig, health)
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
	ApplyWithServers(context.Context, *domain.TrafficGraph, trafficadapter.ServerConfigBundle) error
	Status() domain.TrafficStatus
	Shutdown(context.Context) error
}

// RunEdgeTrafficApplyLoop is the production split-edge traffic owner. It is
// intentionally callable from internal integration fixtures so they exercise
// the identical authenticated-stream-to-listener-manager path as runEdgeImpl.
func RunEdgeTrafficApplyLoop(ctx context.Context, cfg EdgeConfig, handler http.Handler, graphs edgeTrafficGraphProvider, manager edgeTrafficManager) error {
	tlsConfig, certificateHealth, err := edgeTLSConfigWithReloader(cfg)
	if err != nil {
		return err
	}
	return runEdgeTrafficApplyLoop(ctx, cfg, productionEdgeRoleDependencies(), zerowrap.FromCtx(ctx), handler, nil, graphs, manager, tlsConfig, newEdgeAggregateHealth(nil, graphs, manager, certificateHealth))
}

// runEdgeTrafficApplyLoop waits for the authenticated initial graph, installs
// smart/TLS HTTP fallbacks, applies subsequent accepted graphs, and drains
// listeners on shutdown.
func runEdgeTrafficApplyLoop(ctx context.Context, cfg EdgeConfig, deps edgeRoleDependencies, log zerowrap.Logger, handler http.Handler, routes *edgesnapshotclient.Client, graphs edgeTrafficGraphProvider, manager edgeTrafficManager, tlsConfig *tls.Config, health *edgeAggregateHealth) error {
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
	defer health.stop()

	updates := edgeTrafficUpdateChannel(graphs)
	initialGraph, err := waitForInitialEdgeTrafficGraph(ctx, graphs)
	if err != nil {
		return err
	}
	if edgeDedicatedHTTPEnabled(cfg) && edgeGraphAddressConflict(cfg.Edge.ListenAddress, initialGraph.Graph.EntryPoints) {
		return fmt.Errorf("edge.listen_address conflicts with a streamed traffic entrypoint")
	}
	health.beginApply(initialGraph.Generation)
	servers, err := edgeTrafficServerConfigs(initialGraph.Graph, cfg, handler, tlsConfig)
	if err == nil {
		err = manager.ApplyWithServers(ctx, &initialGraph.Graph, servers)
	}
	health.completeApply(initialGraph.Generation, err)
	if err != nil {
		return fmt.Errorf("apply initial edge traffic graph: %w", err)
	}
	log.Info().Uint64("generation", uint64(initialGraph.Generation)).Bool("healthy", graphs.TrafficGraphHealth().Healthy).Msg("edge traffic graph applied")
	if routes != nil {
		go reportAppliedEdgeGeneration(ctx, routes, initialGraph.Generation, log)
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	go applyEdgeTrafficUpdates(runCtx, updates, manager, cfg, handler, tlsConfig, initialGraph.Generation, health, routes, log)

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

func newEdgeTrafficProxy(cfg EdgeConfig, log zerowrap.Logger, accessWriter out.AccessLogWriter, routes *edgesnapshotclient.Client, health *edgeAggregateHealth) (http.Handler, func(), error) {
	proxyCfg, err := buildEdgeProxyConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	proxyService := proxy.NewSnapshotService(routes, proxyCfg, routes)
	routes.SetSnapshotAcceptanceObserver(proxyService)
	handler := edgeHTTPHandlerWithMiddlewareAndHealth(httpproxy.NewHandler(proxyService, nil, log, proxyCfg.RegistryForwardToken), routes, health, cfg, log, accessWriter)
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

func edgeTrafficServerConfigs(graph domain.TrafficGraph, cfg EdgeConfig, handler http.Handler, tlsConfig *tls.Config) (trafficadapter.ServerConfigBundle, error) {
	if err := validateEdgeTrafficTLSMode(cfg, graph); err != nil {
		return trafficadapter.ServerConfigBundle{}, err
	}
	configs := trafficadapter.ServerConfigBundle{
		TLSHTTP:   map[string]trafficadapter.TLSHTTPServerConfig{},
		SmartHTTP: map[string]trafficadapter.SmartTCPHTTPServerConfig{},
		SmartTLS:  map[string]trafficadapter.SmartTCPTLSServerConfig{},
	}
	var plainProtocols http.Protocols
	plainProtocols.SetHTTP1(true)
	plainProtocols.SetUnencryptedHTTP2(true)
	for _, entry := range graph.EntryPoints {
		switch entry.Protocol {
		case domain.EntryPointProtocolTLSMux:
			if tlsConfig != nil {
				configs.TLSHTTP[entry.Name] = trafficadapter.TLSHTTPServerConfig{Handler: handler, TLSConfig: tlsConfig}
			}
		case domain.EntryPointProtocolSmartTCP:
			configs.SmartHTTP[entry.Name] = trafficadapter.SmartTCPHTTPServerConfig{Handler: handler, Protocols: &plainProtocols}
			if tlsConfig != nil {
				configs.SmartTLS[entry.Name] = trafficadapter.SmartTCPTLSServerConfig{Handler: handler, TLSConfig: tlsConfig}
			}
		}
	}
	return configs, nil
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

func applyEdgeTrafficUpdates(ctx context.Context, updates <-chan domain.TrafficGraphSnapshot, manager edgeTrafficManager, cfg EdgeConfig, handler http.Handler, tlsConfig *tls.Config, lastGeneration domain.TrafficGraphGeneration, health *edgeAggregateHealth, routes *edgesnapshotclient.Client, log zerowrap.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case snapshot := <-updates:
			if snapshot.Generation <= lastGeneration {
				continue
			}
			health.beginApply(snapshot.Generation)
			var err error
			if edgeDedicatedHTTPEnabled(cfg) && edgeGraphAddressConflict(cfg.Edge.ListenAddress, snapshot.Graph.EntryPoints) {
				err = fmt.Errorf("traffic graph update conflicts with dedicated external HTTP listener")
			} else {
				servers, configErr := edgeTrafficServerConfigs(snapshot.Graph, cfg, handler, tlsConfig)
				err = configErr
				if err == nil {
					err = manager.ApplyWithServers(ctx, &snapshot.Graph, servers)
				}
			}
			health.completeApply(snapshot.Generation, err)
			if err != nil {
				log.Error().Err(err).Uint64("generation", uint64(snapshot.Generation)).Msg("edge traffic graph update rejected; retaining last known-good graph")
				continue
			}
			lastGeneration = snapshot.Generation
			if routes != nil {
				go reportAppliedEdgeGeneration(ctx, routes, snapshot.Generation, log)
			}
		}
	}
}

// reportAppliedEdgeGeneration sends a readiness acknowledgement only after the
// listener manager has accepted a graph and the independently received route
// snapshot has the identical generation. Component identity is injected by the
// runtime into the immutable role environment; a missing identity deliberately
// leaves control's cutover tracker unsatisfied.
func reportAppliedEdgeGeneration(ctx context.Context, routes *edgesnapshotclient.Client, generation domain.TrafficGraphGeneration, log zerowrap.Logger) {
	componentID := strings.TrimSpace(os.Getenv("GORDON_COMPONENT_ID"))
	if componentID == "" {
		log.Warn().Msg("edge applied state not reported: component identity is unavailable")
		return
	}
	var lastLoggedCode string
	var lastLoggedAt time.Time
	for {
		if ctx.Err() != nil {
			return
		}
		snapshot, err := routes.CurrentSnapshot(ctx)
		if err == nil && snapshot.Generation == domain.RouteTargetGeneration(generation) {
			err = routes.ReportAppliedState(ctx, componentID, uint64(snapshot.Generation), uint64(generation), true)
		}
		if err == nil {
			return
		}
		// Logs are deliberately bounded and disclose only the gRPC status code:
		// request identity, endpoint, and credentials must not leak from a retry.
		code := status.Code(err).String()
		if code != lastLoggedCode || time.Since(lastLoggedAt) >= 5*time.Second {
			log.Warn().Str("grpc_code", code).Msg("edge applied state report rejected")
			lastLoggedCode, lastLoggedAt = code, time.Now()
		}
		// Keep retrying through gRPC reconnects. The report is idempotent at an
		// equal generation and tracker monotonicity rejects any later regression.
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
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
