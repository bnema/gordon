package controlplane

import (
	"context"
	"fmt"
	"strings"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
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

// ProductionEffects adapts typed component events to existing control
// decisions and the narrow runtime command facade.
type ProductionEffects struct {
	config  in.ConfigService
	runtime RouteCommander
	audit   AuditSink
}

func NewProductionEffects(config in.ConfigService, runtime RouteCommander, audit AuditSink) (*ProductionEffects, error) {
	if config == nil {
		return nil, fmt.Errorf("control config service is required")
	}
	if runtime == nil {
		return nil, fmt.Errorf("control runtime command facade is required")
	}
	if audit == nil {
		return nil, fmt.Errorf("control audit sink is required")
	}
	return &ProductionEffects{config: config, runtime: runtime, audit: audit}, nil
}

func (e *ProductionEffects) ImagePushed(ctx context.Context, event domain.ComponentEventEnvelope) error {
	payload, ok := event.Payload.(domain.RegistryImagePushedPayload)
	if !ok {
		return ErrUnhandledComponentEvent
	}
	image := imageReference(payload.Repository, payload.Reference)
	for _, route := range e.config.FindRoutesByImage(ctx, image) {
		if _, err := e.runtime.DeployRoute(domain.WithInternalDeploy(ctx), route); err != nil {
			return fmt.Errorf("deploy route %q for pushed image: %w", route.Domain, err)
		}
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
	route, err := e.config.GetRoute(ctx, payload.Domain)
	if err != nil {
		return fmt.Errorf("get manual deploy route %q: %w", payload.Domain, err)
	}
	if route == nil {
		return fmt.Errorf("manual deploy route %q not found", payload.Domain)
	}
	if _, err := e.runtime.DeployRoute(domain.WithInternalDeploy(ctx), *route); err != nil {
		return fmt.Errorf("deploy manual route %q: %w", payload.Domain, err)
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
