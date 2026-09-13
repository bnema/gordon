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

func devicePolicyFor(apps, services []string) domain.AppDevicePolicy {
	return domain.AppDevicePolicy{
		Name:            "test_gpu",
		CDI:             []string{"example.com/gpu=GPU-test-uuid"},
		AllowedApps:     apps,
		AllowedServices: services,
	}
}

func TestResolveServiceDevices_ExactResolution(t *testing.T) {
	svc := NewService(Deps{}, zerowrap.Default()).
		WithDevicePolicies(map[string]domain.AppDevicePolicy{
			"test_gpu": devicePolicyFor([]string{"blog"}, []string{"web"}),
		})

	got, err := svc.resolveServiceDevices("blog", domain.AppService{
		Name:    "web",
		Devices: []string{"test_gpu"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"example.com/gpu=GPU-test-uuid"}, got)
}

func TestResolveServiceDevices_EmptyIsNil(t *testing.T) {
	svc := NewService(Deps{}, zerowrap.Default())
	got, err := svc.resolveServiceDevices("blog", domain.AppService{Name: "web"})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestResolveServiceDevices_UnknownAndRevokedPolicy(t *testing.T) {
	spec := domain.AppService{
		Name:    "web",
		Devices: []string{"test_gpu"},
	}

	t.Run("unknown policy", func(t *testing.T) {
		svc := NewService(Deps{}, zerowrap.Default())
		_, err := svc.resolveServiceDevices("blog", spec)
		require.ErrorIs(t, err, domain.ErrDevicePolicy)
		assert.Contains(t, err.Error(), "blog")
		assert.Contains(t, err.Error(), "web")
		assert.Contains(t, err.Error(), "test_gpu")
	})

	t.Run("revoked policy", func(t *testing.T) {
		svc := NewService(Deps{}, zerowrap.Default()).
			WithDevicePolicies(map[string]domain.AppDevicePolicy{
				"test_gpu": devicePolicyFor([]string{"blog"}, []string{"web"}),
			})
		_, err := svc.resolveServiceDevices("blog", spec)
		require.NoError(t, err)

		svc.SetDevicePolicies(nil)
		_, err = svc.resolveServiceDevices("blog", spec)
		require.ErrorIs(t, err, domain.ErrDevicePolicy)
	})

	t.Run("refused policy omits device IDs", func(t *testing.T) {
		svc := NewService(Deps{}, zerowrap.Default()).
			WithDevicePolicies(map[string]domain.AppDevicePolicy{
				"test_gpu": devicePolicyFor([]string{"other"}, []string{"web"}),
			})
		_, err := svc.resolveServiceDevices("blog", spec)
		require.ErrorIs(t, err, domain.ErrDevicePolicy)
		assert.NotContains(t, err.Error(), "GPU-test-uuid", "errors must never leak CDI IDs")
	})
}

// TestCreateAndStart_RefusesRevokedDeviceBeforeRuntimeMutation proves the
// immediate-pre-create re-resolution fails before any runtime call when the
// device policy is missing or revoked.
func TestCreateAndStart_RefusesRevokedDeviceBeforeRuntimeMutation(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{Runtime: runtime}, zerowrap.Default())
	p := pinnedService{
		name: "web",
		spec: domain.AppService{
			Name:    "web",
			Devices: []string{"test_gpu"},
		},
	}

	_, _, _, err := svc.createAndStart(context.Background(), "blog", "rev-1", p, "op-1", nil)

	require.ErrorIs(t, err, domain.ErrDevicePolicy)
	assert.Contains(t, err.Error(), "test_gpu")
	runtime.AssertNotCalled(t, "CreateContainer")
	runtime.AssertNotCalled(t, "CreateVolume")
	runtime.AssertNotCalled(t, "StartContainer")
	runtime.AssertNotCalled(t, "ConnectContainerToNetwork")
	runtime.AssertExpectations(t)
}

// TestCreateAndStart_PassesResolvedCDIIDsToRuntime proves the deploy path
// carries authorized policy resolution into ContainerConfig.CDIDevices:
// only the granted service receives IDs, helpers and ordinary services
// receive none.
func TestCreateAndStart_PassesResolvedCDIIDsToRuntime(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)

	private := domain.AppPrivateNetworkName("gordon", "app-1")
	state.EXPECT().LoadOwnership(mock.Anything, "blog").
		Return(domain.AppOwnership{App: "blog", ID: "app-1"}, nil).Once()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{}, nil).Once()
	runtime.EXPECT().CreateNetwork(mock.Anything, private, mock.Anything).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(cfg *domain.ContainerConfig) bool {
		return assert.Equal(t, []string{"example.com/gpu=GPU-test-uuid"}, cfg.CDIDevices)
	})).Return(&domain.Container{ID: "c-1", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-1").Return(nil).Once()

	svc := NewService(Deps{State: state, Runtime: runtime, Networks: NetworkConfig{Prefix: "gordon"}}, zerowrap.Default()).
		WithDevicePolicies(map[string]domain.AppDevicePolicy{
			"test_gpu": devicePolicyFor([]string{"blog"}, []string{"web"}),
		})
	created, _, _, err := svc.createAndStart(ctx, "blog", "rev-1", pinnedService{
		name: "web",
		spec: domain.AppService{Name: "web", Devices: []string{"test_gpu"}},
	}, "op-1", nil)
	require.NoError(t, err)
	require.NotNil(t, created)
	runtime.AssertExpectations(t)
}

// TestCreateContainer_DeviceBearingRuntimeErrorIsRedactedNotDevicePolicy
// proves a CreateContainer failure on a device-bearing service is reported
// as a generic redacted runtime error, never as a device policy violation,
// and never leaks the resolved CDI IDs embedded in the runtime error.
func TestCreateContainer_DeviceBearingRuntimeErrorIsRedactedNotDevicePolicy(t *testing.T) {
	const deviceID = "example.com/gpu=GPU-test-uuid"
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).
		Return(nil, fmt.Errorf("device %s: permission denied", deviceID)).Once()
	svc := NewService(Deps{Runtime: runtime}, zerowrap.Default())

	_, err := svc.createContainer(context.Background(), "web", &domain.ContainerConfig{
		CDIDevices: []string{deviceID},
	})

	require.Error(t, err)
	require.NotErrorIs(t, err, domain.ErrDevicePolicy, "a runtime failure is not a policy refusal")
	assert.NotContains(t, err.Error(), deviceID, "errors must never leak resolved CDI IDs")
	assert.NotContains(t, err.Error(), "permission denied", "no runtime text may reach the caller")
	runtime.AssertExpectations(t)
}

