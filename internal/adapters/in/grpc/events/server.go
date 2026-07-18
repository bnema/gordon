// Package events exposes authenticated typed component events.
package events

import (
	"context"
	"errors"
	"fmt"
	"strings"

	eventsv1 "github.com/bnema/gordon/api/gordon/events/v1"
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/componentevents"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server adapts the typed event wire contract to the control-owned hub.
type Server struct {
	eventsv1.UnimplementedEventServiceServer
	hub     *componentevents.EventHub
	handler out.ComponentEventHandler
}

func NewServer(hub *componentevents.EventHub) *Server { return &Server{hub: hub} }

// NewDispatchingServer acknowledges a delivery only after the control-owned
// dispatcher has completed its effects. This keeps outbox retries effective:
// a failed effect is never hidden by transport-level hub de-duplication.
func NewDispatchingServer(hub *componentevents.EventHub, handler out.ComponentEventHandler) *Server {
	return &Server{hub: hub, handler: handler}
}

// MethodScopes and MethodRoles are consumed by the common component auth
// interceptors. Publish accepts only role-specific publish scopes via its
// sentinel, while Watch is control-only.
func MethodScopes() map[string]domain.ComponentScope {
	return map[string]domain.ComponentScope{
		eventsv1.EventService_PublishEvent_FullMethodName: domain.ComponentScopeAnyEventPublish,
		eventsv1.EventService_WatchEvents_FullMethodName:  domain.ComponentScopeEventsWatch,
	}
}
func MethodRoles() map[string]domain.ComponentRole {
	return map[string]domain.ComponentRole{
		eventsv1.EventService_PublishEvent_FullMethodName: domain.ComponentRoleEventPublisher,
		eventsv1.EventService_WatchEvents_FullMethodName:  domain.ComponentRoleControl,
	}
}

func (s *Server) PublishEvent(ctx context.Context, request *eventsv1.PublishEventRequest) (*eventsv1.PublishEventResponse, error) {
	if s.hub == nil {
		return nil, status.Error(codes.FailedPrecondition, "component event hub not configured")
	}
	identity, ok := interceptors.ComponentIdentityFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "component identity required")
	}
	if request == nil || request.Event == nil {
		return nil, status.Error(codes.InvalidArgument, "event is required")
	}
	claimed := strings.TrimSpace(request.Event.GetOrigin())
	if claimed != "" && claimed != string(identity.Role) {
		return nil, status.Error(codes.PermissionDenied, "claimed event origin does not match authenticated role")
	}
	event, err := EventFromProto(request.Event, identity.Role)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid component event")
	}
	if !roleMayPublish(identity.Role, event.Type) {
		return nil, status.Error(codes.PermissionDenied, "authenticated role may not publish event type")
	}
	if s.handler != nil {
		if err := s.handler.HandleComponentEvent(ctx, event); err != nil {
			return nil, hubError(err)
		}
	}
	ack, err := s.hub.Publish(ctx, event)
	if err != nil {
		return nil, hubError(err)
	}
	return &eventsv1.PublishEventResponse{Ack: &eventsv1.EventAck{EventId: ack.EventID, Duplicate: ack.Duplicate}}, nil
}

func (s *Server) WatchEvents(_ *eventsv1.WatchEventsRequest, stream eventsv1.EventService_WatchEventsServer) error {
	if s.hub == nil {
		return status.Error(codes.FailedPrecondition, "component event hub not configured")
	}
	if _, err := interceptors.RequireComponentRole(stream.Context(), domain.ComponentRoleControl); err != nil {
		return err
	}
	updates, err := s.hub.Subscribe(stream.Context())
	if err != nil {
		return hubError(err)
	}
	for {
		select {
		case <-stream.Context().Done():
			return status.Error(codes.Canceled, stream.Context().Err().Error())
		case event, ok := <-updates:
			if !ok {
				return nil
			}
			message, err := EventToProto(event)
			if err != nil {
				return status.Error(codes.Internal, "invalid component event source data")
			}
			if err := stream.Send(&eventsv1.WatchEventsResponse{Event: message}); err != nil {
				return err
			}
		}
	}
}

