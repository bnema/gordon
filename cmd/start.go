package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
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

	// Create context for the entire server lifecycle
	ctx, cancel := context.WithCancel(context.Background())

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

		// Check for new routes and deploy containers if needed
		if err := handleConfigReload(ctx, cfg, newCfg, manager); err != nil {
			log.Error().Err(err).Msg("Failed to handle config reload")
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

// handleConfigReload compares old and new configurations to detect new routes
// and automatically deploys containers for them if images are available
func handleConfigReload(ctx context.Context, oldCfg, newCfg *config.Config, manager *container.Manager) error {
	oldRoutes := make(map[string]string) // domain -> image
	newRoutes := make(map[string]string) // domain -> image

	// Build maps of routes for comparison
	for _, route := range oldCfg.GetRoutes() {
		oldRoutes[route.Domain] = route.Image
	}

	for _, route := range newCfg.GetRoutes() {
		newRoutes[route.Domain] = route.Image
	}

	// Find new routes and changed routes
	var routesToDeploy []config.Route

	for domain, newImage := range newRoutes {
		if oldImage, exists := oldRoutes[domain]; !exists {
			// New route detected
			log.Info().Str("domain", domain).Str("image", newImage).Msg("New route detected in config")
			routesToDeploy = append(routesToDeploy, config.Route{
				Domain: domain,
				Image:  newImage,
				HTTPS:  true, // Default to HTTPS
			})
		} else if oldImage != newImage {
			// Route with changed image
			log.Info().
				Str("domain", domain).
				Str("old_image", oldImage).
				Str("new_image", newImage).
				Msg("Route image changed in config")
			routesToDeploy = append(routesToDeploy, config.Route{
				Domain: domain,
				Image:  newImage,
				HTTPS:  true, // Default to HTTPS
			})
		}
	}

	// Check for removed routes
	for domain := range oldRoutes {
		if _, exists := newRoutes[domain]; !exists {
			log.Info().Str("domain", domain).Msg("Route removed from config, stopping container")
			if err := manager.StopContainerByDomain(ctx, domain); err != nil {
				log.Warn().Err(err).Str("domain", domain).Msg("Failed to stop container for removed route")
			}
			if err := manager.RemoveContainerByDomain(ctx, domain, true); err != nil {
				log.Warn().Err(err).Str("domain", domain).Msg("Failed to remove container for removed route")
			}
		}
	}

	if len(routesToDeploy) == 0 {
		log.Info().Msg("No new or changed routes detected")
		return nil
	}

	log.Info().Int("count", len(routesToDeploy)).Msg("Processing new/changed routes")

	// For each new/changed route, check if image is available and deploy
	deployedCount := 0
	for _, route := range routesToDeploy {
		if err := deployRouteIfImageAvailable(ctx, route, newCfg, manager); err != nil {
			log.Error().
				Err(err).
				Str("domain", route.Domain).
				Str("image", route.Image).
				Msg("Failed to deploy route")
		} else {
			deployedCount++
		}
	}

	log.Info().
		Int("deployed", deployedCount).
		Int("total", len(routesToDeploy)).
		Msg("Config reload deployment completed")

	return nil
}

// deployRouteIfImageAvailable checks if an image is available and deploys the container
func deployRouteIfImageAvailable(ctx context.Context, route config.Route, cfg *config.Config, manager *container.Manager) error {
	// Build the full image reference
	imageRef := route.Image

	// If registry auth is enabled and image doesn't already contain a registry domain, prepend it
	if cfg.RegistryAuth.Enabled && cfg.Server.RegistryDomain != "" {
		// Check if image already contains a registry domain (has a '.' and doesn't start with official Docker Hub libraries)
		if !strings.Contains(strings.Split(imageRef, ":")[0], ".") {
			imageRef = fmt.Sprintf("%s/%s", cfg.Server.RegistryDomain, route.Image)
		}
	}

	log.Info().
		Str("domain", route.Domain).
		Str("image", route.Image).
		Str("full_image_ref", imageRef).
		Msg("Checking if image is available for new route")

	// Check if image is available locally first
	images, err := manager.Runtime().ListImages(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to list local images, will attempt to pull")
	} else {
		// Check if image exists locally
		imageAvailableLocally := false
		normalizedImageRef := normalizeImageRef(imageRef)

		for _, localImage := range images {
			if normalizeImageRef(localImage) == normalizedImageRef {
				imageAvailableLocally = true
				log.Info().
					Str("domain", route.Domain).
					Str("image", imageRef).
					Msg("Image available locally, deploying container")
				break
			}
		}

		if imageAvailableLocally {
			// Image is available locally, deploy immediately
			_, err := manager.DeployContainer(ctx, route)
			if err != nil {
				return fmt.Errorf("failed to deploy container for route %s: %w", route.Domain, err)
			}

			log.Info().
				Str("domain", route.Domain).
				Str("image", route.Image).
				Msg("Container deployed successfully for new route")

			return nil
		}
	}

	// Image not available locally, try to pull it
	log.Info().
		Str("domain", route.Domain).
		Str("image", imageRef).
		Msg("Image not available locally, attempting to pull")

	// Use authenticated pull if registry auth is configured
	if cfg.RegistryAuth.Enabled {
		err = manager.Runtime().PullImageWithAuth(ctx, imageRef, cfg.RegistryAuth.Username, cfg.RegistryAuth.Password)
	} else {
		err = manager.Runtime().PullImage(ctx, imageRef)
	}

	if err != nil {
		log.Warn().
			Err(err).
			Str("domain", route.Domain).
			Str("image", imageRef).
			Msg("Failed to pull image for new route, container will be deployed when image becomes available")
		return fmt.Errorf("image %s not available for route %s: %w", imageRef, route.Domain, err)
	}

	log.Info().
		Str("domain", route.Domain).
		Str("image", imageRef).
		Msg("Image pulled successfully, deploying container")

	// Image pulled successfully, now deploy the container
	_, err = manager.DeployContainer(ctx, route)
	if err != nil {
		return fmt.Errorf("failed to deploy container for route %s after pulling image: %w", route.Domain, err)
	}

	log.Info().
		Str("domain", route.Domain).
		Str("image", route.Image).
		Msg("Container deployed successfully for new route after pulling image")

	return nil
}

// normalizeImageRef normalizes image references for comparison
func normalizeImageRef(imageRef string) string {
	// Split image and tag
	parts := strings.Split(imageRef, ":")
	image := parts[0]
	tag := "latest"
	if len(parts) > 1 {
		tag = parts[1]
	}

	// Normalize Docker Hub references
	if !strings.Contains(image, "/") {
		// Official library image (e.g., "nginx" -> "docker.io/library/nginx")
		image = "docker.io/library/" + image
	} else if strings.Count(image, "/") == 1 && !strings.Contains(strings.Split(image, "/")[0], ".") {
		// User repository (e.g., "user/repo" -> "docker.io/user/repo")
		image = "docker.io/" + image
	}

	return image + ":" + tag
}
