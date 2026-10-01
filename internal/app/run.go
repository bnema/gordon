// Package app provides the application initialization and wiring.
//
// run.go owns the daemon lifecycle (Run, server start, readiness). Each
// concern it orders lives in its own file: config.go (load/validate),
// services.go (service construction), app_wiring.go (apps engine),
// auth_secrets.go and credentials.go (auth and internal registry
// credentials), http_edge.go (handlers and middleware), listeners.go
// (proxy/TLS/registry listeners), reload.go (hot reload), schedulers.go,
// shutdown.go, and pidfile.go.
package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bnema/zerowrap"
	zerowrapotel "github.com/bnema/zerowrap/otel"
	"github.com/spf13/viper"

	"github.com/bnema/gordon/internal/adapters/out/accesslog"
	"github.com/bnema/gordon/internal/adapters/out/telemetry"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/usecase/logexport"
	"github.com/bnema/gordon/pkg/version"
)

// Run initializes and starts the Gordon application.
func Run(ctx context.Context, configPath string) error {
	// Load configuration
	v, cfg, err := initConfig(configPath)
	if err != nil {
		return err
	}

	// Initialize logger
	log, cleanup, err := initLogger(cfg)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	ctx = zerowrap.WithCtx(ctx, log)

	// Initialize OpenTelemetry
	telProvider, telShutdown, err := telemetry.NewProvider(ctx, cfg.Telemetry, "gordon", version.Version())
	if err != nil {
		log.Warn().Err(err).Msg("failed to initialize telemetry, continuing without it")
	} else {
		// Use a fresh context for shutdown so a canceled app ctx doesn't prevent flushing.
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			telShutdown(shutdownCtx)
		}()
		if cfg.Telemetry.Enabled && cfg.Telemetry.Endpoint != "" {
			// Bridge zerowrap logs to OTel if log export is enabled
			if cfg.Telemetry.Logs && telProvider.LogProvider != nil {
				otelHook := zerowrapotel.NewHookWithProvider(telProvider.LogProvider, "gordon")
				log = zerowrap.WithHook(log, otelHook)
				ctx = zerowrap.WithCtx(ctx, log)
			}
			log.Info().Str("endpoint", cfg.Telemetry.Endpoint).Msg("telemetry initialized")
		}
	}

	log.Info().Msg("Gordon starting")

	warnDeprecatedConfigKeys(v, log)

	// Create PID file
	pidFile := createPidFile(log)
	if pidFile != "" {
		defer removePidFile(pidFile, log)
	}

	// Create all services
	svc, err := createServices(ctx, v, cfg, log)
	if err != nil {
		return err
	}
	svc.workloadLogs = workloadLogExporter(telProvider)

	// Register event handlers
	cleanupHandlers, err := registerEventHandlers(ctx, svc)
	if err != nil {
		return err
	}
	// cleanupHandlers is passed into runServers so it can stop debounce
	// timers before graceful shutdown, preventing deploys during drain.

	// Set up config hot reload
	if err := setupConfigHotReload(ctx, svc.configSvc, svc.reloadCoordinator); err != nil {
		return err
	}

	// Start event bus
	if err := svc.eventBus.Start(); err != nil {
		return log.WrapErr(err, "failed to start event bus")
	}
	defer svc.eventBus.Stop()

	// Start servers, wait for listeners to bind, then sync/auto-start containers.
	return runServers(ctx, v, cfg, svc, svc.reloadCoordinator, cleanupHandlers, log)
}

// initLogger initializes the zerowrap logger.
func initLogger(cfg Config) (zerowrap.Logger, func(), error) {
	logConfig := zerowrap.Config{
		Level:  cfg.Logging.Level,
		Format: cfg.Logging.Format,
	}

	// Always enable file logging so admin API can read process logs
	// (Config flag is respected for backward-compatibility)
	logPath := cfg.Logging.File.Path
	if logPath == "" {
		dataDir := cfg.Server.DataDir
		if dataDir == "" {
			dataDir = DefaultDataDir()
		}
		logPath = filepath.Join(dataDir, "logs", "gordon.log")
	}

	log, cleanup, err := zerowrap.NewWithFile(logConfig, zerowrap.FileConfig{
		Enabled:    cfg.Logging.File.Enabled,
		Path:       logPath,
		MaxSize:    cfg.Logging.File.MaxSize,
		MaxBackups: cfg.Logging.File.MaxBackups,
		MaxAge:     cfg.Logging.File.MaxAge,
		Compress:   true,
	})
	if err != nil {
		return zerowrap.Default(), nil, fmt.Errorf("failed to create logger with file: %w", err)
	}
	return log, cleanup, nil
}

