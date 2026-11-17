package deployment

import (
	"context"
	"net/netip"
	"testing"

	"bnema.dev/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestCreateAndStart_MapsServiceDNSToRuntime(t *testing.T) {
	cases := map[string]struct {
		spec        domain.AppService
		wantDNS     []netip.Addr
		wantOptions []string
	}{
		"custom dns": {
			spec:        domain.AppService{Name: "web", DNS: []string{"9.9.9.9", "2620:fe::fe"}, DNSOptions: []string{"ndots:1"}},
			wantDNS:     []netip.Addr{netip.MustParseAddr("9.9.9.9"), netip.MustParseAddr("2620:fe::fe")},
			wantOptions: []string{"ndots:1"},
		},
		"runtime default": {spec: domain.AppService{Name: "web"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			state := outmocks.NewMockAppState(t)
			runtime := outmocks.NewMockContainerRuntime(t)

			private := domain.AppPrivateNetworkName("gordon", "app-1")
			state.EXPECT().LoadOwnership(mock.Anything, "blog").
				Return(domain.AppOwnership{App: "blog", ID: "app-1"}, nil).Once()
			runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{}, nil).Once()
			runtime.EXPECT().CreateNetwork(mock.Anything, private, mock.Anything).Return(nil).Once()
			runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(cfg *domain.ContainerConfig) bool {
				return assert.Equal(t, tc.wantDNS, cfg.DNS) && assert.Equal(t, tc.wantOptions, cfg.DNSOptions)
			})).Return(&domain.Container{ID: "c-1", Name: "web"}, nil).Once()
			runtime.EXPECT().UpdateRestartPolicy(mock.Anything, mock.Anything, domain.RestartPolicyAlways).Return(nil).Maybe()
			runtime.EXPECT().StartContainer(mock.Anything, "c-1").Return(nil).Once()
			runtime.EXPECT().InspectContainer(mock.Anything, "c-1").Return(&domain.Container{ID: "c-1", Status: "running", StartedAt: executionStartedAt}, nil).Maybe()

			svc := NewService(Deps{State: state, Runtime: runtime, Networks: NetworkConfig{Prefix: "gordon"}}, zerowrap.Default())
			candidate, err := svc.createAndStart(ctx, "blog", "rev-1", pinnedService{name: "web", spec: tc.spec}, "op-1", nil)
			require.NoError(t, err)
			require.NotNil(t, candidate.Container)
		})
	}
}
