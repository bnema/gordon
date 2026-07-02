package domain

import (
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
	assert.NotEqual(t, identity.DedupeKey("deploy_route"), nextGeneration.DedupeKey("deploy_route"))
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
