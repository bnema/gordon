package preview

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

type nameLockEntry struct {
	mu   *sync.Mutex
	refs int
}

// Deployer abstracts the container service deploy method.
type Deployer interface {
	Deploy(ctx context.Context, route domain.Route) (*domain.Container, error)
}

// RouteManager abstracts config service methods needed for preview route lifecycle.
type RouteManager interface {
	AddRoute(ctx context.Context, route domain.Route) error
	RemoveRoute(ctx context.Context, domain string) error
	GetRoute(ctx context.Context, domain string) (*domain.Route, error)
	GetVolumeConfig() (autoCreate bool, prefix string, preserve bool)
}

// Service manages preview lifecycle: CRUD, TTL, persistence.
type Service struct {
	store          out.PreviewStore
	defaultTTL     time.Duration
	deployer       Deployer
	routeManager   RouteManager
	volumeCloner   VolumeCloner
	envLoader      out.EnvLoader
	registryDomain string
	previews       []domain.PreviewRoute
	mu             sync.RWMutex
	nameLocks      map[string]*nameLockEntry
	nameLockMu     sync.Mutex
}

func NewService(store out.PreviewStore, defaultTTL time.Duration) *Service {
	return &Service{
		store:      store,
		defaultTTL: defaultTTL,
		nameLocks:  make(map[string]*nameLockEntry),
	}
}

func (s *Service) WithDeployer(d Deployer) *Service {
	s.deployer = d
	return s
}

func (s *Service) WithRouteManager(rm RouteManager) *Service {
	s.routeManager = rm
	return s
}

func (s *Service) WithVolumeCloner(vc VolumeCloner) *Service {
	s.volumeCloner = vc
	return s
}

func (s *Service) WithRegistryDomain(d string) *Service {
	s.registryDomain = d
	return s
}

func (s *Service) WithEnvLoader(el out.EnvLoader) *Service {
	s.envLoader = el
	return s
}

