package preview

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

type fakeDeployer struct {
	deployFn func(ctx context.Context, route domain.Route) (*domain.Container, error)
}

func (f *fakeDeployer) Deploy(ctx context.Context, route domain.Route) (*domain.Container, error) {
	return f.deployFn(ctx, route)
}

type fakeRouteManager struct {
	addRouteFn    func(ctx context.Context, route domain.Route) error
	removeRouteFn func(ctx context.Context, domain string) error
	getRouteFn    func(ctx context.Context, domain string) (*domain.Route, error)
}

func (f *fakeRouteManager) AddRoute(ctx context.Context, route domain.Route) error {
	if f.addRouteFn != nil {
		return f.addRouteFn(ctx, route)
	}
	return nil
}

func (f *fakeRouteManager) RemoveRoute(ctx context.Context, d string) error {
	if f.removeRouteFn != nil {
		return f.removeRouteFn(ctx, d)
	}
	return nil
}

func (f *fakeRouteManager) GetRoute(ctx context.Context, d string) (*domain.Route, error) {
	if f.getRouteFn != nil {
		return f.getRouteFn(ctx, d)
	}
	return nil, fmt.Errorf("not found")
}

func (f *fakeRouteManager) GetVolumeConfig() (bool, string, bool) {
	return true, "gordon", false
}

func TestPreviewLifecycle_Integration(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store, 2*time.Second)

	ctx := t.Context()

	// Create
	err := svc.CreatePreview(ctx, CreatePreviewRequest{
		Name:      "test-feat",
		Domain:    "myapp--test-feat.example.com",
		BaseRoute: "myapp.example.com",
		Image:     "myapp:preview-test-feat",
		HTTPS:     true,
		PreviewConfig: domain.PreviewConfig{
			TTL:       2 * time.Second,
			Separator: "--",
			DataCopy:  false,
		},
	})
	require.NoError(t, err)

	// Verify created
	all, err := svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, "test-feat", all[0].Name)
	assert.Equal(t, "myapp--test-feat.example.com", all[0].Domain)
	assert.Equal(t, "myapp.example.com", all[0].BaseRoute)

	// Extend
	err = svc.Extend(ctx, "test-feat", 1*time.Hour)
	require.NoError(t, err)

	p, err := svc.Get(ctx, "test-feat")
	require.NoError(t, err)
	assert.True(t, p.ExpiresAt.After(time.Now().Add(55*time.Minute)))

	// Delete
	err = svc.Delete(ctx, "test-feat")
	require.NoError(t, err)

	all, err = svc.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, all)
}

func TestPreviewLifecycle_CleanupExpired(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store, 1*time.Millisecond)

	ctx := t.Context()

	// Create with very short TTL
	err := svc.CreatePreview(ctx, CreatePreviewRequest{
		Name:      "short-lived",
		Domain:    "myapp--short-lived.example.com",
		BaseRoute: "myapp.example.com",
		Image:     "myapp:preview-short-lived",
		HTTPS:     true,
		PreviewConfig: domain.PreviewConfig{
			TTL:       1 * time.Millisecond,
			Separator: "--",
			DataCopy:  false,
		},
	})
	require.NoError(t, err)

	// Wait for expiry
	time.Sleep(10 * time.Millisecond)

	// Cleanup should find it
	expired := svc.CleanupExpired(ctx)
	assert.Len(t, expired, 1)
	assert.Equal(t, "short-lived", expired[0].Name)

	// Should be gone
	all, err := svc.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, all)
}

func TestPreviewLifecycle_WithDeploy(t *testing.T) {
	store := &fakeStore{}
	deployed := false
	routeAdded := ""

	mockDeployer := &fakeDeployer{
		deployFn: func(_ context.Context, route domain.Route) (*domain.Container, error) {
			deployed = true
			return &domain.Container{Name: "gordon-" + route.Domain}, nil
		},
	}
	mockRouteManager := &fakeRouteManager{
		addRouteFn: func(_ context.Context, route domain.Route) error {
			routeAdded = route.Domain
			return nil
		},
	}

	svc := NewService(store, 2*time.Second).
		WithDeployer(mockDeployer).
		WithRouteManager(mockRouteManager).
		WithRegistryDomain("reg.example.com")

	ctx := t.Context()
	err := svc.CreatePreview(ctx, CreatePreviewRequest{
		Name:      "test-feat",
		Domain:    "myapp--test-feat.example.com",
		BaseRoute: "myapp.example.com",
		Image:     "myapp:preview-test-feat",
		HTTPS:     true,
		PreviewConfig: domain.PreviewConfig{
			TTL:       2 * time.Second,
			Separator: "--",
			DataCopy:  false,
		},
	})
	require.NoError(t, err)

	assert.True(t, deployed, "deployer should have been called")
	assert.Equal(t, "myapp--test-feat.example.com", routeAdded)

	all, err := svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, "running", all[0].Status)
	assert.Equal(t, []string{"gordon-myapp--test-feat.example.com"}, all[0].Containers)
}
