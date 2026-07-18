package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/bnema/gordon/internal/adapters/in/http/admin"
	authhandler "github.com/bnema/gordon/internal/adapters/in/http/auth"
	"github.com/bnema/gordon/internal/adapters/in/http/httphelper"
	"github.com/bnema/gordon/internal/adapters/in/http/middleware"
	registryhttp "github.com/bnema/gordon/internal/adapters/in/http/registry"
	"github.com/bnema/gordon/internal/adapters/out/ratelimit"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	configusecase "github.com/bnema/gordon/internal/usecase/config"
	imagesusecase "github.com/bnema/gordon/internal/usecase/images"
	logsusecase "github.com/bnema/gordon/internal/usecase/logs"
	secretsusecase "github.com/bnema/gordon/internal/usecase/secrets"
	volumesusecase "github.com/bnema/gordon/internal/usecase/volumes"
)

// newControlRoleServices constructs only control-owned services. In particular
// it does not call createOutputAdapters or createStorage: container sockets,
// registry storage, and public traffic listeners belong to other roles.
func newControlRoleServices(ctx context.Context, v *viper.Viper, cfg Config, log zerowrap.Logger, configPath string, preflightProduction PreflightProductionDependencies) (*services, error) {
	configSvc := configusecase.NewService(v, nil)
	if err := configSvc.Load(ctx); err != nil {
		return nil, fmt.Errorf("load control configuration: %w", err)
	}
	svc := &services{role: RoleControl, configSvc: configSvc}
	var err error
	// Existing gRPC-only control installations do not need user token storage.
	// Enabling the management listener makes auth mandatory and initializes the
	// same token-management service used by the monolith.
	if strings.TrimSpace(cfg.Control.HTTP.ListenAddress) != "" {
		if svc.tokenStore, svc.authSvc, err = createAuthService(ctx, cfg, log); err != nil {
			return nil, err
		}
		if svc.authSvc == nil {
			return nil, fmt.Errorf("control HTTP requires auth.enabled=true")
		}
		svc.authHandler = authhandler.NewHandler(svc.authSvc, authhandler.InternalAuth{}, log)
	}
	if svc.runtimeCommandClient, err = createRuntimeCommandClient(ctx, cfg.Runtime); err != nil {
		return nil, err
	}
	initRuntimeControlFacade(svc)
	svc.healthSvc = newControlHealthService(configSvc, svc.runtimeControl)
	svc.reloadCoordinator = newReloadCoordinator(v, configSvc, nil, nil, nil, nil, log)
	if err := wireControlManagementFacades(cfg, svc, log); err != nil {
		return nil, err
	}
	checkpointStore, err := NewMigrationCheckpointStore(filepath.Join(resolveDataDir(cfg.Server.DataDir), "migration", "checkpoint.json"))
	if err != nil {
		return nil, fmt.Errorf("create migration checkpoint store: %w", err)
	}
	// Control receives only the sanitized runtime RPC probe. It never opens a
	// Docker-compatible socket or constructs a local runtime adapter.
	runtimeProbe, _ := svc.runtimeCommandClient.(out.RuntimeEnvironmentProbe)
	runtimeInventory, _ := svc.runtimeCommandClient.(out.RuntimeStateSubscriber)
	preflight, err := preflightProduction.build(configPath, cfg, runtimeProbe, runtimeInventory)
	if err != nil {
		return nil, fmt.Errorf("create production migration preflight: %w", err)
	}
	svc.migrationSvc, err = NewMigrationService(preflight, checkpointStore, MigrationEnvOptions{
		Config:      cfg,
		Environment: componentEnvironmentFromEnviron(os.Environ()),
		Directory:   filepath.Join(resolveDataDir(cfg.Server.DataDir), "migration", "env"),
	})
	if err != nil {
		return nil, fmt.Errorf("create migration service: %w", err)
	}
	if err := wireControlMigrationRuntime(svc, preflight, checkpointStore, runtimeInventory); err != nil {
		return nil, err
	}
	svc.adminHandler = admin.NewHandler(admin.HandlerDeps{
		ConfigSvc:      svc.configSvc,
		AuthSvc:        svc.authSvc,
		SecretSvc:      svc.secretSvc,
		HealthSvc:      svc.healthSvc,
		LogSvc:         svc.logSvc,
		ImageSvc:       svc.imageSvc,
		VolumeSvc:      svc.volumeSvc,
		ReloadTrigger:  svc.reloadCoordinator,
		RuntimeControl: svc.runtimeControl,
		NetworkSvc:     controlNetworkService{runtime: controlRuntimeStateSubscriber(svc.runtimeCommandClient)},
		TrafficSvc:     nil,
		MigrationPlan:  func(ctx context.Context) (any, error) { return svc.migrationSvc.Plan(ctx) },
		MigrationPlanFailed: func(result any) bool {
			report, ok := result.(MigrationPreflightReport)
			return ok && !report.Ready
		},
		MigrationPrepare: func(ctx context.Context) (any, error) { return svc.migrationSvc.Prepare(ctx, MigrationCheckpoint{}) },
		MigrationSwitch:  func(ctx context.Context) (any, error) { return svc.migrationSvc.Switch(ctx) },
		MigrationStatus:  func(context.Context) (any, error) { return svc.migrationSvc.Status() },
		MigrationResume:  func(ctx context.Context) (any, error) { return svc.migrationSvc.Resume(ctx) },
		Log:              log,
	})
	return svc, nil
}

