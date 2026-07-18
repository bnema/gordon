package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	edgev1 "github.com/bnema/gordon/api/gordon/edge/v1"
	eventsv1 "github.com/bnema/gordon/api/gordon/events/v1"
	edgesnapshotgrpc "github.com/bnema/gordon/internal/adapters/in/grpc/edgesnapshot"
	eventsgrpc "github.com/bnema/gordon/internal/adapters/in/grpc/events"
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	"github.com/bnema/gordon/internal/adapters/out/filesystem"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/auto"
	autopreview "github.com/bnema/gordon/internal/usecase/auto/preview"
	"github.com/bnema/gordon/internal/usecase/componentevents"
	configusecase "github.com/bnema/gordon/internal/usecase/config"
	"github.com/bnema/gordon/internal/usecase/container"
	controlplaneusecase "github.com/bnema/gordon/internal/usecase/controlplane"
	"github.com/bnema/gordon/internal/usecase/edgesnapshot"
	proxyusecase "github.com/bnema/gordon/internal/usecase/proxy"
	"github.com/bnema/gordon/internal/usecase/runtimecontrol"
	servicecfg "github.com/bnema/gordon/internal/usecase/services"
)

// controlRoleDependencies makes the narrowly-scoped control server testable.
// It receives only the runtime-state boundary, never a runtime adapter or socket.
type controlRoleDependencies struct {
	listen                     func(network, address string) (net.Listener, error)
	newComponentTokenValidator func(Config, zerowrap.Logger) (interceptors.ComponentTokenValidator, error)
	newSnapshotHub             func() *edgesnapshot.SnapshotHub
	newEventHub                func(int) *componentevents.EventHub
	newTrafficGraphHub         func() *edgesnapshot.TrafficGraphHub
	newRuntimeStateSubscriber  func(context.Context, RuntimeControlConfig) (out.RuntimeStateSubscriber, error)
	newRuntimeDrainAckReceiver func(context.Context, RuntimeControlConfig) (out.RouteDrainAckReceiver, error)
	setupConfigHotReload       func(context.Context, configWatcher, loadedConfigApplier) error
	newSnapshotProducer        func(out.RuntimeStateSubscriber, *edgesnapshot.SnapshotHub, edgesnapshot.ProducerOptions) (*edgesnapshot.Producer, error)
	newTrafficGraphProducer    func(*edgesnapshot.SnapshotHub, *edgesnapshot.TrafficGraphHub, edgesnapshot.TrafficGraphProducerOptions) (*edgesnapshot.TrafficGraphProducer, error)
}

