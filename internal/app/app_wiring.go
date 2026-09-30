package app

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/out/appsecrets"
	"github.com/bnema/gordon/internal/adapters/out/appstate"
	"github.com/bnema/gordon/internal/adapters/out/httpprober"
	"github.com/bnema/gordon/internal/adapters/out/imageref"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apps"
	"github.com/bnema/gordon/internal/usecase/apptraffic"
	"github.com/bnema/gordon/internal/usecase/deployment"
	"github.com/bnema/gordon/internal/usecase/health"
)

// appDeployEngine is the deployment-engine subset the daemon-owned app
// service executes through. It mirrors the usecase's own port so the
// composition root stays decoupled from engine internals.
type appDeployEngine interface {
	StartDeploy(ctx context.Context, input deployment.DeployInput) (*deployment.StartDeployResult, error)
	AbandonDeploy(ctx context.Context, claim deployment.DeployClaim) error
	ExecuteDeploy(ctx context.Context, claim deployment.DeployClaim) (*deployment.DeployResult, error)
	Stop(ctx context.Context, app, opID string) (*deployment.LifecycleResult, error)
	Start(ctx context.Context, app, opID string) (*deployment.LifecycleResult, error)
	Restart(ctx context.Context, app, service, opID string) (*deployment.LifecycleResult, error)
	Remove(ctx context.Context, app, opID string) (*deployment.LifecycleResult, error)
}

// newAppDaemonService builds the daemon-owned app administration service.
// ctx is the daemon/supervisor lifecycle context: background deploy
// executions derive from it and are cancelled by Shutdown during graceful
// teardown. It is deliberately never an HTTP request context, so request
// cancellation cannot abort an in-flight replacement.
func newAppDaemonService(ctx context.Context, store out.AppState, deploy appDeployEngine, secrets out.SecretWriter, log zerowrap.Logger) *apps.AppServiceImpl {
	return apps.NewAppServiceImpl(store, deploy, secrets, log).WithDaemonContext(ctx)
}

// initApps wires the single v3 app engine: bbolt state, image digests,
// pass-backed secrets, deployment/lifecycle execution, and traffic
// activation. The daemon is the sole app-state writer; CLI reaches the
// engine only through the admin /apps surface.
func (si *serviceInit) initApps() error {
	dataDir := resolveDataDir(si.cfg.Server.DataDir)
	store, err := appstate.NewStore(dataDir, si.log)
	if err != nil {
		return si.log.WrapErr(err, "failed to open app state")
	}
	store.WithRevisionRetention(si.cfg.Apps.RevisionRetention)
	si.svc.appState = store
	observeManagedContainers(si.svc.metrics, store, si.log)
	// One process-wide GC barrier owns the ordering between resource
	// acquisition/publication and destructive prune.
	si.svc.gcBarrier = newGCBarrier()
	if si.svc.backupSvc != nil {
		si.svc.backupSvc.WithAppState(store)
	}
	if si.svc.volumeBackupSvc != nil {
		si.svc.volumeBackupSvc.WithAppState(store)
	}
	if si.svc.imageSvc != nil {
		si.svc.imageSvc.WithPrunePorts(store, si.svc.runtime, si.svc.gcBarrier)
	}
	if si.svc.volumeSvc != nil {
		si.svc.volumeSvc.WithPrunePorts(store, si.svc.runtime, si.svc.gcBarrier)
	}
	registryDomain := si.svc.configSvc.GetRegistryDomain()
	imagePolicy := domain.ImageSourcePolicy{
		AllowedRegistries:    si.cfg.Images.AllowedRegistries,
		RequireDigest:        si.cfg.Images.RequireDigest,
		InstallationRegistry: registryDomain,
	}
	if err := imagePolicy.Validate(); err != nil {
		return fmt.Errorf("invalid image registry policy: %w", err)
	}
	resolver := imageref.NewResolver(registryDomain, si.svc.manifestStorage, nil).WithPolicy(imagePolicy)
	// The publisher is the single serialized HTTP/L4 boundary: raw
	// activations, fail-closed recovery withdrawal, and reload all go
	// through it. It resolves writers/permission paths late-bound.
	si.svc.appTrafficPublisher = newAppTrafficPublisher(si.svc, si.cfg)
	limits, err := containerResourceLimits(si.cfg)
	if err != nil {
		return err
	}
	si.svc.appDeploySvc = deployment.NewService(deployment.Deps{
		State:   store,
		Runtime: si.svc.runtime,
		Images:  resolver,
		Secrets: si.svc.serviceSecretProvider,
		Registry: deployment.RegistryConfig{
			Domain:      registryDomain,
			PullAddress: net.JoinHostPort("127.0.0.1", strconv.Itoa(si.svc.configSvc.GetRegistryPort())),
			Username:    si.svc.internalRegUser,
			Password:    si.svc.internalRegPass,
		},
		Networks: deployment.NetworkConfig{
			Prefix:   si.cfg.NetworkIsolation.Prefix,
			Internal: si.cfg.NetworkIsolation.Internal,
		},
		Limits:      limits,
		ImagePolicy: imagePolicy,
		Traffic:     si.svc.appTrafficPublisher,
	}, si.log).WithGCBarrier(si.svc.gcBarrier)
	si.svc.appActivator = apptraffic.NewActivator(si.log)
	mountPolicies, err := buildAppMountPolicies(si.cfg)
	if err != nil {
		return err
	}
	devicePolicies, err := buildAppDevicePolicies(si.cfg)
	if err != nil {
		return err
	}
	si.svc.appDeploySvc.SetBindPolicies(mountPolicies)
	si.svc.appDeploySvc.SetDevicePolicies(devicePolicies)
	appSvcImpl := newAppDaemonService(si.ctx, store, si.svc.appDeploySvc, appsecrets.NewStore(si.log), si.log).
		WithEntrypoints(appEntrypointListeners(si.cfg)).
		WithGCBarrier(si.svc.gcBarrier).
		WithImagePolicy(imagePolicy).
		WithBindPolicies(mountPolicies).
		WithDevicePolicies(devicePolicies)
	si.svc.appSvcImpl = appSvcImpl
	si.svc.appSvc = appSvcImpl
	// Health checks resolve from ACTIVE state (loopback backends).
	si.svc.healthSvc = health.NewService(store, si.svc.runtime, httpprober.New(), si.log)
	// ACTIVE-derived host index for the proxy: rebuilt after every
	// activation and at boot. Wired here (store is open); the proxy
	// itself was built earlier in initRuntimeAndProxy.
	si.svc.appHostIndex = apptraffic.NewHostIndex()
	if si.svc.proxySvc != nil {
		si.svc.proxySvc.WithAppTargets(si.svc.appHostIndex)
	}
	return nil
}

