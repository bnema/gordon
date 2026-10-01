package apps_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apps"
)

func bindPolicy(root, source string, apps, services []string) domain.AppBindPolicy {
	return domain.AppBindPolicy{
		Name:            "config",
		Source:          source,
		Root:            root,
		AllowedApps:     apps,
		AllowedServices: services,
	}
}

func TestApply_AcceptsBindsUnderAdministrativePolicy(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "binds")
	require.NoError(t, os.MkdirAll(source, 0o755))

	store := outmocks.NewMockAppState(t)
	spec := testSpec("blog")
	spec.Services[0].Binds = []domain.AppBind{{Name: "config", Path: "/etc/app.conf"}}
	applySuccess(store, spec)

	svc := apps.NewService(store, zerowrap.Default()).
		WithBindPolicies(map[string]domain.AppBindPolicy{
			"config": bindPolicy(root, source, []string{"blog"}, []string{"web"}),
		})
	_, _, err := svc.Apply(context.Background(), spec, []byte("manifest"), false)
	require.NoError(t, err)
}

func TestApply_RejectsBindWithoutPolicyBeforeStoreAccess(t *testing.T) {
	store := outmocks.NewMockAppState(t)
	spec := testSpec("blog")
	spec.Services[0].Binds = []domain.AppBind{{Name: "config", Path: "/etc/app.conf"}}
	svc := apps.NewService(store, zerowrap.Default())

	_, _, err := svc.Apply(context.Background(), spec, []byte("manifest"), false)

	require.ErrorIs(t, err, domain.ErrBindPolicy)
	assert.Contains(t, err.Error(), "blog")
	assert.Contains(t, err.Error(), "web")
	assert.Contains(t, err.Error(), "config")
}

func TestApply_RejectsBindOutsideAllowlists(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "binds")
	require.NoError(t, os.MkdirAll(source, 0o755))
	spec := testSpec("blog")
	spec.Services[0].Binds = []domain.AppBind{{Name: "config", Path: "/etc/app.conf"}}

	t.Run("app not allowed", func(t *testing.T) {
		store := outmocks.NewMockAppState(t)
		svc := apps.NewService(store, zerowrap.Default()).
			WithBindPolicies(map[string]domain.AppBindPolicy{
				"config": bindPolicy(root, source, []string{"other"}, []string{"web"}),
			})
		_, _, err := svc.Apply(context.Background(), spec, []byte("manifest"), false)
		require.ErrorIs(t, err, domain.ErrBindPolicy)
		assert.NotContains(t, err.Error(), source)
	})

	t.Run("service not allowed", func(t *testing.T) {
		store := outmocks.NewMockAppState(t)
		svc := apps.NewService(store, zerowrap.Default()).
			WithBindPolicies(map[string]domain.AppBindPolicy{
				"config": bindPolicy(root, source, []string{"blog"}, []string{"other"}),
			})
		_, _, err := svc.Apply(context.Background(), spec, []byte("manifest"), false)
		require.ErrorIs(t, err, domain.ErrBindPolicy)
		assert.NotContains(t, err.Error(), source)
	})
}

func TestApply_RejectsBindSourceOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	source := filepath.Join(outside, "binds")
	require.NoError(t, os.MkdirAll(source, 0o755))
	store := outmocks.NewMockAppState(t)
	spec := testSpec("blog")
	spec.Services[0].Binds = []domain.AppBind{{Name: "config", Path: "/etc/app.conf"}}
	svc := apps.NewService(store, zerowrap.Default()).
		WithBindPolicies(map[string]domain.AppBindPolicy{
			"config": bindPolicy(root, source, []string{"blog"}, []string{"web"}),
		})

	_, _, err := svc.Apply(context.Background(), spec, []byte("manifest"), false)

	require.ErrorIs(t, err, domain.ErrBindPolicy)
	assert.NotContains(t, err.Error(), source, "apply errors must never leak the policy source path")
	assert.NotContains(t, err.Error(), outside)
}

// TestApply_BindPoliciesReplaceOnReload proves a reload-style replacement takes
// effect immediately: the second apply is refused before any store access.
func TestApply_BindPoliciesReplaceOnReload(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "binds")
	require.NoError(t, os.MkdirAll(source, 0o755))
	spec := testSpec("blog")
	spec.Services[0].Binds = []domain.AppBind{{Name: "config", Path: "/etc/app.conf"}}

	store := outmocks.NewMockAppState(t)
	applySuccess(store, spec)
	svc := apps.NewService(store, zerowrap.Default()).
		WithBindPolicies(map[string]domain.AppBindPolicy{
			"config": bindPolicy(root, source, []string{"blog"}, []string{"web"}),
		})
	_, _, err := svc.Apply(context.Background(), spec, []byte("manifest"), false)
	require.NoError(t, err)

	svc.SetBindPolicies(map[string]domain.AppBindPolicy{
		"config": bindPolicy(root, source, []string{"other"}, []string{"web"}),
	})
	_, _, err = svc.Apply(context.Background(), spec, []byte("manifest"), false)
	require.ErrorIs(t, err, domain.ErrBindPolicy)
}