func (s *Service) Load(ctx context.Context) error {
	previews, err := s.store.Load(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.previews = previews
	s.mu.Unlock()
	return nil
}

func (s *Service) Add(ctx context.Context, p domain.PreviewRoute) error {
	s.mu.Lock()
	s.previews = append(s.previews, p)
	cp := make([]domain.PreviewRoute, len(s.previews))
	copy(cp, s.previews)
	s.mu.Unlock()
	return s.store.Save(ctx, cp)
}

func (s *Service) Update(ctx context.Context, p domain.PreviewRoute) error {
	s.mu.Lock()
	for i := range s.previews {
		if s.previews[i].Name == p.Name {
			s.previews[i] = p
			cp := make([]domain.PreviewRoute, len(s.previews))
			copy(cp, s.previews)
			s.mu.Unlock()
			return s.store.Save(ctx, cp)
		}
	}
	s.mu.Unlock()
	return fmt.Errorf("preview %q: %w", p.Name, domain.ErrPreviewNotFound)
}

func (s *Service) Delete(ctx context.Context, name string) error {
	s.mu.Lock()
	var deleted domain.PreviewRoute
	found := false
	filtered := make([]domain.PreviewRoute, 0, len(s.previews))
	for _, p := range s.previews {
		if p.Name == name {
			found = true
			deleted = p
		} else {
			filtered = append(filtered, p)
		}
	}
	if !found {
		s.mu.Unlock()
		return fmt.Errorf("preview %q: %w", name, domain.ErrPreviewNotFound)
	}
	s.previews = filtered
	cp := make([]domain.PreviewRoute, len(s.previews))
	copy(cp, s.previews)
	s.mu.Unlock()

	if err := s.store.Save(ctx, cp); err != nil {
		return err
	}

	// Clean up the route from config so proxy stops routing to this domain.
	if s.routeManager != nil && deleted.Domain != "" {
		log := zerowrap.FromCtx(ctx)
		if err := s.routeManager.RemoveRoute(ctx, deleted.Domain); err != nil {
			log.Debug().Err(err).Str("domain", deleted.Domain).Msg("preview route already removed from config")
		}
	}
	return nil
}

func (s *Service) Get(_ context.Context, name string) (*domain.PreviewRoute, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.previews {
		if s.previews[i].Name == name {
			p := s.previews[i]
			return &p, nil
		}
	}
	return nil, fmt.Errorf("preview %q: %w", name, domain.ErrPreviewNotFound)
}

func (s *Service) List(_ context.Context) ([]domain.PreviewRoute, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make([]domain.PreviewRoute, len(s.previews))
	copy(cp, s.previews)
	return cp, nil
}

func (s *Service) Extend(ctx context.Context, name string, ttl time.Duration) error {
	s.mu.Lock()
	for i := range s.previews {
		if s.previews[i].Name == name {
			s.previews[i].ExpiresAt = time.Now().Add(ttl)
			cp := make([]domain.PreviewRoute, len(s.previews))
			copy(cp, s.previews)
			s.mu.Unlock()
			return s.store.Save(ctx, cp)
		}
	}
	s.mu.Unlock()
	return fmt.Errorf("preview %q: %w", name, domain.ErrPreviewNotFound)
}

func (s *Service) GetExpired() []domain.PreviewRoute {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var expired []domain.PreviewRoute
	for _, p := range s.previews {
		if p.IsExpired(time.Now()) {
			expired = append(expired, p)
		}
	}
	return expired
}

// CleanupExpired removes expired previews from the service and persists.
// Returns the removed previews so the caller can teardown their resources.
func (s *Service) CleanupExpired(ctx context.Context) []domain.PreviewRoute {
	s.mu.Lock()
	var expired []domain.PreviewRoute
	var active []domain.PreviewRoute
	for _, p := range s.previews {
		if p.IsExpired(time.Now()) {
			expired = append(expired, p)
		} else {
			active = append(active, p)
		}
	}
	s.previews = active
	cp := make([]domain.PreviewRoute, len(active))
	copy(cp, active)
	s.mu.Unlock()

	if len(expired) > 0 {
		if err := s.store.Save(ctx, cp); err != nil {
			// Log but don't fail — expired previews are already removed from memory
			log := zerowrap.FromCtx(ctx)
			log.Warn().Err(err).Msg("failed to persist preview state after cleanup")
		}
	}
	return expired
}

// StartTicker starts a background goroutine that checks for expired previews.
// The teardownFn is called for each expired preview to clean up containers/volumes.
func (s *Service) StartTicker(ctx context.Context, interval time.Duration, teardownFn func(context.Context, domain.PreviewRoute)) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				expired := s.CleanupExpired(ctx)
				for _, p := range expired {
					teardownFn(ctx, p)
				}
			}
		}
	}()
}

func (s *Service) AcquireNameLock(name string) *sync.Mutex {
	s.nameLockMu.Lock()
	defer s.nameLockMu.Unlock()
	entry, ok := s.nameLocks[name]
	if !ok {
		entry = &nameLockEntry{mu: &sync.Mutex{}}
		s.nameLocks[name] = entry
	}
	entry.refs++
	return entry.mu
}

// ReleaseNameLock decrements the refcount and removes the per-name mutex when no longer in use.
func (s *Service) ReleaseNameLock(name string) {
	s.nameLockMu.Lock()
	defer s.nameLockMu.Unlock()
	entry, ok := s.nameLocks[name]
	if !ok {
		return
	}
	entry.refs--
	if entry.refs <= 0 {
		delete(s.nameLocks, name)
	}
}

func (s *Service) qualifyImage(image string) string {
	if s.registryDomain != "" && !strings.Contains(image, "/") {
		return s.registryDomain + "/" + image
	}
	return image
}

// generateVolumeName mirrors the naming convention in container/service.go.
// This MUST stay in sync with the container service's generateVolumeName.
func generateVolumeName(prefix, domainName, volumePath string) string {
	return fmt.Sprintf("%s-%s-%s",
		prefix,
		strings.ReplaceAll(domainName, ".", "-"),
		strings.ReplaceAll(strings.Trim(volumePath, "/"), "/", "-"))
}

