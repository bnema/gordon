package container

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestRuntimePolicyReservesManagedControlSecretsVolume(t *testing.T) {
	identity := testRuntimeCommandIdentity("managed-secrets")
	identity.SourceComponentID = "gordon-control"
	const volume = "gordon-control-secrets-0123456789abcdef"
	policy := RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedControlSecretsVolume: volume}
	authorized := domain.ContainerConfig{
		Name:    "gordon-control-fixture-g1",
		Labels:  map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: string(domain.ComponentRoleControl), domain.LabelComponentOwner: "runtime"},
		Volumes: map[string]string{managedControlSecretsPath: volume},
	}
	require.NoError(t, policy.CheckContainerConfig(identity, "", authorized))

	for _, test := range []struct {
		name string
		cfg  domain.ContainerConfig
	}{
		{name: "workload exact pair", cfg: domain.ContainerConfig{Volumes: map[string]string{managedControlSecretsPath: volume}}},
		{name: "runtime role", cfg: domain.ContainerConfig{Name: "gordon-runtime-fixture-g1", Labels: map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: "runtime", domain.LabelComponentOwner: "runtime"}, Volumes: map[string]string{managedControlSecretsPath: volume}}},
		{name: "edge role", cfg: domain.ContainerConfig{Name: "gordon-edge-fixture-g1", Labels: map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: "edge", domain.LabelComponentOwner: "runtime"}, Volumes: map[string]string{managedControlSecretsPath: volume}}},
		{name: "registry role", cfg: domain.ContainerConfig{Name: "gordon-registry-fixture-g1", Labels: map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: "registry", domain.LabelComponentOwner: "runtime"}, Volumes: map[string]string{managedControlSecretsPath: volume}}},
		{name: "alternate source", cfg: domain.ContainerConfig{Name: authorized.Name, Labels: authorized.Labels, Volumes: map[string]string{managedControlSecretsPath: "other"}}},
		{name: "alternate destination", cfg: domain.ContainerConfig{Name: authorized.Name, Labels: authorized.Labels, Volumes: map[string]string{"/tmp/secrets": volume}}},
		{name: "exact pair plus alternate destination", cfg: domain.ContainerConfig{Name: authorized.Name, Labels: authorized.Labels, Volumes: map[string]string{managedControlSecretsPath: volume, "/tmp/secrets": volume}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.ErrorIs(t, policy.CheckContainerConfig(identity, "", test.cfg), ErrRuntimePolicyDenied)
		})
	}
}

func TestRuntimePolicyFailsClosedForControlWithoutValidManagedSecretsVolume(t *testing.T) {
	identity := testRuntimeCommandIdentity("managed-secrets-invalid")
	identity.SourceComponentID = "gordon-control"
	control := domain.ContainerConfig{
		Name:    "gordon-control-fixture-g1",
		Labels:  map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: string(domain.ComponentRoleControl), domain.LabelComponentOwner: "runtime"},
		Volumes: map[string]string{},
	}
	for _, volume := range []string{"", "gordon-control-fixture-g1", "gordon-control-secrets-ABCDEF0123456789", "gordon-control-secrets-0123"} {
		policy := RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedControlSecretsVolume: volume}
		require.ErrorIs(t, policy.CheckContainerConfig(identity, "", control), ErrRuntimePolicyDenied)
	}
}

func TestRuntimePolicyAllowsSocketOnlyForRuntimeComponent(t *testing.T) {
	identity := testRuntimeCommandIdentity("runtime-socket")
	config := domain.ContainerConfig{Name: "gordon-runtime-fixture-g1", Labels: map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: string(domain.ComponentRoleRuntime)}, ReadOnlyVolumes: map[string]string{"/run/gordon/runtime.sock": "/run/user/1000/podman/podman.sock"}}
	require.NoError(t, (RuntimePolicy{Mode: RuntimePolicyModeEnforce}).CheckContainerConfig(identity, "", config))
	config.Labels[domain.LabelComponentRole] = string(domain.ComponentRoleEdge)
	require.ErrorIs(t, (RuntimePolicy{Mode: RuntimePolicyModeEnforce}).CheckContainerConfig(identity, "", config), ErrRuntimePolicyDenied)
}

