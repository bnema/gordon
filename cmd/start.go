package cmd

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"gordon/internal/config"
	"gordon/internal/container"
	"gordon/internal/events"
	"gordon/internal/proxy"
	"gordon/internal/registry"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the Gordon server",
	Long:  `Start the container registry and reverse proxy server`,
	Run:   runStart,
}

func init() {
	rootCmd.AddCommand(startCmd)
}

func runStart(cmd *cobra.Command, args []string) {
	// Configure zerolog
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load config")
	}

	log.Info().Msg("Starting Gordon server...")
	log.Info().Int("registry_port", cfg.Server.RegistryPort).Msg("Registry server")
	log.Info().Int("proxy_port", cfg.Server.Port).Msg("Proxy server")
	log.Info().Str("runtime", cfg.Server.Runtime).Msg("Container runtime")

	// Initialize event bus
	eventBus := events.NewInMemoryEventBus(100)
	if err := eventBus.Start(); err != nil {
		log.Fatal().Err(err).Msg("Failed to start event bus")
	}

	// Initialize container manager
	manager, err := container.NewManager(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize container manager")
	}

	// Create and subscribe container event handler
	containerHandler := events.NewContainerEventHandler(manager, cfg)
	if err := eventBus.Subscribe(containerHandler); err != nil {
		log.Fatal().Err(err).Msg("Failed to subscribe container event handler")
	}

	// Setup config file watching for live reload
	viper.WatchConfig()
	viper.OnConfigChange(func(e fsnotify.Event) {
		log.Info().
			Str("file", e.Name).
			Str("op", e.Op.String()).
			Msg("Configuration file changed, reloading...")

		// Reload configuration
		newCfg, err := config.Load()
		if err != nil {
			log.Error().Err(err).Msg("Failed to reload configuration")
			return
		}

		// Update the configuration reference
		cfg = newCfg

		// Update container handler config
		containerHandler = events.NewContainerEventHandler(manager, cfg)
		if err := eventBus.Subscribe(containerHandler); err != nil {
			log.Error().Err(err).Msg("Failed to re-subscribe container event handler")
			return
		}

		// Publish config reload event
		if err := eventBus.Publish(events.ConfigReload, nil); err != nil {
			log.Error().Err(err).Msg("Failed to publish config reload event")
		} else {
			log.Info().Msg("Configuration reloaded successfully")
		}
	})

	// Sync existing containers
	ctx, cancel := context.WithCancel(context.Background())
	if err := manager.SyncContainers(ctx); err != nil {
		log.Warn().Err(err).Msg("Failed to sync existing containers")
	}

	// Auto-start containers that match config routes
	log.Info().Msg("Auto-starting containers for configured routes...")
	if err := manager.AutoStartContainers(ctx); err != nil {
		log.Warn().Err(err).Msg("Failed to auto-start containers")
	}

	var wg sync.WaitGroup

	// Start registry server
	registryServer, err := registry.NewServer(cfg, eventBus)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize registry server")
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := registryServer.Start(ctx); err != nil {
			log.Error().Err(err).Msg("Registry server error")
		}
	}()

	// Start proxy server
	proxyServer := proxy.NewServer(cfg, manager)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := proxyServer.Start(ctx); err != nil {
			log.Error().Err(err).Msg("Proxy server error")
		}
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Info().Msg("Shutting down servers...")
	cancel()

	// Stop all managed containers with a separate context for shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	
	log.Info().Msg("Stopping all managed containers...")
	if err := manager.StopAllManagedContainers(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("Failed to stop managed containers")
	}

	// Stop event bus
	if err := eventBus.Stop(); err != nil {
		log.Error().Err(err).Msg("Failed to stop event bus")
	}

	// Give servers time to shutdown gracefully
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Info().Msg("Servers stopped gracefully")
	case <-time.After(10 * time.Second):
		log.Warn().Msg("Force shutdown after timeout")
	}
}
