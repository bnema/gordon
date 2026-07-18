package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	authhttp "github.com/bnema/gordon/internal/adapters/in/http/auth"
	"github.com/bnema/gordon/internal/adapters/in/http/middleware"
	registryhttp "github.com/bnema/gordon/internal/adapters/in/http/registry"
	"github.com/bnema/gordon/internal/adapters/out/eventoutbox"
	"github.com/bnema/gordon/internal/adapters/out/filesystem"
	grpcauth "github.com/bnema/gordon/internal/adapters/out/grpc/auth"
	componentevents "github.com/bnema/gordon/internal/adapters/out/grpc/events"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	registryusecase "github.com/bnema/gordon/internal/usecase/registry"
)

// registryRoleDependencies exposes only registry-owned construction. In
// particular it deliberately has no docker runtime, proxy, admin, or runtime
// endpoint factory.
type registryRoleDependencies struct {
	listen     func(string, string) (net.Listener, error)
	dialEvents func(context.Context, RegistryControlConfig) (out.ComponentEventPublisher, func(), error)
	newServer  func(string, http.Handler) *http.Server
}

func productionRegistryRoleDependencies() registryRoleDependencies {
	return registryRoleDependencies{listen: net.Listen, dialEvents: dialRegistryEvents, newServer: func(addr string, h http.Handler) *http.Server {
		return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 5 * time.Minute, WriteTimeout: 5 * time.Minute, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20}
	}}
}

// runRegistryImpl owns the standalone registry graph: storage, registry HTTP,
// auth, and the durable typed-event outbox. It must remain free of control
// business logic and all runtime/proxy/admin ownership.
func runRegistryImpl(ctx context.Context, configPath string) error {
	return runRegistryWithDependencies(ctx, configPath, productionRegistryRoleDependencies())
}
func runRegistryWithDependencies(ctx context.Context, configPath string, deps registryRoleDependencies) error {
	cfg, err := initRegistryConfig(configPath)
	if err != nil {
		return err
	}
	log := zerowrap.Default()
	ctx = zerowrap.WithCtx(ctx, log)
	publisher, closePublisher, err := deps.dialEvents(ctx, cfg.Control)
	if err != nil {
		return fmt.Errorf("create registry event publisher: %w", err)
	}
	defer closePublisher()
	outbox, err := eventoutbox.New(eventoutbox.Config{Dir: filepath.Join(cfg.Storage.DataDir, "registry", "event-outbox"), MaxEntries: cfg.Control.OutboxMaxEntries, MaxBytes: mustRegistrySize(cfg.Control.OutboxMaxBytes)}, publisher)
	if err != nil {
		return fmt.Errorf("create registry event outbox: %w", err)
	}
	outbox.Start(ctx)
	defer outbox.Close()
	blob, manifests, err := registryStorage(cfg, log)
	if err != nil {
		return err
	}
	service := registryusecase.NewServiceWithComponentEvents(blob, manifests, nil, outbox)
	handler, err := newRegistryRoleHandler(ctx, cfg, service, outbox, log)
	if err != nil {
		return err
	}
	listener, err := deps.listen("tcp", cfg.Listen.Address)
	if err != nil {
		return fmt.Errorf("listen for registry HTTP: %w", err)
	}
	defer listener.Close()
	listener, err = registryTLSListener(listener, cfg.Listen.TLS)
	if err != nil {
		return err
	}
	server := deps.newServer(listener.Addr().String(), handler)
	errs := make(chan error, 1)
	go func() { errs <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown registry HTTP: %w", err)
		}
		return nil
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("registry HTTP server stopped: %w", err)
	}
}
func mustRegistrySize(raw string) int64 {
	size, err := registrySize(raw, "outbox size")
	if err != nil {
		panic(err)
	}
	return size
}
func registryStorage(cfg RegistryConfig, log zerowrap.Logger) (*filesystem.BlobStorage, *filesystem.ManifestStorage, error) {
	root := filepath.Join(cfg.Storage.DataDir, "registry")
	blob, err := filesystem.NewBlobStorage(root, log)
	if err != nil {
		return nil, nil, fmt.Errorf("create registry blob storage: %w", err)
	}
	manifests, err := filesystem.NewManifestStorage(root, log)
	if err != nil {
		return nil, nil, fmt.Errorf("create registry manifest storage: %w", err)
	}
	return blob, manifests, nil
}
func registryTLSListener(listener net.Listener, cfg RegistryTLSConfig) (net.Listener, error) {
	if strings.EqualFold(cfg.Mode, registryTLSDisabled) {
		return listener, nil
	}
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("load registry TLS keypair: %w", err)
	}
	return tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}}), nil
}
func registryEventToken(cfg RegistryControlConfig) string {
	if token := strings.TrimSpace(cfg.EventToken); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv(cfg.EventTokenEnv))
}

