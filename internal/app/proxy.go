// Package app provides the application initialization and wiring.
package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bnema/zerowrap"

	grpccore "github.com/bnema/gordon/internal/adapters/out/grpccore"
	"github.com/bnema/gordon/internal/usecase/proxy"
)

// RunProxy starts the gordon-proxy component.
// This component:
//   - Is internet-facing (HTTP on :80)
//   - Has no Docker socket access
//   - Has no secrets access
//   - Resolves targets via gRPC to gordon-core
func RunProxy(ctx context.Context, configPath string) error {
	// Load configuration (minimal - just for logging)
	_, cfg, err := initConfig(configPath)
	if err != nil {
		// Proxy can run with minimal config, just use defaults
		cfg = Config{}
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
	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "proxy").
		Msg("starting gordon-proxy component")

	// Get core gRPC address from env
	coreAddr := getEnvOrDefault("GORDON_CORE_ADDR", "gordon-core:9090")

	// Create gRPC client to core
	coreClient, err := grpccore.NewClient(coreAddr, log)
	if err != nil {
		return fmt.Errorf("failed to connect to core service: %w", err)
	}
	defer coreClient.Close()

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "proxy").
		Str("core_addr", coreAddr).
		Msg("connected to core service")

	// Get proxy port from env
	proxyPort := getEnvOrDefault("GORDON_PROXY_PORT", "80")

	// Create proxy service with remote dependencies
	proxySvc := proxy.NewRemoteService(coreClient, coreClient, proxy.Config{
		RegistryDomain: getEnvOrDefault("GORDON_REGISTRY_DOMAIN", ""),
		RegistryPort:   5000, // Default registry port
	})

	// Start watching for route changes in background
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()

	go func() {
		onInvalidate := func(domain string) {
			if domain == "" {
				// Invalidate all
				log.Info().Msg("invalidating all proxy targets")
				if err := proxySvc.RefreshTargets(watchCtx); err != nil {
					log.Warn().Err(err).Msg("failed to refresh proxy targets")
				}
			} else {
				log.Debug().Str("domain", domain).Msg("invalidating proxy target")
				proxySvc.InvalidateTarget(watchCtx, domain)
			}
		}

		if err := coreClient.Watch(watchCtx, onInvalidate); err != nil {
			log.Error().Err(err).Msg("route change watch failed")
		}
	}()

	// Create HTTP server
	server := &http.Server{
		Addr:         ":" + proxyPort,
		Handler:      proxySvc,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "proxy").
		Str("port", proxyPort).
		Msg("HTTP proxy listening")

	// Start server in goroutine
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().
				Str(zerowrap.FieldLayer, "app").
				Str(zerowrap.FieldComponent, "proxy").
				Err(err).
				Msg("HTTP server error")
		}
	}()

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-ctx.Done():
		log.Info().
			Str(zerowrap.FieldLayer, "app").
			Str(zerowrap.FieldComponent, "proxy").
			Msg("context cancelled, shutting down")
	case sig := <-quit:
		log.Info().
			Str(zerowrap.FieldLayer, "app").
			Str(zerowrap.FieldComponent, "proxy").
			Str("signal", sig.String()).
			Msg("received shutdown signal")
	}

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "proxy").
		Msg("shutting down HTTP server")

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Warn().Err(err).Msg("HTTP server shutdown error")
	}

	watchCancel() // Stop route watcher

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "proxy").
		Msg("gordon-proxy shutdown complete")

	return nil
}
