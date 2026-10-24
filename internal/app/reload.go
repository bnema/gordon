package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"
	"sync/atomic"
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
	mu                 reloadMutex
	lastRun            time.Time
	debounce           time.Duration
	trailingTimer      *time.Timer
	trailingGeneration uint64
	stopped            atomic.Bool

	// lifecycleCtx owns the context used by debounced trailing reloads. Its
	// base is detached from any caller's cancellation so a short-lived request
	// context cannot abort a coalesced apply, while lifecycleCancel lets Stop
	// tear the coordinator's own lifecycle down explicitly.
	lifecycleMu     sync.Mutex
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
	// handover holds the startup-only handover limits. Reloads re-unmarshal
	// config without them, so every reloaded config inherits these values.
	handover HandoverConfig
	log      zerowrap.Logger
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

// SetHandoverConfig records the startup handover limits that every reload
// keeps, since traffic.handover is not reloadable.
func (c *reloadCoordinator) SetHandoverConfig(handover HandoverConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handover = handover
}

// checkReloadedHandover validates a reloaded traffic.handover without
// applying it: the startup values stay in force until restart. It also
// warns when the kept hold can outlast the reloaded transfer idle timeout.
func (c *reloadCoordinator) checkReloadedHandover(cfg Config) {
	if _, err := parseHandoverLimits(cfg); err != nil {
		c.log.Warn().Err(err).Msg("invalid traffic.handover ignored until restart; keeping startup values")
	}
	// HTTP options were validated by loadedConfig, so the error is moot.
	_ = checkHandoverIdleTimeout(c.log, cfg, c.handover.MaxHold)
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
	return c.requestReload(ctx, true)
}

// ApplyLoadedConfig requests a reload of the config the watcher already
// loaded from disk. It shares the debounce/coalescing policy with Trigger so
// a burst of fsnotify callbacks applies the final state exactly once.
func (c *reloadCoordinator) ApplyLoadedConfig(ctx context.Context) error {
	return c.requestReload(ctx, false)
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
	if c.stopped.Load() {
		return
	}
	if c.trailingTimer != nil {
		c.trailingTimer.Stop()
	}
	c.reloadContext(ctx)
	c.trailingGeneration++
	generation := c.trailingGeneration
	trailingCtx := c.lifecycleCtx
	c.trailingTimer = time.AfterFunc(c.debounce, func() {
		c.runTrailingReload(trailingCtx, generation, loadConfig)
	})
	c.log.Debug().Msg("coalescing config reload trigger")
}

func (c *reloadCoordinator) runTrailingReload(ctx context.Context, generation uint64, loadConfig bool) {
	if err := c.mu.lockContext(ctx); err != nil {
		return
	}
	defer c.mu.Unlock()

	if c.stopped.Load() || generation != c.trailingGeneration {
		return
	}
	c.trailingTimer = nil
	if err := c.reloadLocked(ctx, loadConfig); err != nil {
		c.log.Error().Err(err).Msg("failed trailing config reload")
	}
}

// Stop cancels reload work without waiting for the reload serialization lock.
func (c *reloadCoordinator) Stop() {
	c.stopped.Store(true)
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.lifecycleCancel != nil {
		c.lifecycleCancel()
	}
}

func (c *reloadCoordinator) reloadContext(ctx context.Context) context.Context {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.lifecycleCancel == nil {
		c.lifecycleCtx, c.lifecycleCancel = context.WithCancel(context.WithoutCancel(ctx))
	}
	if c.stopped.Load() {
		c.lifecycleCancel()
	}
	return c.lifecycleCtx
}

