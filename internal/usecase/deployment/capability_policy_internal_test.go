package deployment

import (
	"context"
	"fmt"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func capabilityPolicyFor(apps, services []string) domain.AppCapabilityPolicy {
	return domain.AppCapabilityPolicy{
		Name:            "net-admin",
		Add:             []string{"NET_ADMIN"},
		AllowedApps:     apps,
		AllowedServices: services,
	}
}

func TestResolveServiceCapabilities(t *testing.T) {
	spec := domain.AppService{Name: "web", Capabilities: []string{"net-admin"}}

	t.Run("exact resolution", func(t *testing.T) {
		svc := NewService(Deps{}, zerowrap.Default()).
			WithCapabilityPolicies(map[string]domain.AppCapabilityPolicy{
				"net-admin": capabilityPolicyFor([]string{"blog"}, []string{"web"}),
			})
		got, err := svc.resolveServiceCapabilities("blog", spec)
		require.NoError(t, err)
		assert.Equal(t, []string{"NET_ADMIN"}, got)
	})

	t.Run("empty is nil", func(t *testing.T) {
		svc := NewService(Deps{}, zerowrap.Default())
		got, err := svc.resolveServiceCapabilities("blog", domain.AppService{Name: "web"})
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("unknown policy", func(t *testing.T) {
		svc := NewService(Deps{}, zerowrap.Default())
		_, err := svc.resolveServiceCapabilities("blog", spec)
		require.ErrorIs(t, err, domain.ErrCapabilityPolicy)
		assert.Contains(t, err.Error(), "net-admin")
	})

	t.Run("revoked policy", func(t *testing.T) {
		svc := NewService(Deps{}, zerowrap.Default()).
			WithCapabilityPolicies(map[string]domain.AppCapabilityPolicy{
				"net-admin": capabilityPolicyFor([]string{"blog"}, []string{"web"}),
			})
		_, err := svc.resolveServiceCapabilities("blog", spec)
		require.NoError(t, err)
		svc.SetCapabilityPolicies(nil)
		_, err = svc.resolveServiceCapabilities("blog", spec)
		require.ErrorIs(t, err, domain.ErrCapabilityPolicy)
	})

	t.Run("refused omits capability names", func(t *testing.T) {
		svc := NewService(Deps{}, zerowrap.Default()).
			WithCapabilityPolicies(map[string]domain.AppCapabilityPolicy{
				"net-admin": capabilityPolicyFor([]string{"other"}, []string{"web"}),
			})
		_, err := svc.resolveServiceCapabilities("blog", spec)
		require.ErrorIs(t, err, domain.ErrCapabilityPolicy)
		assert.NotContains(t, err.Error(), "NET_ADMIN")
	})
}

// TestCreateAndStart_RefusesRevokedCapabilityBeforeRuntimeMutation proves
// the pre-create re-resolution fails closed before any runtime call.
func TestCreateAndStart_RefusesRevokedCapabilityBeforeRuntimeMutation(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{Runtime: runtime}, zerowrap.Default())
	p := pinnedService{
		name: "web",
		spec: domain.AppService{Name: "web", Capabilities: []string{"net-admin"}},
	}

	_, err := svc.createAndStart(context.Background(), "blog", "rev-1", p, "op-1", nil)

	require.ErrorIs(t, err, domain.ErrCapabilityPolicy)
	runtime.AssertNotCalled(t, "CreateContainer")
	runtime.AssertNotCalled(t, "CreateVolume")
	runtime.AssertNotCalled(t, "StartContainer")
	runtime.AssertExpectations(t)
}

// TestCreateAndStart_PassesResolvedCapabilitiesToRuntime proves resolved
// capabilities reach ContainerConfig.AddedCapabilities and never CapAdd /
// CapDrop, which stay with the runtime defaults.
func TestCreateAndStart_PassesResolvedCapabilitiesToRuntime(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)

	private := domain.AppPrivateNetworkName("gordon", "app-1")
	state.EXPECT().LoadOwnership(mock.Anything, "blog").
		Return(domain.AppOwnership{App: "blog", ID: "app-1"}, nil).Once()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{}, nil).Once()
	runtime.EXPECT().CreateNetwork(mock.Anything, private, mock.Anything).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(cfg *domain.ContainerConfig) bool {
		return assert.Equal(t, []string{"NET_ADMIN"}, cfg.AddedCapabilities) &&
			assert.Nil(t, cfg.CapAdd, "base capability set stays with the runtime defaults") &&
			assert.Nil(t, cfg.CapDrop, "the default drop is never overridden")
	})).Return(&domain.Container{ID: "c-1", Name: "web"}, nil).Once()
	runtime.EXPECT().UpdateRestartPolicy(mock.Anything, mock.Anything, domain.RestartPolicyAlways).Return(nil).Maybe()
	runtime.EXPECT().StartContainer(mock.Anything, "c-1").Return(nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").Return(&domain.Container{ID: "c-1", Status: "running", StartedAt: executionStartedAt}, nil).Maybe()

	svc := NewService(Deps{State: state, Runtime: runtime, Networks: NetworkConfig{Prefix: "gordon"}}, zerowrap.Default()).
		WithCapabilityPolicies(map[string]domain.AppCapabilityPolicy{
			"net-admin": capabilityPolicyFor([]string{"blog"}, []string{"web"}),
		})
	candidate, err := svc.createAndStart(ctx, "blog", "rev-1", pinnedService{
		name: "web",
		spec: domain.AppService{Name: "web", Capabilities: []string{"net-admin"}},
	}, "op-1", nil)
	require.NoError(t, err)
	require.NotNil(t, candidate.Container)
	runtime.AssertExpectations(t)
}

