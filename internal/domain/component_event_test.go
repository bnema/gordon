package domain

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestComponentEventEnvelopeValidate(t *testing.T) {
	valid := func() ComponentEventEnvelope {
		return ComponentEventEnvelope{
			ID:                  "evt-1",
			Type:                ComponentEventTypeRegistryImagePushed,
			Origin:              ComponentRoleRegistry,
			Timestamp:           time.Unix(100, 0),
			Generation:          42,
			IdempotencyKey:      "push:repo:tag:digest",
			PayloadKind:         ComponentEventPayloadKindJSON,
			SerializedPayload:   []byte(`{"repo":"example"}`),
			RetryCount:          0,
			AuditClassification: ComponentEventAuditWrite,
		}
	}

	t.Run("missing ID", func(t *testing.T) {
		event := valid()
		event.ID = ""

		err := event.Validate()

		require.ErrorIs(t, err, ErrInvalidComponentEvent)
		require.Contains(t, err.Error(), "id is required")
	})

	t.Run("missing type", func(t *testing.T) {
		event := valid()
		event.Type = ""

		err := event.Validate()

		require.ErrorIs(t, err, ErrInvalidComponentEvent)
		require.Contains(t, err.Error(), "type is required")
	})

	t.Run("invalid origin", func(t *testing.T) {
		event := valid()
		event.Origin = ComponentRole("worker")

		err := event.Validate()

		require.ErrorIs(t, err, ErrInvalidComponentEvent)
		require.Contains(t, err.Error(), "origin is invalid")
	})

	t.Run("unknown event type", func(t *testing.T) {
		event := valid()
		event.Type = ComponentEventType("registry.unknown")

		err := event.Validate()

		require.ErrorIs(t, err, ErrInvalidComponentEvent)
		require.Contains(t, err.Error(), "type is invalid")
	})

	t.Run("write event requires idempotency key", func(t *testing.T) {
		event := valid()
		event.IdempotencyKey = ""

		err := event.Validate()

		require.ErrorIs(t, err, ErrInvalidComponentEvent)
		require.Contains(t, err.Error(), "idempotency key is required")
	})

	t.Run("critical event requires idempotency key", func(t *testing.T) {
		event := valid()
		event.Type = ComponentEventTypeRuntimeDeploy
		event.Origin = ComponentRoleRuntime
		event.IdempotencyKey = ""
		event.AuditClassification = ComponentEventAuditCritical

		err := event.Validate()

		require.ErrorIs(t, err, ErrInvalidComponentEvent)
		require.Contains(t, err.Error(), "idempotency key is required")
	})

	t.Run("timestamp is required", func(t *testing.T) {
		event := valid()
		event.Timestamp = time.Time{}

		err := event.Validate()

		require.ErrorIs(t, err, ErrInvalidComponentEvent)
		require.Contains(t, err.Error(), "timestamp is required")
	})

	t.Run("retry count is non-negative", func(t *testing.T) {
		event := valid()
		event.RetryCount = -1

		err := event.Validate()

		require.ErrorIs(t, err, ErrInvalidComponentEvent)
		require.Contains(t, err.Error(), "retry count must be non-negative")
	})

	t.Run("invalid payload kind", func(t *testing.T) {
		event := valid()
		event.PayloadKind = ComponentEventPayloadKind("text")

		err := event.Validate()

		require.ErrorIs(t, err, ErrInvalidComponentEvent)
		require.Contains(t, err.Error(), "payload kind is invalid")
	})

	t.Run("empty payload kind rejects serialized payload", func(t *testing.T) {
		event := valid()
		event.PayloadKind = ComponentEventPayloadKindEmpty

		err := event.Validate()

		require.ErrorIs(t, err, ErrInvalidComponentEvent)
		require.Contains(t, err.Error(), "payload kind is not coherent")
	})

	t.Run("serialized payload kind requires payload", func(t *testing.T) {
		event := valid()
		event.SerializedPayload = nil

		err := event.Validate()

		require.ErrorIs(t, err, ErrInvalidComponentEvent)
		require.Contains(t, err.Error(), "payload kind is not coherent")
	})

	t.Run("audit classification allowlist", func(t *testing.T) {
		event := valid()
		event.AuditClassification = ComponentEventAuditClassification("public")

		err := event.Validate()

		require.ErrorIs(t, err, ErrInvalidComponentEvent)
		require.Contains(t, err.Error(), "audit classification is invalid")
	})

	t.Run("audit classification coherence", func(t *testing.T) {
		event := valid()
		event.Type = ComponentEventTypeEdgeDrain
		event.Origin = ComponentRoleEdge
		event.AuditClassification = ComponentEventAuditNone

		err := event.Validate()

		require.ErrorIs(t, err, ErrInvalidComponentEvent)
		require.Contains(t, err.Error(), "audit classification is not coherent")
	})
}

func TestComponentEventEnvelopeDedupeKey(t *testing.T) {
	event := ComponentEventEnvelope{
		ID:                  "evt-1",
		Type:                ComponentEventTypeRegistryImagePushed,
		Origin:              ComponentRoleRegistry,
		Timestamp:           time.Unix(100, 0),
		Generation:          42,
		IdempotencyKey:      "push:repo:tag:digest",
		PayloadKind:         ComponentEventPayloadKindJSON,
		SerializedPayload:   []byte(`{"repo":"example"}`),
		RetryCount:          0,
		AuditClassification: ComponentEventAuditWrite,
	}

	redelivery := event
	redelivery.ID = "evt-2"
	redelivery.Timestamp = event.Timestamp.Add(time.Hour)
	redelivery.RetryCount = 3

	changedGeneration := event
	changedGeneration.Generation++

	key := event.DedupeKey()

	require.NoError(t, event.Validate())
	require.NotEmpty(t, key)
	require.Equal(t, key, redelivery.DedupeKey())
	require.NotEqual(t, key, changedGeneration.DedupeKey())
	require.Len(t, key, 64)
	_, err := hex.DecodeString(key)
	require.NoError(t, err)
}

func TestComponentEventEnvelopeDedupeKeyUsesIDWhenIdempotencyKeyIsEmpty(t *testing.T) {
	event := ComponentEventEnvelope{
		ID:                  "evt-1",
		Type:                ComponentEventTypeRuntimeStateChanged,
		Origin:              ComponentRoleRuntime,
		Timestamp:           time.Unix(100, 0),
		Generation:          42,
		PayloadKind:         ComponentEventPayloadKindEmpty,
		RetryCount:          0,
		AuditClassification: ComponentEventAuditRead,
	}

	redeliveryWithSameID := event
	redeliveryWithSameID.Timestamp = event.Timestamp.Add(time.Hour)
	redeliveryWithSameID.RetryCount = 2

	samePayloadDifferentID := event
	samePayloadDifferentID.ID = "evt-2"

	require.NoError(t, event.Validate())
	require.Equal(t, event.DedupeKey(), redeliveryWithSameID.DedupeKey())
	require.NotEqual(t, event.DedupeKey(), samePayloadDifferentID.DedupeKey())
}