func (c *reloadCoordinator) reloadLocked(ctx context.Context, loadConfig bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.reloadContext(ctx), cancel)
	defer stop()
	if c.stopped.Load() {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
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
	if err := c.checkReload(ctx); err != nil {
		return err
	}
	reloadCfg, err := c.loadedConfig()
	if err != nil {
		return err
	}
	c.checkReloadedHandover(reloadCfg)
	reloadCfg.Handover = c.handover

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
	if err := c.checkReload(ctx); err != nil {
		return err
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
// version. Side effects then run in one fixed order: traffic graph (the
// only fallible step), management hosts, HTTP entrypoints, container
// config, app policies.
type reloadRuntime struct {
	v   *viper.Viper
	svc *services
	log zerowrap.Logger
}

// reloadPlan holds the values derived from one config before any apply.
type reloadPlan struct {
	setImage       SetImageSnapshot
	imagePolicy    domain.ImageSourcePolicy
	containerCfg   container.Config
	tlsConfig      *tls.Config
	bindPolicies   map[string]domain.AppBindPolicy
	devicePolicies map[string]domain.AppDevicePolicy
	capPolicies    map[string]domain.AppCapabilityPolicy
}

// Apply is the reloadCoordinator's runtime step.
func (r *reloadRuntime) Apply(ctx context.Context, cfg Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	plan, err := r.prepare(ctx, cfg)
	if err != nil {
		return fmt.Errorf("prepare reload: %w", err)
	}
	if r.svc.containerSvc != nil {
		if err := r.applyServing(ctx, cfg, plan); err != nil {
			return fmt.Errorf("apply serving config: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.applyAppPolicies(plan)
	r.svc.publishSetImage(plan.setImage, plan.imagePolicy, cfg)
	return nil
}

func (r *reloadRuntime) prepare(ctx context.Context, cfg Config) (reloadPlan, error) {
	var plan reloadPlan
	var err error
	if plan.setImage, err = buildSetImageSnapshot(cfg); err != nil {
		return plan, err
	}
	if plan.imagePolicy, err = buildImagePolicy(cfg); err != nil {
		return plan, err
	}
	if r.svc.containerSvc != nil {
		if plan.containerCfg, err = buildContainerServiceConfig(ctx, r.v, cfg, r.svc, r.log); err != nil {
			return plan, err
		}
		// The TLS config selects certificates at handshake time, so it is
		// safe to build before management hosts change; building it here
		// surfaces keypair load errors before any side effect.
		if hasTLSCapableEntrypoint(cfg) && r.svc.httpsProxyHandler != nil {
			if plan.tlsConfig, err = proxyTLSConfig(cfg, r.svc.pkiSvc, r.svc.publicTLSSvc, r.log); err != nil {
				return plan, err
			}
		}
	}
	if r.svc.appSvcImpl != nil {
		if plan.bindPolicies, err = buildAppMountPolicies(cfg); err != nil {
			return plan, err
		}
		if plan.devicePolicies, err = buildAppDevicePolicies(cfg); err != nil {
			return plan, err
		}
		if plan.capPolicies, err = buildAppCapabilityPolicies(cfg); err != nil {
			return plan, err
		}
	}
	return plan, nil
}

// applyServing updates everything that serves traffic. The serialized
// traffic graph rebuild is the only fallible step and runs first, so a
// rejected graph leaves management hosts, entrypoints, and container
// config at the previous version.
func (r *reloadRuntime) applyServing(ctx context.Context, cfg Config, plan reloadPlan) error {
	svc := r.svc
	if err := svc.appTrafficPublisher.RebuildWithConfig(ctx, cfg); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	hosts := managementHosts(cfg)
	if svc.pkiSvc != nil {
		svc.pkiSvc.SetAdditionalDomains(hosts)
	}
	if svc.publicTLSSvc != nil {
		svc.publicTLSSvc.SetAdditionalHosts(ctx, hosts)
	}
	svc.tlsHTTPEntryPoints = registerTLSMuxHTTPServers(svc.trafficManager, cfg, svc.httpsProxyHandler, plan.tlsConfig, svc.tlsHTTPEntryPoints)
	svc.smartHTTPEntryPoints = registerSmartTCPHTTPServers(svc.trafficManager, cfg, svc.httpProxyHandler, svc.httpsProxyHandler, plan.tlsConfig, svc.smartHTTPEntryPoints)
	svc.containerSvc.UpdateConfig(plan.containerCfg)
	return nil
}

// applyAppPolicies publishes the reloaded bind, device, and capability policies to both
// app services that enforce them.
func (r *reloadRuntime) applyAppPolicies(plan reloadPlan) {
	if r.svc.appSvcImpl == nil {
		return
	}
	r.svc.appSvcImpl.SetBindPolicies(plan.bindPolicies)
	r.svc.appSvcImpl.SetDevicePolicies(plan.devicePolicies)
	r.svc.appSvcImpl.SetCapabilityPolicies(plan.capPolicies)
	if r.svc.appDeploySvc != nil {
		r.svc.appDeploySvc.SetBindPolicies(plan.bindPolicies)
		r.svc.appDeploySvc.SetDevicePolicies(plan.devicePolicies)
		r.svc.appDeploySvc.SetCapabilityPolicies(plan.capPolicies)
	}
}

// setupConfigHotReload sets up config hot reload.
func setupConfigHotReload(ctx context.Context, configSvc configWatcher, coordinator loadedConfigApplier) error {
	if err := configSvc.Watch(ctx, func() {
		if ctx.Err() == nil {
			_ = coordinator.ApplyLoadedConfig(ctx)
		}
	}); err != nil {
		return fmt.Errorf("failed to watch config: %w", err)
	}

	return nil
}

func (c *reloadCoordinator) checkReload(ctx context.Context) error {
	if c.stopped.Load() {
		return context.Canceled
	}
	return ctx.Err()
}

func (c *reloadCoordinator) loadedConfig() (Config, error) {
	var reloadCfg Config
	if err := c.v.Unmarshal(&reloadCfg); err != nil {
		c.log.Error().Err(err).Msg("failed to unmarshal config on reload")
		return Config{}, fmt.Errorf("failed to unmarshal config on reload: %w", err)
	}
	if err := validateHTTPTimeouts(reloadCfg); err != nil {
		return Config{}, err
	}
	if err := validateEntrypointMigration(c.v, reloadCfg); err != nil {
		return Config{}, err
	}
	if err := validateRetiredAppConfig(c.v); err != nil {
		return Config{}, err
	}
	if err := validateSetImageConfig(c.v, reloadCfg); err != nil {
		return Config{}, err
	}
	if err := validateAppPolicies(c.v, reloadCfg); err != nil {
		return Config{}, err
	}

	return reloadCfg, nil
}

// reloadMutex preserves serialization while allowing queued signal/watcher work
// to leave on cancellation instead of waiting behind a slow reload.
type reloadMutex struct {
	once  sync.Once
	token chan struct{}
}

func (m *reloadMutex) init()   { m.once.Do(func() { m.token = make(chan struct{}, 1) }) }
func (m *reloadMutex) Lock()   { m.init(); m.token <- struct{}{} }
func (m *reloadMutex) Unlock() { <-m.token }
func (m *reloadMutex) lockContext(ctx context.Context) error {
	m.init()
	select {
	case m.token <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *reloadCoordinator) requestReload(ctx context.Context, loadConfig bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.reloadContext(ctx), cancel)
	defer stop()
	if err := c.mu.lockContext(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	if err := c.checkReload(ctx); err != nil {
		return err
	}
	return c.reloadDebouncedLocked(ctx, loadConfig)
}