// wireControlManagementFacades supplies management operations through the
// narrow runtime RPC client and control-owned secret/config stores. It never
// creates local registry storage, a proxy, or a container runtime.
func controlRuntimeStateSubscriber(client out.RuntimeCommandClient) out.RuntimeStateSubscriber {
	subscriber, _ := client.(out.RuntimeStateSubscriber)
	return subscriber
}

func wireControlManagementFacades(cfg Config, svc *services, log zerowrap.Logger) error {
	if svc == nil {
		return fmt.Errorf("control services are required")
	}
	_, _, _, secretStore, err := createDomainSecretStore(cfg, log)
	if err != nil {
		return fmt.Errorf("create control secret store: %w", err)
	}
	svc.secretSvc = secretsusecase.NewService(secretStore, log, nil)

	if svc.runtimeCommandClient == nil {
		return nil
	}
	if runtimeLogs, ok := svc.runtimeCommandClient.(out.RuntimeLogReader); ok {
		svc.logSvc = logsusecase.NewServiceWithRuntimeLogReader(resolveLogFilePath(cfg), cfg.Logging.File.Enabled, nil, runtimeLogs, log)
	}
	if runtimeImages, ok := svc.runtimeCommandClient.(out.RuntimeImageManager); ok {
		// Registry data belongs to gordon-registry. The image service treats nil
		// stores as runtime-only and returns a stable unsupported error for a
		// registry prune request rather than reaching into registry storage.
		svc.imageSvc = imagesusecase.NewServiceWithRuntimeImageManager(runtimeImages, nil, nil, log)
	}
	if runtimeVolumes, ok := svc.runtimeCommandClient.(out.RuntimeVolumeManager); ok {
		svc.volumeSvc = volumesusecase.NewServiceWithRuntimeVolumeManager(runtimeVolumes)
	}
	return nil
}