func TestRuntimePolicy(t *testing.T) {
	identity := testRuntimeCommandIdentity("cmd-policy")

	tests := []struct {
		name       string
		policy     RuntimePolicy
		check      func(RuntimePolicy) error
		wantReason string
	}{
		{
			name:   "digest required",
			policy: RuntimePolicy{Mode: RuntimePolicyModeEnforce, RequireImageDigest: true},
			check: func(p RuntimePolicy) error {
				return p.CheckDeployRoute(domain.DeployRouteCommand{RuntimeCommandIdentity: identity, Domain: "app.example.com", Image: "registry.example.com/app:latest"})
			},
			wantReason: RuntimePolicyReasonDigestRequired,
		},
		{
			name:   "image registry denied",
			policy: RuntimePolicy{Mode: RuntimePolicyModeEnforce, AllowedImageRegistries: []string{"registry.example.com"}},
			check: func(p RuntimePolicy) error {
				return p.CheckDeployRoute(domain.DeployRouteCommand{RuntimeCommandIdentity: identity, Domain: "app.example.com", Image: "evil.example.com/app@sha256:abcdef"})
			},
			wantReason: RuntimePolicyReasonImageRegistryDenied,
		},
		{
			name:   "privileged container denied",
			policy: RuntimePolicy{Mode: RuntimePolicyModeEnforce},
			check: func(p RuntimePolicy) error {
				return p.CheckContainerConfig(identity, "app.example.com", domain.ContainerConfig{Privileged: true})
			},
			wantReason: RuntimePolicyReasonPrivilegedDenied,
		},
		{
			name:   "unsafe host bind denied",
			policy: RuntimePolicy{Mode: RuntimePolicyModeEnforce},
			check: func(p RuntimePolicy) error {
				return p.CheckContainerConfig(identity, "app.example.com", domain.ContainerConfig{Volumes: map[string]string{"/data": "/etc"}})
			},
			wantReason: RuntimePolicyReasonUnsafeHostBindDenied,
		},
		{
			name:   "runtime socket mount denied",
			policy: RuntimePolicy{Mode: RuntimePolicyModeEnforce},
			check: func(p RuntimePolicy) error {
				return p.CheckContainerConfig(identity, "app.example.com", domain.ContainerConfig{Volumes: map[string]string{"/sock": "/run/user/1000/podman/podman.sock"}})
			},
			wantReason: RuntimePolicyReasonSocketMountDenied,
		},
		{
			name:   "unmanaged network denied",
			policy: RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"},
			check: func(p RuntimePolicy) error {
				return p.CheckContainerConfig(identity, "app.example.com", domain.ContainerConfig{NetworkMode: "bridge"})
			},
			wantReason: RuntimePolicyReasonUnmanagedNetworkDenied,
		},
		{
			name:   "capability add denied",
			policy: RuntimePolicy{Mode: RuntimePolicyModeEnforce},
			check: func(p RuntimePolicy) error {
				return p.CheckContainerConfig(identity, "app.example.com", domain.ContainerConfig{CapAdd: []string{"SYS_ADMIN"}})
			},
			wantReason: RuntimePolicyReasonCapabilityDenied,
		},
		{
			name:   "unmanaged mutation denied",
			policy: RuntimePolicy{Mode: RuntimePolicyModeEnforce},
			check: func(p RuntimePolicy) error {
				return p.CheckRestartRoute(domain.RestartRouteCommand{RuntimeCommandIdentity: identity, Domain: "not a domain"})
			},
			wantReason: RuntimePolicyReasonUnmanagedMutation,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.check(tt.policy)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrRuntimePolicyDenied)
			var denied RuntimePolicyDeniedError
			require.True(t, errors.As(err, &denied))
			assert.Equal(t, tt.wantReason, denied.Reason)
			assert.NotContains(t, denied.Message, "/run/user")
		})
	}
}

