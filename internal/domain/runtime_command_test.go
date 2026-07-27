package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeCommandIdentityValidateRequiresIdempotencyFields(t *testing.T) {
	identity := RuntimeCommandIdentity{
		ID:                RuntimeCommandID("cmd-1"),
		IdempotencyKey:    "deploy:example.com:1",
		Generation:        1,
		SourceComponentID: "control-1",
	}

	require.NoError(t, identity.Validate())

	missingKey := identity
	missingKey.IdempotencyKey = ""
	require.ErrorIs(t, missingKey.Validate(), ErrInvalidRuntimeCommand)

	missingGeneration := identity
	missingGeneration.Generation = 0
	require.ErrorIs(t, missingGeneration.Validate(), ErrInvalidRuntimeCommand)
}

func TestRuntimeCommandIdentityDedupeKeyStableForSameLogicalCommand(t *testing.T) {
	identity := RuntimeCommandIdentity{
		ID:                RuntimeCommandID("cmd-1"),
		IdempotencyKey:    "deploy:example.com:1",
		Generation:        7,
		SourceComponentID: "control-1",
	}
	redelivery := identity
	redelivery.ID = RuntimeCommandID("cmd-redelivered")

	assert.Equal(t, identity.DedupeKey("deploy_route"), redelivery.DedupeKey("deploy_route"))

	nextGeneration := identity
	nextGeneration.Generation = 8
	assert.Equal(t, identity.DedupeKey("deploy_route"), nextGeneration.DedupeKey("deploy_route"))
}

func TestRuntimeRouteCommandValidation(t *testing.T) {
	identity := RuntimeCommandIdentity{
		ID:                RuntimeCommandID("cmd-1"),
		IdempotencyKey:    "route:example.com:1",
		Generation:        1,
		SourceComponentID: "control-1",
	}

	restart := RestartRouteCommand{RuntimeCommandIdentity: identity, Domain: "example.com"}
	require.NoError(t, restart.Validate())
	restart.Domain = "not a domain"
	require.ErrorIs(t, restart.Validate(), ErrInvalidRuntimeCommand)

	remove := RemoveRouteCommand{RuntimeCommandIdentity: identity, Domain: "example.com"}
	require.NoError(t, remove.Validate())
	remove.Domain = ""
	require.ErrorIs(t, remove.Validate(), ErrInvalidRuntimeCommand)

	reconcile := ReconcileRuntimeCommand{RuntimeCommandIdentity: identity, ExpectedRouteCount: 0}
	require.NoError(t, reconcile.Validate())
	reconcile.ExpectedRouteCount = -1
	require.ErrorIs(t, reconcile.Validate(), ErrInvalidRuntimeCommand)

	reconcile = ReconcileRuntimeCommand{RuntimeCommandIdentity: identity, ExpectedRouteCount: 1, DesiredRoutes: []Route{{Domain: "example.com", Image: "app:latest"}}}
	require.NoError(t, reconcile.Validate())
	reconcile.DesiredRoutes = nil
	require.ErrorIs(t, reconcile.Validate(), ErrInvalidRuntimeCommand)
}

func TestRuntimeSelfUpdateCommandValidateRequiresComponentLifecyclePolicy(t *testing.T) {
	command := RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: RuntimeCommandIdentity{
			ID:                RuntimeCommandID("cmd-1"),
			IdempotencyKey:    "self-update:runtime-1:1.2.3",
			Generation:        1,
			SourceComponentID: "control-1",
		},
		TargetComponentID:   "runtime-1",
		TargetComponentRole: ComponentRoleRuntime,
		CurrentVersion:      "1.2.2",
		TargetVersion:       "1.2.3",
		Policy:              RuntimeSelfUpdatePolicyManualApproval,
		PolicyDecisionID:    "policy-1",
		ApprovedBy:          "operator",
	}

	require.NoError(t, command.Validate())

	componentLifecycle := command
	componentLifecycle.TargetComponentRole = ComponentRoleRegistry
	require.NoError(t, componentLifecycle.Validate())

	unmanaged := command
	unmanaged.TargetComponentRole = ComponentRole("unmanaged")
	require.ErrorIs(t, unmanaged.Validate(), ErrInvalidRuntimeCommand)

	withoutPolicy := command
	withoutPolicy.PolicyDecisionID = ""
	require.ErrorIs(t, withoutPolicy.Validate(), ErrInvalidRuntimeCommand)
}

