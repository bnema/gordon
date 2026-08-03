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
	messages := make(chan *edgev1.WatchRouteSnapshotsResponse, 3)
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

func TestClientReconnectLowerSnapshotStaysUnhealthyAndIdenticalGenerationConfirmsSync(t *testing.T) {
	finishFirstStream := make(chan struct{})
	reconnected := make(chan struct{})
	messages := make(chan *edgev1.WatchRouteSnapshotsResponse, 2)
	server := &scriptedServer{watch: func(stream edgev1.EdgeService_WatchRouteSnapshotsServer, call int) error {
		if call == 0 {
			if err := stream.Send(testSnapshotMessage(t, 2)); err != nil {
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
	assert.False(t, client.Health().Healthy, "a lower snapshot cannot restore health")

	before := client.Health()
	messages <- testSnapshotMessage(t, 2)
	waitFor(t, func() bool { return client.Health().Healthy })
	health := client.Health()
	assert.Equal(t, domain.RouteTargetGeneration(2), health.LastAcceptedGeneration)
	assert.Equal(t, before.LastUpdate, health.LastUpdate, "sync confirmation is not an accepted update")
	client.Stop()
}

func TestClientConflictingEqualGenerationIsUnhealthy(t *testing.T) {
	messages := make(chan *edgev1.WatchRouteSnapshotsResponse, 1)
	server := &scriptedServer{watch: func(stream edgev1.EdgeService_WatchRouteSnapshotsServer, _ int) error {
		if err := stream.Send(testSnapshotMessage(t, 1)); err != nil {
			return err
		}
		message := <-messages
		return stream.Send(message)
	}}
	client := newTestClient(t, server)
	require.NoError(t, client.Start(context.Background()))
	waitFor(t, func() bool { return client.Health().Healthy })
	conflict := testSnapshotMessage(t, 1)
	conflict.Entries[0].TargetHost = "other-target.example"
	messages <- conflict
	waitFor(t, func() bool { return client.Health().ErrorCategory == ErrorInvalid })
	assert.False(t, client.Health().Healthy)
	current, err := client.CurrentSnapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "target.example", current.Entries[0].TargetHost)
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
	mu     sync.Mutex
	calls  int
	watch  func(edgev1.EdgeService_WatchRouteSnapshotsServer, int) error
	report func(context.Context, *edgev1.ReportDrainStateRequest) error
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

func (s *scriptedServer) ReportDrainState(ctx context.Context, request *edgev1.ReportDrainStateRequest) (*edgev1.ReportDrainStateResponse, error) {
	if s.report != nil {
		if err := s.report(ctx, request); err != nil {
			return nil, err
		}
	}
	return &edgev1.ReportDrainStateResponse{}, nil
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

func testSnapshotMessage(t *testing.T, generation domain.RouteTargetGeneration) *edgev1.WatchRouteSnapshotsResponse {
	t.Helper()
	entry, err := domain.NewReadyRouteTargetEntry("app.example.com", "target.example", 8080, "http", domain.RouteTargetProtocolHTTP1, generation)
	require.NoError(t, err)
	return &edgev1.WatchRouteSnapshotsResponse{Generation: uint64(generation), Entries: []*edgev1.RouteTargetEntry{{
		CanonicalDomain: entry.CanonicalDomain, TargetHost: entry.TargetHost, TargetPort: int32(entry.TargetPort), Scheme: entry.Scheme,
		Protocol: string(entry.Protocol), Status: string(entry.Status), UnavailableReason: string(entry.UnavailableReason),
		Generation: uint64(entry.Generation), UpstreamHost: entry.UpstreamHost, Attachment: string(entry.Attachment), TargetKey: string(entry.TargetKey),
	}}}
}

type snapshotObserver struct {
	mu          sync.Mutex
	transitions [][2]domain.RouteTargetGeneration
}

func (o *snapshotObserver) ObserveAcceptedRouteSnapshot(previous *domain.RouteTargetSnapshot, current domain.RouteTargetSnapshot) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var generation domain.RouteTargetGeneration
	if previous != nil {
		generation = previous.Generation
	}
	o.transitions = append(o.transitions, [2]domain.RouteTargetGeneration{generation, current.Generation})
}

func TestClientObserverOnlyReceivesStrictlyNewerSnapshots(t *testing.T) {
	observer := &snapshotObserver{}
	client := NewClientWithEdgeService(nil)
	client.SetSnapshotAcceptanceObserver(observer)
	_, err := client.accept(testSnapshotMessage(t, 1))
	require.NoError(t, err)
	_, err = client.accept(testSnapshotMessage(t, 1))
	require.NoError(t, err)
	_, err = client.accept(testSnapshotMessage(t, 2))
	require.NoError(t, err)
	observer.mu.Lock()
	defer observer.mu.Unlock()
	require.Equal(t, [][2]domain.RouteTargetGeneration{{0, 1}, {1, 2}}, observer.transitions)
}

func TestClientReportDrainStateUsesOpaqueProtocolFields(t *testing.T) {
	var got *edgev1.ReportDrainStateRequest
	server := &scriptedServer{report: func(_ context.Context, request *edgev1.ReportDrainStateRequest) error {
		got = request
		return nil
	}}
	client := newTestClient(t, server)
	key, err := domain.NewRouteTargetKey("rtk_abcdefghijklmnopqrstuvwxyz234567")
	require.NoError(t, err)
	state := domain.RouteDrainState{CanonicalDomain: "app.example.com", TransitionGeneration: 2, OldTargetKey: key, InFlight: 3, TimeoutReason: domain.RouteDrainTimeoutReasonEdge, AcknowledgedAt: time.Now().UTC()}
	require.NoError(t, client.ReportDrainState(t.Context(), state))
	require.NotNil(t, got)
	assert.Equal(t, "app.example.com", got.GetCanonicalDomain())
	assert.Equal(t, string(key), got.GetOldTargetKey())
	assert.Equal(t, uint64(3), got.GetInFlight())
	assert.Equal(t, edgev1.DrainTimeoutReason_DRAIN_TIMEOUT_REASON_EDGE, got.GetTimeoutReason())
	assert.NotNil(t, got.GetAcknowledgedAt())
}

func TestClientEqualSynchronizationResetsReconnectBackoff(t *testing.T) {
	server := &scriptedServer{watch: func(stream edgev1.EdgeService_WatchRouteSnapshotsServer, call int) error {
		switch call {
		case 0, 1:
			if err := stream.Send(testSnapshotMessage(t, 1)); err != nil {
				return err
			}
			return nil
		default:
			return status.Error(codes.Unauthenticated, "stop test")
		}
	}}
	client := NewClient(testHarness(t, server).Conn(t), WithReconnectBackoff(time.Millisecond, 8*time.Millisecond))
	var mu sync.Mutex
	var delays []time.Duration
	client.retryWait = func(_ context.Context, delay time.Duration) bool {
		mu.Lock()
		delays = append(delays, delay)
		mu.Unlock()
		return true
	}
	require.NoError(t, client.Start(t.Context()))
	waitFor(t, func() bool { return client.Health().ErrorCategory == ErrorAuthentication })
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []time.Duration{time.Millisecond, time.Millisecond}, delays)
}

func TestClientCancelDuringRetryExitsPromptly(t *testing.T) {
	server := &scriptedServer{watch: func(edgev1.EdgeService_WatchRouteSnapshotsServer, int) error {
		return status.Error(codes.Unavailable, "temporary")
	}}
	client := NewClient(testHarness(t, server).Conn(t))
	retryStarted := make(chan struct{})
	client.retryWait = func(ctx context.Context, _ time.Duration) bool {
		close(retryStarted)
		<-ctx.Done()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, client.Start(ctx))
	<-retryStarted
	cancel()
	stopped := make(chan struct{})
	go func() { client.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("client stop leaked while waiting to retry")
	}
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
