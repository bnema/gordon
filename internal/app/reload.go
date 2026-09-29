package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/container"
	"github.com/bnema/gordon/internal/usecase/proxy"
)

type configWatcher interface {
	Watch(ctx context.Context, onChange func()) error
}

// publicTLSReconciler is the interface for reconciling public TLS certificates.
type publicTLSReconciler interface {
	Reconcile(context.Context) error
}

type configReloader interface {
	Reload(ctx context.Context) error
}

type proxyConfigUpdater interface {
	UpdateConfig(config proxy.Config)
}

type reloadTrigger interface {
	Trigger(ctx context.Context) error
}

type loadedConfigApplier interface {
	ApplyLoadedConfig(ctx context.Context) error
}

type reloadCoordinator struct {
	mu                 sync.Mutex
	lastRun            time.Time
	debounce           time.Duration
	trailingTimer      *time.Timer
	trailingGeneration uint64
	stopped            bool

	// lifecycleCtx owns the context used by debounced trailing reloads. Its
	// base is detached from any caller's cancellation so a short-lived request
	// context cannot abort a coalesced apply, while lifecycleCancel lets Stop
	// tear the coordinator's own lifecycle down explicitly.
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc

	configSvc configReloader
	v         *viper.Viper
	proxySvc  proxyConfigUpdater
	// applyRuntime applies one validated config to the running daemon
	// (policies, management hosts, traffic, entrypoints, container
	// config). Nil when no runtime is wired (tests).
	applyRuntime   func(context.Context, Config) error
	registryLimits interface {
		UpdateBlobLimits(maxBlobChunkSize, maxBlobSize int64)
	}
	eventBus  out.EventPublisher
	publicTLS publicTLSReconciler
	log       zerowrap.Logger
}

func newReloadCoordinator(v *viper.Viper, configSvc configReloader, proxySvc proxyConfigUpdater, registryLimits interface {
	UpdateBlobLimits(maxBlobChunkSize, maxBlobSize int64)
}, eventBus out.EventPublisher, publicTLS publicTLSReconciler, applyRuntime func(context.Context, Config) error, log zerowrap.Logger) *reloadCoordinator {
	return &reloadCoordinator{
		debounce:       500 * time.Millisecond,
		configSvc:      configSvc,
		v:              v,
		proxySvc:       proxySvc,
		applyRuntime:   applyRuntime,
		registryLimits: registryLimits,
		eventBus:       eventBus,
		publicTLS:      publicTLS,
		log:            log,
	}
}

func (c *reloadCoordinator) SetRegistryLimits(limits interface {
	UpdateBlobLimits(maxBlobChunkSize, maxBlobSize int64)
}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.registryLimits = limits
}

// Trigger requests a config reload that first re-reads config from disk.
func (c *reloadCoordinator) Trigger(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.reloadDebouncedLocked(ctx, true)
}

// ApplyLoadedConfig requests a reload of the config the watcher already
// loaded from disk. It shares the debounce/coalescing policy with Trigger so
// a burst of fsnotify callbacks applies the final state exactly once.
func (c *reloadCoordinator) ApplyLoadedConfig(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.reloadDebouncedLocked(ctx, false)
}

// reloadDebouncedLocked is the single owner of the debounce/coalescing policy
// shared by every reload entrypoint. Requests inside the debounce window are
// merged into one trailing reload carrying the latest loadConfig intent.
func (c *reloadCoordinator) reloadDebouncedLocked(ctx context.Context, loadConfig bool) error {
	now := time.Now()
	if !c.lastRun.IsZero() && now.Sub(c.lastRun) < c.debounce {
		c.scheduleTrailingReloadLocked(ctx, loadConfig)
		return nil
	}

	return c.reloadLocked(ctx, loadConfig)
}

