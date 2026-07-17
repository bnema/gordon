package app

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
	"google.golang.org/grpc"

	runtimev1 "github.com/bnema/gordon/api/gordon/runtime/v1"
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	runtimegrpc "github.com/bnema/gordon/internal/adapters/in/grpc/runtime"
	"github.com/bnema/gordon/internal/adapters/out/tokenstore"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/componentauth"
	"github.com/bnema/gordon/internal/usecase/config"
	"github.com/bnema/gordon/internal/usecase/container"
	"github.com/bnema/gordon/internal/usecase/images"
	"github.com/bnema/gordon/internal/usecase/logs"
	servicecfg "github.com/bnema/gordon/internal/usecase/services"
	volumesSvc "github.com/bnema/gordon/internal/usecase/volumes"
)

type runtimeRoleDependencies struct {
	buildWorker                func(context.Context, *viper.Viper, Config, zerowrap.Logger) (in.RuntimeWorker, func(), error)
	listen                     func(network, address string) (net.Listener, error)
	newComponentTokenValidator func(Config, zerowrap.Logger) (interceptors.ComponentTokenValidator, error)
}

func productionRuntimeRoleDependencies() runtimeRoleDependencies {
	return runtimeRoleDependencies{
		buildWorker:                buildRuntimeRoleWorkerImpl,
		listen:                     net.Listen,
		newComponentTokenValidator: newRuntimeRoleComponentTokenValidator,
	}
}

// runRuntimeImpl owns runtime-worker-only wiring.
func runRuntimeImpl(ctx context.Context, configPath string) error {
	return runRuntimeWithDependencies(ctx, configPath, productionRuntimeRoleDependencies())
}

// newRuntimeRoleComponentTokenValidator constructs the production validator from
// the configured component-token backend. Runtime startup must not proceed if
// this construction fails, because all component RPCs require authentication.
func newRuntimeRoleComponentTokenValidator(cfg Config, log zerowrap.Logger) (interceptors.ComponentTokenValidator, error) {
	backend, err := resolveSecretsBackend(cfg.Auth.SecretsBackend)
	if err != nil {
		return nil, fmt.Errorf("resolve component token secrets backend: %w", err)
	}
	store, err := tokenstore.NewComponentTokenStore(backend, resolveDataDir(cfg.Server.DataDir), log)
	if err != nil {
		return nil, fmt.Errorf("create component token store: %w", err)
	}
	return componentauth.NewService(store, log, componentauth.Config{}), nil
}

func runRuntimeWithDependencies(ctx context.Context, configPath string, deps runtimeRoleDependencies) error {
	v, cfg, err := initConfig(configPath)
	if err != nil {
		return err
	}
	log, cleanupLog, err := initLogger(cfg)
	if err != nil {
		return err
	}
	if cleanupLog != nil {
		defer cleanupLog()
	}
	ctx = zerowrap.WithCtx(ctx, log)
	warnDeprecatedConfigKeys(v, log)

	validator, err := deps.newComponentTokenValidator(cfg, log)
	if err != nil {
		return log.WrapErr(err, "failed to initialize component token validator")
	}

	worker, cleanupWorker, err := deps.buildWorker(ctx, v, cfg, log)
	if err != nil {
		return err
	}
	if cleanupWorker != nil {
		defer cleanupWorker()
	}
	addr := v.GetString("runtime.listen_address")
	listener, err := deps.listen("tcp", addr)
	if err != nil {
		return log.WrapErr(err, "failed to listen for runtime gRPC")
	}
	defer listener.Close()

	server := grpc.NewServer(
		grpc.UnaryInterceptor(interceptors.ComponentAuthUnaryInterceptor(validator, runtimegrpc.MethodScopes(), runtimegrpc.MethodRoles())),
		grpc.StreamInterceptor(interceptors.ComponentAuthStreamInterceptor(validator, runtimegrpc.MethodScopes(), runtimegrpc.MethodRoles())),
	)
	runtimev1.RegisterRuntimeServiceServer(server, newRuntimeRoleService(worker))

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()
	log.Info().Str("addr", listener.Addr().String()).Msg("gordon-runtime gRPC server started")

	select {
	case <-ctx.Done():
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
		return nil
	case err := <-errCh:
		return log.WrapErr(err, "runtime gRPC server stopped")
	}
}