func dialRegistryEvents(_ context.Context, cfg RegistryControlConfig) (out.ComponentEventPublisher, func(), error) {
	token := registryEventToken(cfg)
	if token == "" {
		return nil, nil, errors.New("control event token is required")
	}
	transport := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	newBearer := grpcauth.NewBearerTokenCredentials
	if cfg.InsecureTLS {
		transport = insecure.NewCredentials()
		newBearer = grpcauth.NewInsecureBearerTokenCredentials
	}
	bearer, err := newBearer(token)
	if err != nil {
		return nil, nil, fmt.Errorf("create event bearer credentials: %w", err)
	}
	conn, err := grpc.NewClient(cfg.EventEndpoint, grpc.WithTransportCredentials(transport), grpc.WithPerRPCCredentials(bearer))
	if err != nil {
		return nil, nil, fmt.Errorf("dial control event service: %w", err)
	}
	return componentevents.NewClient(conn), func() { _ = conn.Close() }, nil
}

func newRegistryRoleHandler(ctx context.Context, cfg RegistryConfig, service *registryusecase.Service, outbox *eventoutbox.Outbox, log zerowrap.Logger) (http.Handler, error) {
	chunk, err := registrySize(cfg.Limits.MaxBlobChunkSize, "limits.max_blob_chunk_size")
	if err != nil {
		return nil, err
	}
	total, err := registrySize(cfg.Limits.MaxBlobSize, "limits.max_blob_size")
	if err != nil {
		return nil, err
	}
	base := http.Handler(registryhttp.NewHandler(service, log, chunk, total))
	if len(cfg.Limits.AllowedIPs) > 0 {
		nets := make([]*net.IPNet, 0, len(cfg.Limits.AllowedIPs))
		for _, raw := range cfg.Limits.AllowedIPs {
			_, network, _ := net.ParseCIDR(raw)
			nets = append(nets, network)
		}
		base = middleware.RegistryCIDRAllowlist(nets, nil, log)(base)
	}
	if cfg.Auth.Enabled {
		authSvc, authHandler, err := registryRoleAuth(ctx, cfg, log)
		if err != nil {
			return nil, err
		}
		base = middleware.RegistryAuthV2(authSvc, middleware.InternalRegistryAuth{}, nil, log)(base)
		mux := http.NewServeMux()
		mux.Handle("/v2/", base)
		mux.Handle("/auth/", authHandler)
		base = mux
	} else {
		// Auth-disabled registry mode remains loopback-only, matching the
		// monolith safety contract without registering any admin surface.
		base = loopbackOnly(base, log)
	}
	mux := http.NewServeMux()
	mux.Handle("/v2/", base)
	mux.Handle("/auth/", base)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		if err := outbox.Healthy(); err != nil {
			http.Error(w, "registry event delivery unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return middleware.Chain(middleware.PanicRecovery(log), middleware.SecurityHeaders)(mux), nil
}
func registryRoleAuth(ctx context.Context, cfg RegistryConfig, log zerowrap.Logger) (in.AuthService, http.Handler, error) {
	compat := Config{}
	compat.Server.DataDir = cfg.Storage.DataDir
	compat.Auth.Enabled = cfg.Auth.Enabled
	compat.Auth.Type = cfg.Auth.Type
	compat.Auth.SecretsBackend = cfg.Auth.SecretsBackend
	compat.Auth.Username = cfg.Auth.Username
	compat.Auth.TokenSecret = cfg.Auth.TokenSecret
	compat.Auth.TokenExpiry = cfg.Auth.TokenExpiry
	compat.Auth.AccessTokenTTL = cfg.Auth.AccessTokenTTL
	_, service, err := createAuthService(ctx, compat, log)
	if err != nil {
		return nil, nil, err
	}
	return service, authhttp.NewHandler(service, authhttp.InternalAuth{}, log), nil
}
