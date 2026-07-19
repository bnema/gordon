package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
	"google.golang.org/grpc"

	runtimev1 "github.com/bnema/gordon/api/gordon/runtime/v1"
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	runtimegrpc "github.com/bnema/gordon/internal/adapters/in/grpc/runtime"
	"github.com/bnema/gordon/internal/adapters/out/filesystem"
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
	buildWorker                func(context.Context, *viper.Viper, Config, zerowrap.Logger) (runtimeRoleWorkerBundle, func(), error)
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
	validator := interceptors.ComponentTokenValidator(componentauth.NewService(store, log, componentauth.Config{}))
	// A prepared replacement has an empty, private state volume, so its
	// component-token store cannot yet contain the control credential. Accept
	// the checkpointed handoff token only on the generated Unix migration
	// listener, and map it to the narrow control role scopes. Normal listeners
	// continue to require a persisted/revocable component token.
	if migrationRuntimeBootstrapConfigured(cfg.Runtime) {
		if token := runtimeControlToken(cfg.Runtime); token != "" {
			validator = migrationBootstrapTokenValidator{primary: validator, token: token}
		}
	}
	return validator, nil
}

type migrationBootstrapTokenValidator struct {
	primary interceptors.ComponentTokenValidator
	token   string
}

func migrationRuntimeBootstrapConfigured(cfg RuntimeControlConfig) bool {
	return validBootstrapRuntimeEndpoint(strings.TrimSpace(cfg.ListenAddress), nil) || validBootstrapRuntimeEndpoint(strings.TrimSpace(cfg.Endpoint), nil)
}

func (v migrationBootstrapTokenValidator) ValidateToken(ctx context.Context, token string, required domain.ComponentScope) (*domain.ComponentIdentity, error) {
	if v.primary != nil {
		identity, err := v.primary.ValidateToken(ctx, token, required)
		if err == nil {
			return identity, nil
		}
		if identity := v.bootstrapIdentity(token); identity != nil {
			if !domain.ComponentRoleAllowsScope(identity.Role, required) {
				return nil, domain.ErrUnauthorized
			}
			return identity, nil
		}
		return nil, err
	}
	identity := v.bootstrapIdentity(token)
	if identity == nil {
		return nil, domain.ErrInvalidToken
	}
	if !domain.ComponentRoleAllowsScope(identity.Role, required) {
		return nil, domain.ErrUnauthorized
	}
	return identity, nil
}

func (v migrationBootstrapTokenValidator) bootstrapIdentity(token string) *domain.ComponentIdentity {
	for _, role := range []domain.ComponentRole{domain.ComponentRoleControl, domain.ComponentRoleEdge, domain.ComponentRoleRegistry} {
		expected := v.token
		if role != domain.ComponentRoleControl {
			expected = migrationComponentToken(v.token, role)
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1 {
			return &domain.ComponentIdentity{Name: migrationBootstrapIdentityName(role), Role: role, Scopes: domain.DefaultComponentScopesForRole(role)}
		}
	}
	return nil
}

func migrationBootstrapIdentityName(role domain.ComponentRole) string {
	if role == domain.ComponentRoleEdge {
		if edgeID := strings.TrimSpace(os.Getenv("GORDON_MIGRATION_EDGE_COMPONENT_ID")); strings.HasPrefix(edgeID, "gordon-edge-") && componentLabelValue.MatchString(edgeID) {
			return edgeID
		}
	}
	return "migration-bootstrap-" + string(role)
}

// migrationComponentToken derives independent, role-limited credentials from
// the private runtime handoff token. The seed remains only in control/runtime;
// edge and registry receive their own 0600 env-file credential.
func migrationComponentToken(seed string, role domain.ComponentRole) string {
	if strings.TrimSpace(seed) == "" || (role != domain.ComponentRoleEdge && role != domain.ComponentRoleRegistry) {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(seed))
	_, _ = mac.Write([]byte("gordon-migration-component-token-v1:" + string(role)))
	return "gordon_migration_" + hex.EncodeToString(mac.Sum(nil))
}

// migrationProbeToken is deliberately domain-separated from both the runtime
// handoff seed and component control credentials. It is only ever materialized
// in the prepared edge's private environment and the coordinator process.
// migrationRegistryForwardToken derives the edge-to-registry-only credential.
// It cannot authenticate to control, runtime, or any other component role.
func migrationRegistryForwardToken(seed string) string {
	if strings.TrimSpace(seed) == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(seed))
	_, _ = mac.Write([]byte("gordon-migration-edge-registry-forward-token-v1"))
	return "gordon_migration_registry_forward_" + hex.EncodeToString(mac.Sum(nil))
}

