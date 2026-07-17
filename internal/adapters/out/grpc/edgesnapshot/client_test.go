package edgesnapshot

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	edgev1 "github.com/bnema/gordon/api/gordon/edge/v1"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/testutils/grpctest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestClientInitialCurrentIsUnavailable(t *testing.T) {
	client := NewClientWithEdgeService(edgev1.NewEdgeServiceClient(testHarness(t, &scriptedServer{}).Conn(t)))

	_, err := client.CurrentSnapshot(context.Background())
	assert.ErrorIs(t, err, ErrSnapshotUnavailable)
	assert.False(t, client.Health().Healthy)
	assert.False(t, client.Health().Connected)
}

func TestClientAcceptsNewerIgnoresStaleAndClones(t *testing.T) {
	messages := make(chan *edgev1.RouteTargetSnapshot, 3)
	server := &scriptedServer{watch: func(stream edgev1.EdgeService_WatchRouteSnapshotsServer, _ int) error {
		for {
			select {
			case message := <-messages:
				if err := stream.Send(message); err != nil {
					return err
				}
			case <-stream.Context().Done():
				return stream.Context().Err()
			}
		}
	}}
	client := newTestClient(t, server)
	require.NoError(t, client.Start(context.Background()))
	messages <- testSnapshotMessage(t, 3)
	waitFor(t, func() bool { return client.Health().LastAcceptedGeneration == 3 })
	messages <- testSnapshotMessage(t, 2)
	messages <- testSnapshotMessage(t, 4)
	waitFor(t, func() bool { return client.Health().LastAcceptedGeneration == 4 })

	got, err := client.CurrentSnapshot(context.Background())
	require.NoError(t, err)
	got.Entries[0].TargetHost = "mutated.example"
	again, err := client.CurrentSnapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "target.example", again.Entries[0].TargetHost)
	assert.True(t, client.Health().Healthy)
	assert.True(t, client.Health().Connected)
	client.Stop()
}

func TestClientReconnectRequiresNewerValidSnapshotForHealth(t *testing.T) {
	finishFirstStream := make(chan struct{})
	reconnected := make(chan struct{})
	messages := make(chan *edgev1.RouteTargetSnapshot, 2)
	server := &scriptedServer{watch: func(stream edgev1.EdgeService_WatchRouteSnapshotsServer, call int) error {
		if call == 0 {
			if err := stream.Send(testSnapshotMessage(t, 1)); err != nil {
				return err
			}
			select {
			case <-finishFirstStream:
				return nil
			case <-stream.Context().Done():
				return stream.Context().Err()
			}
		}
		if call == 1 {
			close(reconnected)
		}
		select {
		case message := <-messages:
			if err := stream.Send(message); err != nil {
				return err
			}
			if message.GetGeneration() == 1 {
				return nil
			}
			<-stream.Context().Done()
			return stream.Context().Err()
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}}
	client := newTestClient(t, server)
	require.NoError(t, client.Start(context.Background()))
	waitFor(t, func() bool { return client.Health().Healthy })
	close(finishFirstStream)

	select {
	case <-reconnected:
	case <-time.After(time.Second):
		require.Fail(t, "client did not reconnect")
	}
	waitFor(t, func() bool { return client.Health().Connected })
	assert.False(t, client.Health().Healthy, "a reconnected stream needs a new snapshot")

	messages <- testSnapshotMessage(t, 1)
	waitFor(t, func() bool { return server.CallCount() >= 3 && client.Health().Connected })
	assert.False(t, client.Health().Healthy, "a stale snapshot cannot restore health")

	messages <- testSnapshotMessage(t, 2)
	waitFor(t, func() bool {
		health := client.Health()
		return health.Healthy && health.LastAcceptedGeneration == 2
	})
	client.Stop()
}

func TestClientAuthenticationFailureIsTerminalAndSanitized(t *testing.T) {
	server := &scriptedServer{watch: func(edgev1.EdgeService_WatchRouteSnapshotsServer, int) error {
		return status.Error(codes.Unauthenticated, "Bearer deliberately-not-safe-to-expose")
	}}
	client := newTestClient(t, server)
	require.NoError(t, client.Start(context.Background()))
	waitFor(t, func() bool { return client.Health().ErrorCategory == ErrorAuthentication })
	time.Sleep(30 * time.Millisecond)
	health := client.Health()
	assert.False(t, health.Healthy)
	assert.False(t, health.Connected)
	assert.Equal(t, 1, server.CallCount())
	assert.NotContains(t, string(health.ErrorCategory), "Bearer")
	client.Stop()
}

func TestClientCancellationStopsAndConcurrentReadersAreSafe(t *testing.T) {
	server := &scriptedServer{watch: func(stream edgev1.EdgeService_WatchRouteSnapshotsServer, _ int) error {
		if err := stream.Send(testSnapshotMessage(t, 1)); err != nil {
			return err
		}
		<-stream.Context().Done()
		return stream.Context().Err()
	}}
	client := newTestClient(t, server)
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, client.Start(ctx))
	waitFor(t, func() bool { return client.Health().Healthy })

	var readers sync.WaitGroup
	for range 20 {
		readers.Go(func() {
			for range 100 {
				_, _ = client.CurrentSnapshot(context.Background())
				_ = client.SnapshotHealth()
			}
		})
	}
	readers.Wait()
	cancel()
	client.Stop()
	assert.False(t, client.Health().Connected)
}

