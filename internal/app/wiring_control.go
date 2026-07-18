package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	edgev1 "github.com/bnema/gordon/api/gordon/edge/v1"
	edgesnapshotgrpc "github.com/bnema/gordon/internal/adapters/in/grpc/edgesnapshot"
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	configusecase "github.com/bnema/gordon/internal/usecase/config"
	"github.com/bnema/gordon/internal/usecase/edgesnapshot"
	proxyusecase "github.com/bnema/gordon/internal/usecase/proxy"
)

// controlRoleDependencies makes the narrowly-scoped control server testable.
// It receives only the runtime-state boundary, never a runtime adapter or socket.
type controlRoleDependencies struct {
	listen                     func(network, address string) (net.Listener, error)
	newComponentTokenValidator func(Config, zerowrap.Logger) (interceptors.ComponentTokenValidator, error)
	newSnapshotHub             func() *edgesnapshot.SnapshotHub
	newRuntimeStateSubscriber  func(context.Context, RuntimeControlConfig) (out.RuntimeStateSubscriber, error)
	newRuntimeDrainAckReceiver func(context.Context, RuntimeControlConfig) (out.RouteDrainAckReceiver, error)
	newSnapshotProducer        func(out.RuntimeStateSubscriber, *edgesnapshot.SnapshotHub, edgesnapshot.ProducerOptions) (*edgesnapshot.Producer, error)
}

func productionControlRoleDependencies() controlRoleDependencies {
	return controlRoleDependencies{
		listen:                     net.Listen,
		newComponentTokenValidator: newRuntimeRoleComponentTokenValidator,
		newSnapshotHub:             edgesnapshot.NewSnapshotHub,
		newRuntimeStateSubscriber:  createRuntimeStateSubscriber,
		newRuntimeDrainAckReceiver: createRuntimeRouteDrainAckReceiver,
		newSnapshotProducer:        edgesnapshot.NewProducer,
	}
}

// runControlImpl owns the authenticated snapshot gRPC server and translates
// the narrow runtime-state boundary through the control-owned producer.
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
	if err := startControlSnapshotProducer(ctx, v, cfg, deps, hub); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return log.WrapErr(err, "publish initial control route snapshot")
	}
	var drainRelay edgesnapshot.RuntimeDrainAckReceiver
	if deps.newRuntimeDrainAckReceiver != nil {
		relay, relayErr := deps.newRuntimeDrainAckReceiver(ctx, cfg.Runtime)
		if relayErr != nil {
			return log.WrapErr(relayErr, "create runtime route drain relay")
		}
		drainRelay = relay
	}
	drainCoordinator, err := edgesnapshot.NewDrainCoordinator(hub, edgesnapshot.DrainCoordinatorOptions{
		Runtime:             drainRelay,
		RegistrationTimeout: v.GetDuration("control.drain_registration_timeout"),
	})
	if err != nil {
		return log.WrapErr(err, "create route drain coordinator")
	}
	defer drainCoordinator.Close()

	listener, err := deps.listen("tcp", strings.TrimSpace(cfg.Control.ListenAddress))
	if err != nil {
		return log.WrapErr(err, "failed to listen for control gRPC")
	}
	defer listener.Close()

	server, err := newControlSnapshotServerWithDrain(cfg, validator, hub, drainCoordinator)
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

func startControlSnapshotProducer(ctx context.Context, v *viper.Viper, cfg Config, deps controlRoleDependencies, hub *edgesnapshot.SnapshotHub) error {
	if deps.newRuntimeStateSubscriber == nil || deps.newSnapshotProducer == nil {
		return fmt.Errorf("control route snapshot producer dependencies are required")
	}
	options, err := controlProducerOptions(v, cfg)
	if err != nil {
		return fmt.Errorf("load control external routes: %w", err)
	}
	subscriber, err := deps.newRuntimeStateSubscriber(ctx, cfg.Runtime)
	if err != nil {
		return fmt.Errorf("create control runtime state subscriber: %w", err)
	}
	producer, err := deps.newSnapshotProducer(subscriber, hub, options)
	if err != nil {
		return fmt.Errorf("create control route snapshot producer: %w", err)
	}
	if err := producer.Start(ctx); err != nil {
		return err
	}
	return nil
}

// controlProducerOptions converts only explicit control routing contracts into
// producer inputs. It intentionally never forwards Config, container state, or
// a runtime endpoint to edge snapshots.
func controlProducerOptions(v *viper.Viper, cfg Config) (edgesnapshot.ProducerOptions, error) {
	options := edgesnapshot.ProducerOptions{EdgeAlias: cfg.Control.EdgeAlias}
	if strings.TrimSpace(cfg.Server.RegistryDomain) != "" {
		options.Registry = &edgesnapshot.RegistryTarget{
			Domain:   cfg.Server.RegistryDomain,
			Alias:    cfg.Control.RegistryAlias,
			Port:     cfg.Control.RegistryPort,
			Scheme:   "http",
			Protocol: domain.RouteTargetProtocolHTTP1,
		}
	}
	if v == nil {
		return options, fmt.Errorf("control configuration is required")
	}
	routes, err := configusecase.LoadExternalRoutes(v.Get("external_routes"))
	if err != nil {
		return options, err
	}
	domains := make([]string, 0, len(routes))
	for domainName := range routes {
		domains = append(domains, domainName)
	}
	sort.Strings(domains)
	options.External = make([]domain.RouteTargetEntry, 0, len(domains))
	for _, domainName := range domains {
		// Generation is intentionally a producer concern. The placeholder only
		// permits entry validation; snapshotFromRuntime re-stamps it atomically.
		entry, resolveErr := proxyusecase.ResolveExternalRouteTarget(domainName, routes[domainName], 1)
		if resolveErr != nil {
			return options, fmt.Errorf("external route %q: %w", domainName, resolveErr)
		}
		options.External = append(options.External, entry)
	}
	return options, nil
}

// newControlSnapshotServer is composable so route orchestration can publish to
// hub without granting the transport access to runtime or configuration state.
func newControlSnapshotServer(cfg Config, validator interceptors.ComponentTokenValidator, hub *edgesnapshot.SnapshotHub) (*grpc.Server, error) {
	return newControlSnapshotServerWithDrain(cfg, validator, hub, nil)
}

func newControlSnapshotServerWithDrain(cfg Config, validator interceptors.ComponentTokenValidator, hub *edgesnapshot.SnapshotHub, drainReceiver edgesnapshot.DrainStateReceiver) (*grpc.Server, error) {
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
	if drainReceiver != nil {
		edgev1.RegisterEdgeServiceServer(server, edgesnapshotgrpc.NewServerWithDrainStateReceiver(hub, drainReceiver))
	} else {
		edgev1.RegisterEdgeServiceServer(server, edgesnapshotgrpc.NewServer(hub))
	}
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