func migrationProbeToken(seed string) string {
	if strings.TrimSpace(seed) == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(seed))
	_, _ = mac.Write([]byte("gordon-migration-probe-token-v1"))
	return "gordon_migration_probe_" + hex.EncodeToString(mac.Sum(nil))
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
	listener, cleanupListener, err := runtimeRoleListener(deps.listen, addr, cfg.Server.DataDir)
	if err != nil {
		return log.WrapErr(err, "failed to listen for runtime gRPC")
	}
	defer listener.Close()
	defer cleanupListener()

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
	runtime                  out.ContainerRuntime
	logReader                out.RuntimeLogReader
	volumeManager            out.RuntimeVolumeManager
	imageManager             out.RuntimeImageManager
	routeDrainAckReceiver    out.RouteDrainAckReceiver
	stateSubscriber          out.RuntimeStateSubscriber
	standaloneServiceManager out.RuntimeStandaloneServiceManager
	environmentProbe         out.RuntimeEnvironmentProbe
}

// Snapshot preserves the concrete worker capability through the role bundle.
// Without this forwarding method the production runtime gRPC service cannot
// publish actual state, because embedding the RuntimeWorker interface hides
// optional methods implemented by the concrete container worker.
func (w runtimeRoleWorkerBundle) Snapshot(ctx context.Context, generation uint64, stateVersion, sourceComponentID string) (domain.RuntimeActualStateSnapshot, error) {
	snapshotter, ok := w.RuntimeWorker.(interface {
		Snapshot(context.Context, uint64, string, string) (domain.RuntimeActualStateSnapshot, error)
	})
	if !ok {
		return domain.RuntimeActualStateSnapshot{}, fmt.Errorf("runtime worker snapshotter not configured")
	}
	snapshot, err := snapshotter.Snapshot(ctx, generation, stateVersion, sourceComponentID)
	if err != nil {
		return domain.RuntimeActualStateSnapshot{}, err
	}
	return w.includeRuntimeSelf(ctx, snapshot, generation, sourceComponentID)
}

// includeRuntimeSelf makes the first authenticated handoff restart-safe. The
// container service cache is intentionally route-focused and may not have
// observed the runtime container that started it; append only that labelled
// self container from the local runtime, never arbitrary engine inventory.
func (w runtimeRoleWorkerBundle) includeRuntimeSelf(ctx context.Context, snapshot domain.RuntimeActualStateSnapshot, generation uint64, sourceComponentID string) (domain.RuntimeActualStateSnapshot, error) {
	if w.runtime == nil || sourceComponentID == "" {
		return snapshot, nil
	}
	for _, state := range snapshot.Containers {
		if state.Name == sourceComponentID {
			return snapshot, nil
		}
	}
	containers, err := w.runtime.ListContainers(ctx, true)
	if err != nil {
		return domain.RuntimeActualStateSnapshot{}, fmt.Errorf("list runtime self container: %w", err)
	}
	for _, candidate := range containers {
		if candidate == nil || candidate.Name != sourceComponentID || candidate.Labels[domain.LabelComponent] != "true" || candidate.Labels[domain.LabelComponentRole] != string(domain.ComponentRoleRuntime) {
			continue
		}
		status := domain.ContainerStatus(strings.ToLower(candidate.Status))
		switch status {
		case domain.ContainerStatusRunning, domain.ContainerStatusStopped, domain.ContainerStatusCreated, domain.ContainerStatusExited, domain.ContainerStatusPaused:
		default:
			status = domain.ContainerStatusUnknown
		}
		snapshot.Containers = append(snapshot.Containers, domain.RuntimeContainerState{Name: candidate.Name, Image: candidate.Image, ImageID: candidate.ImageID, Status: status, StartedAt: candidate.Created, Labels: domain.SanitizeRuntimeStateLabels(candidate.Labels), Generation: generation})
		break
	}
	return snapshot, nil
}

