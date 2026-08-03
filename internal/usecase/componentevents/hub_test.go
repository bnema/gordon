package componentevents

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/require"
)

func testEvent(id string) domain.ComponentEventEnvelope {
	return domain.ComponentEventEnvelope{ID: id, Type: domain.ComponentEventTypeRegistryImagePushed, Origin: domain.ComponentRoleRegistry, Timestamp: time.Now().UTC(), IdempotencyKey: "push:app:v1", Payload: domain.RegistryImagePushedPayload{Repository: "app", Reference: "v1", Digest: "sha256:abc"}, AuditClassification: domain.ComponentEventAuditWrite}
}

func TestEventHubDedupeAndReconnectLatest(t *testing.T) {
	hub := NewEventHub(2)
	ack, err := hub.Publish(context.Background(), testEvent("one"))
	require.NoError(t, err)
	require.False(t, ack.Duplicate)
	retry := testEvent("two")
	ack, err = hub.Publish(context.Background(), retry)
	require.NoError(t, err)
	require.True(t, ack.Duplicate)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates, err := hub.Subscribe(ctx)
	require.NoError(t, err)
	select {
	case event := <-updates:
		require.Equal(t, "one", event.ID)
	case <-time.After(time.Second):
		t.Fatal("latest event was not replayed")
	}
}

func TestEventHubSlowSubscriberGetsLatest(t *testing.T) {
	hub := NewEventHub(2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates, err := hub.Subscribe(ctx)
	require.NoError(t, err)
	first := testEvent("one")
	first.IdempotencyKey = "one"
	_, err = hub.Publish(context.Background(), first)
	require.NoError(t, err)
	second := testEvent("two")
	second.IdempotencyKey = "two"
	_, err = hub.Publish(context.Background(), second)
	require.NoError(t, err)
	select {
	case event := <-updates:
		require.Equal(t, "two", event.ID)
	case <-time.After(time.Second):
		t.Fatal("latest event was not delivered")
	}
}
