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

	// Create and subscribe auto-route event handler
	var autoRouteHandler *events.AutoRouteHandler
	autoRouteHandler = events.NewAutoRouteHandler(cfg, manager)
	if err := eventBus.Subscribe(autoRouteHandler); err != nil {
		log.Fatal().Err(err).Msg("Failed to subscribe auto-route event handler")
	}

	// Start proxy server (needs to be accessible for config reload)
	proxyServer := proxy.NewServer(cfg, manager)

	// Create and subscribe proxy event handler
	var proxyHandler *proxy.ProxyEventHandler
	proxyHandler = proxy.NewProxyEventHandler(proxyServer)
	if err := eventBus.Subscribe(proxyHandler); err != nil {
		log.Fatal().Err(err).Msg("Failed to subscribe proxy event handler")
	}

	// Create context for the entire server lifecycle
	ctx, cancel := context.WithCancel(context.Background())

	// Setup config file watching for live reload with debouncing
	var reloadTimer *time.Timer
	var reloadMutex sync.Mutex
	
	viper.WatchConfig()
	viper.OnConfigChange(func(e fsnotify.Event) {
		log.Info().
			Str("file", e.Name).
			Str("op", e.Op.String()).
			Msg("Configuration file changed, reloading...")

		// Debounce config reloads to prevent multiple events from a single logical change
		reloadMutex.Lock()
		defer reloadMutex.Unlock()
		
		if reloadTimer != nil {
			reloadTimer.Stop()
		}
		
		reloadTimer = time.AfterFunc(500*time.Millisecond, func() {
			// Reload configuration
			newCfg, err := config.Load()
			if err != nil {
				log.Error().Err(err).Msg("Failed to reload configuration")
				return
			}

			// Update the configuration reference
			cfg = newCfg

			// Update proxy server config
			proxyServer.UpdateConfig(cfg)

			// Unsubscribe old handlers before creating new ones
			if err := eventBus.Unsubscribe(containerHandler); err != nil {
				log.Warn().Err(err).Msg("Failed to unsubscribe old container event handler")
			}
			if err := eventBus.Unsubscribe(autoRouteHandler); err != nil {
				log.Warn().Err(err).Msg("Failed to unsubscribe old auto-route event handler")
			}
			if err := eventBus.Unsubscribe(proxyHandler); err != nil {
				log.Warn().Err(err).Msg("Failed to unsubscribe old proxy event handler")
			}

			// Update container handler config
			containerHandler = events.NewContainerEventHandler(manager, cfg)
			if err := eventBus.Subscribe(containerHandler); err != nil {
				log.Error().Err(err).Msg("Failed to re-subscribe container event handler")
				return
			}

			// Update auto-route handler config
			autoRouteHandler = events.NewAutoRouteHandler(cfg, manager)
			if err := eventBus.Subscribe(autoRouteHandler); err != nil {
				log.Error().Err(err).Msg("Failed to re-subscribe auto-route event handler")
				return
			}

			// Re-subscribe proxy handler (config ref updated automatically)
			proxyHandler = proxy.NewProxyEventHandler(proxyServer)
			if err := eventBus.Subscribe(proxyHandler); err != nil {
				log.Error().Err(err).Msg("Failed to re-subscribe proxy event handler")
				return
			}

			// Publish config reload event to handle route changes
			if err := eventBus.Publish(events.ConfigReload, nil); err != nil {
				log.Error().Err(err).Msg("Failed to publish config reload event")
			} else {
				log.Info().Msg("Configuration reloaded successfully")
			}
		})
	})

	// Sync existing containers
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

	// Start proxy server (already created above)
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