func (s *Service) cloneBaseRouteVolumes(ctx context.Context, baseRoute, previewDomain string) ([]string, error) {
	if s.volumeCloner == nil || s.routeManager == nil {
		return nil, nil
	}

	_, prefix, _ := s.routeManager.GetVolumeConfig()
	if prefix == "" {
		prefix = "gordon"
	}

	log := zerowrap.FromCtx(ctx)
	basePrefix := prefix + "-" + strings.ReplaceAll(baseRoute, ".", "-") + "-"

	vols, err := s.volumeCloner.ListVolumes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}

	var sourceVols []string
	for _, v := range vols {
		if strings.HasPrefix(v.Name, basePrefix) {
			sourceVols = append(sourceVols, v.Name)
		}
	}

	if len(sourceVols) == 0 {
		return nil, nil
	}

	namer := func(sourceVolName string) string {
		pathSuffix := strings.TrimPrefix(sourceVolName, basePrefix)
		return generateVolumeName(prefix, previewDomain, pathSuffix)
	}

	for _, src := range sourceVols {
		log.Debug().Str("source", src).Str("target", namer(src)).Msg("will clone volume for preview")
	}

	return CloneVolumes(ctx, s.volumeCloner, namer, sourceVols)
}

// CreatePreviewRequest contains parameters for creating a preview.
type CreatePreviewRequest struct {
	Name          string
	Domain        string
	BaseRoute     string
	Image         string
	HTTPS         bool
	PreviewConfig domain.PreviewConfig
}

// CreatePreview orchestrates the full preview creation: lock, clone volumes, start containers, register.
func (s *Service) CreatePreview(ctx context.Context, req CreatePreviewRequest) error {
	lock := s.AcquireNameLock(req.Name)
	lock.Lock()
	defer func() {
		lock.Unlock()
		s.ReleaseNameLock(req.Name)
	}()

	log := zerowrap.FromCtx(ctx)
	now := time.Now()

	preview := domain.PreviewRoute{
		Domain:    req.Domain,
		Image:     req.Image,
		BaseRoute: req.BaseRoute,
		Name:      req.Name,
		CreatedAt: now,
		ExpiresAt: now.Add(req.PreviewConfig.TTL),
		HTTPS:     req.HTTPS,
		Status:    domain.PreviewStatusDeploying,
	}

	if err := s.Add(ctx, preview); err != nil {
		return fmt.Errorf("register preview %q: %w", req.Name, err)
	}

	if req.PreviewConfig.DataCopy {
		cloned, err := s.cloneBaseRouteVolumes(ctx, req.BaseRoute, req.Domain)
		if err != nil {
			log.Warn().Err(err).Str("base_route", req.BaseRoute).Msg("failed to clone volumes, deploying with empty volumes")
		} else if len(cloned) > 0 {
			preview.Volumes = cloned
		}
	}

	if s.routeManager != nil {
		imageRef := s.qualifyImage(req.Image)
		if err := s.routeManager.AddRoute(ctx, domain.Route{
			Domain: req.Domain,
			Image:  imageRef,
			HTTPS:  req.HTTPS,
		}); err != nil {
			preview.Status = domain.PreviewStatusFailed
			_ = s.Update(ctx, preview)
			return fmt.Errorf("register preview route %q: %w", req.Domain, err)
		}
	}

	var baseEnv []string
	if s.envLoader != nil {
		var envErr error
		baseEnv, envErr = s.envLoader.LoadEnv(ctx, req.BaseRoute)
		if envErr != nil {
			log.Warn().Err(envErr).Str("base_route", req.BaseRoute).Msg("failed to load base route env, deploying without env vars")
		}
	}

	if s.deployer != nil {
		imageRef := s.qualifyImage(req.Image)
		deployCtx := domain.WithInternalDeploy(ctx)
		container, err := s.deployer.Deploy(deployCtx, domain.Route{
			Domain: req.Domain,
			Image:  imageRef,
			HTTPS:  req.HTTPS,
			Env:    baseEnv,
		})
		if err != nil {
			preview.Status = domain.PreviewStatusFailed
			_ = s.Update(ctx, preview)
			return fmt.Errorf("deploy preview %q: %w", req.Name, err)
		}
		preview.Containers = []string{container.Name}
	}

	preview.Status = domain.PreviewStatusRunning
	if err := s.Update(ctx, preview); err != nil {
		return fmt.Errorf("finalize preview %q: %w", req.Name, err)
	}

	log.Info().
		Str("name", req.Name).
		Str("domain", req.Domain).
		Str("image", req.Image).
		Str("base_route", req.BaseRoute).
		Msg("preview environment created")

	return nil
}