func TestClientRejectsSplitUnreachableSnapshot(t *testing.T) {
	server := &scriptedServer{watch: func(stream edgev1.EdgeService_WatchRouteSnapshotsServer, _ int) error {
		message := testSnapshotMessage(t, 1)
		message.Entries[0].TargetHost = "localhost"
		require.NoError(t, stream.Send(message))
		<-stream.Context().Done()
		return stream.Context().Err()
	}}
	client := newTestClient(t, server)
	require.NoError(t, client.Start(context.Background()))
	waitFor(t, func() bool { return client.Health().ErrorCategory == ErrorInvalid })
	_, err := client.CurrentSnapshot(context.Background())
	assert.True(t, errors.Is(err, ErrSnapshotUnavailable))
	assert.False(t, client.Health().Healthy)
	client.Stop()
}

type scriptedServer struct {
	edgev1.UnimplementedEdgeServiceServer
	mu    sync.Mutex
	calls int
	watch func(edgev1.EdgeService_WatchRouteSnapshotsServer, int) error
}

func (s *scriptedServer) WatchRouteSnapshots(_ *edgev1.WatchRouteSnapshotsRequest, stream edgev1.EdgeService_WatchRouteSnapshotsServer) error {
	s.mu.Lock()
	call := s.calls
	s.calls++
	s.mu.Unlock()
	if s.watch == nil {
		<-stream.Context().Done()
		return stream.Context().Err()
	}
	return s.watch(stream, call)
}

func (s *scriptedServer) CallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newTestClient(t *testing.T, server edgev1.EdgeServiceServer) *Client {
	t.Helper()
	return NewClient(testHarness(t, server).Conn(t), WithReconnectBackoff(time.Millisecond, 8*time.Millisecond))
}

func testHarness(t *testing.T, server edgev1.EdgeServiceServer) *grpctest.Harness {
	t.Helper()
	return grpctest.NewHarness(t, func(registrar grpc.ServiceRegistrar) {
		edgev1.RegisterEdgeServiceServer(registrar, server)
	})
}

func testSnapshotMessage(t *testing.T, generation domain.RouteTargetGeneration) *edgev1.RouteTargetSnapshot {
	t.Helper()
	entry, err := domain.NewReadyRouteTargetEntry("app.example.com", "target.example", 8080, "http", domain.RouteTargetProtocolHTTP1, generation)
	require.NoError(t, err)
	return &edgev1.RouteTargetSnapshot{Generation: uint64(generation), Entries: []*edgev1.RouteTargetEntry{{
		CanonicalDomain: entry.CanonicalDomain, TargetHost: entry.TargetHost, TargetPort: int32(entry.TargetPort), Scheme: entry.Scheme,
		Protocol: string(entry.Protocol), Status: string(entry.Status), UnavailableReason: string(entry.UnavailableReason),
		Generation: uint64(entry.Generation), UpstreamHost: entry.UpstreamHost, Attachment: string(entry.Attachment), TargetKey: string(entry.TargetKey),
	}}}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	require.Fail(t, "condition was not met before timeout")
}
