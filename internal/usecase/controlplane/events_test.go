package controlplane

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/require"
)

type countingHandler struct {
	calls atomic.Int32
	fail  atomic.Bool
}

func (h *countingHandler) Handle(_ context.Context, _ domain.Event) error {
	h.calls.Add(1)
	if h.fail.Load() {
		return errors.New("temporary")
	}
	return nil
}
func (h *countingHandler) CanHandle(domain.EventType) bool { return true }
func componentEvent(typ domain.ComponentEventType, payload domain.ComponentEventPayload) domain.ComponentEventEnvelope {
	return domain.ComponentEventEnvelope{ID: "event", Type: typ, Origin: domain.ComponentRoleControl, Timestamp: time.Now().UTC(), IdempotencyKey: "key", Payload: payload, AuditClassification: domain.ComponentEventAuditWrite}
}
func TestEventDispatcherRetriesFailureAndDeduplicatesSuccess(t *testing.T) {
	h := &countingHandler{}
	d := NewEventDispatcher(EventDispatcherOptions{ImagePushed: []out.EventHandler{h}})
	event := componentEvent(domain.ComponentEventTypeRegistryImagePushed, domain.RegistryImagePushedPayload{Repository: "app", Reference: "v1", Digest: "sha256:abc"})
	event.Origin = domain.ComponentRoleRegistry
	h.fail.Store(true)
	require.Error(t, d.HandleComponentEvent(context.Background(), event))
	h.fail.Store(false)
	require.NoError(t, d.HandleComponentEvent(context.Background(), event))
	require.NoError(t, d.HandleComponentEvent(context.Background(), event))
	require.EqualValues(t, 2, h.calls.Load(), "failed effects must not be acknowledged")
}
func TestEventDispatcherSingleflightsConcurrentDelivery(t *testing.T) {
	h := &countingHandler{}
	d := NewEventDispatcher(EventDispatcherOptions{ImagePushed: []out.EventHandler{h}})
	event := componentEvent(domain.ComponentEventTypeRegistryImagePushed, domain.RegistryImagePushedPayload{Repository: "app", Reference: "v1", Digest: "sha256:abc"})
	event.Origin = domain.ComponentRoleRegistry
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Go(func() { errs <- d.HandleComponentEvent(context.Background(), event) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, h.calls.Load())
}
func TestEventDispatcherManualIntentSuppressesOnlyMatchingPush(t *testing.T) {
	manual, image := &countingHandler{}, &countingHandler{}
	d := NewEventDispatcher(EventDispatcherOptions{ManualDeploy: []out.EventHandler{manual}, ImagePushed: []out.EventHandler{image}, IntentTTL: time.Minute})
	deploy := componentEvent(domain.ComponentEventTypeManualDeploy, domain.ComponentManualDeployPayload{Domain: "app.example", Image: "app:v1", CorrelationID: "manual-1"})
	require.NoError(t, d.HandleComponentEvent(context.Background(), deploy))
	matching := componentEvent(domain.ComponentEventTypeRegistryImagePushed, domain.RegistryImagePushedPayload{Repository: "app", Reference: "v1", Digest: "sha256:one"})
	matching.Origin = domain.ComponentRoleRegistry
	matching.ID = "push-1"
	matching.IdempotencyKey = "push-1"
	require.NoError(t, d.HandleComponentEvent(context.Background(), matching))
	unrelated := matching
	unrelated.ID = "push-2"
	unrelated.IdempotencyKey = "push-2"
	unrelated.Payload = domain.RegistryImagePushedPayload{Repository: "other", Reference: "v1", Digest: "sha256:two"}
	require.NoError(t, d.HandleComponentEvent(context.Background(), unrelated))
	require.EqualValues(t, 1, manual.calls.Load())
	require.EqualValues(t, 1, image.calls.Load(), "only the exact image intent may be suppressed")
}
