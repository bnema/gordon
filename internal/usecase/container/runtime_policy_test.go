package container

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestRuntimePolicyRequiresExactProfileForEveryComponentLifecycleAction(t *testing.T) {
	tests := []struct {
		name   string
		role   domain.ComponentRole
		action domain.RuntimeComponentLifecycleAction
	}{
		{name: "replace", role: domain.ComponentRoleControl, action: domain.RuntimeComponentLifecycleReplace},
		{name: "start", role: domain.ComponentRoleRuntime, action: domain.RuntimeComponentLifecycleStart},
		{name: "stop", role: domain.ComponentRoleRegistry, action: domain.RuntimeComponentLifecycleStop},
		{name: "connect", role: domain.ComponentRoleEdge, action: domain.RuntimeComponentLifecycleConnect},
		{name: "remove", role: domain.ComponentRoleRegistry, action: domain.RuntimeComponentLifecycleRemove},
		{name: "transfer", role: domain.ComponentRoleRuntime, action: domain.RuntimeComponentLifecycleTransferChannel},
		{name: "activate", role: domain.ComponentRoleEdge, action: domain.RuntimeComponentLifecycleActivate},
		{name: "drain", role: domain.ComponentRoleEdge, action: domain.RuntimeComponentLifecycleDrain},
	}
	policy := RuntimePolicy{Mode: RuntimePolicyModeEnforce}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile, ok := domain.FixedRuntimeComponentLifecycleProfile(test.role)
			require.True(t, ok)
			command := domain.RuntimeSelfUpdateCommand{
				RuntimeCommandIdentity: testRuntimeCommandIdentity("profile-" + test.name),
				TargetComponentID:      "gordon-" + string(test.role) + "-fixture-g1",
				TargetComponentRole:    test.role,
				PolicyDecisionID:       "migration:fixture",
				LifecycleAction:        test.action,
				LifecycleProfile:       profile,
			}
			require.NoError(t, policy.CheckSelfUpdate(command))

			missing := command
			missing.LifecycleProfile = domain.RuntimeComponentLifecycleProfile{}
			require.ErrorIs(t, policy.CheckSelfUpdate(missing), ErrRuntimePolicyDenied)

			for name, forge := range map[string]func(*domain.RuntimeComponentLifecycleProfile){
				"wrong UID":               func(p *domain.RuntimeComponentLifecycleProfile) { p.ProcessIdentity.UID++ },
				"wrong user":              func(p *domain.RuntimeComponentLifecycleProfile) { p.ProcessIdentity.User = "0:0" },
				"wrong user namespace":    func(p *domain.RuntimeComponentLifecycleProfile) { p.UsernsMode = "keep-id" },
				"missing capability drop": func(p *domain.RuntimeComponentLifecycleProfile) { p.CapDrop = nil },
				"missing hardening":       func(p *domain.RuntimeComponentLifecycleProfile) { p.NoNewPrivileges = false },
				"wrong volume profile":    func(p *domain.RuntimeComponentLifecycleProfile) { p.GenerationVolumeOptions = []string{"z"} },
			} {
				t.Run(name, func(t *testing.T) {
					wrong := command
					wrong.LifecycleProfile.CapDrop = append([]string(nil), command.LifecycleProfile.CapDrop...)
					wrong.LifecycleProfile.GenerationVolumeOptions = append([]string(nil), command.LifecycleProfile.GenerationVolumeOptions...)
					forge(&wrong.LifecycleProfile)
					require.ErrorIs(t, policy.CheckSelfUpdate(wrong), ErrRuntimePolicyDenied)
				})
			}
		})
	}
}

func TestRuntimePolicyRequiresEmptyProfileForProfilelessLifecycleAction(t *testing.T) {
	policy := RuntimePolicy{Mode: RuntimePolicyModeEnforce}
	command := domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: testRuntimeCommandIdentity("profile-none"),
		TargetComponentID:      "gordon-network-fixture-g1",
		TargetComponentRole:    domain.ComponentRoleRuntime,
		PolicyDecisionID:       "migration:fixture",
		LifecycleAction:        domain.RuntimeComponentLifecycleEnsureNetwork,
	}
	require.NoError(t, policy.CheckSelfUpdate(command))

	command.LifecycleProfile, _ = domain.FixedRuntimeComponentLifecycleProfile(domain.ComponentRoleRuntime)
	require.ErrorIs(t, policy.CheckSelfUpdate(command), ErrRuntimePolicyDenied)
}

func TestRuntimePolicyReadActionsRequireIdentityOnlyProfile(t *testing.T) {
	policy := RuntimePolicy{Mode: RuntimePolicyModeEnforce}
	identity, ok := domain.FixedComponentProcessIdentity(domain.ComponentRoleControl)
	require.True(t, ok)
	base := domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: testRuntimeCommandIdentity("read-profile"), TargetComponentID: "gordon-control-fixture-g1",
		TargetComponentRole: domain.ComponentRoleControl, PolicyDecisionID: "migration:fixture",
		LifecycleProfile: domain.RuntimeComponentLifecycleProfile{ProcessIdentity: identity},
	}
	for _, action := range []domain.RuntimeComponentLifecycleAction{domain.RuntimeComponentLifecycleHealth, domain.RuntimeComponentLifecycleLogs} {
		command := base
		command.LifecycleAction = action
		require.NoError(t, command.Validate())
		require.NoError(t, policy.CheckSelfUpdate(command))

		missing := command
		missing.LifecycleProfile.ProcessIdentity = domain.ComponentProcessIdentity{}
		require.ErrorIs(t, policy.CheckSelfUpdate(missing), ErrRuntimePolicyDenied)

		wrong := command
		wrong.LifecycleProfile.ProcessIdentity.UID++
		require.ErrorIs(t, policy.CheckSelfUpdate(wrong), ErrRuntimePolicyDenied)

		mutationProfile := command
		mutationProfile.LifecycleProfile.UsernsMode = "keep-id"
		require.ErrorIs(t, policy.CheckSelfUpdate(mutationProfile), ErrRuntimePolicyDenied)

		mutationField := command
		mutationField.DesiredImage = "example.invalid/forged:latest"
		require.ErrorIs(t, policy.CheckSelfUpdate(mutationField), ErrRuntimePolicyDenied)
	}
}