func productionControlRoleDependencies() controlRoleDependencies {
	return controlRoleDependencies{
		listen:                     net.Listen,
		newComponentTokenValidator: newRuntimeRoleComponentTokenValidator,
		newSnapshotHub:             edgesnapshot.NewSnapshotHub,
		newEventHub:                componentevents.NewEventHub,
		newTrafficGraphHub:         edgesnapshot.NewTrafficGraphHub,
		newRuntimeStateSubscriber:  createRuntimeStateSubscriber,
		newRuntimeDrainAckReceiver: createRuntimeRouteDrainAckReceiver,
		setupConfigHotReload:       setupConfigHotReload,
		newSnapshotProducer:        edgesnapshot.NewProducer,
		newTrafficGraphProducer:    edgesnapshot.NewTrafficGraphProducer,
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
	defer cleanupControlLogger(cleanup)
	ctx = zerowrap.WithCtx(ctx, log)
	warnDeprecatedConfigKeys(v, log)

	// Control owns configuration, user authentication/token management, and
	// the remote-compatible admin API. This graph deliberately contains no
	// local runtime, registry storage, or public proxy listener.
	controlServices, err := newControlRoleServices(ctx, v, cfg, log)
	if err != nil {
		return log.WrapErr(err, "initialize control services")
	}
	setupHotReload := deps.setupConfigHotReload
	if setupHotReload == nil {
		setupHotReload = setupConfigHotReload
	}
	if err := setupHotReload(ctx, controlServices.configSvc, controlServices.reloadCoordinator); err != nil {
		return log.WrapErr(err, "watch control configuration")
	}

	dispatcher, err := newControlEventDispatcher(ctx, v, cfg)
	if err != nil {
		return log.WrapErr(err, "create control component event dispatcher")
	}

	return runControlServers(ctx, v, cfg, deps, log, controlServices, dispatcher)
}

func runControlServers(ctx context.Context, v *viper.Viper, cfg Config, deps controlRoleDependencies, log zerowrap.Logger, controlServices *services, dispatcher *controlplaneusecase.EventDispatcher) error {
	validator, err := deps.newComponentTokenValidator(cfg, log)
	if err != nil {
		return log.WrapErr(err, "failed to initialize control component token validator")
	}
	hub := deps.newSnapshotHub()
	if hub == nil {
		return fmt.Errorf("control route snapshot hub is required")
	}
	if err := startControlSnapshotProducer(ctx, v, cfg, deps, hub); err != nil {
		return controlSnapshotProducerError(err, log)
	}
	eventHub, err := controlEventHub(deps)
	if err != nil {
		return err
	}
	trafficHub, err := initializeControlTrafficGraph(ctx, v, cfg, deps, hub)
	if err != nil {
		return log.WrapErr(err, "publish initial control traffic graph")
	}
	drainRelay, err := initializeControlDrainRelay(ctx, cfg, deps)
	if err != nil {
		return log.WrapErr(err, "create runtime route drain relay")
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

	server, err := newControlSnapshotServerWithTrafficGraphDrainAndEvents(cfg, validator, hub, trafficHub, drainCoordinator, eventsgrpc.NewDispatchingServer(eventHub, dispatcher))
	if err != nil {
		return err
	}
	return serveControlHTTPAndGRPC(ctx, cfg, deps.listen, listener, server, controlServices, dispatcher, log)
}

func serveControlHTTPAndGRPC(ctx context.Context, cfg Config, listen func(string, string) (net.Listener, error), grpcListener net.Listener, grpcServer *grpc.Server, controlServices *services, dispatcher *controlplaneusecase.EventDispatcher, log zerowrap.Logger) error {
	httpListener, err := controlHTTPListener(cfg, listen)
	if err != nil {
		return log.WrapErr(err, "failed to listen for control HTTP")
	}
	var httpServer *http.Server
	if httpListener != nil {
		defer httpListener.Close()
		// The production admin deploy endpoint enters the same durable dispatcher
		// as remote registry events, so manual suppression is observable after restart.
		controlServices.adminHandler.SetComponentEventHandler(dispatcher)
		httpServer = newControlHTTPServer(controlHTTPHandler(controlServices, cfg, log))
	}

	errCh := make(chan error, 2)
	go func() { errCh <- grpcServer.Serve(grpcListener) }()
	if httpServer != nil {
		go func() {
			if serveErr := httpServer.Serve(httpListener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				errCh <- serveErr
			}
		}()
		log.Info().Str("addr", httpListener.Addr().String()).Msg("gordon-control management HTTP server started")
	}
	log.Info().Str("addr", grpcListener.Addr().String()).Msg("gordon-control snapshot gRPC server started")

	select {
	case <-ctx.Done():
		shutdownControlHTTP(httpServer)
		gracefulStop(grpcServer)
		return nil
	case serveErr := <-errCh:
		shutdownControlHTTP(httpServer)
		gracefulStop(grpcServer)
		return log.WrapErr(serveErr, "control server stopped")
	}
}

func shutdownControlHTTP(server *http.Server) {
	if server == nil {
		return
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
}

func initializeControlDrainRelay(ctx context.Context, cfg Config, deps controlRoleDependencies) (edgesnapshot.RuntimeDrainAckReceiver, error) {
	if deps.newRuntimeDrainAckReceiver == nil {
		return nil, nil
	}
	return deps.newRuntimeDrainAckReceiver(ctx, cfg.Runtime)
}

// newControlEventDispatcher wires typed component events to existing control
// decisions and the narrow runtime command facade. It never creates a local
// container runtime or service.
// newControlProductionEffects assembles the legacy handlers around a runtime
// command facade. The event adapter below is intentionally a bridge only: the
// auto dispatcher remains the sole image classification decision point.
func newControlProductionEffects(ctx context.Context, cfg Config, config *configusecase.Service, runtime controlplaneusecase.RouteCommander, log zerowrap.Logger) (*controlplaneusecase.ProductionEffects, error) {
	containerFacade := controlplaneusecase.NewRuntimeCommandContainerService(runtime)
	// Registry owns blobs beneath data_dir/registry; control reads only this
	// shared durable registry store to inspect pushed image labels.
	blobStorage, err := filesystem.NewBlobStorage(filepath.Join(resolveDataDir(cfg.Server.DataDir), "registry"), log)
	if err != nil {
		return nil, fmt.Errorf("open image label blob storage: %w", err)
	}
	registryDomain, legacyRegistryDomains := resolveRegistryDomains(cfg)
	autoRoute := container.NewAutoRouteHandler(ctx, config, containerFacade, blobStorage, registryDomain, legacyRegistryDomains...)
	previewStore := filesystem.NewPreviewStore(filepath.Join(resolveDataDir(cfg.Server.DataDir), "previews.json"))
	previewService := autopreview.NewService(previewStore, config.GetPreviewConfig().TTL).
		WithDeployer(containerFacade).
		WithRouteManager(config).
		WithRegistryDomain(config.GetRegistryDomain())
	if err := previewService.Load(ctx); err != nil {
		return nil, fmt.Errorf("load control previews: %w", err)
	}
	autoPreview := autopreview.NewAutoPreviewHandler(ctx, config, previewService)
	automation := auto.NewImagePushDispatcher(config, autoRoute, autoPreview)
	imagePushed, err := controlplaneusecase.NewImagePushedHandlers(automation, container.NewImagePushedHandler(ctx, containerFacade, config))
	if err != nil {
		return nil, err
	}
	return controlplaneusecase.NewProductionEffects(
		imagePushed,
		container.NewManualDeployHandler(ctx, containerFacade, config),
		runtime,
		controlplaneusecase.NewLogAuditSink(),
	)
}

func newControlEventDispatcher(ctx context.Context, v *viper.Viper, cfg Config) (*controlplaneusecase.EventDispatcher, error) {
	controlConfig := configusecase.NewService(v, nil)
	if err := controlConfig.Load(ctx); err != nil {
		return nil, fmt.Errorf("load control event configuration: %w", err)
	}
	runtimeClient, err := createRuntimeCommandClient(ctx, cfg.Runtime)
	if err != nil {
		return nil, fmt.Errorf("create control event runtime client: %w", err)
	}
	var eventRuntime controlplaneusecase.RouteCommander = unavailableControlRouteCommander{}
	if runtimeClient != nil {
		eventRuntime = runtimecontrol.NewService(controlConfig, runtimeClient, "gordon-control")
	}
	effects, err := newControlProductionEffects(ctx, cfg, controlConfig, eventRuntime, zerowrap.FromCtx(ctx))
	if err != nil {
		return nil, fmt.Errorf("create control component event effects: %w", err)
	}
	store, err := filesystem.NewComponentEventStore(filepath.Join(resolveDataDir(cfg.Server.DataDir), "component-events.json"), 1024)
	if err != nil {
		return nil, fmt.Errorf("open control component event store: %w", err)
	}
	return controlplaneusecase.NewEventDispatcher(controlplaneusecase.EventDispatcherOptions{
		ImagePushedEffect:  effects.ImagePushed,
		ConfigReloadEffect: effects.ConfigReload,
		ManualDeployEffect: effects.ManualDeploy,
		SecretsEffect:      effects.SecretsChanged,
		RuntimeState:       effects.RuntimeState,
		RuntimeEvent:       effects.RuntimeEvent,
		PolicyAudit:        effects.PolicyAudit,
		AckStore:           store,
		IntentStore:        store,
	}), nil
}

func controlSnapshotProducerError(err error, log zerowrap.Logger) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return log.WrapErr(err, "publish initial control route snapshot")
}

func controlEventHub(deps controlRoleDependencies) (*componentevents.EventHub, error) {
	newHub := deps.newEventHub
	if newHub == nil {
		newHub = componentevents.NewEventHub
	}
	hub := newHub(1024)
	if hub == nil {
		return nil, fmt.Errorf("control component event hub is required")
	}
	return hub, nil
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
func initializeControlTrafficGraph(ctx context.Context, v *viper.Viper, cfg Config, deps controlRoleDependencies, routes *edgesnapshot.SnapshotHub) (*edgesnapshot.TrafficGraphHub, error) {
	newHub := deps.newTrafficGraphHub
	if newHub == nil {
		newHub = edgesnapshot.NewTrafficGraphHub
	}
	graphs := newHub()
	if graphs == nil {
		return nil, fmt.Errorf("control traffic graph hub is required")
	}
	if err := startControlTrafficGraphProducer(ctx, v, cfg, deps, routes, graphs); err != nil {
		return nil, err
	}
	return graphs, nil
}

func startControlTrafficGraphProducer(ctx context.Context, v *viper.Viper, cfg Config, deps controlRoleDependencies, routes *edgesnapshot.SnapshotHub, graphs *edgesnapshot.TrafficGraphHub) error {
	newProducer := deps.newTrafficGraphProducer
	if newProducer == nil {
		newProducer = edgesnapshot.NewTrafficGraphProducer
	}
	options, err := controlTrafficGraphProducerOptions(v, cfg)
	if err != nil {
		return err
	}
	producer, err := newProducer(routes, graphs, options)
	if err != nil {
		return fmt.Errorf("create control traffic graph producer: %w", err)
	}
	return producer.Start(ctx)
}

// controlTrafficGraphProducerOptions selects only edge-safe, canonical routing
// values. The edge receives the resulting graph, never this Config value.
func controlTrafficGraphProducerOptions(v *viper.Viper, cfg Config) (edgesnapshot.TrafficGraphProducerOptions, error) {
	routeOptions, err := controlProducerOptions(v, cfg)
	if err != nil {
		return edgesnapshot.TrafficGraphProducerOptions{}, err
	}
	services, err := servicecfg.ToDomain(cfg.Services)
	if err != nil {
		return edgesnapshot.TrafficGraphProducerOptions{}, fmt.Errorf("convert standalone service config: %w", err)
	}
	return edgesnapshot.TrafficGraphProducerOptions{
		EntryPoints:          cfg.EntryPoints,
		Traffic:              cfg.Traffic,
		ExternalRouteTargets: routeOptions.External,
		NetworkServices:      cfg.NetworkServices,
		Services:             services,
	}, nil
}

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
	return newControlSnapshotServerWithTrafficGraphAndDrain(cfg, validator, hub, nil, drainReceiver)
}

func newControlSnapshotServerWithTrafficGraphAndDrain(cfg Config, validator interceptors.ComponentTokenValidator, hub *edgesnapshot.SnapshotHub, trafficHub *edgesnapshot.TrafficGraphHub, drainReceiver edgesnapshot.DrainStateReceiver) (*grpc.Server, error) {
	return newControlSnapshotServerWithTrafficGraphDrainAndEvents(cfg, validator, hub, trafficHub, drainReceiver, nil)
}

// newControlSnapshotServerWithTrafficGraphDrainAndEvents co-hosts the typed
// event intake with the existing sanitized edge streams. Event transport is
// authenticated with its own method scopes; it never receives runtime sockets.
func newControlSnapshotServerWithTrafficGraphDrainAndEvents(cfg Config, validator interceptors.ComponentTokenValidator, hub *edgesnapshot.SnapshotHub, trafficHub *edgesnapshot.TrafficGraphHub, drainReceiver edgesnapshot.DrainStateReceiver, eventServer eventsv1.EventServiceServer) (*grpc.Server, error) {
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
	scopes := edgesnapshotgrpc.MethodScopes()
	roles := edgesnapshotgrpc.MethodRoles()
	if eventServer != nil {
		for method, scope := range eventsgrpc.MethodScopes() {
			scopes[method] = scope
		}
		for method, role := range eventsgrpc.MethodRoles() {
			roles[method] = role
		}
	}
	server := grpc.NewServer(
		grpc.Creds(transport),
		grpc.UnaryInterceptor(interceptors.ComponentAuthUnaryInterceptor(validator, scopes, roles)),
		grpc.StreamInterceptor(interceptors.ComponentAuthStreamInterceptor(validator, scopes, roles)),
	)
	if trafficHub != nil {
		serverAdapter := edgesnapshotgrpc.NewServerWithTrafficGraphSource(hub, trafficHub)
		if drainReceiver != nil {
			serverAdapter = edgesnapshotgrpc.NewServerWithDrainStateReceiverAndTrafficGraphSource(hub, drainReceiver, trafficHub)
		}
		edgev1.RegisterEdgeServiceServer(server, serverAdapter)
	} else if drainReceiver != nil {
		edgev1.RegisterEdgeServiceServer(server, edgesnapshotgrpc.NewServerWithDrainStateReceiver(hub, drainReceiver))
	} else {
		edgev1.RegisterEdgeServiceServer(server, edgesnapshotgrpc.NewServer(hub))
	}
	if eventServer != nil {
		eventsv1.RegisterEventServiceServer(server, eventServer)
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

func cleanupControlLogger(cleanup func()) {
	if cleanup != nil {
		cleanup()
	}
}

// unavailableControlRouteCommander preserves a fully wired dispatcher in
// configurations that are only serving route snapshots. It is not a no-op:
// a command event is rejected for transport retry until a runtime endpoint is
// configured.
type unavailableControlRouteCommander struct{}

func (unavailableControlRouteCommander) DeployRoute(context.Context, domain.Route) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, fmt.Errorf("runtime.endpoint is required for control component events")
}

func (unavailableControlRouteCommander) ReconcileConfiguredRoutes(context.Context, string) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, fmt.Errorf("runtime.endpoint is required for control component events")
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