// initAccessLog creates an access log writer when access logging is enabled.
// Returns nil, nil when disabled — callers must treat nil writer as "disabled".
func initAccessLog(cfg Config, log zerowrap.Logger) (*accesslog.Writer, error) {
	if !cfg.Logging.AccessLog.Enabled {
		return nil, nil
	}

	filePath := cfg.Logging.AccessLog.FilePath
	if filePath == "" && cfg.Logging.AccessLog.Output == "file" {
		dataDir := cfg.Server.DataDir
		if dataDir == "" {
			dataDir = DefaultDataDir()
		}
		filePath = filepath.Join(dataDir, "logs", "access.log")
	}

	writer, err := accesslog.New(accesslog.Config{
		Format:           cfg.Logging.AccessLog.Format,
		Output:           cfg.Logging.AccessLog.Output,
		FilePath:         filePath,
		MaxSize:          cfg.Logging.AccessLog.MaxSize,
		MaxBackups:       cfg.Logging.AccessLog.MaxBackups,
		MaxAge:           cfg.Logging.AccessLog.MaxAge,
		SyslogIdentifier: cfg.Logging.AccessLog.SyslogIdentifier,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize access log: %w", err)
	}

	log.Info().
		Str("format", cfg.Logging.AccessLog.Format).
		Str("output", cfg.Logging.AccessLog.Output).
		Msg("access log enabled")

	return writer, nil
}

// runServers starts the HTTP servers and waits for shutdown.
// Signal handling notes:
// - SIGINT/SIGTERM: Triggers graceful shutdown via signal.NotifyContext
// - SIGUSR1: Triggers config reload without restart
// (SIGUSR2 manual route deploy was removed with the declarative-apps
// cutover; app deploys are explicit via `gordon apps deploy`.)
// The deferred signal.Stop calls ensure signal handlers are properly
// cleaned up before program exit, preventing signal handler leaks.
func runServers(ctx context.Context, v *viper.Viper, cfg Config, svc *services, reload reloadTrigger, cleanupHandlers func(), log zerowrap.Logger) error {
	// Initialize access log writer. Kept here (not in Run) to keep Run's cyclomatic
	// complexity within the project limit of 15.
	accessWriterConcrete, err := initAccessLog(cfg, log)
	if err != nil {
		return err
	}
	if accessWriterConcrete != nil {
		defer accessWriterConcrete.Close()
	}
	// Convert to interface only when non-nil to avoid the Go nil-interface pitfall
	// where a typed nil pointer becomes a non-nil interface value.
	var accessWriter out.AccessLogWriter
	if accessWriterConcrete != nil {
		accessWriter = accessWriterConcrete
	}
	accessWriter = withAccessLogExport(svc, accessWriter)
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Set up SIGUSR1 for reload.
	// Note: signal.Stop must be called (via defer) to release the channel
	// and prevent signal handler leaks when the function returns.
	reloadChan := make(chan os.Signal, 1)
	signal.Notify(reloadChan, syscall.SIGUSR1)
	defer signal.Stop(reloadChan)

	errChan := make(chan error, 4)

	registryHandler, httpProxyHandler, httpsProxyHandler := createHTTPHandlers(svc, cfg, log, accessWriter)
	svc.httpProxyHandler = httpProxyHandler
	svc.httpsProxyHandler = httpsProxyHandler

	registrySrv, registryReady, internalRegistrySrv, internalRegistryReady := startRegistryServers(cfg, registryHandler, errChan, log)

	// closeStarted shuts down any servers that were started before an error occurred,
	// preventing leaked listeners during partial startup failures.
	closeStarted := func(servers ...*http.Server) {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		for _, srv := range servers {
			if srv != nil {
				if err := srv.Shutdown(shutdownCtx); err != nil {
					log.Error().Err(err).Msg("failed to shut down server during startup cleanup")
				}
			}
		}
	}

	proxySrv, proxyReady, tlsSrv, _, err := startProxyServers(cfg, httpProxyHandler, httpsProxyHandler, svc.pkiSvc, svc.publicTLSSvc, svc.trafficManager, log)
	if err != nil {
		closeStarted(registrySrv, internalRegistrySrv)
		return err
	}

	// Owner-only local administration socket. Started before readiness is
	// announced so the CLI can reach the daemon as soon as it is usable,
	// and closed on every exit path. TCP /admin/* registration is unchanged.
	localAdmin, err := startLocalAdminServer(svc, errChan, log)
	if err != nil {
		closeStarted(registrySrv, internalRegistrySrv, proxySrv, tlsSrv)
		shutdownTrafficManagerForStartupCleanup(svc.trafficManager, log)
		return err
	}

	// cleanupAfterLocalAdmin releases the local admin socket and traffic
	// manager in addition to the servers that already bound, so no startup
	// failure after this point leaks a listening socket or a live manager.
	cleanupAfterLocalAdmin := func(servers ...*http.Server) {
		cleanupStartupResources(localAdmin, svc.trafficManager, log, servers...)
	}

	svc.tlsHTTPEntryPoints = tlsMuxHTTPServerNames(cfg)
	svc.smartHTTPEntryPoints = smartTCPHTTPServerNames(cfg)

	// Wait for both registry listeners and the HTTP proxy before applying traffic.
	if err := waitForServerReady(ctx, internalRegistryReady, errChan); err != nil {
		cleanupAfterLocalAdmin(registrySrv, internalRegistrySrv, proxySrv, tlsSrv)
		return err
	}
	if err := waitForCoreProxyReady(ctx, registryReady, proxyReady, errChan); err != nil {
		cleanupAfterLocalAdmin(registrySrv, internalRegistrySrv, proxySrv, tlsSrv)
		return err
	}
	// Apply only the installation graph fail-fast before slower app
	// readiness checks. Persisted ACTIVE binds are intentionally excluded
	// until boot reconciliation re-inspects them.
	if err := applyTrafficRuntimeConfig(ctx, svc.trafficManager, cfg, svc.configSvc, nil); err != nil {
		cleanupAfterLocalAdmin(registrySrv, internalRegistrySrv, proxySrv, tlsSrv)
		return err
	}

	// Reconcile ACTIVE and rebuild its host index before announcing the
	// daemon ready. Applying traffic first publishes an empty app graph and
	// leaves healthy converged apps returning 404 until a later lifecycle
	// operation happens to rebuild it.
	reconcileAppsAtBoot(ctx, svc, log)
	stopLogExport := startContainerLogExport(ctx, svc, log)
	defer stopLogExport()

	logEvent := log.Info().
		Int("proxy_port", cfg.Server.Port).
		Int("registry_port", cfg.Server.RegistryPort)
	if cfg.Server.TLSPort != 0 {
		logEvent = logEvent.Int("tls_port", cfg.Server.TLSPort)
	}
	logEvent.Msg("Gordon is running")

	startPublicTLSRuntimeWithWarning(ctx, svc.publicTLSRuntime, log)

	schedulerCleanup, err := startOptionalSchedulers(ctx, cfg, svc, log, v)
	if err != nil {
		cleanupAfterLocalAdmin(registrySrv, internalRegistrySrv, proxySrv, tlsSrv)
		return err
	}
	if schedulerCleanup != nil {
		defer schedulerCleanup()
	}

	waitForShutdown(ctx, errChan, reloadChan, reload, svc.eventBus, log)
	cleanupHandlers() // Stop debounce timers before draining containers
	// Stop app administration before tearing down traffic/runtime dependencies,
	// so no new mutation can begin during shutdown.
	localAdmin.Close()
	registrySrvs := []*http.Server{registrySrv, internalRegistrySrv}
	return gracefulShutdown(registrySrvs, proxySrv, tlsSrv, svc.containerSvc, svc.proxySvc, svc.pkiSvc, svc.publicTLSSvc, svc.trafficManager, svc.appMonitor, svc.appSvcImpl, svc.appState, log)
}

// workloadLogExporter returns the OTLP workload log exporter, or nil
// when telemetry log export is disabled. A typed nil must not leak into
// the out.LogExporter interface.
func workloadLogExporter(provider *telemetry.Provider) out.LogExporter {
	if provider == nil || provider.WorkloadLogs == nil {
		return nil
	}
	return provider.WorkloadLogs
}

// withAccessLogExport also exports access entries over OTLP when
// workload log export is enabled. next may be nil (no local sink).
func withAccessLogExport(svc *services, next out.AccessLogWriter) out.AccessLogWriter {
	if svc.workloadLogs == nil {
		return next
	}
	return logexport.NewAccessLogExporter(svc.appHostIndex, svc.workloadLogs, next)
}

// startContainerLogExport follows ACTIVE app containers and exports
// their output when telemetry log export is enabled. The returned stop
// function cancels every follower and waits for them, so buffered
// records reach the exporter before telemetry shuts down.
func startContainerLogExport(ctx context.Context, svc *services, log zerowrap.Logger) func() {
	if svc.workloadLogs == nil || svc.appState == nil || svc.runtime == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	collector := logexport.NewCollector(svc.appState, svc.runtime, svc.workloadLogs)
	go func() {
		defer close(done)
		collector.Run(ctx)
	}()
	log.Info().Msg("app container log export enabled")
	return func() {
		cancel()
		<-done
	}
}

func waitForCoreProxyReady(ctx context.Context, registryReady <-chan struct{}, proxyReady <-chan struct{}, errChan <-chan error) error {
	if err := waitForServerReady(ctx, registryReady, errChan); err != nil {
		return err
	}
	return waitForServerReady(ctx, proxyReady, errChan)
}

func waitForServerReady(ctx context.Context, ready <-chan struct{}, errChan <-chan error) error {
	if ready == nil {
		return nil
	}
	select {
	case <-ready:
		return nil
	case err := <-errChan:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