// controlHTTPHandler intentionally shares the auth and admin adapters with the
// monolith. It does not apply monolith's loopback-only wrapper: this is the
// authenticated remote API endpoint used by remote CLI clients.
func controlHTTPHandler(svc *services, cfg Config, log zerowrap.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, request *http.Request) {
		if svc == nil || svc.configSvc == nil || svc.runtimeControl == nil {
			http.Error(w, "control unavailable", http.StatusServiceUnavailable)
			return
		}
		// A control listener is not healthy merely because it can accept TCP:
		// verify the control-to-runtime facade used by both status and deploy.
		checkCtx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		if _, err := svc.runtimeControl.RouteStatuses(checkCtx, nil); err != nil {
			http.Error(w, "control unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if svc == nil || svc.authHandler == nil || svc.adminHandler == nil || svc.authSvc == nil || !cfg.Auth.Enabled {
		return mux
	}

	trustedNets := httphelper.ParseTrustedProxies(cfg.API.RateLimit.TrustedProxies)
	authRate := registryhttp.RateLimitMiddleware(nil, nil, cfg.API.RateLimit.TrustedProxies, log)
	if !cfg.API.RateLimit.Enabled {
		authRate = registryhttp.RateLimitMiddleware(
			ratelimit.NewMemoryStore(50, 100, log),
			ratelimit.NewMemoryStore(5, 10, log),
			cfg.API.RateLimit.TrustedProxies, log,
		)
	}
	authChain := middleware.Chain(
		middleware.PanicRecovery(log), middleware.RequestLogger(log, trustedNets), middleware.SecurityHeaders, authRate,
	)(svc.authHandler)
	mux.Handle("/auth/", otelhttp.NewHandler(authChain, "gordon.control.auth"))

	var globalLimiter, ipLimiter out.RateLimiter
	if cfg.API.RateLimit.Enabled {
		globalLimiter = ratelimit.NewMemoryStore(cfg.API.RateLimit.GlobalRPS, cfg.API.RateLimit.Burst, log)
		ipLimiter = ratelimit.NewMemoryStore(cfg.API.RateLimit.PerIPRPS, cfg.API.RateLimit.Burst, log)
	}
	adminChain := middleware.Chain(
		middleware.PanicRecovery(log), middleware.RequestLogger(log, trustedNets), middleware.SecurityHeaders,
		admin.AuthMiddleware(svc.authSvc, globalLimiter, ipLimiter, trustedNets, log),
	)(svc.adminHandler)
	mux.Handle("/admin/", otelhttp.NewHandler(adminChain, "gordon.control.admin"))
	return mux
}

// wireControlMigrationRuntime composes only authenticated runtime clients. A
// missing endpoint or WS05 split deploy/drain checker leaves cutover disabled;
// this function never falls back to a local Docker-compatible adapter/socket.
func wireControlMigrationRuntime(svc *services, preflight *MigrationPreflight, checkpointStore *MigrationCheckpointStore, runtimeInventory out.RuntimeStateSubscriber) error {
	updater, ok := svc.runtimeCommandClient.(out.RuntimeSelfUpdater)
	if !ok {
		return nil
	}
	launcher, err := NewRuntimeComponentLauncher(updater)
	if err != nil {
		return fmt.Errorf("create runtime component launcher: %w", err)
	}
	orchestrator, err := NewMigrationOrchestrator(preflight, checkpointStore, launcher)
	if err != nil {
		return fmt.Errorf("create migration orchestrator: %w", err)
	}
	orchestrator.WithRuntimeSnapshotAppNetworks(runtimeInventory)
	// Cutover stays disabled until the authenticated control/runtime client
	// also exposes the real split edge checks. This is the WS05 deploy/drain
	// gate; control never substitutes local socket probes.
	if checks, ok := svc.runtimeCommandClient.(TrafficSwitchChecks); ok {
		switcher, switchErr := NewTrafficSwitch(updater, checks)
		if switchErr != nil {
			return fmt.Errorf("create migration traffic switch: %w", switchErr)
		}
		orchestrator.WithTrafficSwitcher(switcher)
	}
	svc.migrationSvc.WithMigrationOrchestrator(orchestrator)
	return nil
}

// controlHealthService translates the runtime status facade into the existing
// admin health DTO contract without obtaining a container runtime or socket.
type controlHealthService struct {
	config  in.ConfigService
	runtime interface {
		RouteStatuses(context.Context, []domain.Route) (map[string]string, error)
	}
}

func newControlHealthService(configSvc in.ConfigService, runtime interface {
	RouteStatuses(context.Context, []domain.Route) (map[string]string, error)
}) *controlHealthService {
	return &controlHealthService{config: configSvc, runtime: runtime}
}

func (s *controlHealthService) CheckRoute(ctx context.Context, route domain.Route) *domain.RouteHealth {
	result := &domain.RouteHealth{Domain: route.Domain, ContainerStatus: "unavailable"}
	if s == nil || s.runtime == nil {
		result.Error = "runtime unavailable"
		return result
	}
	statuses, err := s.runtime.RouteStatuses(ctx, []domain.Route{route})
	if err != nil {
		result.Error = "runtime unavailable"
		return result
	}
	result.ContainerStatus = statuses[route.Domain]
	result.Healthy = result.ContainerStatus == string(domain.ContainerStatusRunning)
	if !result.Healthy {
		result.Error = "container is " + result.ContainerStatus
	}
	return result
}

func (s *controlHealthService) CheckAllRoutes(ctx context.Context) map[string]*domain.RouteHealth {
	results := map[string]*domain.RouteHealth{}
	if s == nil || s.config == nil {
		return results
	}
	for _, route := range s.config.GetRoutes(ctx) {
		results[route.Domain] = s.CheckRoute(ctx, route)
	}
	return results
}

func controlHTTPListener(cfg Config, listen func(string, string) (net.Listener, error)) (net.Listener, error) {
	address := strings.TrimSpace(cfg.Control.HTTP.ListenAddress)
	if address == "" {
		return nil, nil
	}
	if sameListenPort(address, cfg.Control.ListenAddress) {
		return nil, fmt.Errorf("control.http.listen_address must not share control.listen_address port")
	}
	listener, err := listen("tcp", address)
	if err != nil {
		return nil, err
	}
	if cfg.Control.HTTP.InsecureTLS {
		return listener, nil
	}
	if cfg.Control.HTTP.TLSCertFile == "" || cfg.Control.HTTP.TLSKeyFile == "" {
		_ = listener.Close()
		return nil, fmt.Errorf("control HTTP TLS requires control.http.tls_cert_file and control.http.tls_key_file; set control.http.insecure_tls=true only for explicit private test/plaintext use")
	}
	certificate, err := tls.LoadX509KeyPair(cfg.Control.HTTP.TLSCertFile, cfg.Control.HTTP.TLSKeyFile)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("load control HTTP TLS certificate: %w", err)
	}
	return tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}), nil
}

func sameListenPort(left, right string) bool {
	_, leftPort, leftErr := net.SplitHostPort(left)
	_, rightPort, rightErr := net.SplitHostPort(right)
	// Port zero asks the kernel to choose an ephemeral port for each listener;
	// two such requests do not share a socket and must remain valid for tests
	// and private dynamic deployments.
	return leftErr == nil && rightErr == nil && leftPort != "0" && rightPort != "0" && leftPort == rightPort
}

func newControlHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}
