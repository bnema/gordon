package events

import (
	"context"
	"testing"
	"time"

	eventsv1 "github.com/bnema/gordon/api/gordon/events/v1"
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/testutils/grpctest"
	"github.com/bnema/gordon/internal/usecase/componentevents"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func eventRequest(origin string) *eventsv1.PublishEventRequest {
	return &eventsv1.PublishEventRequest{Event: &eventsv1.EventEnvelope{Id: "event-1", Type: string(domain.ComponentEventTypeRegistryImagePushed), Origin: origin, Timestamp: timestamppb.New(time.Now()), IdempotencyKey: "push-app-v1", AuditClassification: string(domain.ComponentEventAuditWrite), Payload: &eventsv1.EventEnvelope_RegistryImagePushed{RegistryImagePushed: &eventsv1.RegistryImagePushedPayload{Repository: "app", Reference: "v1", Digest: "sha256:abc"}}}}
}

func eventClient(t *testing.T, role domain.ComponentRole, scopes ...domain.ComponentScope) eventsv1.EventServiceClient {
	t.Helper()
	validator := grpctest.NewAuthFixture("test", role, scopes...)
	harness := grpctest.NewHarness(t, func(registrar grpc.ServiceRegistrar) {
		eventsv1.RegisterEventServiceServer(registrar, NewServer(componentevents.NewEventHub(8)))
	}, grpc.UnaryInterceptor(interceptors.ComponentAuthUnaryInterceptor(validator, MethodScopes(), MethodRoles())), grpc.StreamInterceptor(interceptors.ComponentAuthStreamInterceptor(validator, MethodScopes(), MethodRoles())))
	return eventsv1.NewEventServiceClient(harness.AuthenticatedConn(t, grpctest.LocalComponentToken))
}

func TestPublishEventAuthAndDedupe(t *testing.T) {
	client := eventClient(t, domain.ComponentRoleRegistry, domain.ComponentScopeRegistryEventPublish)
	ack, err := client.PublishEvent(context.Background(), eventRequest("registry"))
	require.NoError(t, err)
	require.False(t, ack.GetAck().GetDuplicate())
	ack, err = client.PublishEvent(context.Background(), eventRequest("registry"))
	require.NoError(t, err)
	require.True(t, ack.GetAck().GetDuplicate())

	_, err = client.PublishEvent(context.Background(), eventRequest("runtime"))
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestPublishEventRejectsWrongRoleScopeAndMissingAuth(t *testing.T) {
	for _, tc := range []struct {
		role   domain.ComponentRole
		scopes []domain.ComponentScope
		code   codes.Code
	}{
		{domain.ComponentRoleEdge, []domain.ComponentScope{domain.ComponentScopeEdgeDrain}, codes.PermissionDenied},
		{domain.ComponentRoleRegistry, []domain.ComponentScope{domain.ComponentScopeRegistryStatus}, codes.PermissionDenied},
	} {
		t.Run(string(tc.role), func(t *testing.T) {
			_, err := eventClient(t, tc.role, tc.scopes...).PublishEvent(context.Background(), eventRequest(string(tc.role)))
			require.Equal(t, tc.code, status.Code(err))
		})
	}

	validator := grpctest.NewAuthFixture("registry", domain.ComponentRoleRegistry, domain.ComponentScopeRegistryEventPublish)
	harness := grpctest.NewHarness(t, func(registrar grpc.ServiceRegistrar) {
		eventsv1.RegisterEventServiceServer(registrar, NewServer(componentevents.NewEventHub(8)))
	}, grpc.UnaryInterceptor(interceptors.ComponentAuthUnaryInterceptor(validator, MethodScopes(), MethodRoles())))
	_, err := eventsv1.NewEventServiceClient(harness.Conn(t)).PublishEvent(context.Background(), eventRequest("registry"))
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestEventProtoPreservesImageAutomationInputs(t *testing.T) {
	original := domain.ComponentEventEnvelope{
		ID: "image", Type: domain.ComponentEventTypeRegistryImagePushed, Origin: domain.ComponentRoleRegistry,
		Timestamp: time.Now().UTC(), IdempotencyKey: "push-app-v1", AuditClassification: domain.ComponentEventAuditWrite,
		Payload: domain.RegistryImagePushedPayload{
			Repository: "app", Reference: "v1", Digest: "sha256:abc", Manifest: []byte(`{"schemaVersion":2}`),
			Annotations: map[string]string{"org.opencontainers.image.source": "https://example.test/app"},
		},
	}
	wire, err := EventToProto(original)
	require.NoError(t, err)
	decoded, err := EventFromProto(wire, domain.ComponentRoleRegistry)
	require.NoError(t, err)
	payload, ok := decoded.Payload.(domain.RegistryImagePushedPayload)
	require.True(t, ok)
	require.Equal(t, original.Payload, payload)
}

func TestEventProtoRejectsForbiddenUntypedPayload(t *testing.T) {
	_, err := EventFromProto(&eventsv1.EventEnvelope{Id: "bad", Type: string(domain.ComponentEventTypeRegistryImagePushed), Timestamp: timestamppb.Now(), AuditClassification: string(domain.ComponentEventAuditWrite)}, domain.ComponentRoleRegistry)
	require.Error(t, err)
}