func hubError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "component event delivery canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "component event delivery timed out")
	case errors.Is(err, domain.ErrInvalidComponentEvent):
		return status.Error(codes.InvalidArgument, "invalid component event")
	default:
		return status.Error(codes.Internal, "component event delivery failed")
	}
}

func roleMayPublish(role domain.ComponentRole, typ domain.ComponentEventType) bool {
	switch role {
	case domain.ComponentRoleRegistry:
		return typ == domain.ComponentEventTypeRegistryImagePushed
	case domain.ComponentRoleRuntime:
		return typ == domain.ComponentEventTypeRuntimeStateChanged || typ == domain.ComponentEventTypeRuntimeDeploy || typ == domain.ComponentEventTypeContainerDeployed
	case domain.ComponentRoleEdge:
		return typ == domain.ComponentEventTypeEdgeDrain
	case domain.ComponentRoleControl:
		return typ == domain.ComponentEventTypeConfigReload || typ == domain.ComponentEventTypeSecretsChanged || typ == domain.ComponentEventTypeManualDeploy || typ == domain.ComponentEventTypePolicyDenied || typ == domain.ComponentEventTypeAudit
	default:
		return false
	}
}

// EventFromProto validates the selected oneof and derives origin from auth.
func EventFromProto(message *eventsv1.EventEnvelope, origin domain.ComponentRole) (domain.ComponentEventEnvelope, error) {
	if message == nil || message.GetTimestamp() == nil {
		return domain.ComponentEventEnvelope{}, fmt.Errorf("event and timestamp are required")
	}
	if err := message.GetTimestamp().CheckValid(); err != nil {
		return domain.ComponentEventEnvelope{}, fmt.Errorf("timestamp is invalid")
	}
	payload, err := payloadFromProto(message)
	if err != nil {
		return domain.ComponentEventEnvelope{}, err
	}
	event := domain.ComponentEventEnvelope{ID: message.GetId(), Type: domain.ComponentEventType(message.GetType()), Origin: origin, Timestamp: message.GetTimestamp().AsTime(), Generation: message.GetGeneration(), IdempotencyKey: message.GetIdempotencyKey(), Payload: payload, RetryCount: int(message.GetRetryCount()), AuditClassification: domain.ComponentEventAuditClassification(message.GetAuditClassification())}
	if err := event.Validate(); err != nil {
		return domain.ComponentEventEnvelope{}, err
	}
	return event, nil
}

func payloadFromProto(message *eventsv1.EventEnvelope) (domain.ComponentEventPayload, error) {
	if payload := message.GetRegistryImagePushed(); payload != nil {
		return domain.RegistryImagePushedPayload{Repository: payload.GetRepository(), Reference: payload.GetReference(), Digest: payload.GetDigest()}, nil
	}
	if payload := message.GetRuntimeStateChanged(); payload != nil {
		return domain.RuntimeStateChangedPayload{ComponentID: payload.GetComponentId(), State: payload.GetState()}, nil
	}
	if payload := message.GetRuntimeDeploy(); payload != nil {
		return domain.RuntimeDeployPayload{Domain: payload.GetDomain(), Image: payload.GetImage(), Generation: payload.GetGeneration()}, nil
	}
	if payload := message.GetContainerDeployed(); payload != nil {
		return domain.ContainerDeployedPayload{Domain: payload.GetDomain(), Image: payload.GetImage(), DeploymentID: payload.GetDeploymentId(), Generation: payload.GetGeneration()}, nil
	}
	if payload := message.GetConfigReload(); payload != nil {
		return domain.ComponentConfigReloadPayload{Version: payload.GetVersion()}, nil
	}
	if payload := message.GetSecretsChanged(); payload != nil {
		return domain.ComponentSecretsChangedPayload{Version: payload.GetVersion()}, nil
	}
	if payload := message.GetManualDeploy(); payload != nil {
		return domain.ComponentManualDeployPayload{Domain: payload.GetDomain(), Image: payload.GetImage(), CorrelationID: payload.GetCorrelationId()}, nil
	}
	if payload := message.GetPolicyDenied(); payload != nil {
		return domain.PolicyDeniedPayload{DecisionID: payload.GetDecisionId(), Action: payload.GetAction(), Reason: payload.GetReason()}, nil
	}
	if payload := message.GetAudit(); payload != nil {
		return domain.AuditPayload{Action: payload.GetAction(), Subject: payload.GetSubject(), Outcome: payload.GetOutcome()}, nil
	}
	if payload := message.GetEdgeDrain(); payload != nil {
		return domain.EdgeDrainPayload{Domain: payload.GetDomain(), Generation: payload.GetGeneration()}, nil
	}
	return nil, fmt.Errorf("exactly one known typed payload is required")
}

