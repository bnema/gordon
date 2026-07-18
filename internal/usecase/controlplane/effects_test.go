package controlplane

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

type captureEventHandler struct{ events []domain.Event }

func (h *captureEventHandler) CanHandle(domain.EventType) bool { return true }
func (h *captureEventHandler) Handle(_ context.Context, event domain.Event) error {
	h.events = append(h.events, event)
	return nil
}

type captureAuditSink struct {
	events []domain.ComponentEventEnvelope
}

func (s *captureAuditSink) AuditComponentEvent(_ context.Context, event domain.ComponentEventEnvelope) error {
	s.events = append(s.events, event)
	return nil
}

type captureRouteCommander struct{}

func (captureRouteCommander) DeployRoute(context.Context, domain.Route) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}
func (captureRouteCommander) ReconcileConfiguredRoutes(context.Context, string) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}

func TestProductionEffectsImagePushedBridgesExactAutomationPayload(t *testing.T) {
	automation := &captureEventHandler{}
	configured := &captureEventHandler{}
	image, err := NewImagePushedHandlers(automation, configured)
	require.NoError(t, err)
	audit := &captureAuditSink{}
	effects, err := NewProductionEffects(image, &captureEventHandler{}, captureRouteCommander{}, audit)
	require.NoError(t, err)

	manifest := []byte(`{"schemaVersion":2}`)
	annotations := map[string]string{"org.opencontainers.image.source": "example/repo"}
	event := imagePushedComponentEvent(manifest, annotations)
	require.NoError(t, effects.ImagePushed(t.Context(), event))

	require.Len(t, automation.events, 1)
	require.Len(t, configured.events, 1)
	payload, ok := automation.events[0].Data.(domain.ImagePushedPayload)
	require.True(t, ok)
	assert.Equal(t, "repo", automation.events[0].ImageName)
	assert.Equal(t, "v1", automation.events[0].Tag)
	assert.Equal(t, manifest, payload.Manifest)
	assert.Equal(t, annotations, payload.Annotations)
	manifest[0] = 'x'
	annotations["org.opencontainers.image.source"] = "mutated"
	assert.Equal(t, byte('{'), payload.Manifest[0])
	assert.Equal(t, "example/repo", payload.Annotations["org.opencontainers.image.source"])
	assert.Equal(t, automation.events[0], configured.events[0])
	assert.Equal(t, []domain.ComponentEventEnvelope{event}, audit.events)
}

func imagePushedComponentEvent(manifest []byte, annotations map[string]string) domain.ComponentEventEnvelope {
	return domain.ComponentEventEnvelope{
		ID: "push-1", Type: domain.ComponentEventTypeRegistryImagePushed, Origin: domain.ComponentRoleRegistry,
		Timestamp: time.Now().UTC(), IdempotencyKey: "repo:v1:sha256:abc", AuditClassification: domain.ComponentEventAuditCritical,
		Payload: domain.RegistryImagePushedPayload{Repository: "repo", Reference: "v1", Digest: "sha256:abc", Manifest: manifest, Annotations: annotations},
	}
}