func newRuntimeRoleService(worker runtimeRoleWorkerBundle) runtimev1.RuntimeServiceServer {
	return runtimegrpc.NewServerWithEnvironmentProbe(
		worker.RuntimeWorker,
		worker.logReader,
		worker.volumeManager,
		worker.imageManager,
		worker.stateSubscriber,
		worker.routeDrainAckReceiver,
		worker.standaloneServiceManager,
		worker.environmentProbe,
		runtimeRoleComponentID(),
	)
}

// runtimeRoleListener supports TCP for normal deployments and the tightly
// constrained Unix migration endpoint for split bootstrap. A Unix listener
// never follows symlinks, removes only a stale socket, and is removed at stop.
func runtimeRoleListener(listen func(string, string) (net.Listener, error), endpoint, dataDir string) (net.Listener, func(), error) {
	if path, ok := runtimeBootstrapSocketPath(endpoint, resolveDataDir(dataDir)); ok {
		if err := prepareRuntimeSocketPath(path); err != nil {
			return nil, nil, err
		}
		listener, err := listen("unix", path)
		if err != nil {
			return nil, nil, err
		}
		if err := os.Chmod(path, 0o600); err != nil {
			_ = listener.Close()
			_ = os.Remove(path)
			return nil, nil, fmt.Errorf("restrict runtime Unix socket: %w", err)
		}
		return listener, func() { _ = removeRuntimeSocket(path) }, nil
	}
	if strings.HasPrefix(strings.TrimSpace(endpoint), "unix:") {
		return nil, nil, fmt.Errorf("invalid runtime Unix listener")
	}
	listener, err := listen("tcp", endpoint)
	return listener, func() {}, err
}

func prepareRuntimeSocketPath(path string) error {
	parent := filepath.Dir(path)
	if err := secureRuntimeSocketParent(parent); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("runtime Unix socket path is not a socket")
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove stale runtime Unix socket: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("lstat runtime Unix socket: %w", err)
	}
	return nil
}

func secureRuntimeSocketParent(parent string) error {
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create runtime Unix socket directory: %w", err)
	}
	for current := parent; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("invalid runtime Unix socket directory")
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	if err := os.Chmod(parent, 0o700); err != nil { // #nosec G302 -- private socket directory requires owner execute.
		return fmt.Errorf("restrict runtime Unix socket directory: %w", err)
	}
	return nil
}

func removeRuntimeSocket(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return err
	}
	return os.Remove(path)
}

