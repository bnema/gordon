// Package app provides the application initialization and wiring.
package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	grpcregistry "github.com/bnema/gordon/internal/adapters/in/grpc/registry"
	"github.com/bnema/gordon/internal/adapters/in/http/registry"
	"github.com/bnema/gordon/internal/adapters/out/filesystem"
	"github.com/bnema/gordon/internal/adapters/out/grpccore"
	gordon "github.com/bnema/gordon/internal/grpc"
	registrySvc "github.com/bnema/gordon/internal/usecase/registry"
	"github.com/bnema/zerowrap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// RunRegistry starts the gordon-registry component.
// This component provides Docker registry HTTP API on :5000 and gRPC inspection on :9092.
func RunRegistry(ctx context.Context, configPath string) error {
	// Load configuration
	v, cfg, err := initConfig(configPath)
	if err != nil {
		return err
	}
	_ = v

	// Initialize logger
	log, cleanup, err := initLogger(cfg)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "registry").
		Msg("starting gordon-registry component")

	// Create storage
	dataDir := cfg.Server.DataDir
	if dataDir == "" {
		dataDir = "/var/lib/gordon"
	}

	registryDir := filepath.Join(dataDir, "registry")
	blobStorage, err := filesystem.NewBlobStorage(registryDir, log)
	if err != nil {
		return fmt.Errorf("failed to create blob storage: %w", err)
	}

	manifestStorage, err := filesystem.NewManifestStorage(registryDir, log)
	if err != nil {
		return fmt.Errorf("failed to create manifest storage: %w", err)
	}

	// Create event publisher that connects to core
	coreAddr := getEnvOrDefault("GORDON_CORE_ADDR", "gordon-core:9090")
	eventPublisher, err := grpccore.NewEventPublisher(coreAddr)
	if err != nil {
		return fmt.Errorf("failed to create event publisher: %w", err)
	}
	defer eventPublisher.Close()

	// Create registry service with gRPC event publisher
	registryService := registrySvc.NewService(blobStorage, manifestStorage, eventPublisher)

	// Create HTTP registry handler
	httpRegistryHandler := registry.NewHandler(registryService, log)

	// Create gRPC server for manifest inspection
	grpcPort := getEnvOrDefault("GORDON_REGISTRY_GRPC_PORT", "9092")
	grpcLis, err := net.Listen("tcp", ":"+grpcPort)
	if err != nil {
		return fmt.Errorf("failed to listen on gRPC port %s: %w", grpcPort, err)
	}

	grpcServer := grpc.NewServer()
	grpcRegistryServer := grpcregistry.NewServer(registryService)
	gordon.RegisterRegistryInspectServiceServer(grpcServer, grpcRegistryServer)

	// Register health check
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "registry").
		Str("port", grpcPort).
		Msg("gRPC server listening")

	// Start gRPC server in goroutine
	go func() {
		if err := grpcServer.Serve(grpcLis); err != nil {
			log.Error().
				Str(zerowrap.FieldLayer, "app").
				Str(zerowrap.FieldComponent, "registry").
				Err(err).
				Msg("gRPC server error")
		}
	}()

	// Create HTTP server for registry API
	registryPort := getEnvOrDefault("GORDON_REGISTRY_PORT", "5000")
	httpServer := &http.Server{
		Addr:              ":" + registryPort,
		Handler:           httpRegistryHandler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "registry").
		Str("port", registryPort).
		Msg("HTTP registry server listening")

	// Start HTTP server in goroutine
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().
				Str(zerowrap.FieldLayer, "app").
				Str(zerowrap.FieldComponent, "registry").
				Err(err).
				Msg("HTTP registry server error")
		}
	}()

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-ctx.Done():
		log.Info().
			Str(zerowrap.FieldLayer, "app").
			Str(zerowrap.FieldComponent, "registry").
			Msg("context cancelled, shutting down")
	case sig := <-quit:
		log.Info().
			Str(zerowrap.FieldLayer, "app").
			Str(zerowrap.FieldComponent, "registry").
			Str("signal", sig.String()).
			Msg("received shutdown signal")
	}

	// Graceful shutdown
	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "registry").
		Msg("shutting down gRPC server")
	grpcServer.GracefulStop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "registry").
		Msg("shutting down HTTP server")

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error().
			Str(zerowrap.FieldLayer, "app").
			Str(zerowrap.FieldComponent, "registry").
			Err(err).
			Msg("HTTP server shutdown error")
	}

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "registry").
		Msg("gordon-registry shutdown complete")

	return nil
}
