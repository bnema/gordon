// Package config implements the configuration management use case.
package config

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"

	"gordon/internal/boundaries/out"
	"gordon/internal/domain"
)

// Config holds the loaded configuration.
type Config struct {
	ServerPort           int
	RegistryPort         int
	RegistryDomain       string
	DataDir              string
	AutoRouteEnabled     bool
	NetworkIsolation     bool
	NetworkPrefix        string
	Routes               map[string]string
	ExternalRoutes       map[string]string // domain -> "host:port"
	RegistryAuthEnabled  bool
	RegistryAuthUsername string
	RegistryAuthPassword string
	VolumeAutoCreate     bool
	VolumePrefix         string
	VolumePreserve       bool
	NetworkGroups        map[string][]string
	Attachments          map[string][]string
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

	// Load configuration from viper
	s.config = s.loadConfigValues()

	// Load complex nested structures
	s.config.Routes = loadStringMap(s.viper.Get("routes"))
	s.config.ExternalRoutes = loadStringMap(s.viper.Get("external_routes"))
	s.config.NetworkGroups = loadStringArrayMap(s.viper.Get("network_groups"))
	s.config.Attachments = loadStringArrayMap(s.viper.Get("attachments"))

	log.Info().
		Int("server_port", s.config.ServerPort).
		Int("registry_port", s.config.RegistryPort).
		Int(zerowrap.FieldCount, len(s.config.Routes)).
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

// loadConfigValues loads simple config values from viper.
func (s *Service) loadConfigValues() Config {
	// Prefer gordon_domain over registry_domain
	registryDomain := s.viper.GetString("server.gordon_domain")
	if registryDomain == "" {
		registryDomain = s.viper.GetString("server.registry_domain")
	}

	return Config{
		ServerPort:           s.viper.GetInt("server.port"),
		RegistryPort:         s.viper.GetInt("server.registry_port"),
		RegistryDomain:       registryDomain,
		DataDir:              s.viper.GetString("server.data_dir"),
		AutoRouteEnabled:     s.viper.GetBool("auto_route.enabled"),
		NetworkIsolation:     s.viper.GetBool("network_isolation.enabled"),
		NetworkPrefix:        s.viper.GetString("network_isolation.network_prefix"),
		RegistryAuthEnabled:  s.viper.GetBool("auth.enabled"),
		RegistryAuthUsername: s.viper.GetString("auth.username"),
		RegistryAuthPassword: s.viper.GetString("auth.password"),
		VolumeAutoCreate:     s.viper.GetBool("volumes.auto_create"),
		VolumePrefix:         s.viper.GetString("volumes.prefix"),
		VolumePreserve:       s.viper.GetBool("volumes.preserve"),
		Routes:               make(map[string]string),
		ExternalRoutes:       make(map[string]string),
		NetworkGroups:        make(map[string][]string),
		Attachments:          make(map[string][]string),
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

// loadStringArrayMap loads a map[string][]string from a viper value.
func loadStringArrayMap(raw any) map[string][]string {
	result := make(map[string][]string)
	if raw == nil {
		return result
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return result
	}
	for k, v := range m {
		if arr, ok := v.([]any); ok {
			var strs []string
			for _, item := range arr {
				if s, ok := item.(string); ok {
					strs = append(strs, s)
				}
			}
			result[k] = strs
		}
	}
	return result
}

// GetRoutes returns all configured routes.
func (s *Service) GetRoutes(_ context.Context) []domain.Route {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var routes []domain.Route
	for domainName, image := range s.config.Routes {
		route := domain.Route{
			Image: image,
			HTTPS: true,
		}

		// Check if domain has http:// prefix
		if strings.HasPrefix(domainName, "http://") {
			route.Domain = strings.TrimPrefix(domainName, "http://")
			route.HTTPS = false
		} else {
			route.Domain = domainName
		}

		routes = append(routes, route)
	}

	return routes
}

// GetRoute returns a single route by domain.
func (s *Service) GetRoute(_ context.Context, domainName string) (*domain.Route, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	image, exists := s.config.Routes[domainName]
	if !exists {
		return nil, domain.ErrRouteNotFound
	}

	route := &domain.Route{
		Domain: domainName,
		Image:  image,
		HTTPS:  true,
	}

	// Check if domain has http:// prefix
	if strings.HasPrefix(domainName, "http://") {
		route.Domain = strings.TrimPrefix(domainName, "http://")
		route.HTTPS = false
	}

	return route, nil
}

// AddRoute adds a new route to the configuration and persists it.
func (s *Service) AddRoute(ctx context.Context, route domain.Route) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "AddRoute",
		"domain":              route.Domain,
	})
	log := zerowrap.FromCtx(ctx)

	// Validate route
	if route.Domain == "" {
		return domain.ErrRouteDomainEmpty
	}
	if route.Image == "" {
		return domain.ErrRouteImageEmpty
	}

	// Store previous value for rollback
	s.mu.Lock()
	previousImage, existed := s.config.Routes[route.Domain]
	if s.config.Routes == nil {
		s.config.Routes = make(map[string]string)
	}
	s.config.Routes[route.Domain] = route.Image
	s.mu.Unlock()

	// Persist to disk - rollback on failure
	if err := s.Save(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to persist route to disk, rolling back")
		s.mu.Lock()
		if existed {
			s.config.Routes[route.Domain] = previousImage
		} else {
			delete(s.config.Routes, route.Domain)
		}
		s.mu.Unlock()
		return err
	}

	log.Info().Str("image", route.Image).Msg("route added to configuration")
	return nil
}

