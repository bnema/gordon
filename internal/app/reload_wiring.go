// Package app provides the application initialization and wiring.
package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"

	"github.com/bnema/gordon/internal/adapters/in/http/registry"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/proxy"
	"github.com/bnema/gordon/pkg/bytesize"
)

// proxyConfigResult holds parsed proxy and blob chunk size config.
type proxyConfigResult struct {
	proxyConfig      proxy.Config
	maxBlobChunkSize int64
	maxBlobSize      int64
}

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

// setupConfigHotReload sets up config hot reload.
func setupConfigHotReload(ctx context.Context, configSvc configWatcher, coordinator loadedConfigApplier) error {
	if err := configSvc.Watch(ctx, func() {
		_ = coordinator.ApplyLoadedConfig(ctx)
	}); err != nil {
		return fmt.Errorf("failed to watch config: %w", err)
	}

	return nil
}

type reloadCoordinator struct {
	mu       sync.Mutex
	lastRun  time.Time
	debounce time.Duration

	configSvc            configReloader
	v                    *viper.Viper
	proxySvc             proxyConfigUpdater
	applyContainerConfig func(context.Context, Config) error
	registryLimits       interface {
		UpdateBlobLimits(maxBlobChunkSize, maxBlobSize int64)
	}
	eventBus  out.EventPublisher
	publicTLS publicTLSReconciler
	log       zerowrap.Logger
}

func newReloadCoordinator(v *viper.Viper, configSvc configReloader, proxySvc proxyConfigUpdater, registryLimits interface {
	UpdateBlobLimits(maxBlobChunkSize, maxBlobSize int64)
}, eventBus out.EventPublisher, publicTLS publicTLSReconciler, log zerowrap.Logger) *reloadCoordinator {
	return &reloadCoordinator{
		debounce:       500 * time.Millisecond,
		configSvc:      configSvc,
		v:              v,
		proxySvc:       proxySvc,
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

func (c *reloadCoordinator) SetContainerConfigApplier(apply func(context.Context, Config) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.applyContainerConfig = apply
}

func (c *reloadCoordinator) Trigger(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.reloadLocked(ctx, true)
}

func (c *reloadCoordinator) ApplyLoadedConfig(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.reloadLocked(ctx, false)
}

func (c *reloadCoordinator) reloadLocked(ctx context.Context, loadConfig bool) error {
	now := time.Now()
	if !c.lastRun.IsZero() && now.Sub(c.lastRun) < c.debounce {
		c.log.Debug().Dur("since_last_reload", now.Sub(c.lastRun)).Msg("skipping config reload trigger due to debounce")
		return nil
	}

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

	reloadedProxy, err := buildProxyConfig(reloadCfg, c.log)
	if err != nil {
		c.log.Error().Err(err).Msg("failed to parse proxy config on reload")
		return fmt.Errorf("failed to parse proxy config on reload: %w", err)
	}

	if c.applyContainerConfig != nil {
		if err := c.applyContainerConfig(ctx, reloadCfg); err != nil {
			c.log.Error().Err(err).Msg("failed to apply container config on reload")
			return fmt.Errorf("failed to apply container config on reload: %w", err)
		}
	}
	// Control-only processes deliberately do not own a proxy. Hot reload still
	// updates their config/event state, but must not dereference monolith-only
	// proxy wiring.
	if c.proxySvc != nil {
		c.proxySvc.UpdateConfig(reloadedProxy.proxyConfig)
	}
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

// buildProxyConfig parses size-related config fields and builds the proxy config.
func buildProxyConfig(cfg Config, log zerowrap.Logger) (*proxyConfigResult, error) {
	maxProxyBodySize := int64(512 << 20) // 512MB default
	if cfg.Server.MaxProxyBodySize != "" {
		parsedSize, err := bytesize.Parse(cfg.Server.MaxProxyBodySize)
		if err != nil {
			return nil, log.WrapErrWithFields(err, "invalid server.max_proxy_body_size configuration", map[string]any{"value": cfg.Server.MaxProxyBodySize})
		}
		maxProxyBodySize = parsedSize
	}

	maxBlobChunkSize := int64(registry.DefaultMaxBlobChunkSize)
	if cfg.Server.MaxBlobChunkSize != "" {
		parsedSize, err := bytesize.Parse(cfg.Server.MaxBlobChunkSize)
		if err != nil {
			return nil, log.WrapErrWithFields(err, "invalid server.max_blob_chunk_size configuration", map[string]any{"value": cfg.Server.MaxBlobChunkSize})
		}
		maxBlobChunkSize = parsedSize
	}

	maxBlobSize := int64(registry.DefaultMaxBlobSize)
	if cfg.Server.MaxBlobSize != "" {
		parsedSize, err := bytesize.Parse(cfg.Server.MaxBlobSize)
		if err != nil {
			return nil, log.WrapErrWithFields(err, "invalid server.max_blob_size configuration", map[string]any{"value": cfg.Server.MaxBlobSize})
		}
		maxBlobSize = parsedSize
	}

	maxProxyResponseSize := int64(1 << 30) // 1GB default
	if cfg.Server.MaxProxyResponseSize != "" {
		parsedSize, err := bytesize.Parse(cfg.Server.MaxProxyResponseSize)
		if err != nil {
			return nil, log.WrapErrWithFields(err, "invalid server.max_proxy_response_size configuration", map[string]any{"value": cfg.Server.MaxProxyResponseSize})
		}
		maxProxyResponseSize = parsedSize
	}

	maxConcurrentConns := cfg.Server.MaxConcurrentConns
	if maxConcurrentConns < 0 {
		maxConcurrentConns = 10000 // default when explicitly set to -1
	}
	// 0 means no limit (as documented in proxy.Config)

	registryDomain, _ := resolveRegistryDomains(cfg)

	return &proxyConfigResult{
		proxyConfig: proxy.Config{
			RegistryDomain:     registryDomain,
			RegistryPort:       cfg.Server.RegistryPort,
			MaxBodySize:        maxProxyBodySize,
			MaxResponseSize:    maxProxyResponseSize,
			MaxConcurrentConns: maxConcurrentConns,
		},
		maxBlobChunkSize: maxBlobChunkSize,
		maxBlobSize:      maxBlobSize,
	}, nil
}