func TestRuntimePolicyAllowedCases(t *testing.T) {
	identity := testRuntimeCommandIdentity("cmd-policy-allow")
	policy := RuntimePolicy{
		Mode:                   RuntimePolicyModeEnforce,
		ManagedNetworkPrefix:   "gordon",
		AllowedImageRegistries: []string{"registry.example.com"},
		RequireImageDigest:     true,
		AllowedCapAdd:          []string{"NET_BIND_SERVICE"},
		RuntimeComponentID:     "runtime-1",
		RuntimeComponentRole:   domain.ComponentRoleRuntime,
	}

	assert.NoError(t, policy.CheckDeployRoute(domain.DeployRouteCommand{RuntimeCommandIdentity: identity, Domain: "app.example.com", Image: "registry.example.com/app@sha256:abcdef"}))
	assert.NoError(t, policy.CheckContainerConfig(identity, "app.example.com", domain.ContainerConfig{NetworkMode: "gordon-app-example-com", Volumes: map[string]string{"/data": "app-data"}, CapAdd: []string{"NET_BIND_SERVICE"}}))
	assert.NoError(t, policy.CheckSelfUpdate(domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: identity, TargetComponentID: "runtime-1", TargetComponentRole: domain.ComponentRoleRuntime, TargetVersion: "v1.2.3", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "decision-1"}))
	assert.ErrorIs(t, policy.CheckSelfUpdate(domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: identity, TargetComponentID: "edge-1", TargetComponentRole: domain.ComponentRoleEdge, TargetVersion: "v1.2.3", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "decision-1"}), ErrRuntimePolicyDenied)
}

func TestServiceRuntimePolicyDeniesContainerConfigBeforeCreate(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, Config{}, nil)
	svc.SetRuntimePolicy(RuntimePolicy{Mode: RuntimePolicyModeEnforce})

	err := svc.checkRuntimeContainerPolicy("app.example.com", &domain.ContainerConfig{Privileged: true})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRuntimePolicyDenied)
	var denied RuntimePolicyDeniedError
	require.True(t, errors.As(err, &denied))
	assert.Equal(t, RuntimePolicyReasonPrivilegedDenied, denied.Reason)
}

func TestRuntimeWorkerPropagatesPolicyToLocalService(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, Config{}, nil)
	worker := NewRuntimeWorkerWithPolicy(svc, RuntimePolicy{Mode: RuntimePolicyModeEnforce, RequireImageDigest: true})
	require.NotNil(t, worker)
	assert.True(t, svc.runtimePolicy.Enforced())
	assert.True(t, svc.runtimePolicy.RequireImageDigest)
}

func TestRuntimeWorkerPolicyObserveAndEnforce(t *testing.T) {
	identity := testRuntimeCommandIdentity("cmd-policy-worker")
	command := domain.DeployRouteCommand{RuntimeCommandIdentity: identity, Domain: "app.example.com", Image: "app:latest"}

	observeFake := &fakeRuntimeWorkerService{}
	observeWorker := NewRuntimeWorkerWithPolicy(observeFake, RuntimePolicy{Mode: RuntimePolicyModeObserve, RequireImageDigest: true})
	observeResult, err := observeWorker.DeployRoute(testCtx(), command)
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, observeResult.Status)
	assert.Equal(t, []string{"Deploy:app.example.com:app:latest"}, observeFake.calls)
	observeEvents := observeWorker.PolicyDeniedEvents()
	require.Len(t, observeEvents, 1)
	assert.Equal(t, RuntimePolicyReasonDigestRequired, observeEvents[0].Reason)
	assert.NotContains(t, observeEvents[0].Message, "app:latest")

	enforceFake := &fakeRuntimeWorkerService{}
	enforceWorker := NewRuntimeWorkerWithPolicy(enforceFake, RuntimePolicy{Mode: RuntimePolicyModeEnforce, RequireImageDigest: true})
	enforceResult, err := enforceWorker.DeployRoute(testCtx(), command)
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusDenied, enforceResult.Status)
	require.NotNil(t, enforceResult.Error)
	assert.Equal(t, "runtime_policy_denied:image_digest_required", enforceResult.Error.Code)
	assert.Empty(t, enforceFake.calls)
}

func TestRuntimePolicyDeniedEventFromError(t *testing.T) {
	identity := testRuntimeCommandIdentity("cmd-policy-event")
	policy := RuntimePolicy{Mode: RuntimePolicyModeEnforce, RequireImageDigest: true}
	err := policy.CheckDeployRoute(domain.DeployRouteCommand{RuntimeCommandIdentity: identity, Domain: "app.example.com", Image: "app:latest"})
	require.Error(t, err)

	event, ok := RuntimePolicyDeniedEventFromError(err, "decision-1")
	require.True(t, ok)
	assert.Equal(t, identity.ID, event.CommandID)
	assert.Equal(t, "app.example.com", event.RouteDomain)
	assert.Equal(t, RuntimePolicyReasonDigestRequired, event.Reason)
	assert.Equal(t, identity.SourceComponentID, event.SourceComponentID)
	assert.NotContains(t, event.Message, "app:latest")
}
