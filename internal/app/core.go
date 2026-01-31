// Package app provides the application initialization and wiring.
package app

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bnema/zerowrap"

	grpccore "github.com/bnema/gordon/internal/adapters/in/grpc/core"
	"github.com/bnema/gordon/internal/adapters/out/docker"
	"github.com/bnema/gordon/internal/adapters/out/eventbus"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	gordonv1 "github.com/bnema/gordon/internal/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// coreServices holds the services specific to the core component.
type coreServices struct {
	runtime      out.ContainerRuntime
	eventBus     out.EventBus
	configSvc    in.ConfigService
	containerSvc in.ContainerService
	lifecycle    *LifecycleManager
	log          zerowrap.Logger
}

// RunCore starts the gordon-core component.
// This is the orchestrator component that:
//   - Has Docker socket access
//   - Runs admin API on :5000
//   - Provides CoreService gRPC on :9090
//   - Deploys and manages other sub-containers
func RunCore(ctx context.Context, configPath string) error {
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

	ctx = zerowrap.WithCtx(ctx, log)
	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "core").
		Msg("starting gordon-core component")

	// Create PID file
	pidFile := createPidFile(log)
	if pidFile != "" {
		defer removePidFile(pidFile, log)
	}

	// Create core services
	svc, err := createCoreServices(ctx, cfg, log)
	if err != nil {
		return err
	}

	// Start event bus
	if err := svc.eventBus.Start(); err != nil {
		return log.WrapErr(err, "failed to start event bus")
	}
	defer svc.eventBus.Stop()

	// Start gRPC server
	grpcPort := getEnvOrDefault("GORDON_CORE_GRPC_PORT", "9090")
	grpcLis, err := net.Listen("tcp", ":"+grpcPort)
	if err != nil {
		return fmt.Errorf("failed to listen on gRPC port %s: %w", grpcPort, err)
	}

	grpcServer := grpc.NewServer()
	coreServer := grpccore.NewServer(
		svc.containerSvc,
		svc.configSvc,
		svc.runtime,
		svc.eventBus,
		log,
	)
	gordonv1.RegisterCoreServiceServer(grpcServer, coreServer)

	// Register health check
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "core").
		Str("port", grpcPort).
		Msg("gRPC server listening")

	// Start gRPC server in goroutine
	go func() {
		if err := grpcServer.Serve(grpcLis); err != nil {
			log.Error().
				Str(zerowrap.FieldLayer, "app").
				Str(zerowrap.FieldComponent, "core").
				Err(err).
				Msg("gRPC server error")
		}
	}()

	// Deploy and manage sub-containers (gordon-secrets, gordon-registry, gordon-proxy)
	if svc.lifecycle != nil {
		log.Info().
			Str(zerowrap.FieldLayer, "app").
			Str(zerowrap.FieldComponent, "core").
			Msg("deploying sub-containers")

		if err := svc.lifecycle.DeployAll(ctx); err != nil {
			log.Error().
				Str(zerowrap.FieldLayer, "app").
				Str(zerowrap.FieldComponent, "core").
				Err(err).
				Msg("failed to deploy sub-containers, continuing anyway")
		} else {
			// Start monitoring loop in background
			go svc.lifecycle.MonitorLoop(ctx)
			log.Info().
				Str(zerowrap.FieldLayer, "app").
				Str(zerowrap.FieldComponent, "core").
				Msg("sub-container monitoring started")
		}
	}

	// TODO: Phase 7 - Start admin API HTTP on :5000

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "core").
		Msg("gordon-core ready")

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-ctx.Done():
		log.Info().
			Str(zerowrap.FieldLayer, "app").
			Str(zerowrap.FieldComponent, "core").
			Msg("context cancelled, shutting down")
	case sig := <-quit:
		log.Info().
			Str(zerowrap.FieldLayer, "app").
			Str(zerowrap.FieldComponent, "core").
			Str("signal", sig.String()).
			Msg("received shutdown signal")
	}

	// Graceful shutdown
	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "core").
		Msg("shutting down gRPC server")
	grpcServer.GracefulStop()

	// Shutdown managed containers
	if svc.containerSvc != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := svc.containerSvc.Shutdown(shutdownCtx); err != nil {
			log.Warn().Err(err).Msg("error during container shutdown")
		}
	}

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "core").
		Msg("gordon-core shutdown complete")

	return nil
}

// createCoreServices creates the services needed for the core component.
func createCoreServices(ctx context.Context, cfg Config, log zerowrap.Logger) (*coreServices, error) {
	svc := &coreServices{log: log}

	// Create Docker runtime
	runtime, err := docker.NewRuntime()
	if err != nil {
		return nil, log.WrapErr(err, "failed to create Docker runtime")
	}
	if err := runtime.Ping(ctx); err != nil {
		return nil, log.WrapErr(err, "Docker is not available")
	}

	dockerVersion, _ := runtime.Version(ctx)
	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "core").
		Str("docker_version", dockerVersion).
		Msg("Docker runtime initialized")
	svc.runtime = runtime

	// Create lifecycle manager for sub-container orchestration
	selfImage := GetSelfImage(runtime)
	svc.lifecycle = NewLifecycleManager(runtime, selfImage, log)
	svc.lifecycle.InitializeSpecs(cfg)

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "core").
		Str("self_image", selfImage).
		Msg("lifecycle manager initialized")

	// Create event bus (always needed)
	svc.eventBus = eventbus.NewInMemory(100, log)

	// TODO: Create other services (config, container, auth) as needed
	// For now, return minimal services

	return svc, nil
}
