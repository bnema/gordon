package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	edgev1 "github.com/bnema/gordon/api/gordon/edge/v1"
	edgesnapshotgrpc "github.com/bnema/gordon/internal/adapters/in/grpc/edgesnapshot"
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	"github.com/bnema/gordon/internal/usecase/edgesnapshot"
)

// controlRoleDependencies makes the narrowly-scoped control server testable.
// It intentionally has no runtime, config-service, or route-orchestration dependency.
type controlRoleDependencies struct {
	listen                     func(network, address string) (net.Listener, error)
	newComponentTokenValidator func(Config, zerowrap.Logger) (interceptors.ComponentTokenValidator, error)
	newSnapshotHub             func() *edgesnapshot.SnapshotHub
}

func productionControlRoleDependencies() controlRoleDependencies {
	return controlRoleDependencies{
		listen:                     net.Listen,
		newComponentTokenValidator: newRuntimeRoleComponentTokenValidator,
		newSnapshotHub:             edgesnapshot.NewSnapshotHub,
	}
}

// runControlImpl owns only the authenticated snapshot gRPC server. Route
// orchestration publishes into the returned hub in a later phase; this role
// never reads runtime state to synthesize snapshots.
func runControlImpl(ctx context.Context, configPath string) error {
	return runControlWithDependencies(ctx, configPath, productionControlRoleDependencies())
}

func runControlWithDependencies(ctx context.Context, configPath string, deps controlRoleDependencies) error {
	v, cfg, err := initConfig(configPath)
	if err != nil {
		return err
	}
	log, cleanup, err := initLogger(cfg)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	ctx = zerowrap.WithCtx(ctx, log)
	warnDeprecatedConfigKeys(v, log)

	validator, err := deps.newComponentTokenValidator(cfg, log)
	if err != nil {
		return log.WrapErr(err, "failed to initialize control component token validator")
	}
	hub := deps.newSnapshotHub()
	if hub == nil {
		return fmt.Errorf("control route snapshot hub is required")
	}
	listener, err := deps.listen("tcp", strings.TrimSpace(cfg.Control.ListenAddress))
	if err != nil {
		return log.WrapErr(err, "failed to listen for control gRPC")
	}
	defer listener.Close()

	server, err := newControlSnapshotServer(cfg, validator, hub)
	if err != nil {
		return err
	}
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()
	log.Info().Str("addr", listener.Addr().String()).Msg("gordon-control snapshot gRPC server started")

	select {
	case <-ctx.Done():
		gracefulStop(server)
		return nil
	case serveErr := <-errCh:
		return log.WrapErr(serveErr, "control snapshot gRPC server stopped")
	}
}

// newControlSnapshotServer is composable so route orchestration can publish to
// hub without granting the transport access to runtime or configuration state.
func newControlSnapshotServer(cfg Config, validator interceptors.ComponentTokenValidator, hub *edgesnapshot.SnapshotHub) (*grpc.Server, error) {
	if validator == nil {
		return nil, fmt.Errorf("control component token validator is required")
	}
	if hub == nil {
		return nil, fmt.Errorf("control route snapshot hub is required")
	}
	transport, err := controlServerTransportCredentials(cfg)
	if err != nil {
		return nil, err
	}
	server := grpc.NewServer(
		grpc.Creds(transport),
		grpc.UnaryInterceptor(interceptors.ComponentAuthUnaryInterceptor(validator, edgesnapshotgrpc.MethodScopes(), edgesnapshotgrpc.MethodRoles())),
		grpc.StreamInterceptor(interceptors.ComponentAuthStreamInterceptor(validator, edgesnapshotgrpc.MethodScopes(), edgesnapshotgrpc.MethodRoles())),
	)
	edgev1.RegisterEdgeServiceServer(server, edgesnapshotgrpc.NewServer(hub))
	return server, nil
}

func controlServerTransportCredentials(cfg Config) (credentials.TransportCredentials, error) {
	if cfg.Control.InsecureTLS {
		return insecure.NewCredentials(), nil
	}
	if cfg.Server.TLSCertFile == "" || cfg.Server.TLSKeyFile == "" {
		return nil, fmt.Errorf("control TLS requires server.tls_cert_file and server.tls_key_file; set control.insecure_tls=true only for explicit private plaintext")
	}
	certificate, err := tls.LoadX509KeyPair(cfg.Server.TLSCertFile, cfg.Server.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load control TLS certificate: %w", err)
	}
	return credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}), nil
}

func gracefulStop(server *grpc.Server) {
	stopped := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		server.Stop()
	}
}
