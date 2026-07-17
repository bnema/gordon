package app

import (
	"context"
	"crypto/tls"
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
	"github.com/bnema/gordon/internal/adapters/out/accesslog"
	grpcauth "github.com/bnema/gordon/internal/adapters/out/grpc/auth"
	edgesnapshotclient "github.com/bnema/gordon/internal/adapters/out/grpc/edgesnapshot"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/usecase/proxy"
	"github.com/bnema/gordon/pkg/bytesize"
)

// edgeRoleDependencies contains the entire edge role surface. Its deliberate
// absence of runtime/socket/config-service constructors is an ownership guard:
// an edge consumes snapshots and proxies requests, nothing more.
type edgeRoleDependencies struct {
	listen        func(network, address string) (net.Listener, error)
	dialSnapshot  func(context.Context, EdgeControlConfig) (*edgesnapshotclient.Client, *grpc.ClientConn, error)
	newHTTPServer func(string, http.Handler) *http.Server
}

func productionEdgeRoleDependencies() edgeRoleDependencies {
	return edgeRoleDependencies{
		listen:       net.Listen,
		dialSnapshot: newEdgeSnapshotClient,
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

	snapshotClient, stopSnapshotClient, err := startEdgeSnapshotClient(ctx, cfg.Control, deps, log)
	if err != nil {
		return err
	}
	defer stopSnapshotClient()

	proxyCfg, err := buildEdgeProxyConfig(cfg)
	if err != nil {
		return err
	}
	proxyService := proxy.NewSnapshotService(snapshotClient, proxyCfg)
	proxyHandler := httpproxy.NewHandler(proxyService, nil, log)
	listener, err := listenEdge(cfg, deps)
	if err != nil {
		return log.WrapErr(err, "listen for edge HTTP")
	}
	defer listener.Close()

	server := deps.newHTTPServer(listener.Addr().String(), edgeHTTPHandlerWithMiddleware(proxyHandler, snapshotClient, cfg, log, accessWriter))
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()
	log.Info().Str("addr", listener.Addr().String()).Str("tls_mode", cfg.Edge.TLS.Mode).Msg("gordon-edge HTTP server started")

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	case serveErr := <-errCh:
		if serveErr == http.ErrServerClosed {
			return nil
		}
		return log.WrapErr(serveErr, "edge HTTP server stopped")
	}
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

func startEdgeSnapshotClient(ctx context.Context, control EdgeControlConfig, deps edgeRoleDependencies, log zerowrap.Logger) (*edgesnapshotclient.Client, func(), error) {
	snapshotClient, connection, err := deps.dialSnapshot(ctx, control)
	if err != nil {
		return nil, nil, log.WrapErr(err, "create edge route snapshot client")
	}
	if snapshotClient == nil || connection == nil {
		return nil, nil, fmt.Errorf("edge route snapshot client and connection are required")
	}
	if err := snapshotClient.Start(ctx); err != nil {
		_ = connection.Close()
		return nil, nil, log.WrapErr(err, "start edge route snapshot client")
	}
	return snapshotClient, func() {
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
	if strings.EqualFold(cfg.Edge.TLS.Mode, edgeTLSModeExternal) {
		middlewares = append(middlewares, middleware.ProxyCIDRAllowlist(trustedNets, log))
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
