package controlplane

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

func TestEventDispatcherExpiredManualIntentDoesNotSuppressPush(t *testing.T) {
	manual, image := &countingHandler{}, &countingHandler{}
	dispatcher := NewEventDispatcher(EventDispatcherOptions{ManualDeploy: []out.EventHandler{manual}, ImagePushed: []out.EventHandler{image}, IntentTTL: time.Millisecond})
	deploy := componentEvent(domain.ComponentEventTypeManualDeploy, domain.ComponentManualDeployPayload{Domain: "app.example", Image: "app:v1", CorrelationID: "manual-1"})
	require.NoError(t, dispatcher.HandleComponentEvent(t.Context(), deploy))
	time.Sleep(5 * time.Millisecond)
	push := componentEvent(domain.ComponentEventTypeRegistryImagePushed, domain.RegistryImagePushedPayload{Repository: "app", Reference: "v1", Digest: "sha256:abc"})
	push.Origin, push.ID, push.IdempotencyKey = domain.ComponentRoleRegistry, "push-1", "push-1"
	require.NoError(t, dispatcher.HandleComponentEvent(t.Context(), push))
	require.EqualValues(t, 1, image.calls.Load())
}

func TestEventDispatcherFailedManualDeployDoesNotPersistSuppression(t *testing.T) {
	manual, image := &countingHandler{}, &countingHandler{}
	manual.fail.Store(true)
	dispatcher := NewEventDispatcher(EventDispatcherOptions{ManualDeploy: []out.EventHandler{manual}, ImagePushed: []out.EventHandler{image}})
	deploy := componentEvent(domain.ComponentEventTypeManualDeploy, domain.ComponentManualDeployPayload{Domain: "app.example", Image: "app:v1", CorrelationID: "manual-1"})
	require.Error(t, dispatcher.HandleComponentEvent(context.Background(), deploy))
	manual.fail.Store(false)
	push := componentEvent(domain.ComponentEventTypeRegistryImagePushed, domain.RegistryImagePushedPayload{Repository: "app", Reference: "v1", Digest: "sha256:abc"})
	push.Origin, push.ID, push.IdempotencyKey = domain.ComponentRoleRegistry, "push-1", "push-1"
	require.NoError(t, dispatcher.HandleComponentEvent(context.Background(), push))
	require.EqualValues(t, 1, image.calls.Load())
}
