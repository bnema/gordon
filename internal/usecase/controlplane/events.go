// Package controlplane owns control-plane decisions made from component events.
package controlplane

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

var (
	ErrUnhandledComponentEvent = errors.New("component event has no control-plane route")
	ErrEffectNotConfigured     = errors.New("component event effect is not configured")
)

// ManualIntentStore preserves manual-deploy correlation across a control
// restart. Implementations must store only image references and expiry times.
type ManualIntentStore interface {
	LoadManualDeploymentIntents(context.Context) (map[string]time.Time, error)
	SaveManualDeploymentIntents(context.Context, map[string]time.Time) error
}

// EventDispatcherOptions deliberately accepts existing domain-event handlers.
// It is an adapter only: deployment, routing, preview, and proxy decisions stay
// in their established handlers rather than being reimplemented here.
type EventDispatcherOptions struct {
	ImagePushed  []out.EventHandler
	ConfigReload []out.EventHandler
	ManualDeploy []out.EventHandler
	Secrets      []out.EventHandler
	// Typed effects preserve the transport envelope for production decisions.
	// Legacy handlers remain supported through the domain.Event adapter below.
	ImagePushedEffect  func(context.Context, domain.ComponentEventEnvelope) error
	ConfigReloadEffect func(context.Context, domain.ComponentEventEnvelope) error
	ManualDeployEffect func(context.Context, domain.ComponentEventEnvelope) error
	SecretsEffect      func(context.Context, domain.ComponentEventEnvelope) error
	RuntimeState       func(context.Context, domain.ComponentEventEnvelope) error
	RuntimeEvent       func(context.Context, domain.ComponentEventEnvelope) error
	PolicyAudit        func(context.Context, domain.ComponentEventEnvelope) error
	AckStore           out.ComponentEventAckStore
	IntentStore        ManualIntentStore
	Capacity           int
	IntentTTL          time.Duration
}

// EventDispatcher validates typed envelopes, serializes equal dedupe keys, and
// records success only after all effects complete. A failed delivery can thus
// be retried by the registry outbox or component client.
type EventDispatcher struct {
	opts         EventDispatcherOptions
	mu           sync.Mutex
	complete     map[string]*list.Element
	lru          *list.List
	flights      map[string]*eventFlight
	intents      map[string]manualIntent
	intentsOnce  sync.Once
	intentsError error
	// afterCompletedCheck is a test-only barrier invoked after the unlocked
	// preflight completion check and before flight inspection/creation.
	afterCompletedCheck func()
}
type completedEvent struct {
	key string
	at  time.Time
}
type eventFlight struct {
	done chan struct{}
	err  error
}
type manualIntent struct{ expires time.Time }

func NewEventDispatcher(opts EventDispatcherOptions) *EventDispatcher {
	if opts.Capacity <= 0 {
		opts.Capacity = 1024
	}
	if opts.IntentTTL <= 0 {
		opts.IntentTTL = 5 * time.Minute
	}
	return &EventDispatcher{opts: opts, complete: make(map[string]*list.Element), lru: list.New(), flights: make(map[string]*eventFlight), intents: make(map[string]manualIntent)}
}

// HandleComponentEvent implements the component-event inbound boundary.
func (d *EventDispatcher) HandleComponentEvent(ctx context.Context, event domain.ComponentEventEnvelope) error {
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate component event: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.loadIntents(ctx); err != nil {
		return err
	}
	key := event.DedupeKey()
	completed, err := d.isCompleted(ctx, key)
	if err != nil {
		return err
	}
	if completed {
		return nil
	}
	if d.afterCompletedCheck != nil {
		d.afterCompletedCheck()
	}

	d.mu.Lock()
	if e := d.complete[key]; e != nil {
		d.lru.MoveToFront(e)
		d.mu.Unlock()
		return nil
	}
	if flight := d.flights[key]; flight != nil {
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-flight.done:
			return flight.err
		}
	}
	flight := &eventFlight{done: make(chan struct{})}
	d.flights[key] = flight
	d.mu.Unlock()

	err = d.dispatch(ctx, event)
	if err == nil {
		err = d.markCompleted(ctx, key)
	}
	d.mu.Lock()
	flight.err = err
	delete(d.flights, key)
	close(flight.done)
	d.mu.Unlock()
	return err
}