func TestRuntimePolicyReservesManagedControlSecretsVolume(t *testing.T) {
	identity := testRuntimeCommandIdentity("managed-secrets")
	identity.SourceComponentID = "gordon-control"
	const volume = "gordon-control-secrets-0123456789abcdef"
	const foreignVolume = "gordon-control-secrets-fedcba9876543210"
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
		{name: "workload foreign writable source", cfg: domain.ContainerConfig{Volumes: map[string]string{"/data": foreignVolume}}},
		{name: "runtime foreign writable source", cfg: domain.ContainerConfig{Name: "gordon-runtime-fixture-g1", Labels: map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: "runtime", domain.LabelComponentOwner: "runtime"}, Volumes: map[string]string{"/data": foreignVolume}}},
		{name: "edge current read-only source", cfg: domain.ContainerConfig{Name: "gordon-edge-fixture-g1", Labels: map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: "edge", domain.LabelComponentOwner: "runtime"}, ReadOnlyVolumes: map[string]string{"/data": volume}}},
		{name: "registry foreign read-only source", cfg: domain.ContainerConfig{Name: "gordon-registry-fixture-g1", Labels: map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: "registry", domain.LabelComponentOwner: "runtime"}, ReadOnlyVolumes: map[string]string{"/data": foreignVolume}}},
		{name: "control foreign source at managed destination", cfg: domain.ContainerConfig{Name: authorized.Name, Labels: authorized.Labels, Volumes: map[string]string{managedControlSecretsPath: foreignVolume}}},
		{name: "exact pair plus foreign source", cfg: domain.ContainerConfig{Name: authorized.Name, Labels: authorized.Labels, Volumes: map[string]string{managedControlSecretsPath: volume, "/tmp/foreign-secrets": foreignVolume}}},
		{name: "control foreign source at alternate destination", cfg: domain.ContainerConfig{Name: authorized.Name, Labels: authorized.Labels, Volumes: map[string]string{"/tmp/secrets": foreignVolume}}},
		{name: "control foreign read-only source", cfg: domain.ContainerConfig{Name: authorized.Name, Labels: authorized.Labels, ReadOnlyVolumes: map[string]string{"/tmp/secrets": foreignVolume}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.ErrorIs(t, policy.CheckContainerConfig(identity, "", test.cfg), ErrRuntimePolicyDenied)
		})
	}
}

func TestRuntimePolicyDeniesManagedSecretsPatternForEveryNonControlRoleAndMountMode(t *testing.T) {
	identity := testRuntimeCommandIdentity("managed-secrets-role-isolation")
	identity.SourceComponentID = "gordon-control"
	const configuredVolume = "gordon-control-secrets-0123456789abcdef"
	policy := RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedControlSecretsVolume: configuredVolume}

	roles := []struct {
		name   string
		config domain.ContainerConfig
	}{
		{name: "workload", config: domain.ContainerConfig{}},
		{name: "runtime", config: domain.ContainerConfig{Name: "gordon-runtime-fixture-g1", Labels: map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: string(domain.ComponentRoleRuntime), domain.LabelComponentOwner: "runtime"}}},
		{name: "edge", config: domain.ContainerConfig{Name: "gordon-edge-fixture-g1", Labels: map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: string(domain.ComponentRoleEdge), domain.LabelComponentOwner: "runtime"}}},
		{name: "registry", config: domain.ContainerConfig{Name: "gordon-registry-fixture-g1", Labels: map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: string(domain.ComponentRoleRegistry), domain.LabelComponentOwner: "runtime"}}},
	}
	volumes := []struct {
		name   string
		source string
	}{
		{name: "current", source: configuredVolume},
		{name: "foreign", source: "gordon-control-secrets-fedcba9876543210"},
	}

	for _, role := range roles {
		for _, volume := range volumes {
			t.Run(role.name+"/writable/"+volume.name, func(t *testing.T) {
				cfg := role.config
				cfg.Volumes = map[string]string{"/alternate-secrets": volume.source}
				require.ErrorIs(t, policy.CheckContainerConfig(identity, "app.example.com", cfg), ErrRuntimePolicyDenied)
			})
			t.Run(role.name+"/read-only/"+volume.name, func(t *testing.T) {
				cfg := role.config
				cfg.ReadOnlyVolumes = map[string]string{"/alternate-secrets": volume.source}
				require.ErrorIs(t, policy.CheckContainerConfig(identity, "app.example.com", cfg), ErrRuntimePolicyDenied)
			})
		}
	}
}

func TestRuntimePolicyReservesManagedControlSecretsPatternWithoutLocalConfiguration(t *testing.T) {
	identity := testRuntimeCommandIdentity("managed-secrets-global-reservation")
	cfg := domain.ContainerConfig{ReadOnlyVolumes: map[string]string{"/data": "gordon-control-secrets-fedcba9876543210"}}

	require.ErrorIs(t, (RuntimePolicy{Mode: RuntimePolicyModeEnforce}).CheckContainerConfig(identity, "app.example.com", cfg), ErrRuntimePolicyDenied)
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
			wantReason: RuntimePolicyReasonUnsafeHostBindDenied,
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
