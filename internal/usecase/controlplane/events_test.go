package controlplane

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/adapters/out/filesystem"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	containerusecase "github.com/bnema/gordon/internal/usecase/container"
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
func TestEventDispatcherRoutesEveryTypedPayloadToOneEffect(t *testing.T) {
	var calls atomic.Int32
	effect := func(context.Context, domain.ComponentEventEnvelope) error { calls.Add(1); return nil }
	d := NewEventDispatcher(EventDispatcherOptions{
		ImagePushedEffect: effect, ConfigReloadEffect: effect, ManualDeployEffect: effect, SecretsEffect: effect,
		RuntimeState: effect, RuntimeEvent: effect, PolicyAudit: effect,
	})
	tests := []struct {
		typ     domain.ComponentEventType
		payload domain.ComponentEventPayload
		origin  domain.ComponentRole
	}{
		{domain.ComponentEventTypeRegistryImagePushed, domain.RegistryImagePushedPayload{Repository: "app", Reference: "v1", Digest: "sha256:abc"}, domain.ComponentRoleRegistry},
		{domain.ComponentEventTypeRuntimeStateChanged, domain.RuntimeStateChangedPayload{ComponentID: "runtime", State: "ready"}, domain.ComponentRoleRuntime},
		{domain.ComponentEventTypeRuntimeDeploy, domain.RuntimeDeployPayload{Domain: "app.example", Image: "app:v1", Generation: 1}, domain.ComponentRoleRuntime},
		{domain.ComponentEventTypeContainerDeployed, domain.ContainerDeployedPayload{Domain: "app.example", Image: "app:v1", DeploymentID: "deploy", Generation: 1}, domain.ComponentRoleRuntime},
		{domain.ComponentEventTypeConfigReload, domain.ComponentConfigReloadPayload{Version: "v1"}, domain.ComponentRoleControl},
		{domain.ComponentEventTypeSecretsChanged, domain.ComponentSecretsChangedPayload{Version: "v1"}, domain.ComponentRoleControl},
		{domain.ComponentEventTypeManualDeploy, domain.ComponentManualDeployPayload{Domain: "app.example", Image: "other:v1", CorrelationID: "manual"}, domain.ComponentRoleControl},
		{domain.ComponentEventTypePolicyDenied, domain.PolicyDeniedPayload{DecisionID: "decision", Action: "deploy", Reason: "denied"}, domain.ComponentRoleControl},
		{domain.ComponentEventTypeAudit, domain.AuditPayload{Action: "deploy", Subject: "app.example", Outcome: "ok"}, domain.ComponentRoleControl},
		{domain.ComponentEventTypeEdgeDrain, domain.EdgeDrainPayload{Domain: "app.example", Generation: 1}, domain.ComponentRoleEdge},
	}
	for i, test := range tests {
		event := componentEvent(test.typ, test.payload)
		event.ID = fmt.Sprintf("event-%d", i)
		event.IdempotencyKey = event.ID
		event.Origin = test.origin
		require.NoError(t, d.HandleComponentEvent(context.Background(), event), test.typ)
	}
	require.EqualValues(t, len(tests), calls.Load())
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

type failOnceAckStore struct {
	failed    bool
	processed map[string]bool
}

func (s *failOnceAckStore) IsComponentEventProcessed(_ context.Context, key string) (bool, error) {
	return s.processed[key], nil
}
func (s *failOnceAckStore) MarkComponentEventProcessed(_ context.Context, key string, _ time.Time) error {
	if !s.failed {
		s.failed = true
		return errors.New("ack store unavailable")
	}
	s.processed[key] = true
	return nil
}

type durableEffectRuntimeService struct{ deploys atomic.Int32 }

func (s *durableEffectRuntimeService) Deploy(context.Context, domain.Route) (*domain.Container, error) {
	s.deploys.Add(1)
	return &domain.Container{}, nil
}
func (*durableEffectRuntimeService) Restart(context.Context, string, bool) error { return nil }
func (*durableEffectRuntimeService) ReconcileRemovedRoute(context.Context, string) (*domain.CleanupReport, error) {
	return &domain.CleanupReport{}, nil
}
func (*durableEffectRuntimeService) SyncContainers(context.Context) error              { return nil }
func (*durableEffectRuntimeService) AutoStart(context.Context, []domain.Route) error   { return nil }
func (*durableEffectRuntimeService) List(context.Context) map[string]*domain.Container { return nil }
func (*durableEffectRuntimeService) ListRoutesWithDetails(context.Context) []domain.RouteInfo {
	return nil
}
func (*durableEffectRuntimeService) ListNetworks(context.Context) ([]*domain.NetworkInfo, error) {
	return nil, nil
}

func TestEventEffectAckFailureRedeliveryAfterWorkerRestartMutatesOnce(t *testing.T) {
	resultPath := t.TempDir() + "/runtime-command-results.json"
	store, err := filesystem.NewRuntimeCommandResultStore(filesystem.RuntimeCommandResultStoreConfig{Path: resultPath})
	require.NoError(t, err)
	runtime := &durableEffectRuntimeService{}
	event := componentEvent(domain.ComponentEventTypeRegistryImagePushed, domain.RegistryImagePushedPayload{Repository: "app", Reference: "v1", Digest: "sha256:abc"})
	event.Origin = domain.ComponentRoleRegistry
	ack := &failOnceAckStore{processed: make(map[string]bool)}
	effect := func(worker *containerusecase.RuntimeWorker) func(context.Context, domain.ComponentEventEnvelope) error {
		return func(ctx context.Context, envelope domain.ComponentEventEnvelope) error {
			_, deployErr := worker.DeployRoute(ctx, domain.DeployRouteCommand{RuntimeCommandIdentity: domain.RuntimeCommandIdentity{ID: "event-command", IdempotencyKey: envelope.DedupeKey(), Generation: 1, SourceComponentID: "control"}, Domain: "app.example", Image: "app:v1"})
			return deployErr
		}
	}
	first := containerusecase.NewRuntimeWorkerWithPolicyAndResultStore(runtime, containerusecase.NewRuntimePolicy(containerusecase.RuntimePolicyModeObserve), store)
	dispatcher := NewEventDispatcher(EventDispatcherOptions{ImagePushedEffect: effect(first), AckStore: ack})
	require.Error(t, dispatcher.HandleComponentEvent(t.Context(), event), "effect succeeded but acknowledgement failed")

	restartedStore, err := filesystem.NewRuntimeCommandResultStore(filesystem.RuntimeCommandResultStoreConfig{Path: resultPath})
	require.NoError(t, err)
	second := containerusecase.NewRuntimeWorkerWithPolicyAndResultStore(runtime, containerusecase.NewRuntimePolicy(containerusecase.RuntimePolicyModeObserve), restartedStore)
	restartedDispatcher := NewEventDispatcher(EventDispatcherOptions{ImagePushedEffect: effect(second), AckStore: ack})
	require.NoError(t, restartedDispatcher.HandleComponentEvent(t.Context(), event))
	require.EqualValues(t, 1, runtime.deploys.Load())
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
func TestEventDispatcherManualIntentSurvivesControlRestart(t *testing.T) {
	store, err := filesystem.NewComponentEventStore(t.TempDir()+"/component-events.json", 16)
	require.NoError(t, err)
	manual := &countingHandler{}
	first := NewEventDispatcher(EventDispatcherOptions{ManualDeploy: []out.EventHandler{manual}, AckStore: store, IntentStore: store, IntentTTL: time.Minute})
	deploy := componentEvent(domain.ComponentEventTypeManualDeploy, domain.ComponentManualDeployPayload{Domain: "app.example", Image: "app:v1", CorrelationID: "manual-1"})
	require.NoError(t, first.HandleComponentEvent(context.Background(), deploy))

	image := &countingHandler{}
	second := NewEventDispatcher(EventDispatcherOptions{ImagePushed: []out.EventHandler{image}, AckStore: store, IntentStore: store, IntentTTL: time.Minute})
	push := componentEvent(domain.ComponentEventTypeRegistryImagePushed, domain.RegistryImagePushedPayload{Repository: "app", Reference: "v1", Digest: "sha256:abc"})
	push.Origin = domain.ComponentRoleRegistry
	push.ID, push.IdempotencyKey = "push-after-restart", "push-after-restart"
	require.NoError(t, second.HandleComponentEvent(context.Background(), push))
	require.EqualValues(t, 1, manual.calls.Load())
	require.Zero(t, image.calls.Load(), "persisted manual intent must suppress the matching retry after restart")
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