func (d *EventDispatcher) isCompleted(ctx context.Context, key string) (bool, error) {
	d.mu.Lock()
	if e := d.complete[key]; e != nil {
		d.lru.MoveToFront(e)
		d.mu.Unlock()
		return true, nil
	}
	d.mu.Unlock()
	if d.opts.AckStore == nil {
		return false, nil
	}
	processed, err := d.opts.AckStore.IsComponentEventProcessed(ctx, key)
	if err != nil {
		return false, fmt.Errorf("read component event completion: %w", err)
	}
	return processed, nil
}
func (d *EventDispatcher) markCompleted(ctx context.Context, key string) error {
	if d.opts.AckStore != nil {
		if err := d.opts.AckStore.MarkComponentEventProcessed(ctx, key, time.Now().UTC()); err != nil {
			return fmt.Errorf("record component event completion: %w", err)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if e := d.complete[key]; e != nil {
		d.lru.MoveToFront(e)
		return nil
	}
	d.complete[key] = d.lru.PushFront(completedEvent{key: key, at: time.Now().UTC()})
	if d.lru.Len() > d.opts.Capacity {
		old := d.lru.Back()
		delete(d.complete, old.Value.(completedEvent).key)
		d.lru.Remove(old)
	}
	return nil
}

func (d *EventDispatcher) dispatch(ctx context.Context, event domain.ComponentEventEnvelope) error {
	switch payload := event.Payload.(type) {
	case domain.RegistryImagePushedPayload:
		return d.dispatchImagePushed(ctx, event, payload)
	case domain.ComponentConfigReloadPayload:
		return d.dispatchConfigReload(ctx, event, payload)
	case domain.ComponentManualDeployPayload:
		return d.dispatchManualDeploy(ctx, event, payload)
	case domain.ComponentSecretsChangedPayload:
		return d.dispatchSecretsChanged(ctx, event, payload)
	case domain.RuntimeStateChangedPayload:
		return d.runState(ctx, event)
	case domain.RuntimeDeployPayload, domain.ContainerDeployedPayload, domain.EdgeDrainPayload:
		return d.runRuntime(ctx, event)
	case domain.PolicyDeniedPayload, domain.AuditPayload:
		return d.runPolicyAudit(ctx, event)
	default:
		return ErrUnhandledComponentEvent
	}
}

func (d *EventDispatcher) dispatchImagePushed(ctx context.Context, event domain.ComponentEventEnvelope, payload domain.RegistryImagePushedPayload) error {
	suppressed, err := d.consumeManualIntent(ctx, imageReference(payload.Repository, payload.Reference))
	if err != nil || suppressed {
		return err
	}
	if d.opts.ImagePushedEffect != nil {
		return d.opts.ImagePushedEffect(ctx, event)
	}
	return runLegacyHandlers(ctx, d.opts.ImagePushed, domain.Event{ID: event.ID, Type: domain.EventImagePushed, Timestamp: event.Timestamp, ImageName: payload.Repository, Tag: payload.Reference, Data: domain.ImagePushedPayload{Name: payload.Repository, Reference: payload.Reference, Manifest: append([]byte(nil), payload.Manifest...), Annotations: cloneAnnotations(payload.Annotations)}})
}

func (d *EventDispatcher) dispatchConfigReload(ctx context.Context, event domain.ComponentEventEnvelope, payload domain.ComponentConfigReloadPayload) error {
	if d.opts.ConfigReloadEffect != nil {
		return d.opts.ConfigReloadEffect(ctx, event)
	}
	return runLegacyHandlers(ctx, d.opts.ConfigReload, domain.Event{ID: event.ID, Type: domain.EventConfigReload, Timestamp: event.Timestamp, Data: domain.ConfigReloadPayload{Source: payload.Version}})
}

func (d *EventDispatcher) dispatchManualDeploy(ctx context.Context, event domain.ComponentEventEnvelope, payload domain.ComponentManualDeployPayload) error {
	var err error
	if d.opts.ManualDeployEffect != nil {
		err = d.opts.ManualDeployEffect(ctx, event)
	} else {
		err = runLegacyHandlers(ctx, d.opts.ManualDeploy, domain.Event{ID: event.ID, Type: domain.EventManualDeploy, Timestamp: event.Timestamp, Route: payload.Domain, Data: &domain.ManualDeployPayload{Domain: payload.Domain}})
	}
	if err != nil {
		return err
	}
	return d.rememberManualIntent(ctx, imageReferenceFromImage(payload.Image))
}

func (d *EventDispatcher) dispatchSecretsChanged(ctx context.Context, event domain.ComponentEventEnvelope, payload domain.ComponentSecretsChangedPayload) error {
	if d.opts.SecretsEffect != nil {
		return d.opts.SecretsEffect(ctx, event)
	}
	return runLegacyHandlers(ctx, d.opts.Secrets, domain.Event{ID: event.ID, Type: domain.EventSecretsChanged, Timestamp: event.Timestamp, Data: domain.SecretsChangedPayload{Operation: payload.Version}})
}
func runLegacyHandlers(ctx context.Context, handlers []out.EventHandler, event domain.Event) error {
	for _, handler := range handlers {
		if handler != nil && handler.CanHandle(event.Type) {
			if err := handler.Handle(ctx, event); err != nil {
				return err
			}
		}
	}
	return nil
}
func (d *EventDispatcher) runState(ctx context.Context, event domain.ComponentEventEnvelope) error {
	if d.opts.RuntimeState == nil {
		return ErrEffectNotConfigured
	}
	return d.opts.RuntimeState(ctx, event)
}
func (d *EventDispatcher) runRuntime(ctx context.Context, event domain.ComponentEventEnvelope) error {
	if d.opts.RuntimeEvent == nil {
		return ErrEffectNotConfigured
	}
	return d.opts.RuntimeEvent(ctx, event)
}
func (d *EventDispatcher) runPolicyAudit(ctx context.Context, event domain.ComponentEventEnvelope) error {
	if d.opts.PolicyAudit == nil {
		return ErrEffectNotConfigured
	}
	return d.opts.PolicyAudit(ctx, event)
}

func imageReference(repository, reference string) string {
	if strings.TrimSpace(reference) == "" {
		reference = "latest"
	}
	return strings.TrimSpace(repository) + ":" + strings.TrimSpace(reference)
}
func imageReferenceFromImage(image string) string { return strings.TrimSpace(image) }
func (d *EventDispatcher) rememberManualIntent(ctx context.Context, image string) error {
	if image == "" {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pruneIntentsLocked(time.Now())
	if _, exists := d.intents[image]; !exists && len(d.intents) >= d.opts.Capacity {
		discard := ""
		var oldest time.Time
		for candidate, intent := range d.intents {
			if discard == "" || intent.expires.Before(oldest) {
				discard, oldest = candidate, intent.expires
			}
		}
		delete(d.intents, discard)
	}
	d.intents[image] = manualIntent{expires: time.Now().Add(d.opts.IntentTTL)}
	return d.saveIntentsLocked(ctx)
}
func (d *EventDispatcher) consumeManualIntent(ctx context.Context, image string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pruneIntentsLocked(time.Now())
	if _, ok := d.intents[image]; ok {
		delete(d.intents, image)
		if err := d.saveIntentsLocked(ctx); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}
func (d *EventDispatcher) pruneIntentsLocked(now time.Time) {
	for image, intent := range d.intents {
		if !intent.expires.After(now) {
			delete(d.intents, image)
		}
	}
}

func (d *EventDispatcher) loadIntents(ctx context.Context) error {
	if d.opts.IntentStore == nil {
		return nil
	}
	d.intentsOnce.Do(func() {
		intents, err := d.opts.IntentStore.LoadManualDeploymentIntents(ctx)
		if err != nil {
			d.intentsError = fmt.Errorf("load manual deployment intents: %w", err)
			return
		}
		now := time.Now()
		d.mu.Lock()
		defer d.mu.Unlock()
		for image, expires := range intents {
			if expires.After(now) {
				d.intents[image] = manualIntent{expires: expires}
			}
		}
	})
	return d.intentsError
}

func (d *EventDispatcher) saveIntentsLocked(ctx context.Context) error {
	if d.opts.IntentStore == nil {
		return nil
	}
	intents := make(map[string]time.Time, len(d.intents))
	for image, intent := range d.intents {
		intents[image] = intent.expires.UTC()
	}
	if err := d.opts.IntentStore.SaveManualDeploymentIntents(ctx, intents); err != nil {
		return fmt.Errorf("save manual deployment intents: %w", err)
	}
	return nil
}

func cloneAnnotations(annotations map[string]string) map[string]string {
	if len(annotations) == 0 {
		return nil
	}
	copy := make(map[string]string, len(annotations))
	for key, value := range annotations {
		copy[key] = value
	}
	return copy
}