func runtimeRoleComponentID() string {
	id := strings.TrimSpace(os.Getenv("GORDON_COMPONENT_ID"))
	if strings.HasPrefix(id, "gordon-runtime-") && !strings.ContainsAny(id, " /\\") {
		return id
	}
	return "gordon-runtime"
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

func buildRuntimeRoleWorkerImpl(ctx context.Context, v *viper.Viper, cfg Config, log zerowrap.Logger) (runtimeRoleWorkerBundle, func(), error) {
	runtimeSocket := resolveRuntimeConfig(v.GetString("server.runtime"))
	runtimeAdapter, eventBus, err := createOutputAdapters(ctx, log, RoleRuntime, runtimeSocket)
	if err != nil {
		return runtimeRoleWorkerBundle{}, nil, err
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
		return runtimeRoleWorkerBundle{}, nil, err
	}
	if svc.tokenStore, svc.authSvc, err = createAuthService(ctx, cfg, log); err != nil {
		cleanup()
		return runtimeRoleWorkerBundle{}, nil, err
	}
	if err := setupInternalRegistryAuth(svc, log); err != nil {
		cleanup()
		return runtimeRoleWorkerBundle{}, nil, err
	}
	svc.configSvc = config.NewService(v, svc.eventBus)

	si := &serviceInit{ctx: ctx, v: v, cfg: cfg, log: log, svc: svc}
	if err := si.initSecrets(); err != nil {
		cleanup()
		return runtimeRoleWorkerBundle{}, nil, err
	}
	if svc.containerSvc, err = createContainerService(ctx, v, cfg, svc, log); err != nil {
		cleanup()
		return runtimeRoleWorkerBundle{}, nil, err
	}
	if err := svc.eventBus.Start(); err != nil {
		cleanup()
		return runtimeRoleWorkerBundle{}, nil, log.WrapErr(err, "failed to start runtime event bus")
	}

	resultStore, err := filesystem.NewRuntimeCommandResultStore(filesystem.RuntimeCommandResultStoreConfig{
		Path: filepath.Join(resolveDataDir(cfg.Server.DataDir), "runtime-command-results.json"),
	})
	if err != nil {
		cleanup()
		return runtimeRoleWorkerBundle{}, nil, fmt.Errorf("open runtime command result store: %w", err)
	}
	if err := resultStore.Healthy(); err != nil {
		cleanup()
		return runtimeRoleWorkerBundle{}, nil, fmt.Errorf("runtime command result store unhealthy: %w", err)
	}
	policy := runtimeRolePolicy(cfg, v)
	cutoverStore, err := NewMigrationCheckpointStore(migrationCheckpointPath(cfg.Server.DataDir))
	if err != nil {
		cleanup()
		return runtimeRoleWorkerBundle{}, nil, fmt.Errorf("open runtime migration cutover store: %w", err)
	}
	lifecycle := container.WithMigrationCutoverCommitter(container.NewRuntimeComponentLifecycleManager(svc.runtime, policy), cutoverStore)
	worker := container.NewRuntimeWorkerWithPolicyAndResultStore(svc.containerSvc, policy, resultStore).
		WithComponentLifecycleManager(lifecycle)
	drainRegistry = container.NewRuntimeDrainRegistry(svc.containerSvc.RuntimeDrainRouteState)
	svc.containerSvc.SetProxyDrainWaiter(drainRegistry)
	standaloneServiceManager := newRuntimeRoleStandaloneServiceManager(svc.runtime, cfg, v)
	var environmentProbe out.RuntimeEnvironmentProbe = svc.runtime
	bundle := runtimeRoleWorkerBundle{
		RuntimeWorker:            worker,
		runtime:                  svc.runtime,
		logReader:                logs.NewLocalRuntimeLogReader(svc.containerSvc, svc.runtime),
		volumeManager:            volumesSvc.NewLocalRuntimeVolumeManager(svc.runtime),
		imageManager:             images.NewLocalRuntimeImageManager(svc.runtime),
		routeDrainAckReceiver:    drainRegistry,
		standaloneServiceManager: standaloneServiceManager,
		environmentProbe:         environmentProbe,
	}
	bundle.stateSubscriber = &pollingRuntimeStateSubscriber{snapshotter: bundle, interval: time.Second, sourceComponentID: runtimeRoleComponentID()}
	return bundle, cleanup, nil
}

func newRuntimeRoleStandaloneServiceManager(runtime out.ContainerRuntime, cfg Config, v *viper.Viper) out.RuntimeStandaloneServiceManager {
	return container.NewRuntimeStandaloneServicePolicyManager(servicecfg.NewLocalRuntimeStandaloneServiceManager(runtime), runtimeRolePolicy(cfg, v))
}

func runtimeRolePolicy(cfg Config, v *viper.Viper) container.RuntimePolicy {
	managedNetworkPrefix := ""
	if v != nil {
		managedNetworkPrefix = v.GetString("network_isolation.network_prefix")
	}
	registryStorageRoot := strings.TrimSpace(cfg.Runtime.RegistryStorageRoot)
	if registryStorageRoot == "" {
		registryStorageRoot = filepath.Join(resolveDataDir(cfg.Server.DataDir), "registry")
	}
	return container.RuntimePolicy{
		Mode:                   container.RuntimePolicyModeEnforce,
		ManagedNetworkPrefix:   managedNetworkPrefix,
		AllowedImageRegistries: cfg.Images.AllowedRegistries,
		RequireImageDigest:     cfg.Images.RequireDigest,
		RuntimeComponentID:     "gordon-runtime",
		MigrationStateRoot:     filepath.Join(resolveDataDir(cfg.Server.DataDir), "migration"),
		RegistryStorageRoot:    registryStorageRoot,
	}
}
