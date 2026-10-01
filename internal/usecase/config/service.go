// Package config implements the configuration management use case.
package config

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// Config holds the loaded configuration.
type Config struct {
	ServerPort            int
	RegistryPort          int
	RegistryDomain        string
	LegacyRegistryDomains []string
	DataDir               string
	NetworkIsolation      bool
	NetworkPrefix         string
	ExternalRoutes        map[string]string // domain -> "host:port"
	RegistryAuthEnabled   bool
	RegistryAuthUsername  string
	RegistryAuthPassword  string
	VolumeAutoCreate      bool
	VolumePrefix          string
	VolumePreserve        bool
}

// Service implements the ConfigService interface.
type Service struct {
	viper         *viper.Viper
	eventBus      out.EventPublisher
	config        Config
	mu            sync.RWMutex
	lastSaveTime  int64 // Unix nano timestamp of last save (to debounce file watcher)
	debounceDelay int64 // Debounce delay in nanoseconds (default 500ms)
}

// NewService creates a new config service.
func NewService(v *viper.Viper, eventBus out.EventPublisher) *Service {
	return &Service{
		viper:         v,
		eventBus:      eventBus,
		debounceDelay: int64(500 * time.Millisecond), // 500ms debounce for file watcher
	}
}

// Load loads the configuration from the configured source.
func (s *Service) Load(ctx context.Context) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "Load",
	})
	log := zerowrap.FromCtx(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()

	newConfig := s.loadConfigValues()

	externalRoutes, err := loadExternalRoutes(s.viper.Get("external_routes"))
	if err != nil {
		return log.WrapErr(err, "failed to load external routes")
	}
	newConfig.ExternalRoutes = externalRoutes

	s.config = newConfig

	log.Info().
		Int("server_port", s.config.ServerPort).
		Int("registry_port", s.config.RegistryPort).
		Msg("configuration loaded")

	return nil
}

// Reload re-reads the configuration file from disk and loads it into memory.
// This should be used when you want to pick up external changes to the config file.
// It differs from Load() which only loads from cached viper values.
func (s *Service) Reload(ctx context.Context) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "Reload",
	})
	log := zerowrap.FromCtx(ctx)

	// Re-read the config file from disk
	if err := s.viper.ReadInConfig(); err != nil {
		return log.WrapErr(err, "failed to read config file")
	}

	// Load the values into memory
	return s.Load(ctx)
}

// loadConfigValues loads installation config values from viper.
// Retired application keys (routes, attachments, network_groups, auto,
// preview) are NOT loaded here: presence fails startup/reload closed
// with a config-retired diagnostic before any mutation.
func (s *Service) loadConfigValues() Config {
	// RegistryDomain identifies the registry in image references. It may be a
	// DNS name or an IP address (with an optional port). Fall back to the
	// Gordon domain only for installations that share one public endpoint.
	registryDomain := s.viper.GetString("server.registry_domain")
	if registryDomain == "" {
		registryDomain = s.viper.GetString("server.gordon_domain")
	}

	legacyRegistryDomains := append([]string{}, s.viper.GetStringSlice("server.legacy_registry_domains")...)

	return Config{
		ServerPort:            s.viper.GetInt("server.port"),
		RegistryPort:          s.viper.GetInt("server.registry_port"),
		RegistryDomain:        registryDomain,
		LegacyRegistryDomains: legacyRegistryDomains,
		DataDir:               s.viper.GetString("server.data_dir"),
		NetworkIsolation:      s.viper.GetBool("network_isolation.enabled"),
		NetworkPrefix:         s.viper.GetString("network_isolation.network_prefix"),
		VolumeAutoCreate:      s.viper.GetBool("volumes.auto_create"),
		VolumePrefix:          s.viper.GetString("volumes.prefix"),
		VolumePreserve:        s.viper.GetBool("volumes.preserve"),
		ExternalRoutes:        make(map[string]string),
	}
}

// loadStringMap loads a map[string]string from a viper value.
func loadStringMap(raw any) map[string]string {
	result := make(map[string]string)
	if raw == nil {
		return result
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return result
	}
	for k, v := range m {
		if vs, ok := v.(string); ok {
			result[k] = vs
		}
	}
	return result
}