func TestRestartOneService_RevokedCapabilityFailsWithoutRuntimeMutation(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{Runtime: runtime}, zerowrap.Default())
	eff := domain.AppEffectiveService{
		Container: "c1",
		Spec:      domain.AppService{Name: "web", Capabilities: []string{"net-admin"}},
	}
	op := &domain.AppOperation{Op: "op-1", Steps: []domain.AppOperationStep{{
		ID: "service.web.restart", State: domain.AppStepPending, Before: eff.Container,
	}}}
	result := svc.restartOneService(context.Background(), "blog", "op-1", "web", eff, op, 0)

	assert.Equal(t, domain.AppStepFailed, op.Steps[0].State)
	assert.Contains(t, op.Steps[0].Error, "net-admin")
	assert.Equal(t, "failed", result.Result)
	runtime.AssertNotCalled(t, "RestartContainer")
	runtime.AssertExpectations(t)
}

func TestEnsureServiceRunning_RevokedCapabilityFailsWithoutRuntimeMutation(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{Runtime: runtime}, zerowrap.Default())
	eff := domain.AppEffectiveService{
		Container: "c1",
		Spec:      domain.AppService{Name: "web", Capabilities: []string{"net-admin"}},
	}
	op := &domain.AppOperation{Op: "op-1", Steps: []domain.AppOperationStep{{
		ID: "service.web.start", State: domain.AppStepPending, Before: eff.Container,
	}}}
	result := &LifecycleResult{Services: map[string]ServiceResult{}}

	svc.ensureServiceRunning(context.Background(), "blog", "op-1", "web", eff, op, 0, result)

	assert.Equal(t, domain.AppStepFailed, op.Steps[0].State)
	assert.Contains(t, op.Steps[0].Error, "net-admin")
	runtime.AssertNotCalled(t, "StartContainer")
	runtime.AssertExpectations(t)
}

// TestUnchangedRefusal_CapabilitiesNeverKeepRunningContainer proves a
// service with capability grants is always replaced on deploy: the resolved
// capabilities depend on host policy that ACTIVE does not record, so a
// changed policy must reach the container on the next deploy.
func TestUnchangedRefusal_CapabilitiesNeverKeepRunningContainer(t *testing.T) {
	svc := NewService(Deps{}, zerowrap.Default())
	p := pinnedService{name: "web", spec: domain.AppService{Name: "web", Capabilities: []string{"net-admin"}}}
	reason := svc.unchangedRefusal(context.Background(), "blog", p, domain.AppEffectiveService{Spec: p.spec})
	assert.Equal(t, "administrative capabilities", reason)
}