type runtimeRoleWorkerBundle struct {
	in.RuntimeWorker
	logReader                out.RuntimeLogReader
	volumeManager            out.RuntimeVolumeManager
	imageManager             out.RuntimeImageManager
	routeDrainAckReceiver    out.RouteDrainAckReceiver
	standaloneServiceManager out.RuntimeStandaloneServiceManager
}

func (w runtimeRoleWorkerBundle) RuntimeLogReader() out.RuntimeLogReader { return w.logReader }

func (w runtimeRoleWorkerBundle) RuntimeVolumeManager() out.RuntimeVolumeManager {
	return w.volumeManager
}

func (w runtimeRoleWorkerBundle) RuntimeImageManager() out.RuntimeImageManager {
	return w.imageManager
}

func (w runtimeRoleWorkerBundle) RuntimeStandaloneServiceManager() out.RuntimeStandaloneServiceManager {
	return w.standaloneServiceManager
}

func (w runtimeRoleWorkerBundle) RouteDrainAckReceiver() out.RouteDrainAckReceiver {
	return w.routeDrainAckReceiver
}

func runtimeRoleLogReader(worker in.RuntimeWorker) out.RuntimeLogReader {
	if w, ok := worker.(interface{ RuntimeLogReader() out.RuntimeLogReader }); ok {
		return w.RuntimeLogReader()
	}
	return nil
}

func runtimeRoleVolumeManager(worker in.RuntimeWorker) out.RuntimeVolumeManager {
	if w, ok := worker.(interface {
		RuntimeVolumeManager() out.RuntimeVolumeManager
	}); ok {
		return w.RuntimeVolumeManager()
	}
	return nil
}

func runtimeRoleImageManager(worker in.RuntimeWorker) out.RuntimeImageManager {
	if w, ok := worker.(interface {
		RuntimeImageManager() out.RuntimeImageManager
	}); ok {
		return w.RuntimeImageManager()
	}
	return nil
}

func runtimeRoleStandaloneServiceManager(worker in.RuntimeWorker) out.RuntimeStandaloneServiceManager {
	if w, ok := worker.(interface {
		RuntimeStandaloneServiceManager() out.RuntimeStandaloneServiceManager
	}); ok {
		return w.RuntimeStandaloneServiceManager()
	}
	return nil
}

func runtimeRoleRouteDrainAckReceiver(worker in.RuntimeWorker) out.RouteDrainAckReceiver {
	if w, ok := worker.(interface {
		RouteDrainAckReceiver() out.RouteDrainAckReceiver
	}); ok {
		return w.RouteDrainAckReceiver()
	}
	return nil
}

func newRuntimeRoleService(worker in.RuntimeWorker) runtimev1.RuntimeServiceServer {
	return runtimegrpc.NewServerWithAllRuntimePortsAndRouteDrainAckReceiverAndStandaloneServiceManager(
		worker,
		runtimeRoleLogReader(worker),
		runtimeRoleVolumeManager(worker),
		runtimeRoleImageManager(worker),
		runtimeRoleStateSubscriber(worker),
		runtimeRoleRouteDrainAckReceiver(worker),
		runtimeRoleStandaloneServiceManager(worker),
		"gordon-runtime",
	)
}

func runtimeRoleStateSubscriber(worker in.RuntimeWorker) out.RuntimeStateSubscriber {
	snapshotter, ok := worker.(interface {
		Snapshot(context.Context, uint64, string, string) (domain.RuntimeActualStateSnapshot, error)
	})
	if !ok {
		return nil
	}
	return &pollingRuntimeStateSubscriber{snapshotter: snapshotter, interval: time.Second, sourceComponentID: "gordon-runtime"}
}

type pollingRuntimeStateSubscriber struct {
	snapshotter interface {
		Snapshot(context.Context, uint64, string, string) (domain.RuntimeActualStateSnapshot, error)
	}
	interval          time.Duration
	sourceComponentID string

	generationMu sync.Mutex
	generation   uint64
}