// loadExternalRoutes loads and canonicalizes external route mappings.
func loadExternalRoutes(raw any) (map[string]string, error) {
	result := make(map[string]string)
	if raw == nil {
		return result, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return result, nil
	}
	for k, v := range m {
		vs, ok := v.(string)
		if !ok {
			continue
		}
		canonicalKey, ok := domain.CanonicalRouteDomain(k)
		if !ok {
			return nil, fmt.Errorf("invalid external route key %q: %w", k, domain.ErrRouteDomainInvalid)
		}
		if _, exists := result[canonicalKey]; exists {
			return nil, fmt.Errorf("duplicate external route key %q canonicalizes to %q", k, canonicalKey)
		}
		result[canonicalKey] = vs
	}
	return result, nil
}

// loadStringArrayMap loads a map[string][]string from a viper value.
func (s *Service) Watch(ctx context.Context, onChange func()) error {
	log := zerowrap.FromCtx(ctx)

	s.viper.OnConfigChange(func(e fsnotify.Event) {
		// Check if this event is within the debounce window of our own Save
		lastSave := atomic.LoadInt64(&s.lastSaveTime)
		if lastSave > 0 && time.Now().UnixNano()-lastSave < s.debounceDelay {
			log.Debug().Str("file", e.Name).Msg("skipping config reload (triggered by save)")
			return
		}

		log.Info().Str("file", e.Name).Msg("config file changed")

		if err := s.viper.ReadInConfig(); err != nil {
			log.WrapErr(err, "failed to reload config")
			return
		}

		if err := s.Load(ctx); err != nil {
			log.WrapErr(err, "failed to load updated config")
			return
		}

		if onChange != nil {
			onChange()
		}
	})

	s.viper.WatchConfig()
	log.Info().Msg("watching for configuration changes")

	return nil
}

// GetServerPort returns the configured server port.
func (s *Service) GetServerPort() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.ServerPort
}

// GetRegistryPort returns the configured registry port.
func (s *Service) GetRegistryPort() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.RegistryPort
}

// GetRegistryDomain returns the configured registry domain.
func (s *Service) GetRegistryDomain() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.RegistryDomain
}

// GetLegacyRegistryDomains returns the configured legacy registry domains.
func (s *Service) GetLegacyRegistryDomains() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string{}, s.config.LegacyRegistryDomains...)
}

// GetDataDir returns the configured data directory.
func (s *Service) GetDataDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.DataDir
}

// IsNetworkIsolationEnabled returns whether network isolation is enabled.
func (s *Service) IsNetworkIsolationEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.NetworkIsolation
}

// GetNetworkPrefix returns the prefix for created networks.
func (s *Service) GetNetworkPrefix() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.NetworkPrefix
}

// GetConfig returns a copy of the current configuration.
func (s *Service) GetConfig() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

// GetVolumeConfig returns volume configuration.
func (s *Service) GetVolumeConfig() (autoCreate bool, prefix string, preserve bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.VolumeAutoCreate, s.config.VolumePrefix, s.config.VolumePreserve
}

// GetExternalRoutes returns all configured external routes.
func (s *Service) GetExternalRoutes() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]string, len(s.config.ExternalRoutes))
	for k, v := range s.config.ExternalRoutes {
		result[k] = v
	}
	return result
}

// ExtractDomainFromImageName extracts domain from image names like "myapp.bamen.dev:latest".
func ExtractDomainFromImageName(imageName string) (string, bool) {
	parts := strings.Split(imageName, ":")
	imageNamePart := parts[0]

	// Simple domain check - contains at least one dot and valid characters
	if strings.Contains(imageNamePart, ".") && !strings.HasPrefix(imageNamePart, ".") && !strings.HasSuffix(imageNamePart, ".") {
		// Additional check: should not look like a registry path
		if !strings.Contains(imageNamePart, "/") || strings.Count(imageNamePart, ".") > 0 {
			return imageNamePart, true
		}
	}

	return "", false
}
