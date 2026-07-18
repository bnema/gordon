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

var ErrUnhandledComponentEvent = errors.New("component event has no control-plane route")

// EventDispatcherOptions deliberately accepts existing domain-event handlers.
// It is an adapter only: deployment, routing, preview, and proxy decisions stay
// in their established handlers rather than being reimplemented here.
type EventDispatcherOptions struct {
	ImagePushed  []out.EventHandler
	ConfigReload []out.EventHandler
	ManualDeploy []out.EventHandler
	Secrets      []out.EventHandler
	RuntimeState func(context.Context, domain.ComponentEventEnvelope) error
	RuntimeEvent func(context.Context, domain.ComponentEventEnvelope) error
	PolicyAudit  func(context.Context, domain.ComponentEventEnvelope) error
	AckStore     out.ComponentEventAckStore
	Capacity     int
	IntentTTL    time.Duration
}

// EventDispatcher validates typed envelopes, serializes equal dedupe keys, and
// records success only after all effects complete. A failed delivery can thus
// be retried by the registry outbox or component client.
type EventDispatcher struct {
	opts     EventDispatcherOptions
	mu       sync.Mutex
	complete map[string]*list.Element
	lru      *list.List
	flights  map[string]*eventFlight
	intents  map[string]manualIntent
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
	key := event.DedupeKey()
	completed, err := d.isCompleted(ctx, key)
	if err != nil {
		return err
	}
	if completed {
		return nil
	}

	d.mu.Lock()
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
		if d.consumeManualIntent(imageReference(payload.Repository, payload.Reference)) {
			return nil
		}
		return runLegacyHandlers(ctx, d.opts.ImagePushed, domain.Event{ID: event.ID, Type: domain.EventImagePushed, Timestamp: event.Timestamp, ImageName: payload.Repository, Tag: payload.Reference, Data: domain.ImagePushedPayload{Name: payload.Repository, Reference: payload.Reference}})
	case domain.ComponentConfigReloadPayload:
		return runLegacyHandlers(ctx, d.opts.ConfigReload, domain.Event{ID: event.ID, Type: domain.EventConfigReload, Timestamp: event.Timestamp, Data: domain.ConfigReloadPayload{Source: payload.Version}})
	case domain.ComponentManualDeployPayload:
		if err := runLegacyHandlers(ctx, d.opts.ManualDeploy, domain.Event{ID: event.ID, Type: domain.EventManualDeploy, Timestamp: event.Timestamp, Route: payload.Domain, Data: &domain.ManualDeployPayload{Domain: payload.Domain}}); err != nil {
			return err
		}
		d.rememberManualIntent(imageReferenceFromImage(payload.Image))
		return nil
	case domain.ComponentSecretsChangedPayload:
		return runLegacyHandlers(ctx, d.opts.Secrets, domain.Event{ID: event.ID, Type: domain.EventSecretsChanged, Timestamp: event.Timestamp, Data: domain.SecretsChangedPayload{Operation: payload.Version}})
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
		return nil
	}
	return d.opts.RuntimeState(ctx, event)
}
func (d *EventDispatcher) runRuntime(ctx context.Context, event domain.ComponentEventEnvelope) error {
	if d.opts.RuntimeEvent == nil {
		return nil
	}
	return d.opts.RuntimeEvent(ctx, event)
}
func (d *EventDispatcher) runPolicyAudit(ctx context.Context, event domain.ComponentEventEnvelope) error {
	if d.opts.PolicyAudit == nil {
		return nil
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
func (d *EventDispatcher) rememberManualIntent(image string) {
	if image == "" {
		return
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
}
func (d *EventDispatcher) consumeManualIntent(image string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pruneIntentsLocked(time.Now())
	if _, ok := d.intents[image]; ok {
		delete(d.intents, image)
		return true
	}
	return false
}
func (d *EventDispatcher) pruneIntentsLocked(now time.Time) {
	for image, intent := range d.intents {
		if !intent.expires.After(now) {
			delete(d.intents, image)
		}
	}
}
