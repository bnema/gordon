package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeRuntimeStateLabelsRetainsComponentGenerationIdentity(t *testing.T) {
	labels := SanitizeRuntimeStateLabels(map[string]string{
		LabelComponent:            "true",
		LabelComponentRole:        "runtime",
		LabelComponentGeneration:  "1",
		LabelComponentMigrationID: "migration",
		"private.token":           "must-not-leak",
	})
	require.Equal(t, "true", labels[LabelComponent])
	require.Equal(t, "runtime", labels[LabelComponentRole])
	require.Equal(t, "1", labels[LabelComponentGeneration])
	require.Equal(t, "migration", labels[LabelComponentMigrationID])
	require.NotContains(t, labels, "private.token")
}

func TestRuntimeActualStateSnapshotValidateRequiresSourceAndVersion(t *testing.T) {
	snapshot := RuntimeActualStateSnapshot{
		Generation:        1,
		StateVersion:      "routes-v1",
		SourceComponentID: "runtime-1",
		Routes: []RuntimeRouteState{{
			Domain:          "example.com",
			Generation:      1,
			EdgeTargetAlias: "app-example",
			TargetPort:      8080,
			Scheme:          "http",
			Protocol:        RouteTargetProtocolHTTP1,
			Status:          RouteTargetStatusReady,
		}},
	}

	require.NoError(t, snapshot.Validate())

	missingSource := snapshot
	missingSource.SourceComponentID = ""
	require.ErrorIs(t, missingSource.Validate(), ErrInvalidRuntimeState)

	missingVersion := snapshot
	missingVersion.StateVersion = ""
	require.ErrorIs(t, missingVersion.Validate(), ErrInvalidRuntimeState)
}

func TestRuntimeRouteStateValidateRejectsLocalhostEdgeTarget(t *testing.T) {
	state := RuntimeRouteState{
		Domain:          "example.com",
		Generation:      1,
		EdgeTargetAlias: "localhost",
		TargetPort:      8080,
		Scheme:          "http",
		Protocol:        RouteTargetProtocolHTTP1,
		Status:          RouteTargetStatusReady,
	}

	require.ErrorIs(t, state.Validate(), ErrInvalidRuntimeState)

	state.EdgeTargetAlias = "127.0.0.1"
	require.ErrorIs(t, state.Validate(), ErrInvalidRuntimeState)

	state.EdgeTargetAlias = "app-example"
	require.NoError(t, state.Validate())
}

func TestRuntimeRouteStateValidateUnavailableRejectsEndpointFields(t *testing.T) {
	state := RuntimeRouteState{
		Domain:            "example.com",
		Generation:        1,
		Status:            RouteTargetStatusUnavailable,
		UnavailableReason: RouteTargetUnavailableReasonStarting,
	}
	require.NoError(t, state.Validate())

	withEndpoint := state
	withEndpoint.EdgeTargetAlias = "app-example"
	require.ErrorIs(t, withEndpoint.Validate(), ErrInvalidRuntimeState)

	missingReason := state
	missingReason.UnavailableReason = RouteTargetUnavailableReasonNone
	require.ErrorIs(t, missingReason.Validate(), ErrInvalidRuntimeState)

	unknownReason := state
	unknownReason.UnavailableReason = RouteTargetUnavailableReason("raw_runtime_error")
	require.ErrorIs(t, unknownReason.Validate(), ErrInvalidRuntimeState)
}

func TestRuntimeRouteStateValidateRequiresSchemeAndProtocolForRoutableTarget(t *testing.T) {
	state := RuntimeRouteState{
		Domain:          "example.com",
		Generation:      1,
		EdgeTargetAlias: "app-example",
		TargetPort:      8080,
		Scheme:          "http",
		Protocol:        RouteTargetProtocolHTTP1,
		Status:          RouteTargetStatusReady,
	}
	require.NoError(t, state.Validate())

	missingScheme := state
	missingScheme.Scheme = ""
	require.ErrorIs(t, missingScheme.Validate(), ErrInvalidRuntimeState)

	missingProtocol := state
	missingProtocol.Protocol = ""
	require.ErrorIs(t, missingProtocol.Validate(), ErrInvalidRuntimeState)
}

func TestRuntimeContainerStateValidateRejectsUnsanitizedLabels(t *testing.T) {
	state := RuntimeContainerState{
		Name:   "gordon-example.com",
		Status: ContainerStatusRunning,
		Labels: map[string]string{
			LabelDomain:  "example.com",
			LabelEnvHash: "sha256:abc",
		},
	}
	require.NoError(t, state.Validate())

	state.Labels["com.example.secret"] = "raw-secret"
	require.ErrorIs(t, state.Validate(), ErrInvalidRuntimeState)

	sanitized := state.SanitizedLabels()
	require.NotContains(t, sanitized, "com.example.secret")
	require.Equal(t, "example.com", sanitized[LabelDomain])
}

func TestRuntimeSnapshotValidateChecksContainerNetworkAndVolumeState(t *testing.T) {
	snapshot := RuntimeActualStateSnapshot{
		Generation:        1,
		StateVersion:      "state-v1",
		SourceComponentID: "runtime-1",
		Containers: []RuntimeContainerState{{
			Name:   "gordon-example.com",
			Status: ContainerStatusRunning,
		}},
		Networks: []RuntimeNetworkState{{Name: "gordon-example", Aliases: []string{"app-example"}}},
		Volumes:  []RuntimeVolumeState{{Name: "gordon-example-data"}},
	}
	require.NoError(t, snapshot.Validate())

	badNetwork := snapshot
	badNetwork.Networks = []RuntimeNetworkState{{Name: "gordon-example", Aliases: []string{"127.0.0.1"}}}
	require.ErrorIs(t, badNetwork.Validate(), ErrInvalidRuntimeState)

	badVolume := snapshot
	badVolume.Volumes = []RuntimeVolumeState{{Name: "/host/path"}}
	require.ErrorIs(t, badVolume.Validate(), ErrInvalidRuntimeState)
}

func TestRuntimeEdgeNetworkAttachmentStateValidateRequiresAliasTarget(t *testing.T) {
	state := RuntimeEdgeNetworkAttachmentState{
		RouteDomain: "example.com",
		NetworkName: "gordon-edge",
		EdgeAlias:   "edge",
		TargetAlias: "app-example",
		TargetPort:  8080,
		Attached:    true,
		Generation:  1,
	}

	require.NoError(t, state.Validate())

	localhost := state
	localhost.TargetAlias = "::1"
	require.ErrorIs(t, localhost.Validate(), ErrInvalidRuntimeState)

	missingAlias := state
	missingAlias.TargetAlias = ""
	require.ErrorIs(t, missingAlias.Validate(), ErrInvalidRuntimeState)
}
