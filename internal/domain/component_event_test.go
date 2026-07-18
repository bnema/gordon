package domain

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func validComponentEvent() ComponentEventEnvelope {
	return ComponentEventEnvelope{ID: "evt-1", Type: ComponentEventTypeRegistryImagePushed, Origin: ComponentRoleRegistry, Timestamp: time.Unix(100, 0), Generation: 42, IdempotencyKey: "push:repo:tag:digest", Payload: RegistryImagePushedPayload{Repository: "library/app", Reference: "v1", Digest: "sha256:abc"}, AuditClassification: ComponentEventAuditWrite}
}

func TestComponentEventEnvelopeValidate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func(*ComponentEventEnvelope)
		contains string
	}{
		{"missing ID", func(e *ComponentEventEnvelope) { e.ID = "" }, "id is required"},
		{"unknown type", func(e *ComponentEventEnvelope) { e.Type = "unknown" }, "type is invalid"},
		{"unknown origin", func(e *ComponentEventEnvelope) { e.Origin = "worker" }, "origin is invalid"},
		{"missing idempotency", func(e *ComponentEventEnvelope) { e.IdempotencyKey = "" }, "idempotency key"},
		{"untyped payload", func(e *ComponentEventEnvelope) { e.Payload = nil }, "typed payload"},
		{"wrong typed payload", func(e *ComponentEventEnvelope) {
			e.Payload = RuntimeStateChangedPayload{ComponentID: "r", State: "ready"}
		}, "payload does not match"},
		{"invalid secret payload", func(e *ComponentEventEnvelope) {
			e.Type = ComponentEventTypeSecretsChanged
			e.Origin = ComponentRoleControl
			e.Payload = ComponentSecretsChangedPayload{}
		}, "invalid typed payload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := validComponentEvent()
			tc.change(&event)
			err := event.Validate()
			require.ErrorIs(t, err, ErrInvalidComponentEvent)
			require.Contains(t, err.Error(), tc.contains)
		})
	}
}

func TestComponentEventEnvelopeSupportsAllTypedPayloads(t *testing.T) {
	tests := []ComponentEventEnvelope{
		validComponentEvent(),
		{ID: "1", Type: ComponentEventTypeRuntimeStateChanged, Origin: ComponentRoleRuntime, Timestamp: time.Now(), Payload: RuntimeStateChangedPayload{"r", "ready"}, AuditClassification: ComponentEventAuditRead},
		{ID: "2", Type: ComponentEventTypeRuntimeDeploy, Origin: ComponentRoleRuntime, Timestamp: time.Now(), IdempotencyKey: "k", Payload: RuntimeDeployPayload{"d", "i", 1}, AuditClassification: ComponentEventAuditCritical},
		{ID: "3", Type: ComponentEventTypeContainerDeployed, Origin: ComponentRoleRuntime, Timestamp: time.Now(), IdempotencyKey: "k", Payload: ContainerDeployedPayload{"d", "i", "deployment", 1}, AuditClassification: ComponentEventAuditCritical},
		{ID: "4", Type: ComponentEventTypeConfigReload, Origin: ComponentRoleControl, Timestamp: time.Now(), Payload: ComponentConfigReloadPayload{"v1"}, AuditClassification: ComponentEventAuditRead},
		{ID: "5", Type: ComponentEventTypeSecretsChanged, Origin: ComponentRoleControl, Timestamp: time.Now(), IdempotencyKey: "k", Payload: ComponentSecretsChangedPayload{"v1"}, AuditClassification: ComponentEventAuditSecurity},
		{ID: "6", Type: ComponentEventTypeManualDeploy, Origin: ComponentRoleControl, Timestamp: time.Now(), IdempotencyKey: "k", Payload: ComponentManualDeployPayload{"d", "i", "c"}, AuditClassification: ComponentEventAuditWrite},
		{ID: "7", Type: ComponentEventTypePolicyDenied, Origin: ComponentRoleControl, Timestamp: time.Now(), IdempotencyKey: "k", Payload: PolicyDeniedPayload{"d", "deploy", "denied"}, AuditClassification: ComponentEventAuditSecurity},
		{ID: "8", Type: ComponentEventTypeAudit, Origin: ComponentRoleControl, Timestamp: time.Now(), IdempotencyKey: "k", Payload: AuditPayload{"deploy", "d", "denied"}, AuditClassification: ComponentEventAuditWrite},
		{ID: "9", Type: ComponentEventTypeEdgeDrain, Origin: ComponentRoleEdge, Timestamp: time.Now(), IdempotencyKey: "k", Payload: EdgeDrainPayload{"d", 1}, AuditClassification: ComponentEventAuditCritical},
	}
	for _, event := range tests {
		require.NoError(t, event.Validate(), event.Type)
	}
}

func TestComponentEventEnvelopeDedupeKey(t *testing.T) {
	event := validComponentEvent()
	retry := event
	retry.ID = "evt-2"
	retry.Timestamp = retry.Timestamp.Add(time.Hour)
	retry.RetryCount = 3
	require.Equal(t, event.DedupeKey(), retry.DedupeKey())
	require.Len(t, event.DedupeKey(), 64)
	_, err := hex.DecodeString(event.DedupeKey())
	require.NoError(t, err)
}
