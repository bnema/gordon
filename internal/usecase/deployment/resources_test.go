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

func TestResolveServiceResources(t *testing.T) {
	limits := ResourceLimits{MemoryBytes: 8 << 30, NanoCPUs: 4e9}
	for _, tc := range []struct {
		name            string
		requested, want domain.AppResources
		key             string
	}{
		{name: "inherit", want: domain.AppResources{CPUs: 4, Memory: 8 << 30}},
		{name: "override", requested: domain.AppResources{CPUs: .5, Memory: 512 << 20, MemorySwap: 512 << 20}, want: domain.AppResources{CPUs: .5, Memory: 512 << 20, MemorySwap: 512 << 20}},
		{name: "cpu ceiling", requested: domain.AppResources{CPUs: 5}, key: "cpus"},
		{name: "memory ceiling", requested: domain.AppResources{Memory: 9 << 30}, key: "memory"},
		{name: "swap ceiling", requested: domain.AppResources{Memory: 1 << 30, MemorySwap: 9 << 30}, key: "memory_swap"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := limits.ResolveServiceResources(domain.AppService{Name: "web", Resources: tc.requested})
			if tc.key != "" {
				require.ErrorIs(t, err, domain.ErrInvalidAppSpec)
				for _, text := range []string{"web", tc.key, "requested", "installation limit"} {
					assert.Contains(t, err.Error(), text)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	want := domain.AppResources{CPUs: 8, Memory: 16 << 30, MemorySwap: 32 << 30}
	got, err := (ResourceLimits{}).ResolveServiceResources(domain.AppService{Name: "web", Resources: want})
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestCreateAndStartResources(t *testing.T) {
	for _, requested := range []domain.AppResources{{}, {CPUs: .5, Memory: 512 << 20, MemorySwap: 512 << 20}} {
		t.Run("resources", func(t *testing.T) {
			state := outmocks.NewMockAppState(t)
			runtime := outmocks.NewMockContainerRuntime(t)
			limits := ResourceLimits{MemoryBytes: 8 << 30, NanoCPUs: 4e9, PidsLimit: 100}
			spec := domain.AppService{Name: "web", Resources: requested}
			want, err := limits.ResolveServiceResources(spec)
			require.NoError(t, err)
			state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-1"}, nil).Once()
			runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{}, nil).Once()
			runtime.EXPECT().CreateNetwork(mock.Anything, domain.AppPrivateNetworkName("gordon", "app-1"), mock.Anything).Return(nil).Once()
			runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(cfg *domain.ContainerConfig) bool {
				return assert.Equal(t, want.Memory, cfg.MemoryLimit) && assert.Equal(t, want.MemorySwap, cfg.MemorySwap) && assert.Equal(t, int64(want.CPUs*1e9), cfg.NanoCPUs) && assert.Equal(t, int64(100), cfg.PidsLimit)
			})).Return(&domain.Container{ID: "c-1", Name: "web"}, nil).Once()
			runtime.EXPECT().UpdateRestartPolicy(mock.Anything, "c-1", domain.RestartPolicyAlways).Return(nil).Once()
			runtime.EXPECT().StartContainer(mock.Anything, "c-1").Return(nil).Once()
			runtime.EXPECT().InspectContainer(mock.Anything, "c-1").Return(&domain.Container{ID: "c-1", Status: "running", StartedAt: executionStartedAt}, nil).Maybe()
			svc := NewService(Deps{State: state, Runtime: runtime, Networks: NetworkConfig{Prefix: "gordon"}, Limits: limits}, zerowrap.Default())
			_, err = svc.createAndStart(context.Background(), "blog", "rev-1", pinnedService{name: "web", spec: spec}, "op-1", nil)
			require.NoError(t, err)
		})
	}
}

func TestPrepareCandidateRejectsResourcesBeforeMutation(t *testing.T) {
	svc := NewService(Deps{Limits: ResourceLimits{NanoCPUs: 1e9}}, zerowrap.Default())
	_, err := svc.prepareCandidate(context.Background(), "blog", "rev-1", pinnedService{spec: domain.AppService{Name: "web", Resources: domain.AppResources{CPUs: 2}}}, "op-1", nil)
	require.ErrorContains(t, err, "resources.cpus requested 2")
}

func TestResourceChangeForcesReplacement(t *testing.T) {
	svc := NewService(Deps{}, zerowrap.Default())
	p := pinnedService{name: "web", spec: domain.AppService{Name: "web", Resources: domain.AppResources{CPUs: .5}}}
	assert.True(t, svc.effectiveResourcesChanged(p.spec, nil))
	assert.False(t, svc.effectiveResourcesChanged(p.spec, p.spec.Resources.ResourceView()))
}
