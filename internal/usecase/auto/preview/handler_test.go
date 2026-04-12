package preview

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

type captureStore struct {
	mu       sync.Mutex
	previews []domain.PreviewRoute
	history  [][]domain.PreviewRoute
}

func (s *captureStore) Load(_ context.Context) ([]domain.PreviewRoute, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]domain.PreviewRoute, len(s.previews))
	copy(cp, s.previews)
	return cp, nil
}

func (s *captureStore) Save(_ context.Context, previews []domain.PreviewRoute) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.previews = make([]domain.PreviewRoute, len(previews))
	copy(s.previews, previews)
	snapshot := make([]domain.PreviewRoute, len(previews))
	copy(snapshot, previews)
	s.history = append(s.history, snapshot)
	return nil
}

func (s *captureStore) snapshot() []domain.PreviewRoute {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]domain.PreviewRoute, len(s.previews))
	copy(cp, s.previews)
	return cp
}

func (s *captureStore) saves() [][]domain.PreviewRoute {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([][]domain.PreviewRoute, len(s.history))
	for i := range s.history {
		cp[i] = make([]domain.PreviewRoute, len(s.history[i]))
		copy(cp[i], s.history[i])
	}
	return cp
}

type fakeAutoConfigProvider struct {
	previewConfig  domain.PreviewConfig
	routesByImage  map[string][]domain.Route
	allowedDomains []string
}

func (f *fakeAutoConfigProvider) IsAutoEnabled() bool                    { return false }
func (f *fakeAutoConfigProvider) IsPreviewEnabled() bool                 { return true }
func (f *fakeAutoConfigProvider) GetPreviewTagPatterns() []string        { return f.previewConfig.TagPatterns }
func (f *fakeAutoConfigProvider) GetPreviewConfig() domain.PreviewConfig { return f.previewConfig }
func (f *fakeAutoConfigProvider) GetAllowedDomains() []string            { return f.allowedDomains }
func (f *fakeAutoConfigProvider) FindRoutesByImage(_ context.Context, imageName string) []domain.Route {
	return f.routesByImage[imageName]
}

func TestAutoPreviewHandler_CanHandle(t *testing.T) {
	h := &AutoPreviewHandler{}
	assert.True(t, h.CanHandle(domain.EventImagePushed))
	assert.False(t, h.CanHandle(domain.EventConfigReload))
}

func TestResolveBaseRoutes_ReturnsAllEligibleBases(t *testing.T) {
	routes := []domain.Route{
		{Domain: "app-preview-old.example.com", Image: "myapp"},
		{Domain: "app.example.com", Image: "myapp"},
		{Domain: "alias.example.com", Image: "myapp"},
	}

	baseRoutes := resolveBaseRoutes(routes, domain.PreviewConfig{Separator: "-preview-"})
	assert.ElementsMatch(t, []string{"app.example.com", "alias.example.com"}, baseRoutes)
}

func TestResolveBaseRoutes_TreatsSeparatorWithoutBaseAsEligible(t *testing.T) {
	routes := []domain.Route{
		{Domain: "my--app.example.com", Image: "myapp"},
		{Domain: "other.example.com", Image: "myapp"},
	}

	baseRoutes := resolveBaseRoutes(routes, domain.PreviewConfig{Separator: "--"})
	assert.ElementsMatch(t, []string{"my--app.example.com", "other.example.com"}, baseRoutes)
}

func TestAutoPreviewHandler_Handle_CreatesPreviewPerBaseRoute(t *testing.T) {
	store := &captureStore{}
	svc := NewService(store, time.Hour)
	h := NewAutoPreviewHandler(t.Context(), &fakeAutoConfigProvider{
		previewConfig: domain.PreviewConfig{
			Separator:   "--",
			TagPatterns: []string{"preview-*"},
			TTL:         time.Hour,
		},
		allowedDomains: []string{"*"},
		routesByImage: map[string][]domain.Route{
			"myapp": {
				{Domain: "app.example.com", Image: "myapp"},
				{Domain: "alias.example.com", Image: "myapp"},
			},
		},
	}, svc)

	err := h.Handle(t.Context(), domain.Event{
		ID:   "evt-1",
		Type: domain.EventImagePushed,
		Data: domain.ImagePushedPayload{Name: "myapp", Reference: "preview-login"},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return len(store.saves()) >= 4
	}, time.Second, 10*time.Millisecond)

	var domains []string
	var baseRoutes []string
	for _, save := range store.saves() {
		for _, preview := range save {
			domains = append(domains, preview.Domain)
			baseRoutes = append(baseRoutes, preview.BaseRoute)
		}
	}

	assert.Contains(t, domains, "app--login.example.com")
	assert.Contains(t, domains, "alias--login.example.com")
	assert.Contains(t, baseRoutes, "app.example.com")
	assert.Contains(t, baseRoutes, "alias.example.com")
}
