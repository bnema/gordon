package app

import (
	"context"
	"time"

	"github.com/bnema/zerowrap"
	zerowrapotel "github.com/bnema/zerowrap/otel"

	"github.com/bnema/gordon/internal/adapters/out/telemetry"
	"github.com/bnema/gordon/pkg/version"
)

// runMonolithImpl owns the default all-in-one Gordon wiring graph.
func runMonolithImpl(ctx context.Context, configPath string) error {
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

	// Register event handlers
	cleanupHandlers, err := registerEventHandlers(ctx, svc, cfg)
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
