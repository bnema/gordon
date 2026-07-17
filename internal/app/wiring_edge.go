package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	httpproxy "github.com/bnema/gordon/internal/adapters/in/http/proxy"
	grpcauth "github.com/bnema/gordon/internal/adapters/out/grpc/auth"
	edgesnapshotclient "github.com/bnema/gordon/internal/adapters/out/grpc/edgesnapshot"
	"github.com/bnema/gordon/internal/usecase/proxy"
)

// edgeRoleDependencies contains the entire edge role surface. Its deliberate
// absence of runtime/socket/config-service constructors is an ownership guard:
// an edge consumes snapshots and proxies requests, nothing more.
type edgeRoleDependencies struct {
	listen        func(network, address string) (net.Listener, error)
	dialSnapshot  func(context.Context, ControlConfig) (*edgesnapshotclient.Client, *grpc.ClientConn, error)
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

// runEdgeImpl starts the minimal Phase 4 edge HTTP role. Public TLS and traffic
// manager ownership intentionally remain Phase 6: this role does not create a
// public TLS listener or silently downgrade one to plaintext.
func runEdgeImpl(ctx context.Context, configPath string) error {
	return runEdgeWithDependencies(ctx, configPath, productionEdgeRoleDependencies())
}

func runEdgeWithDependencies(ctx context.Context, configPath string, deps edgeRoleDependencies) error {
	v, cfg, err := initConfig(configPath)
	if err != nil {
		return err
	}
	log, cleanup, err := initLogger(cfg)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	ctx = zerowrap.WithCtx(ctx, log)
	warnDeprecatedConfigKeys(v, log)

	snapshotClient, connection, err := deps.dialSnapshot(ctx, cfg.Control)
	if err != nil {
		return log.WrapErr(err, "create edge route snapshot client")
	}
	if snapshotClient == nil || connection == nil {
		return fmt.Errorf("edge route snapshot client and connection are required")
	}
	defer connection.Close()
	if err := snapshotClient.Start(ctx); err != nil {
		return log.WrapErr(err, "start edge route snapshot client")
	}
	defer snapshotClient.Stop()

	proxyConfig, err := buildProxyConfig(cfg, log)
	if err != nil {
		return err
	}
	proxyService := proxy.NewSnapshotService(snapshotClient, proxyConfig.proxyConfig)
	proxyHandler := httpproxy.NewHandler(proxyService, nil, log)
	listener, err := deps.listen("tcp", edgeHTTPListenAddress(cfg))
	if err != nil {
		return log.WrapErr(err, "listen for edge HTTP")
	}
	defer listener.Close()

	server := deps.newHTTPServer(listener.Addr().String(), edgeHTTPHandler(proxyHandler, snapshotClient))
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()
	log.Info().Str("addr", listener.Addr().String()).Msg("gordon-edge minimal HTTP server started; public TLS is deferred to Phase 6")

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

func edgeHTTPListenAddress(cfg Config) string {
	return net.JoinHostPort("", strconv.Itoa(cfg.Server.Port))
}

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

// newEdgeSnapshotClient dials control using TLS with normal hostname
// verification by default. Plaintext and insecure bearer credentials are only
// selected by the explicit control.insecure_tls opt-in.
func newEdgeSnapshotClient(_ context.Context, cfg ControlConfig) (*edgesnapshotclient.Client, *grpc.ClientConn, error) {
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
// explicit config wins, then the explicitly named environment variable.
func controlToken(cfg ControlConfig) string {
	if token := strings.TrimSpace(cfg.Token); token != "" {
		return token
	}
	if envKey := strings.TrimSpace(cfg.TokenEnv); envKey != "" {
		return strings.TrimSpace(os.Getenv(envKey))
	}
	return ""
}