func (c *reloadCoordinator) scheduleTrailingReloadLocked(ctx context.Context, loadConfig bool) {
	if c.stopped {
		return
	}
	if c.trailingTimer != nil {
		c.trailingTimer.Stop()
	}
	if c.lifecycleCancel == nil {
		c.lifecycleCtx, c.lifecycleCancel = context.WithCancel(context.WithoutCancel(ctx))
	}
	c.trailingGeneration++
	generation := c.trailingGeneration
	trailingCtx := c.lifecycleCtx
	c.trailingTimer = time.AfterFunc(c.debounce, func() {
		c.runTrailingReload(trailingCtx, generation, loadConfig)
	})
	c.log.Debug().Msg("coalescing config reload trigger")
}

func (c *reloadCoordinator) runTrailingReload(ctx context.Context, generation uint64, loadConfig bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stopped || generation != c.trailingGeneration {
		return
	}
	c.trailingTimer = nil
	if err := c.reloadLocked(ctx, loadConfig); err != nil {
		c.log.Error().Err(err).Msg("failed trailing config reload")
	}
}

func (c *reloadCoordinator) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	c.trailingGeneration++
	if c.trailingTimer != nil {
		c.trailingTimer.Stop()
		c.trailingTimer = nil
	}
	if c.lifecycleCancel != nil {
		c.lifecycleCancel()
	}
}

func (c *reloadCoordinator) reloadLocked(ctx context.Context, loadConfig bool) error {
	now := time.Now()
	if loadConfig {
		if err := c.configSvc.Reload(ctx); err != nil {
			c.log.Error().Err(err).Msg("failed to reload config")
			return fmt.Errorf("failed to reload config: %w", err)
		}
	}

	if err := c.applyLoadedConfig(ctx, now); err != nil {
		return err
	}

	return nil
}

func (c *reloadCoordinator) applyLoadedConfig(ctx context.Context, now time.Time) error {
	var reloadCfg Config
	if err := c.v.Unmarshal(&reloadCfg); err != nil {
		c.log.Error().Err(err).Msg("failed to unmarshal config on reload")
		return fmt.Errorf("failed to unmarshal config on reload: %w", err)
	}
	if err := validateEntrypointMigration(c.v, reloadCfg); err != nil {
		return err
	}
	if err := validateRetiredAppConfig(c.v); err != nil {
		return err
	}
	if err := validateAppPolicies(c.v, reloadCfg); err != nil {
		return err
	}

	reloadedProxy, err := buildProxyConfig(reloadCfg, c.log)
	if err != nil {
		c.log.Error().Err(err).Msg("failed to parse proxy config on reload")
		return fmt.Errorf("failed to parse proxy config on reload: %w", err)
	}

	if c.applyRuntime != nil {
		if err := c.applyRuntime(ctx, reloadCfg); err != nil {
			c.log.Error().Err(err).Msg("failed to apply runtime config on reload")
			return fmt.Errorf("failed to apply runtime config on reload: %w", err)
		}
	}
	c.proxySvc.UpdateConfig(reloadedProxy.proxyConfig)
	if c.registryLimits != nil {
		c.registryLimits.UpdateBlobLimits(reloadedProxy.maxBlobChunkSize, reloadedProxy.maxBlobSize)
	}

	// Reconcile public TLS before publishing reload events so certificate
	// authorization reflects the loaded config even if event delivery fails.
	// A transient ACME issue must not abort the rest of the reload.
	if c.publicTLS != nil {
		if err := c.publicTLS.Reconcile(ctx); err != nil {
			c.log.Warn().Err(err).Msg("failed to reconcile public TLS certificates after reload, continuing")
		}
	}

	if c.eventBus != nil {
		if err := c.eventBus.Publish(domain.EventConfigReload, nil); err != nil {
			c.log.Error().Err(err).Msg("failed to publish config reload event")
			return fmt.Errorf("failed to publish config reload event: %w", err)
		}
	}

	c.lastRun = now

	c.log.Debug().Msg("config hot reload complete")
	return nil
}

// reloadRuntime applies a validated config to the running daemon. It
// prepares every derived value before the first side effect, so a config
// that cannot be converted leaves the whole runtime at the previous
// version. Side effects then run in one fixed order: management hosts,
// traffic graph, HTTP entrypoints, container config, app policies.
type reloadRuntime struct {
	v   *viper.Viper
	svc *services
	log zerowrap.Logger
}

