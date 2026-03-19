package preview

import (
	"context"
	"fmt"
	"sync"
	"time"

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
		if p.IsExpired() {
			expired = append(expired, p)
		}
	}
	return expired
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
