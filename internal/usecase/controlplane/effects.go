package controlplane

import (
	"context"
	"fmt"
	"strings"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// RouteCommander is the narrow runtime command facade needed by component
// event effects. In split mode it is implemented by runtimecontrol.Service;
// it deliberately is not a ContainerService or ContainerRuntime.
type RouteCommander interface {
	DeployRoute(context.Context, domain.Route) (domain.RuntimeCommandResult, error)
	ReconcileConfiguredRoutes(context.Context, string) (domain.RuntimeCommandResult, error)
}

// AuditSink records the fixed, non-secret component event metadata that is
// useful to operators. Payloads are intentionally not handed to audit sinks.
type AuditSink interface {
	AuditComponentEvent(context.Context, domain.ComponentEventEnvelope) error
}

// ProductionEffects adapts typed component events to established domain event
// handlers. Image automation deliberately stays in auto.ImagePushDispatcher:
// this adapter must not grow a second, subtly different deploy path.
type ProductionEffects struct {
	imagePushed out.EventHandler
	manual      out.EventHandler
	runtime     RouteCommander
	audit       AuditSink
}

// ImagePushedHandlers runs configured routes before label/tag automation. A
// newly auto-created route must not be rediscovered by the configured handler
// in the same event, which would issue a second runtime deployment. Neither
// action is reimplemented in control-plane code.
type ImagePushedHandlers struct {
	automation out.EventHandler
	configured out.EventHandler
}

func NewImagePushedHandlers(automation, configured out.EventHandler) (*ImagePushedHandlers, error) {
	if automation == nil || configured == nil {
		return nil, fmt.Errorf("image automation and configured-route handlers are required")
	}
	return &ImagePushedHandlers{automation: automation, configured: configured}, nil
}

func (h *ImagePushedHandlers) CanHandle(eventType domain.EventType) bool {
	return eventType == domain.EventImagePushed
}

func (h *ImagePushedHandlers) Handle(ctx context.Context, event domain.Event) error {
	if err := h.configured.Handle(ctx, event); err != nil {
		return err
	}
	return h.automation.Handle(ctx, event)
}

// NewProductionEffects requires existing image and manual handlers from
// wiring, preserving monolith automation while retaining the established
// runtime reconciliation effect for broad configuration changes.
func NewProductionEffects(imagePushed, manual out.EventHandler, runtime RouteCommander, audit AuditSink) (*ProductionEffects, error) {
	if imagePushed == nil {
		return nil, fmt.Errorf("control image-pushed handler is required")
	}
	if manual == nil {
		return nil, fmt.Errorf("control manual-deploy handler is required")
	}
	if runtime == nil {
		return nil, fmt.Errorf("control runtime command facade is required")
	}
	if audit == nil {
		return nil, fmt.Errorf("control audit sink is required")
	}
	return &ProductionEffects{imagePushed: imagePushed, manual: manual, runtime: runtime, audit: audit}, nil
}

func (e *ProductionEffects) ImagePushed(ctx context.Context, event domain.ComponentEventEnvelope) error {
	payload, ok := event.Payload.(domain.RegistryImagePushedPayload)
	if !ok {
		return ErrUnhandledComponentEvent
	}
	if err := e.imagePushed.Handle(withComponentEventDedupeKey(ctx, event.DedupeKey()), imagePushedEvent(event, payload)); err != nil {
		return fmt.Errorf("handle pushed image: %w", err)
	}
	return e.audit.AuditComponentEvent(ctx, event)
}

func (e *ProductionEffects) ConfigReload(ctx context.Context, event domain.ComponentEventEnvelope) error {
	if _, ok := event.Payload.(domain.ComponentConfigReloadPayload); !ok {
		return ErrUnhandledComponentEvent
	}
	if _, err := e.runtime.ReconcileConfiguredRoutes(ctx, "config.reload"); err != nil {
		return fmt.Errorf("reconcile configuration reload: %w", err)
	}
	return e.audit.AuditComponentEvent(ctx, event)
}

func (e *ProductionEffects) ManualDeploy(ctx context.Context, event domain.ComponentEventEnvelope) error {
	payload, ok := event.Payload.(domain.ComponentManualDeployPayload)
	if !ok {
		return ErrUnhandledComponentEvent
	}
	if err := e.manual.Handle(withComponentEventDedupeKey(ctx, event.DedupeKey()), domain.Event{ID: event.ID, Type: domain.EventManualDeploy, Timestamp: event.Timestamp, Route: payload.Domain, Data: &domain.ManualDeployPayload{Domain: payload.Domain}}); err != nil {
		return fmt.Errorf("handle manual deploy: %w", err)
	}
	return e.audit.AuditComponentEvent(ctx, event)
}

func (e *ProductionEffects) SecretsChanged(ctx context.Context, event domain.ComponentEventEnvelope) error {
	if _, ok := event.Payload.(domain.ComponentSecretsChangedPayload); !ok {
		return ErrUnhandledComponentEvent
	}
	// The transport deliberately includes only a revision, not secret names or
	// values. Reconciliation is consequently the only safe broad effect.
	if _, err := e.runtime.ReconcileConfiguredRoutes(ctx, "secrets.changed"); err != nil {
		return fmt.Errorf("reconcile changed secrets: %w", err)
	}
	return e.audit.AuditComponentEvent(ctx, event)
}

func imagePushedEvent(event domain.ComponentEventEnvelope, payload domain.RegistryImagePushedPayload) domain.Event {
	return domain.Event{
		ID: event.ID, Type: domain.EventImagePushed, Timestamp: event.Timestamp,
		ImageName: payload.Repository, Tag: payload.Reference,
		Data: domain.ImagePushedPayload{
			Name: payload.Repository, Reference: payload.Reference,
			Manifest: append([]byte(nil), payload.Manifest...), Annotations: cloneAnnotations(payload.Annotations),
		},
	}
}

func (e *ProductionEffects) RuntimeState(ctx context.Context, event domain.ComponentEventEnvelope) error {
	if _, ok := event.Payload.(domain.RuntimeStateChangedPayload); !ok {
		return ErrUnhandledComponentEvent
	}
	return e.audit.AuditComponentEvent(ctx, event)
}

func (e *ProductionEffects) RuntimeEvent(ctx context.Context, event domain.ComponentEventEnvelope) error {
	switch event.Payload.(type) {
	case domain.RuntimeDeployPayload, domain.ContainerDeployedPayload, domain.EdgeDrainPayload:
		return e.audit.AuditComponentEvent(ctx, event)
	default:
		return ErrUnhandledComponentEvent
	}
}

func (e *ProductionEffects) PolicyAudit(ctx context.Context, event domain.ComponentEventEnvelope) error {
	switch event.Payload.(type) {
	case domain.PolicyDeniedPayload, domain.AuditPayload:
		return e.audit.AuditComponentEvent(ctx, event)
	default:
		return ErrUnhandledComponentEvent
	}
}

// LogAuditSink sends only fixed envelope metadata to the existing structured
// application log. It never serializes typed payloads, which keeps secret
// revisions and future payload additions out of audit logs.
type LogAuditSink struct{}

func NewLogAuditSink() LogAuditSink { return LogAuditSink{} }

func (LogAuditSink) AuditComponentEvent(ctx context.Context, event domain.ComponentEventEnvelope) error {
	log := zerowrap.FromCtx(ctx)
	log.Info().
		Str("component_event_id", strings.TrimSpace(event.ID)).
		Str("component_event_type", string(event.Type)).
		Str("component_origin", string(event.Origin)).
		Str("audit_classification", string(event.AuditClassification)).
		Msg("component event effect completed")
	return nil
}
