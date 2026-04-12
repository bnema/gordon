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
		{Domain: "app-preview-old.example.com", Image: "myapp", HTTPS: true},
		{Domain: "app.example.com", Image: "myapp", HTTPS: true},
		{Domain: "alias.example.com", Image: "myapp", HTTPS: false},
	}

	baseRoutes := resolveBaseRoutes(routes, domain.PreviewConfig{Separator: "-preview-"})
	assert.ElementsMatch(t, []baseRouteInfo{
		{Domain: "app.example.com", HTTPS: true},
		{Domain: "alias.example.com", HTTPS: false},
	}, baseRoutes)
}

func TestResolveBaseRoutes_TreatsSeparatorWithoutBaseAsEligible(t *testing.T) {
	routes := []domain.Route{
		{Domain: "my--app.example.com", Image: "myapp", HTTPS: false},
		{Domain: "other.example.com", Image: "myapp", HTTPS: true},
	}

	baseRoutes := resolveBaseRoutes(routes, domain.PreviewConfig{Separator: "--"})
	assert.ElementsMatch(t, []baseRouteInfo{
		{Domain: "my--app.example.com", HTTPS: false},
		{Domain: "other.example.com", HTTPS: true},
	}, baseRoutes)
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
				{Domain: "app.example.com", Image: "myapp", HTTPS: true},
				{Domain: "alias.example.com", Image: "myapp", HTTPS: false},
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
		for _, save := range store.saves() {
			if len(save) != 2 {
				continue
			}

			pairs := map[string]domain.PreviewRoute{}
			for _, preview := range save {
				pairs[preview.Domain] = preview
			}

			app, ok := pairs["app--login.example.com"]
			if !ok || app.BaseRoute != "app.example.com" || !app.HTTPS {
				continue
			}

			alias, ok := pairs["alias--login.example.com"]
			if ok && alias.BaseRoute == "alias.example.com" && !alias.HTTPS {
				return true
			}
		}
		return false
	}, time.Second, 10*time.Millisecond)
}