func TestRuntimeComponentLifecycleRequirementCoversEveryAction(t *testing.T) {
	tests := []struct {
		action RuntimeComponentLifecycleAction
		mode   RuntimeComponentLifecycleProfileMode
	}{
		{RuntimeComponentLifecycleReplace, RuntimeComponentLifecycleProfileFull},
		{RuntimeComponentLifecycleEnsureNetwork, RuntimeComponentLifecycleProfileNone},
		{RuntimeComponentLifecycleStart, RuntimeComponentLifecycleProfileFull},
		{RuntimeComponentLifecycleStop, RuntimeComponentLifecycleProfileFull},
		{RuntimeComponentLifecycleHealth, RuntimeComponentLifecycleProfileIdentityOnly},
		{RuntimeComponentLifecycleLogs, RuntimeComponentLifecycleProfileIdentityOnly},
		{RuntimeComponentLifecycleConnect, RuntimeComponentLifecycleProfileFull},
		{RuntimeComponentLifecycleRemove, RuntimeComponentLifecycleProfileFull},
		{RuntimeComponentLifecycleTransferChannel, RuntimeComponentLifecycleProfileFull},
		{RuntimeComponentLifecycleActivate, RuntimeComponentLifecycleProfileFull},
		{RuntimeComponentLifecycleDrain, RuntimeComponentLifecycleProfileFull},
	}
	for _, test := range tests {
		requirement, ok := RuntimeComponentLifecycleRequirement(test.action)
		require.True(t, ok, test.action)
		assert.Equal(t, test.mode, requirement.ProfileMode, test.action)
	}

	_, ok := RuntimeComponentLifecycleRequirement(RuntimeComponentLifecycleAction("exec"))
	assert.False(t, ok)
}

func TestNewRuntimeComponentLifecycleReadCommandBuildsMinimalIdentityPayload(t *testing.T) {
	identity := RuntimeCommandIdentity{ID: "health", IdempotencyKey: "health", Generation: 1, SourceComponentID: "gordon-control"}
	command, err := NewRuntimeComponentLifecycleReadCommand(identity, "gordon-edge-fixture-g1", ComponentRoleEdge, "migration:fixture", RuntimeComponentLifecycleHealth)
	require.NoError(t, err)
	require.NoError(t, command.Validate())
	assert.Equal(t, identity, command.RuntimeCommandIdentity)
	assert.Equal(t, RuntimeComponentLifecycleHealth, command.LifecycleAction)
	expectedIdentity, ok := FixedComponentProcessIdentity(ComponentRoleEdge)
	require.True(t, ok)
	assert.Equal(t, RuntimeComponentLifecycleProfile{ProcessIdentity: expectedIdentity}, command.LifecycleProfile)
	assert.True(t, command.HasOnlyReadLifecycleIdentity())

	_, err = NewRuntimeComponentLifecycleReadCommand(identity, "gordon-edge-fixture-g1", ComponentRoleEdge, "migration:fixture", RuntimeComponentLifecycleStart)
	require.ErrorIs(t, err, ErrInvalidRuntimeCommand)
}

func TestRuntimeSelfUpdateCommandValidateRequiresNoProfileForEnsureNetwork(t *testing.T) {
	command := RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: RuntimeCommandIdentity{ID: "network", IdempotencyKey: "network", Generation: 1, SourceComponentID: "gordon-control"},
		TargetComponentID:      "gordon-network-fixture-g1", TargetComponentRole: ComponentRoleRuntime,
		TargetVersion: "v2", Policy: RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture",
		LifecycleAction: RuntimeComponentLifecycleEnsureNetwork,
	}
	require.NoError(t, command.Validate())

	command.LifecycleProfile, _ = FixedRuntimeComponentLifecycleProfile(ComponentRoleRuntime)
	require.ErrorIs(t, command.Validate(), ErrInvalidRuntimeCommand)
}

func TestRuntimeSelfUpdateCommandValidateBoundsAndSanitizesEdgeAppNetworks(t *testing.T) {
	profile, ok := FixedRuntimeComponentLifecycleProfile(ComponentRoleEdge)
	require.True(t, ok)
	command := RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: RuntimeCommandIdentity{ID: RuntimeCommandID("cmd-edge"), IdempotencyKey: "self-update:edge:1", Generation: 1, SourceComponentID: "control-1"},
		TargetComponentID:      "gordon-edge-fixture-g1", TargetComponentRole: ComponentRoleEdge, TargetVersion: "1.2.3", Policy: RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: RuntimeComponentLifecycleActivate, LifecycleProfile: profile,
		EdgeAppNetworks: []string{"gordon-app-one", "gordon-app-two"},
	}
	require.NoError(t, command.Validate())

	unsafeName := command
	unsafeName.EdgeAppNetworks = []string{"../engine-network"}
	require.ErrorIs(t, unsafeName.Validate(), ErrInvalidRuntimeCommand)

	tooLong := command
	tooLong.EdgeAppNetworks = []string{strings.Repeat("a", MaxEdgeAppNetworkNameLength+1)}
	require.ErrorIs(t, tooLong.Validate(), ErrInvalidRuntimeCommand)

	duplicate := command
	duplicate.EdgeAppNetworks = []string{"gordon-app-one", "gordon-app-one"}
	require.ErrorIs(t, duplicate.Validate(), ErrInvalidRuntimeCommand)

	tooMany := command
	tooMany.EdgeAppNetworks = make([]string, MaxEdgeAppNetworks+1)
	for i := range tooMany.EdgeAppNetworks {
		tooMany.EdgeAppNetworks[i] = "gordon-app"
	}
	require.ErrorIs(t, tooMany.Validate(), ErrInvalidRuntimeCommand)

	nonActivation := command
	nonActivation.LifecycleAction = RuntimeComponentLifecycleStart
	require.ErrorIs(t, nonActivation.Validate(), ErrInvalidRuntimeCommand)
}