// narrowedFixture models a policy narrowed from {NET_ADMIN, IPC_LOCK} to
// {IPC_LOCK} after a container was created with both: policy resolution
// still succeeds, but the existing container holds the revoked NET_ADMIN.
func narrowedFixture(t *testing.T, containerCaps []string) (*Service, *outmocks.MockContainerRuntime, domain.AppEffectiveService) {
	t.Helper()
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{Runtime: runtime}, zerowrap.Default()).
		WithCapabilityPolicies(map[string]domain.AppCapabilityPolicy{
			"net-admin": {Name: "net-admin", Add: []string{"IPC_LOCK"}, AllowedApps: []string{"blog"}, AllowedServices: []string{"web"}},
		})
	eff := domain.AppEffectiveService{
		Container: "c1",
		Spec:      domain.AppService{Name: "web", Capabilities: []string{"net-admin"}},
	}
	runtime.EXPECT().InspectContainer(mock.Anything, "c1").
		Return(&domain.Container{ID: "c1", CapAdd: containerCaps}, nil).Maybe()
	return svc, runtime, eff
}

var staleCaps = []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETUID", "SETGID", "NET_BIND_SERVICE", "NET_ADMIN", "IPC_LOCK"}

func TestVerifyReusableCapabilities(t *testing.T) {
	t.Run("narrowed policy refuses a container holding a revoked capability", func(t *testing.T) {
		svc, _, eff := narrowedFixture(t, staleCaps)
		err := svc.verifyReusableCapabilities(context.Background(), "blog", eff)
		require.ErrorIs(t, err, domain.ErrCapabilityPolicy)
		assert.Contains(t, err.Error(), "NET_ADMIN")
		assert.NotContains(t, err.Error(), "IPC_LOCK")
	})
	t.Run("CAP_ prefixed engine names compare equal", func(t *testing.T) {
		svc, _, eff := narrowedFixture(t, []string{"CAP_CHOWN", "CAP_NET_BIND_SERVICE", "CAP_IPC_LOCK"})
		require.NoError(t, svc.verifyReusableCapabilities(context.Background(), "blog", eff))
	})
	t.Run("fewer capabilities than authorized is allowed", func(t *testing.T) {
		svc, _, eff := narrowedFixture(t, []string{"CHOWN"})
		require.NoError(t, svc.verifyReusableCapabilities(context.Background(), "blog", eff))
	})
	t.Run("prefixed revoked capability is still caught", func(t *testing.T) {
		svc, _, eff := narrowedFixture(t, []string{"CAP_NET_ADMIN"})
		require.ErrorIs(t, svc.verifyReusableCapabilities(context.Background(), "blog", eff), domain.ErrCapabilityPolicy)
	})
	t.Run("uninspectable container fails closed", func(t *testing.T) {
		runtime := outmocks.NewMockContainerRuntime(t)
		runtime.EXPECT().InspectContainer(mock.Anything, "c1").Return(nil, fmt.Errorf("daemon down at /run/podman.sock")).Once()
		svc := NewService(Deps{Runtime: runtime}, zerowrap.Default()).
			WithCapabilityPolicies(map[string]domain.AppCapabilityPolicy{
				"net-admin": capabilityPolicyFor([]string{"blog"}, []string{"web"}),
			})
		eff := domain.AppEffectiveService{Container: "c1", Spec: domain.AppService{Name: "web", Capabilities: []string{"net-admin"}}}
		err := svc.verifyReusableCapabilities(context.Background(), "blog", eff)
		require.ErrorIs(t, err, domain.ErrCapabilityPolicy)
		assert.NotContains(t, err.Error(), "podman.sock")
	})
	t.Run("missing container is left to the rebuild path", func(t *testing.T) {
		runtime := outmocks.NewMockContainerRuntime(t)
		runtime.EXPECT().InspectContainer(mock.Anything, "c1").Return(nil, domain.ErrContainerNotFound).Once()
		svc := NewService(Deps{Runtime: runtime}, zerowrap.Default()).
			WithCapabilityPolicies(map[string]domain.AppCapabilityPolicy{
				"net-admin": capabilityPolicyFor([]string{"blog"}, []string{"web"}),
			})
		eff := domain.AppEffectiveService{Container: "c1", Spec: domain.AppService{Name: "web", Capabilities: []string{"net-admin"}}}
		require.NoError(t, svc.verifyReusableCapabilities(context.Background(), "blog", eff))
	})
	t.Run("service without capabilities never inspects", func(t *testing.T) {
		runtime := outmocks.NewMockContainerRuntime(t)
		svc := NewService(Deps{Runtime: runtime}, zerowrap.Default())
		eff := domain.AppEffectiveService{Container: "c1", Spec: domain.AppService{Name: "web"}}
		require.NoError(t, svc.verifyReusableCapabilities(context.Background(), "blog", eff))
	})
}

