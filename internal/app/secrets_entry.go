// Package app provides the application initialization and wiring.
package app

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/in/grpc/secrets"
	secretsAdapter "github.com/bnema/gordon/internal/adapters/out/secrets"
	"github.com/bnema/gordon/internal/adapters/out/tokenstore"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	gordon "github.com/bnema/gordon/internal/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// RunSecrets starts the gordon-secrets component.
// This component provides isolated secret and token management via gRPC.
// It has no HTTP interface and no Docker socket access.
func RunSecrets(ctx context.Context, configPath string) error {
	// Load configuration
	v, cfg, err := initConfig(configPath)
	if err != nil {
		return err
	}
	_ = v // v is used for potential future config reloading

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
		Str(zerowrap.FieldComponent, "secrets").
		Msg("starting gordon-secrets component")

	// Create data directory if needed
	dataDir := cfg.Server.DataDir
	if dataDir == "" {
		dataDir = "/var/lib/gordon"
	}

	// Initialize secrets backend
	backend := domain.SecretsBackend(cfg.Auth.SecretsBackend)
	if backend == "" {
		backend = domain.SecretsBackendUnsafe
	}

	// Create token store
	tokenStore, err := tokenstore.NewStore(backend, dataDir, log)
	if err != nil {
		return fmt.Errorf("failed to create token store: %w", err)
	}

	// Create secret providers
	providers := []out.SecretProvider{
		secretsAdapter.NewPassProvider(log),
	}

	// Create gRPC server
	grpcPort := getEnvOrDefault("GORDON_SECRETS_GRPC_PORT", "9091")
	lis, err := net.Listen("tcp", ":"+grpcPort)
	if err != nil {
		return fmt.Errorf("failed to listen on port %s: %w", grpcPort, err)
	}

	grpcServer := grpc.NewServer()
	secretsServer := grpcsecrets.NewServer(tokenStore, providers)
	gordon.RegisterSecretsServiceServer(grpcServer, secretsServer)

	// Register health check
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "secrets").
		Str("port", grpcPort).
		Msg("gRPC server listening")

	// Start server in goroutine
	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Error().
				Str(zerowrap.FieldLayer, "app").
				Str(zerowrap.FieldComponent, "secrets").
				Err(err).
				Msg("gRPC server error")
		}
	}()

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-ctx.Done():
		log.Info().
			Str(zerowrap.FieldLayer, "app").
			Str(zerowrap.FieldComponent, "secrets").
			Msg("context cancelled, shutting down")
	case sig := <-quit:
		log.Info().
			Str(zerowrap.FieldLayer, "app").
			Str(zerowrap.FieldComponent, "secrets").
			Str("signal", sig.String()).
			Msg("received shutdown signal")
	}

	// Graceful shutdown
	grpcServer.GracefulStop()
	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "secrets").
		Msg("gordon-secrets shutdown complete")

	return nil
}

// getEnvOrDefault returns the environment variable value or default if not set.
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
