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

func networkTestService(
	t *testing.T,
	state *outmocks.MockAppState,
	runtime *outmocks.MockContainerRuntime,
	networks NetworkConfig,
	limits ResourceLimits,
) *Service {
	t.Helper()
	return NewService(Deps{State: state, Runtime: runtime, Networks: networks, Limits: limits}, zerowrap.Default())
}

// TestCreateAndStart_UsesIncarnationPrivateNetworkAndLimits proves the
// created container joins the app's incarnation-owned network (never the
// runtime default) and carries the configured resource limits.
func TestCreateAndStart_UsesIncarnationPrivateNetworkAndLimits(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)

	private := domain.AppPrivateNetworkName("gordon", "app-1")
	state.EXPECT().LoadOwnership(mock.Anything, "blog").
		Return(domain.AppOwnership{App: "blog", ID: "app-1"}, nil).Once()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{}, nil).Once()
	runtime.EXPECT().CreateNetwork(mock.Anything, private, mock.MatchedBy(func(cfg domain.NetworkConfig) bool {
		return cfg.Internal && domain.NetworkOwnedBy(cfg.Labels, domain.AppPrivateNetworkLabels("blog", "app-1"))
	})).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(cfg *domain.ContainerConfig) bool {
		return cfg.NetworkMode == private &&
			cfg.MemoryLimit == int64(1)<<30 &&
			cfg.NanoCPUs == 2_000_000_000 &&
			cfg.PidsLimit == 256 &&
			assert.Equal(t, []string{"web"}, cfg.Aliases) &&
			assert.Equal(t, "web", cfg.Hostname)
	})).Return(&domain.Container{ID: "c-1", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-1").Return(nil).Once()

	svc := networkTestService(t, state, runtime,
		NetworkConfig{Prefix: "gordon", Internal: true},
		ResourceLimits{MemoryBytes: 1 << 30, NanoCPUs: 2_000_000_000, PidsLimit: 256},
	)
	created, binds, udp, err := svc.createAndStart(ctx, "blog", "rev-1",
		pinnedService{name: "web", spec: domain.AppService{Name: "web"}}, "op-1")
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Empty(t, binds)
	assert.Empty(t, udp)
}

// TestCreateAndStart_ReusesOwnedNetwork proves recovery reattaches the
// already-owned incarnation network instead of creating or replacing it.
func TestCreateAndStart_ReusesOwnedNetwork(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)

	private := domain.AppPrivateNetworkName("gordon", "app-1")
	state.EXPECT().LoadOwnership(mock.Anything, "blog").
		Return(domain.AppOwnership{App: "blog", ID: "app-1"}, nil).Once()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{
		{Name: private, Labels: domain.AppPrivateNetworkLabels("blog", "app-1")},
	}, nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).
		Return(&domain.Container{ID: "c-1", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-1").Return(nil).Once()

	svc := networkTestService(t, state, runtime, NetworkConfig{Prefix: "gordon"}, ResourceLimits{})
	_, _, _, err := svc.createAndStart(ctx, "blog", "rev-1",
		pinnedService{name: "web", spec: domain.AppService{Name: "web"}}, "op-1")
	require.NoError(t, err)
}

// TestCreateAndStart_RefusesForeignNetwork proves an unowned network with
// the derived name fails closed before any container is created.
func TestCreateAndStart_RefusesForeignNetwork(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)

	private := domain.AppPrivateNetworkName("gordon", "app-1")
	state.EXPECT().LoadOwnership(mock.Anything, "blog").
		Return(domain.AppOwnership{App: "blog", ID: "app-1"}, nil).Once()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{
		{Name: private, Labels: map[string]string{domain.LabelManaged: "true"}},
	}, nil).Once()

	svc := networkTestService(t, state, runtime, NetworkConfig{Prefix: "gordon"}, ResourceLimits{})
	_, _, _, err := svc.createAndStart(ctx, "blog", "rev-1",
		pinnedService{name: "web", spec: domain.AppService{Name: "web"}}, "op-1")
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
}

