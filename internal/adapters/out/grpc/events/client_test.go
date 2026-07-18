package events

import (
	"context"
	"testing"
	"time"

	eventsv1 "github.com/bnema/gordon/api/gordon/events/v1"
	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type retryService struct {
	calls int
	ids   []string
	err   error
}

func (s *retryService) PublishEvent(_ context.Context, request *eventsv1.PublishEventRequest, _ ...grpc.CallOption) (*eventsv1.PublishEventResponse, error) {
	s.calls++
	s.ids = append(s.ids, request.GetEvent().GetId())
	if s.calls < 3 {
		return nil, s.err
	}
	return &eventsv1.PublishEventResponse{Ack: &eventsv1.EventAck{EventId: request.GetEvent().GetId()}}, nil
}
func (*retryService) WatchEvents(context.Context, *eventsv1.WatchEventsRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[eventsv1.WatchEventsResponse], error) {
	return nil, status.Error(codes.Unimplemented, "not used")
}
func publishEvent() domain.ComponentEventEnvelope {
	return domain.ComponentEventEnvelope{ID: "event-1", Type: domain.ComponentEventTypeRegistryImagePushed, Origin: domain.ComponentRoleRegistry, Timestamp: time.Now(), IdempotencyKey: "push", Payload: domain.RegistryImagePushedPayload{Repository: "app", Reference: "v1", Digest: "sha256:abc"}, AuditClassification: domain.ComponentEventAuditWrite}
}

func TestClientRetriesTransientWithoutChangingIdentity(t *testing.T) {
	service := &retryService{err: status.Error(codes.Unavailable, "temporary")}
	client := NewClientWithEventService(service, WithRetry(time.Millisecond, time.Millisecond, 3))
	client.wait = func(context.Context, time.Duration) bool { return true }
	require.NoError(t, client.PublishComponentEvent(context.Background(), publishEvent()))
	require.Equal(t, 3, service.calls)
	require.Equal(t, []string{"event-1", "event-1", "event-1"}, service.ids)
}
func TestClientStopsOnCancellation(t *testing.T) {
	service := &retryService{err: status.Error(codes.Unavailable, "temporary")}
	client := NewClientWithEventService(service, WithRetry(time.Hour, time.Hour, 3))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, client.PublishComponentEvent(ctx, publishEvent()), context.Canceled)
	require.Zero(t, service.calls)
}
func TestClientDoesNotRetryValidationFailure(t *testing.T) {
	service := &retryService{err: status.Error(codes.InvalidArgument, "bad")}
	client := NewClientWithEventService(service, WithRetry(time.Millisecond, time.Millisecond, 3))
	require.Error(t, client.PublishComponentEvent(context.Background(), publishEvent()))
	require.Equal(t, 1, service.calls)
}