// reloadPlan holds the values derived from one config before any apply.
type reloadPlan struct {
	containerCfg   container.Config
	bindPolicies   map[string]domain.AppBindPolicy
	devicePolicies map[string]domain.AppDevicePolicy
}

// Apply is the reloadCoordinator's runtime step.
func (r *reloadRuntime) Apply(ctx context.Context, cfg Config) error {
	plan, err := r.prepare(ctx, cfg)
	if err != nil {
		return err
	}
	if r.svc.containerSvc != nil {
		if err := r.applyServing(ctx, cfg, plan.containerCfg); err != nil {
			return err
		}
	}
	r.applyAppPolicies(plan)
	return nil
}

func (r *reloadRuntime) prepare(ctx context.Context, cfg Config) (reloadPlan, error) {
	var plan reloadPlan
	var err error
	if r.svc.containerSvc != nil {
		if plan.containerCfg, err = buildContainerServiceConfig(ctx, r.v, cfg, r.svc, r.log); err != nil {
			return plan, err
		}
	}
	if r.svc.appSvcImpl != nil {
		if plan.bindPolicies, err = buildAppMountPolicies(cfg); err != nil {
			return plan, err
		}
		if plan.devicePolicies, err = buildAppDevicePolicies(cfg); err != nil {
			return plan, err
		}
	}
	return plan, nil
}

// applyServing updates everything that serves traffic: management hosts,
// the serialized traffic graph, TLS/smart-TCP HTTP entrypoints, and the
// container service config.
func (r *reloadRuntime) applyServing(ctx context.Context, cfg Config, containerCfg container.Config) error {
	svc := r.svc
	managementHosts := []string{cfg.Server.GordonDomain}
	if svc.pkiSvc != nil {
		svc.pkiSvc.SetAdditionalDomains(managementHosts)
	}
	if svc.publicTLSSvc != nil {
		svc.publicTLSSvc.SetAdditionalHosts(ctx, managementHosts)
	}
	var tlsConfig *tls.Config
	if hasTLSCapableEntrypoint(cfg) && svc.httpsProxyHandler != nil {
		var err error
		if tlsConfig, err = proxyTLSConfig(cfg, svc.pkiSvc, svc.publicTLSSvc, r.log); err != nil {
			return err
		}
	}
	if err := svc.appTrafficPublisher.RebuildWithConfig(ctx, cfg); err != nil {
		return err
	}
	svc.tlsHTTPEntryPoints = registerTLSMuxHTTPServers(svc.trafficManager, cfg, svc.httpsProxyHandler, tlsConfig, svc.tlsHTTPEntryPoints)
	svc.smartHTTPEntryPoints = registerSmartTCPHTTPServers(svc.trafficManager, cfg, svc.httpProxyHandler, svc.httpsProxyHandler, tlsConfig, svc.smartHTTPEntryPoints)
	svc.containerSvc.UpdateConfig(containerCfg)
	return nil
}

// applyAppPolicies publishes the reloaded bind and device policies to both
// app services that enforce them.
func (r *reloadRuntime) applyAppPolicies(plan reloadPlan) {
	if r.svc.appSvcImpl == nil {
		return
	}
	r.svc.appSvcImpl.SetBindPolicies(plan.bindPolicies)
	r.svc.appSvcImpl.SetDevicePolicies(plan.devicePolicies)
	if r.svc.appDeploySvc != nil {
		r.svc.appDeploySvc.SetBindPolicies(plan.bindPolicies)
		r.svc.appDeploySvc.SetDevicePolicies(plan.devicePolicies)
	}
}

// setupConfigHotReload sets up config hot reload.
func setupConfigHotReload(ctx context.Context, configSvc configWatcher, coordinator loadedConfigApplier) error {
	if err := configSvc.Watch(ctx, func() {
		_ = coordinator.ApplyLoadedConfig(ctx)
	}); err != nil {
		return fmt.Errorf("failed to watch config: %w", err)
	}

	return nil
}