// TestRestartOneService_RevokedDeviceFailsWithoutRuntimeMutation proves an
// in-place restart fails closed when the device policy is missing/revoked,
// before touching the runtime.
func TestRestartOneService_RevokedDeviceFailsWithoutRuntimeMutation(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{Runtime: runtime}, zerowrap.Default())
	eff := domain.AppEffectiveService{
		Container: "c1",
		Spec: domain.AppService{
			Name:    "web",
			Devices: []string{"test_gpu"},
		},
	}

	op := &domain.AppOperation{Op: "op-1", Steps: []domain.AppOperationStep{{
		ID: "service.web.restart", State: domain.AppStepPending, Before: eff.Container,
	}}}
	result := svc.restartOneService(context.Background(), "blog", "op-1", "web", eff, op, 0)

	assert.Equal(t, domain.AppStepFailed, op.Steps[0].State)
	assert.Contains(t, op.Steps[0].Error, "test_gpu")
	assert.Equal(t, "failed", result.Result)
	runtime.AssertNotCalled(t, "RestartContainer")
	runtime.AssertExpectations(t)
}

// TestEnsureServiceRunning_RevokedDeviceFailsWithoutRuntimeMutation proves
// the boot/start recovery path fails closed before starting a container
// when the device policy is missing/revoked.
func TestEnsureServiceRunning_RevokedDeviceFailsWithoutRuntimeMutation(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{Runtime: runtime}, zerowrap.Default())
	eff := domain.AppEffectiveService{
		Container: "c1",
		Spec: domain.AppService{
			Name:    "web",
			Devices: []string{"test_gpu"},
		},
	}
	op := &domain.AppOperation{Op: "op-1", Steps: []domain.AppOperationStep{{
		ID: "service.web.start", State: domain.AppStepPending, Before: eff.Container,
	}}}
	result := &LifecycleResult{Services: map[string]ServiceResult{}}

	svc.ensureServiceRunning(context.Background(), "blog", "op-1", "web", eff, op, 0, result)

	assert.Equal(t, domain.AppStepFailed, op.Steps[0].State)
	assert.Contains(t, op.Steps[0].Error, "test_gpu")
	runtime.AssertNotCalled(t, "StartContainer")
	runtime.AssertExpectations(t)
}