func (s *pollingRuntimeStateSubscriber) SubscribeRuntimeState(ctx context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
	if s.snapshotter == nil {
		return nil, fmt.Errorf("runtime worker snapshotter not configured")
	}
	interval := s.interval
	if interval <= 0 {
		interval = time.Second
	}
	ch := make(chan domain.RuntimeActualStateSnapshot, 1)
	go func() {
		defer close(ch)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			generation := s.nextGeneration()
			snapshot, err := s.snapshotter.Snapshot(ctx, generation, runtimeStateVersion(generation), s.sourceComponentID)
			if err == nil {
				select {
				case ch <- snapshot:
				case <-ctx.Done():
					return
				}
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func (s *pollingRuntimeStateSubscriber) nextGeneration() uint64 {
	s.generationMu.Lock()
	defer s.generationMu.Unlock()
	s.generation++
	return s.generation
}

func runtimeStateVersion(generation uint64) string {
	return "runtime-state:" + strconv.FormatUint(generation, 10)
}

func buildRuntimeRoleWorkerImpl(ctx context.Context, v *viper.Viper, cfg Config, log zerowrap.Logger) (in.RuntimeWorker, func(), error) {
	runtimeSocket := resolveRuntimeConfig(v.GetString("server.runtime"))
	runtimeAdapter, eventBus, err := createOutputAdapters(ctx, log, RoleRuntime, runtimeSocket)
	if err != nil {
		return nil, nil, err
	}

	svc := &services{runtime: runtimeAdapter, eventBus: eventBus}
	var drainRegistry *container.RuntimeDrainRegistry
	cleanup := func() {
		if drainRegistry != nil {
			drainRegistry.Close()
		}
		if svc.eventBus != nil {
			svc.eventBus.Stop()
		}
	}

	if svc.logWriter, err = createLogWriter(cfg, log); err != nil {
		cleanup()
		return nil, nil, err
	}
	if svc.tokenStore, svc.authSvc, err = createAuthService(ctx, cfg, log); err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := setupInternalRegistryAuth(svc, log); err != nil {
		cleanup()
		return nil, nil, err
	}
	svc.configSvc = config.NewService(v, svc.eventBus)

	si := &serviceInit{ctx: ctx, v: v, cfg: cfg, log: log, svc: svc}
	if err := si.initSecrets(); err != nil {
		cleanup()
		return nil, nil, err
	}
	if svc.containerSvc, err = createContainerService(ctx, v, cfg, svc, log); err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := svc.eventBus.Start(); err != nil {
		cleanup()
		return nil, nil, log.WrapErr(err, "failed to start runtime event bus")
	}

	policy := runtimeRolePolicy(cfg, v)
	worker := container.NewRuntimeWorkerWithPolicy(svc.containerSvc, policy)
	drainRegistry = container.NewRuntimeDrainRegistry(svc.containerSvc.RuntimeDrainRouteState)
	svc.containerSvc.SetProxyDrainWaiter(drainRegistry)
	standaloneServiceManager := newRuntimeRoleStandaloneServiceManager(svc.runtime, cfg, v)
	return runtimeRoleWorkerBundle{RuntimeWorker: worker, logReader: logs.NewLocalRuntimeLogReader(svc.containerSvc, svc.runtime), volumeManager: volumesSvc.NewLocalRuntimeVolumeManager(svc.runtime), imageManager: images.NewLocalRuntimeImageManager(svc.runtime), routeDrainAckReceiver: drainRegistry, standaloneServiceManager: standaloneServiceManager}, cleanup, nil
}

func newRuntimeRoleStandaloneServiceManager(runtime out.ContainerRuntime, cfg Config, v *viper.Viper) out.RuntimeStandaloneServiceManager {
	return container.NewRuntimeStandaloneServicePolicyManager(servicecfg.NewLocalRuntimeStandaloneServiceManager(runtime), runtimeRolePolicy(cfg, v))
}

func runtimeRolePolicy(cfg Config, v *viper.Viper) container.RuntimePolicy {
	managedNetworkPrefix := ""
	if v != nil {
		managedNetworkPrefix = v.GetString("network_isolation.network_prefix")
	}
	return container.RuntimePolicy{
		Mode:                   container.RuntimePolicyModeEnforce,
		ManagedNetworkPrefix:   managedNetworkPrefix,
		AllowedImageRegistries: cfg.Images.AllowedRegistries,
		RequireImageDigest:     cfg.Images.RequireDigest,
		RuntimeComponentID:     "gordon-runtime",
	}
}
