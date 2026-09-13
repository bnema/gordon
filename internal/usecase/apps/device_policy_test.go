package apps_test

import (
	"context"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apps"
)

func devicePolicy(apps, services []string) domain.AppDevicePolicy {
	return domain.AppDevicePolicy{
		Name:            "test_gpu",
		CDI:             []string{"example.com/gpu=GPU-test-uuid"},
		AllowedApps:     apps,
		AllowedServices: services,
	}
}

func TestApply_AcceptsDevicesUnderAdministrativePolicy(t *testing.T) {
	store := outmocks.NewMockAppState(t)
	spec := testSpec("blog")
	spec.Services[0].Devices = []string{"test_gpu"}
	applySuccess(store, spec)

	svc := apps.NewService(store, zerowrap.Default()).
		WithDevicePolicies(map[string]domain.AppDevicePolicy{
			"test_gpu": devicePolicy([]string{"blog"}, []string{"web"}),
		})
	_, _, err := svc.Apply(context.Background(), spec, []byte("manifest"), false)
	require.NoError(t, err)
}

func TestApply_RejectsDeviceWithoutPolicyBeforeStoreAccess(t *testing.T) {
	store := outmocks.NewMockAppState(t)
	spec := testSpec("blog")
	spec.Services[0].Devices = []string{"test_gpu"}
	svc := apps.NewService(store, zerowrap.Default())

	_, _, err := svc.Apply(context.Background(), spec, []byte("manifest"), false)

	require.ErrorIs(t, err, domain.ErrDevicePolicy)
	assert.Contains(t, err.Error(), "blog")
	assert.Contains(t, err.Error(), "web")
	assert.Contains(t, err.Error(), "test_gpu")
}

func TestApply_RejectsDeviceOutsideAllowlists(t *testing.T) {
	spec := testSpec("blog")
	spec.Services[0].Devices = []string{"test_gpu"}

	t.Run("app not allowed", func(t *testing.T) {
		store := outmocks.NewMockAppState(t)
		svc := apps.NewService(store, zerowrap.Default()).
			WithDevicePolicies(map[string]domain.AppDevicePolicy{
				"test_gpu": devicePolicy([]string{"other"}, []string{"web"}),
			})
		_, _, err := svc.Apply(context.Background(), spec, []byte("manifest"), false)
		require.ErrorIs(t, err, domain.ErrDevicePolicy)
		assert.NotContains(t, err.Error(), "GPU-test-uuid")
	})

	t.Run("service not allowed", func(t *testing.T) {
		store := outmocks.NewMockAppState(t)
		svc := apps.NewService(store, zerowrap.Default()).
			WithDevicePolicies(map[string]domain.AppDevicePolicy{
				"test_gpu": devicePolicy([]string{"blog"}, []string{"other"}),
			})
		_, _, err := svc.Apply(context.Background(), spec, []byte("manifest"), false)
		require.ErrorIs(t, err, domain.ErrDevicePolicy)
		assert.NotContains(t, err.Error(), "GPU-test-uuid")
	})
}

// TestApply_DevicePoliciesReplaceOnReload proves a reload-style replacement
// takes effect immediately: the second apply is refused before any store
// access.
func TestApply_DevicePoliciesReplaceOnReload(t *testing.T) {
	spec := testSpec("blog")
	spec.Services[0].Devices = []string{"test_gpu"}

	store := outmocks.NewMockAppState(t)
	applySuccess(store, spec)
	svc := apps.NewService(store, zerowrap.Default()).
		WithDevicePolicies(map[string]domain.AppDevicePolicy{
			"test_gpu": devicePolicy([]string{"blog"}, []string{"web"}),
		})
	_, _, err := svc.Apply(context.Background(), spec, []byte("manifest"), false)
	require.NoError(t, err)

	svc.SetDevicePolicies(map[string]domain.AppDevicePolicy{
		"test_gpu": devicePolicy([]string{"other"}, []string{"web"}),
	})
	_, _, err = svc.Apply(context.Background(), spec, []byte("manifest"), false)
	require.ErrorIs(t, err, domain.ErrDevicePolicy)
}

func TestApply_DeviceDryRunRefusedWithoutMutation(t *testing.T) {
	store := outmocks.NewMockAppState(t)
	spec := testSpec("blog")
	spec.Services[0].Devices = []string{"test_gpu"}
	svc := apps.NewService(store, zerowrap.Default())

	_, _, err := svc.Apply(context.Background(), spec, []byte("manifest"), true)
	require.ErrorIs(t, err, domain.ErrDevicePolicy)
	store.AssertNotCalled(t, "StageApply", context.Background(), spec.Name, uint64(0))
}