// UpdateRoute updates an existing route and persists it.
func (s *Service) UpdateRoute(ctx context.Context, route domain.Route) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "UpdateRoute",
		"domain":              route.Domain,
	})
	log := zerowrap.FromCtx(ctx)

	// Validate route
	if route.Domain == "" {
		return domain.ErrRouteDomainEmpty
	}
	if route.Image == "" {
		return domain.ErrRouteImageEmpty
	}

	// Store previous value for rollback
	s.mu.Lock()
	previousImage, exists := s.config.Routes[route.Domain]
	if !exists {
		s.mu.Unlock()
		return domain.ErrRouteNotFound
	}
	s.config.Routes[route.Domain] = route.Image
	s.mu.Unlock()

	// Persist to disk - rollback on failure
	if err := s.Save(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to persist route update to disk, rolling back")
		s.mu.Lock()
		s.config.Routes[route.Domain] = previousImage
		s.mu.Unlock()
		return err
	}

	log.Info().Str("image", route.Image).Msg("route updated")
	return nil
}

// RemoveRoute removes a route from the configuration and persists it.
func (s *Service) RemoveRoute(ctx context.Context, domainName string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "RemoveRoute",
		"domain":              domainName,
	})
	log := zerowrap.FromCtx(ctx)

	// Store previous value for rollback
	s.mu.Lock()
	previousImage, exists := s.config.Routes[domainName]
	if !exists {
		s.mu.Unlock()
		return domain.ErrRouteNotFound
	}
	delete(s.config.Routes, domainName)
	s.mu.Unlock()

	// Persist to disk - rollback on failure
	if err := s.Save(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to persist route removal to disk, rolling back")
		s.mu.Lock()
		s.config.Routes[domainName] = previousImage
		s.mu.Unlock()
		return err
	}

	log.Info().Msg("route removed")
	return nil
}

// Save persists the current configuration to disk.
func (s *Service) Save(ctx context.Context) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "SaveConfig",
	})
	log := zerowrap.FromCtx(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Update viper with current config
	s.viper.Set("routes", s.config.Routes)
	s.viper.Set("attachments", s.config.Attachments)

	// Record save time to debounce file watcher events
	atomic.StoreInt64(&s.lastSaveTime, time.Now().UnixNano())

	// Write config to disk
	if err := s.viper.WriteConfig(); err != nil {
		return log.WrapErr(err, "failed to write config")
	}

	log.Info().Msg("configuration saved to disk")
	return nil
}

// Watch starts watching for configuration changes.
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

		// Publish config reload event
		if s.eventBus != nil {
			if err := s.eventBus.Publish(domain.EventConfigReload, domain.ConfigReloadPayload{
				Source: "file",
			}); err != nil {
				log.WrapErr(err, "failed to publish config reload event")
			}
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

// GetDataDir returns the configured data directory.
func (s *Service) GetDataDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.DataDir
}

// IsAutoRouteEnabled returns whether auto-route is enabled.
func (s *Service) IsAutoRouteEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.AutoRouteEnabled
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

// GetRegistryAuthConfig returns registry authentication configuration.
func (s *Service) GetRegistryAuthConfig() (enabled bool, username, password string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.RegistryAuthEnabled, s.config.RegistryAuthUsername, s.config.RegistryAuthPassword
}

