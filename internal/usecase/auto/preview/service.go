package preview

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// Service manages preview lifecycle: CRUD, TTL, persistence.
type Service struct {
	store      out.PreviewStore
	defaultTTL time.Duration
	previews   []domain.PreviewRoute
	mu         sync.RWMutex
	nameLocks  map[string]*sync.Mutex
	nameLockMu sync.Mutex
}

func NewService(store out.PreviewStore, defaultTTL time.Duration) *Service {
	return &Service{
		store:      store,
		defaultTTL: defaultTTL,
		nameLocks:  make(map[string]*sync.Mutex),
	}
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

func (s *Service) Delete(ctx context.Context, name string) error {
	s.mu.Lock()
	filtered := make([]domain.PreviewRoute, 0, len(s.previews))
	for _, p := range s.previews {
		if p.Name != name {
			filtered = append(filtered, p)
		}
	}
	s.previews = filtered
	cp := make([]domain.PreviewRoute, len(s.previews))
	copy(cp, s.previews)
	s.mu.Unlock()
	return s.store.Save(ctx, cp)
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
	return nil, fmt.Errorf("preview %q not found", name)
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
	return fmt.Errorf("preview %q not found", name)
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
	mu, ok := s.nameLocks[name]
	if !ok {
		mu = &sync.Mutex{}
		s.nameLocks[name] = mu
	}
	return mu
}

// ReleaseNameLock removes the per-name mutex from the map after use.
func (s *Service) ReleaseNameLock(name string) {
	s.nameLockMu.Lock()
	defer s.nameLockMu.Unlock()
	delete(s.nameLocks, name)
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

	now := time.Now()
	preview := domain.PreviewRoute{
		Domain:    req.Domain,
		Image:     req.Image,
		BaseRoute: req.BaseRoute,
		Name:      req.Name,
		CreatedAt: now,
		ExpiresAt: now.Add(req.PreviewConfig.TTL),
		HTTPS:     req.HTTPS,
	}

	// TODO: Volume cloning and container startup will be wired in Task 13
	if err := s.Add(ctx, preview); err != nil {
		return fmt.Errorf("register preview %q: %w", req.Name, err)
	}
	return nil
}