func TestRestartOneService_NarrowedCapabilityPolicyRefusesInPlaceRestart(t *testing.T) {
	svc, runtime, eff := narrowedFixture(t, staleCaps)
	op := &domain.AppOperation{Op: "op-1", Steps: []domain.AppOperationStep{{
		ID: "service.web.restart", State: domain.AppStepPending, Before: eff.Container,
	}}}
	result := svc.restartOneService(context.Background(), "blog", "op-1", "web", eff, op, 0)

	assert.Equal(t, "failed", result.Result)
	assert.Equal(t, domain.AppStepFailed, op.Steps[0].State)
	assert.Contains(t, op.Steps[0].Error, "NET_ADMIN")
	runtime.AssertNotCalled(t, "RestartContainer")
	runtime.AssertNotCalled(t, "StartContainer")
}

func TestEnsureServiceRunning_NarrowedCapabilityPolicyRefusesStart(t *testing.T) {
	svc, runtime, eff := narrowedFixture(t, staleCaps)
	op := &domain.AppOperation{Op: "op-1", Steps: []domain.AppOperationStep{{
		ID: "service.web.start", State: domain.AppStepPending, Before: eff.Container,
	}}}
	result := &LifecycleResult{Services: map[string]ServiceResult{}}

	svc.ensureServiceRunning(context.Background(), "blog", "op-1", "web", eff, op, 0, result)

	assert.Equal(t, domain.AppStepFailed, op.Steps[0].State)
	assert.Contains(t, op.Steps[0].Error, "NET_ADMIN")
	runtime.AssertNotCalled(t, "StartContainer")
	runtime.AssertNotCalled(t, "IsContainerRunning")
}

func TestStartRecovered_NarrowedCapabilityPolicyRefusesStart(t *testing.T) {
	svc, runtime, eff := narrowedFixture(t, staleCaps)
	started, err := svc.startRecovered(context.Background(), "blog", "web", eff, recoveryKey{app: "blog", service: "web", container: "c1"})

	require.ErrorIs(t, err, domain.ErrCapabilityPolicy)
	assert.False(t, started)
	runtime.AssertNotCalled(t, "StartContainer")
}

func TestRecoverHealth_NarrowedCapabilityPolicyRefusesRestart(t *testing.T) {
	svc, runtime, eff := narrowedFixture(t, staleCaps)
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c1").Return("unhealthy", true, nil).Once()
	key := recoveryKey{app: "blog", service: "web", container: "c1"}
	for i := 0; i < unhealthyThreshold; i++ {
		_, _, err := svc.recoverHealth(context.Background(), "blog", "web", eff, key)
		if err != nil {
			require.ErrorIs(t, err, domain.ErrCapabilityPolicy)
			runtime.AssertNotCalled(t, "RestartContainer")
			return
		}
		if i < unhealthyThreshold-1 {
			runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c1").Return("unhealthy", true, nil).Once()
		}
	}
	t.Fatal("expected recoverHealth to refuse the restart")
}