// TestCreateAndStart_JoinsDeclaredSharedNetworks proves a service is
// connected only to the shared networks its manifest declares.
func TestCreateAndStart_JoinsDeclaredSharedNetworks(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)

	private := domain.AppPrivateNetworkName("gordon", "app-1")
	shared := domain.AppSharedNetworkName("gordon", "database")
	state.EXPECT().LoadOwnership(mock.Anything, "blog").
		Return(domain.AppOwnership{App: "blog", ID: "app-1"}, nil).Once()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{}, nil).Once()
	runtime.EXPECT().CreateNetwork(mock.Anything, private, mock.Anything).Return(nil).Once()
	runtime.EXPECT().CreateNetwork(mock.Anything, shared, mock.MatchedBy(func(cfg domain.NetworkConfig) bool {
		return domain.NetworkOwnedBy(cfg.Labels, domain.AppSharedNetworkLabels("database"))
	})).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).
		Return(&domain.Container{ID: "c-1", Name: "web"}, nil).Once()
	runtime.EXPECT().ConnectContainerToNetwork(mock.Anything, "c-1", shared).Return(nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-1").Return(nil).Once()

	svc := networkTestService(t, state, runtime, NetworkConfig{Prefix: "gordon"}, ResourceLimits{})
	_, _, _, err := svc.createAndStart(ctx, "blog", "rev-1", pinnedService{
		name:           "web",
		spec:           domain.AppService{Name: "web"},
		sharedNetworks: []domain.AppSharedNetwork{{Network: "database", Services: []string{"web"}}},
	}, "op-1")
	require.NoError(t, err)
}

// TestCreateAndStart_RoutesDeclaredReadOnlyVolumeToReadOnlyMount proves a
// manifest volume declared read-only reaches the runtime config as a
// read-only mount instead of a writable one.
func TestCreateAndStart_RoutesDeclaredReadOnlyVolumeToReadOnlyMount(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)

	private := domain.AppPrivateNetworkName("gordon", "app-1")
	runtimeName := domain.RuntimeVolumeName("blog", "web", "config")
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-1"}, nil)
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil)
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{}, nil).Once()
	runtime.EXPECT().CreateNetwork(mock.Anything, private, mock.Anything).Return(nil).Once()
	runtime.EXPECT().CreateVolume(mock.Anything, runtimeName, mock.Anything).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(cfg *domain.ContainerConfig) bool {
		_, readOnly := cfg.ReadOnlyVolumes["/config"]
		_, writable := cfg.Volumes["/config"]
		return readOnly && !writable && cfg.ReadOnlyVolumes["/config"] == runtimeName
	})).Return(&domain.Container{ID: "c-1", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-1").Return(nil).Once()

	svc := networkTestService(t, state, runtime, NetworkConfig{Prefix: "gordon"}, ResourceLimits{})
	_, _, _, err := svc.createAndStart(ctx, "blog", "rev-1", pinnedService{
		name: "web",
		spec: domain.AppService{
			Name:    "web",
			Volumes: []domain.AppVolume{{Name: "config", Path: "/config", ReadOnly: true}},
		},
	}, "op-1")
	require.NoError(t, err)
}

// TestCreateAndStart_DistinctAppsNeverShareNetwork proves two apps receive
// different incarnation networks, neither of which is a runtime default.
func TestCreateAndStart_DistinctAppsNeverShareNetwork(t *testing.T) {
	networks := map[string]string{}
	for _, tc := range []struct{ app, id string }{{"blog", "app-1"}, {"shop", "app-2"}} {
		private := domain.AppPrivateNetworkName("gordon", tc.id)
		require.NotEqual(t, "default", private)
		require.NotEqual(t, "bridge", private)
		networks[tc.app] = private
	}
	assert.NotEqual(t, networks["blog"], networks["shop"])
}