// GetVolumeConfig returns volume configuration.
func (s *Service) GetVolumeConfig() (autoCreate bool, prefix string, preserve bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.VolumeAutoCreate, s.config.VolumePrefix, s.config.VolumePreserve
}

// GetNetworkGroups returns network group configuration.
func (s *Service) GetNetworkGroups() map[string][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string][]string)
	for k, v := range s.config.NetworkGroups {
		result[k] = append([]string{}, v...)
	}
	return result
}

// GetAttachments returns attachment configuration.
// Deprecated: Use GetAllAttachments instead.
func (s *Service) GetAttachments() map[string][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string][]string)
	for k, v := range s.config.Attachments {
		result[k] = append([]string{}, v...)
	}
	return result
}

// GetAllAttachments returns all configured attachments.
func (s *Service) GetAllAttachments(_ context.Context) map[string][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string][]string)
	for k, v := range s.config.Attachments {
		result[k] = append([]string{}, v...)
	}
	return result
}

// GetAttachmentsFor returns attachments for a specific domain or network group.
func (s *Service) GetAttachmentsFor(_ context.Context, domainOrGroup string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	images, exists := s.config.Attachments[domainOrGroup]
	if !exists {
		return nil, domain.ErrAttachmentNotFound
	}

	return append([]string{}, images...), nil
}

// AddAttachment adds an image to a domain/group's attachments.
func (s *Service) AddAttachment(ctx context.Context, domainOrGroup, image string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "AddAttachment",
		"target":              domainOrGroup,
		"image":               image,
	})
	log := zerowrap.FromCtx(ctx)

	// Validate input
	if domainOrGroup == "" {
		return domain.ErrAttachmentTargetEmpty
	}
	if image == "" {
		return domain.ErrAttachmentImageEmpty
	}

	s.mu.Lock()

	// Initialize map if needed
	if s.config.Attachments == nil {
		s.config.Attachments = make(map[string][]string)
	}

	// Check if already exists
	existing := s.config.Attachments[domainOrGroup]
	for _, img := range existing {
		if img == image {
			s.mu.Unlock()
			return domain.ErrAttachmentExists
		}
	}

	// Store previous value for rollback
	previousImages := append([]string{}, existing...)
	hadKey := len(existing) > 0

	// Add the image
	s.config.Attachments[domainOrGroup] = append(existing, image)
	s.mu.Unlock()

	// Persist to disk - rollback on failure
	if err := s.Save(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to persist attachment to disk, rolling back")
		s.mu.Lock()
		if hadKey {
			s.config.Attachments[domainOrGroup] = previousImages
		} else {
			delete(s.config.Attachments, domainOrGroup)
		}
		s.mu.Unlock()
		return err
	}

	log.Info().Msg("attachment added to configuration")
	return nil
}

// RemoveAttachment removes an image from a domain/group's attachments.
func (s *Service) RemoveAttachment(ctx context.Context, domainOrGroup, image string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "RemoveAttachment",
		"target":              domainOrGroup,
		"image":               image,
	})
	log := zerowrap.FromCtx(ctx)

	// Validate input
	if domainOrGroup == "" {
		return domain.ErrAttachmentTargetEmpty
	}
	if image == "" {
		return domain.ErrAttachmentImageEmpty
	}

	s.mu.Lock()

	existing, exists := s.config.Attachments[domainOrGroup]
	if !exists {
		s.mu.Unlock()
		return domain.ErrAttachmentNotFound
	}

	// Find and remove the image
	found := false
	newImages := make([]string, 0, len(existing))
	for _, img := range existing {
		if img == image {
			found = true
		} else {
			newImages = append(newImages, img)
		}
	}

	if !found {
		s.mu.Unlock()
		return domain.ErrAttachmentNotFound
	}

	// Store previous value for rollback
	previousImages := existing

	// Update or remove the key
	if len(newImages) == 0 {
		delete(s.config.Attachments, domainOrGroup)
	} else {
		s.config.Attachments[domainOrGroup] = newImages
	}
	s.mu.Unlock()

	// Persist to disk - rollback on failure
	if err := s.Save(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to persist attachment removal to disk, rolling back")
		s.mu.Lock()
		s.config.Attachments[domainOrGroup] = previousImages
		s.mu.Unlock()
		return err
	}

	log.Info().Msg("attachment removed from configuration")
	return nil
}

// GetExternalRoutes returns all configured external routes.
func (s *Service) GetExternalRoutes() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]string)
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