func EventToProto(event domain.ComponentEventEnvelope) (*eventsv1.EventEnvelope, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	if event.RetryCount > int(^uint32(0)) {
		return nil, fmt.Errorf("retry count overflows transport")
	}
	// #nosec G115 -- event.RetryCount is checked against uint32's maximum above.
	retryCount := uint32(event.RetryCount)
	out := &eventsv1.EventEnvelope{Id: event.ID, Type: string(event.Type), Origin: string(event.Origin), Timestamp: timestamppb.New(event.Timestamp), Generation: event.Generation, IdempotencyKey: event.IdempotencyKey, RetryCount: retryCount, AuditClassification: string(event.AuditClassification)}
	switch payload := event.Payload.(type) {
	case domain.RegistryImagePushedPayload:
		out.Payload = &eventsv1.EventEnvelope_RegistryImagePushed{RegistryImagePushed: &eventsv1.RegistryImagePushedPayload{Repository: payload.Repository, Reference: payload.Reference, Digest: payload.Digest}}
	case domain.RuntimeStateChangedPayload:
		out.Payload = &eventsv1.EventEnvelope_RuntimeStateChanged{RuntimeStateChanged: &eventsv1.RuntimeStateChangedPayload{ComponentId: payload.ComponentID, State: payload.State}}
	case domain.RuntimeDeployPayload:
		out.Payload = &eventsv1.EventEnvelope_RuntimeDeploy{RuntimeDeploy: &eventsv1.RuntimeDeployPayload{Domain: payload.Domain, Image: payload.Image, Generation: payload.Generation}}
	case domain.ContainerDeployedPayload:
		out.Payload = &eventsv1.EventEnvelope_ContainerDeployed{ContainerDeployed: &eventsv1.ContainerDeployedPayload{Domain: payload.Domain, Image: payload.Image, DeploymentId: payload.DeploymentID, Generation: payload.Generation}}
	case domain.ComponentConfigReloadPayload:
		out.Payload = &eventsv1.EventEnvelope_ConfigReload{ConfigReload: &eventsv1.ConfigReloadPayload{Version: payload.Version}}
	case domain.ComponentSecretsChangedPayload:
		out.Payload = &eventsv1.EventEnvelope_SecretsChanged{SecretsChanged: &eventsv1.SecretsChangedPayload{Version: payload.Version}}
	case domain.ComponentManualDeployPayload:
		out.Payload = &eventsv1.EventEnvelope_ManualDeploy{ManualDeploy: &eventsv1.ManualDeployPayload{Domain: payload.Domain, Image: payload.Image, CorrelationId: payload.CorrelationID}}
	case domain.PolicyDeniedPayload:
		out.Payload = &eventsv1.EventEnvelope_PolicyDenied{PolicyDenied: &eventsv1.PolicyDeniedPayload{DecisionId: payload.DecisionID, Action: payload.Action, Reason: payload.Reason}}
	case domain.AuditPayload:
		out.Payload = &eventsv1.EventEnvelope_Audit{Audit: &eventsv1.AuditPayload{Action: payload.Action, Subject: payload.Subject, Outcome: payload.Outcome}}
	case domain.EdgeDrainPayload:
		out.Payload = &eventsv1.EventEnvelope_EdgeDrain{EdgeDrain: &eventsv1.EdgeDrainPayload{Domain: payload.Domain, Generation: payload.Generation}}
	default:
		return nil, fmt.Errorf("unknown typed payload")
	}
	return out, nil
}