// appRouteSource adapts the ACTIVE-derived host index plus installation
// external routes to the route-source interfaces (PKI AppRoutes,
// public-TLS RouteSource). bbolt stays authoritative; this adapter only
// projects. Fail-closed: unwired index yields zero app hosts. The index
// is late-bound (built after PKI/public-TLS), so it resolves per call.
type appRouteSource struct {
	index    func() *apptraffic.HostIndex
	external func() map[string]string
}

// AppHosts implements out.AppHostSource.
func (s *appRouteSource) AppHosts() []out.AppHost {
	if s == nil || s.index == nil {
		return nil
	}
	index := s.index()
	if index == nil {
		return nil
	}
	return index.AppHosts()
}

// GetExternalRoutes implements the installation half of out.AppRoutes
// and the public-TLS RouteSource.
func (s *appRouteSource) GetExternalRoutes() map[string]string {
	if s == nil || s.external == nil {
		return nil
	}
	return s.external()
}

// GetRoutes implements the public-TLS RouteSource as a consumer-local
// adapter: host-only entries, Image explicitly unused (no app meaning).
func (s *appRouteSource) GetRoutes(context.Context) []domain.Route {
	hosts := s.AppHosts()
	routes := make([]domain.Route, 0, len(hosts))
	for _, h := range hosts {
		routes = append(routes, domain.Route{Domain: h.Host})
	}
	return routes
}

// appRoutes returns the shared ACTIVE-derived route source, late-bound
// to the host index built in initApps.
func (si *serviceInit) appRoutes() *appRouteSource {
	return &appRouteSource{
		index:    func() *apptraffic.HostIndex { return si.svc.appHostIndex },
		external: func() map[string]string { return si.svc.configSvc.GetExternalRoutes() },
	}
}

// appEntrypointPolicies maps installation entrypoints to the projection
// policy (trusted CIDRs, raw-fallback behavior, transport limits).
func appEntrypointPolicies(cfg Config) map[string]apptraffic.EntrypointPolicy {
	policies := make(map[string]apptraffic.EntrypointPolicy, len(cfg.EntryPoints))
	for name, entry := range cfg.EntryPoints {
		policies[name] = apptraffic.EntrypointPolicy{
			Name:                    name,
			Address:                 entry.Address,
			Protocol:                entry.Protocol,
			TrustedCIDRs:            entry.TrustedCIDRs,
			RawFallback:             entry.RawFallback,
			RawFallbackTrustedCIDRs: entry.RawFallbackTrustedCIDRs,
			AllowPublicRawFallback:  entry.AllowPublicRawFallback,
		}
	}
	return policies
}

// appEntrypointListeners maps installation entrypoints to the canonical
// listener an app L4 publish declaration must match exactly.
func appEntrypointListeners(cfg Config) map[string]domain.EntryPointListener {
	listeners := make(map[string]domain.EntryPointListener, len(cfg.EntryPoints))
	for name, entry := range cfg.EntryPoints {
		listeners[name] = domain.EntryPointListener{Address: entry.Address, Protocol: entry.Protocol}
	}
	return listeners
}

// rebuildAppHostIndex re-projects ACTIVE state into the proxy host index
// and applies the full HTTP/L4 traffic graph through the serialized
// publisher. Call after every activation (deploy/start/restart/stop/
// remove) and at boot; callers decide whether a failure is fatal to the
// mutation.
func rebuildAppHostIndex(ctx context.Context, svc *services) error {
	if svc.appTrafficPublisher == nil {
		return nil
	}
	return svc.appTrafficPublisher.RebuildTraffic(ctx)
}

// reconcileAppsAtBoot reconciles declarative apps intended to run,
// rebuilds the proxy host index from reconciled ACTIVE state, then
// starts the single periodic recovery monitor. Failures only warn: the
// monitor still starts so a partially failed boot self-heals.
func reconcileAppsAtBoot(ctx context.Context, svc *services, log zerowrap.Logger) {
	if svc.appDeploySvc == nil {
		return
	}
	if err := svc.appDeploySvc.ReconcileBoot(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to reconcile apps at boot")
	}
	if err := rebuildAppHostIndex(ctx, svc); err != nil {
		log.Warn().Err(err).Msg("failed to rebuild app host index at boot")
	}
	// Exactly one monitor per process: a second boot reconciliation must
	// not orphan a running loop in the daemon context.
	if svc.appMonitor == nil {
		svc.appMonitor = newAppMonitor(svc.appDeploySvc, log)
	}
	svc.appMonitor.Start(ctx)
}
