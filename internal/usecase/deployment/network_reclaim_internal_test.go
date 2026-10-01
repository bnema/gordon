package deployment

import (
	"context"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func reclaimService(t *testing.T, state *outmocks.MockAppState, runtime *outmocks.MockContainerRuntime) *Service {
	t.Helper()
	return NewService(Deps{
		State: state, Runtime: runtime,
		Networks: NetworkConfig{Prefix: "gordon"},
	}, zerowrap.Default())
}

// TestReclaimPrivateNetworks_RemovesOwnedEmptyIncarnationNetwork proves the
// app's own empty private network is removed immediately, by exact derived
// name and verified ownership.
func TestReclaimPrivateNetworks_RemovesOwnedEmptyIncarnationNetwork(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)

	private := domain.AppPrivateNetworkName("gordon", "app-1")
	ownership := domain.AppOwnership{
		App: "blog", ID: "app-1",
		Networks: []domain.AppOwnedNetwork{{Name: private, Role: domain.AppNetworkRolePrivate}},
	}
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{
		{Name: private, Labels: domain.AppPrivateNetworkLabels("blog", "app-1")},
	}, nil).Once()
	runtime.EXPECT().RemoveNetwork(mock.Anything, private).Return(nil).Once()

	warnings, err := reclaimService(t, state, runtime).reclaimPrivateNetworks(ctx, "blog", ownership)
	require.NoError(t, err)
	assert.Empty(t, warnings)
}

// TestReclaimPrivateNetworks_KeepsSharedNetworks proves a declared shared
// network is never removed by an app removal.
func TestReclaimPrivateNetworks_KeepsSharedNetworks(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)

	private := domain.AppPrivateNetworkName("gordon", "app-1")
	shared := domain.AppSharedNetworkName("gordon", "shared-backend")
	ownership := domain.AppOwnership{
		App: "blog", ID: "app-1",
		Networks: []domain.AppOwnedNetwork{
			{Name: private, Role: domain.AppNetworkRolePrivate},
			{Name: shared, Role: domain.AppNetworkRoleShared},
		},
	}
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{
		{Name: private, Labels: domain.AppPrivateNetworkLabels("blog", "app-1")},
		{Name: shared, Labels: domain.AppSharedNetworkLabels("shared-backend")},
	}, nil).Once()
	runtime.EXPECT().RemoveNetwork(mock.Anything, private).Return(nil).Once()

	warnings, err := reclaimService(t, state, runtime).reclaimPrivateNetworks(ctx, "blog", ownership)
	require.NoError(t, err)
	assert.Empty(t, warnings)
	runtime.AssertNotCalled(t, "RemoveNetwork", mock.Anything, shared)
}

// TestReclaimPrivateNetworks_RefusesForeignOwnership proves a network whose
// labels do not prove this exact incarnation is left in place and reported,
// never deleted.
func TestReclaimPrivateNetworks_RefusesForeignOwnership(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)

	private := domain.AppPrivateNetworkName("gordon", "app-1")
	ownership := domain.AppOwnership{
		App: "blog", ID: "app-1",
		Networks: []domain.AppOwnedNetwork{{Name: private, Role: domain.AppNetworkRolePrivate}},
	}
	// A different incarnation's labels (or a hand-made network) must never
	// be adopted, let alone deleted.
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{
		{Name: private, Labels: domain.AppPrivateNetworkLabels("blog", "app-99")},
	}, nil).Once()

	warnings, err := reclaimService(t, state, runtime).reclaimPrivateNetworks(ctx, "blog", ownership)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Equal(t, private, warnings[0].Leftover)
	assert.Contains(t, warnings[0].Detail, "labels")
	runtime.AssertNotCalled(t, "RemoveNetwork", mock.Anything, mock.Anything)
}

// TestReclaimPrivateNetworks_RefusesAttachedNetwork proves a network that
// still has attachments is left in place and reported.
func TestReclaimPrivateNetworks_RefusesAttachedNetwork(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)

	private := domain.AppPrivateNetworkName("gordon", "app-1")
	ownership := domain.AppOwnership{
		App: "blog", ID: "app-1",
		Networks: []domain.AppOwnedNetwork{{Name: private, Role: domain.AppNetworkRolePrivate}},
	}
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{
		{
			Name:       private,
			Labels:     domain.AppPrivateNetworkLabels("blog", "app-1"),
			Containers: []string{"c-still-there"},
		},
	}, nil).Once()

	warnings, err := reclaimService(t, state, runtime).reclaimPrivateNetworks(ctx, "blog", ownership)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0].Detail, "attached containers")
	runtime.AssertNotCalled(t, "RemoveNetwork", mock.Anything, mock.Anything)
}

// TestReclaimPrivateNetworks_NeverTouchesAnotherIncarnationName proves a
// stale ownership entry naming a network this incarnation does not own is
// ignored: a re-used name must never delete the previous incarnation's
// network.
func TestReclaimPrivateNetworks_NeverTouchesAnotherIncarnationName(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)

	other := domain.AppPrivateNetworkName("gordon", "app-old")
	ownership := domain.AppOwnership{
		App: "blog", ID: "app-1",
		Networks: []domain.AppOwnedNetwork{{Name: other, Role: domain.AppNetworkRolePrivate}},
	}

	warnings, err := reclaimService(t, state, runtime).reclaimPrivateNetworks(ctx, "blog", ownership)
	require.NoError(t, err)
	assert.Empty(t, warnings)
	runtime.AssertNotCalled(t, "ListNetworks", mock.Anything)
	runtime.AssertNotCalled(t, "RemoveNetwork", mock.Anything, mock.Anything)
}

// TestReclaimPrivateNetworks_RuntimeFailureIsRetryable proves a failed
// removal is reported so the caller keeps app state and the stopped intent
// for a retry.
func TestReclaimPrivateNetworks_RuntimeFailureIsRetryable(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)

	private := domain.AppPrivateNetworkName("gordon", "app-1")
	ownership := domain.AppOwnership{
		App: "blog", ID: "app-1",
		Networks: []domain.AppOwnedNetwork{{Name: private, Role: domain.AppNetworkRolePrivate}},
	}
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{
		{Name: private, Labels: domain.AppPrivateNetworkLabels("blog", "app-1")},
	}, nil).Once()
	runtime.EXPECT().RemoveNetwork(mock.Anything, private).Return(assert.AnError).Once()

	_, err := reclaimService(t, state, runtime).reclaimPrivateNetworks(ctx, "blog", ownership)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "remove private network")
}
