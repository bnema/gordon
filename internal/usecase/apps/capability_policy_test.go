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

func capabilityPolicy(appNames, services []string) domain.AppCapabilityPolicy {
	return domain.AppCapabilityPolicy{
		Name:            "net-admin",
		Add:             []string{"NET_ADMIN"},
		AllowedApps:     appNames,
		AllowedServices: services,
	}
}

func TestApply_AcceptsCapabilitiesUnderAdministrativePolicy(t *testing.T) {
	store := outmocks.NewMockAppState(t)
	spec := testSpec("blog")
	spec.Services[0].Capabilities = []string{"net-admin"}
	applySuccess(store, spec)

	svc := apps.NewService(store, zerowrap.Default()).
		WithCapabilityPolicies(map[string]domain.AppCapabilityPolicy{
			"net-admin": capabilityPolicy([]string{"blog"}, []string{"web"}),
		})
	_, _, err := svc.Apply(context.Background(), spec, []byte("manifest"), false)
	require.NoError(t, err)
}

func TestApply_RejectsCapabilityWithoutPolicyBeforeStoreAccess(t *testing.T) {
	store := outmocks.NewMockAppState(t)
	spec := testSpec("blog")
	spec.Services[0].Capabilities = []string{"net-admin"}
	svc := apps.NewService(store, zerowrap.Default())

	_, _, err := svc.Apply(context.Background(), spec, []byte("manifest"), false)

	require.ErrorIs(t, err, domain.ErrCapabilityPolicy)
	assert.Contains(t, err.Error(), "blog")
	assert.Contains(t, err.Error(), "web")
	assert.Contains(t, err.Error(), "net-admin")
}

func TestApply_RejectsCapabilityOutsideAllowlists(t *testing.T) {
	spec := testSpec("blog")
	spec.Services[0].Capabilities = []string{"net-admin"}
	for name, policy := range map[string]domain.AppCapabilityPolicy{
		"app not allowed":     capabilityPolicy([]string{"other"}, []string{"web"}),
		"service not allowed": capabilityPolicy([]string{"blog"}, []string{"other"}),
	} {
		t.Run(name, func(t *testing.T) {
			store := outmocks.NewMockAppState(t)
			svc := apps.NewService(store, zerowrap.Default()).
				WithCapabilityPolicies(map[string]domain.AppCapabilityPolicy{"net-admin": policy})
			_, _, err := svc.Apply(context.Background(), spec, []byte("manifest"), false)
			require.ErrorIs(t, err, domain.ErrCapabilityPolicy)
			assert.NotContains(t, err.Error(), "NET_ADMIN")
		})
	}
}

func TestApply_CapabilityPoliciesReplaceOnReload(t *testing.T) {
	spec := testSpec("blog")
	spec.Services[0].Capabilities = []string{"net-admin"}

	store := outmocks.NewMockAppState(t)
	applySuccess(store, spec)
	svc := apps.NewService(store, zerowrap.Default()).
		WithCapabilityPolicies(map[string]domain.AppCapabilityPolicy{
			"net-admin": capabilityPolicy([]string{"blog"}, []string{"web"}),
		})
	_, _, err := svc.Apply(context.Background(), spec, []byte("manifest"), false)
	require.NoError(t, err)

	svc.SetCapabilityPolicies(nil)
	_, _, err = svc.Apply(context.Background(), spec, []byte("manifest"), false)
	require.ErrorIs(t, err, domain.ErrCapabilityPolicy)
}

func TestApply_CapabilityDryRunRefusedWithoutMutation(t *testing.T) {
	store := outmocks.NewMockAppState(t)
	spec := testSpec("blog")
	spec.Services[0].Capabilities = []string{"net-admin"}
	svc := apps.NewService(store, zerowrap.Default())

	_, _, err := svc.Apply(context.Background(), spec, []byte("manifest"), true)
	require.ErrorIs(t, err, domain.ErrCapabilityPolicy)
}
