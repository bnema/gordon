// Package edgesnapshot consumes the control-owned route snapshot stream.
package edgesnapshot

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	edgev1 "github.com/bnema/gordon/api/gordon/edge/v1"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	defaultInitialBackoff = 100 * time.Millisecond
	defaultMaxBackoff     = 5 * time.Second
)

var (
	// ErrSnapshotUnavailable is returned until the first valid snapshot arrives.
	ErrSnapshotUnavailable = out.ErrRouteSnapshotUnavailable
	errWatchAlreadyStarted = errors.New("route snapshot watch is already running")
)

// ErrorCategory is a sanitized stream health error classification.
type ErrorCategory = out.RouteSnapshotErrorCategory

const (
	ErrorNone           = out.RouteSnapshotErrorNone
	ErrorTransport      = out.RouteSnapshotErrorTransport
	ErrorAuthentication = out.RouteSnapshotErrorAuthentication
	ErrorInvalid        = out.RouteSnapshotErrorInvalid
)

// Health is an immutable, sanitized view of the stream state.
type Health = out.RouteSnapshotHealth

// Option configures a Client.
type Option func(*Client)

// WithReconnectBackoff sets the bounded reconnect delays. Invalid values retain
// the safe defaults. It is primarily useful where a local test transport needs
// shorter retry timings.
func WithReconnectBackoff(initial, maximum time.Duration) Option {
	return func(client *Client) {
		if initial > 0 && maximum >= initial {
			client.initialBackoff = initial
			client.maxBackoff = maximum
		}
	}
}

// Client stores the newest validated route snapshot received from control.
// A Client does not own its grpc.ClientConn; the caller is responsible for
// closing that connection after Stop returns.
type Client struct {
	client edgev1.EdgeServiceClient

	mu       sync.RWMutex
	snapshot domain.RouteTargetSnapshot
	hasData  bool
	health   Health

	running bool
	runID   uint64
	cancel  context.CancelFunc
	done    chan struct{}

	initialBackoff time.Duration
	maxBackoff     time.Duration
	retryWait      func(context.Context, time.Duration) bool
	observer       out.RouteSnapshotAcceptanceObserver
}

var (
	_ out.RouteSnapshotProvider       = (*Client)(nil)
	_ out.RouteSnapshotHealthReporter = (*Client)(nil)
	_ out.EdgeDrainReporter           = (*Client)(nil)
)

// NewClient constructs a snapshot client from a gRPC connection. Per-RPC
// credentials belong on conn and are consequently carried by every watch RPC.
func NewClient(conn grpc.ClientConnInterface, options ...Option) *Client {
	return NewClientWithEdgeService(edgev1.NewEdgeServiceClient(conn), options...)
}

// NewClientWithEdgeService constructs a snapshot client from a generated API
// client. It is useful for tests and does not start network activity.
func NewClientWithEdgeService(service edgev1.EdgeServiceClient, options ...Option) *Client {
	client := &Client{
		client:         service,
		initialBackoff: defaultInitialBackoff,
		maxBackoff:     defaultMaxBackoff,
		retryWait:      waitForRetry,
	}
	for _, option := range options {
		if option != nil {
			option(client)
		}
	}
	return client
}

// Start explicitly starts one watch loop. A terminal authentication or invalid
// snapshot failure remains stopped until Start is called again.
func (c *Client) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("route snapshot watch context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.client == nil {
		return errors.New("route snapshot service is required")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return errWatchAlreadyStarted
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.running = true
	c.runID++
	runID := c.runID
	c.cancel = cancel
	c.done = make(chan struct{})
	go c.watch(runCtx, runID, c.done)
	return nil
}

// Stop cancels a running watch and waits for its goroutine to exit.
func (c *Client) Stop() {
	c.mu.RLock()
	cancel, done := c.cancel, c.done
	c.mu.RUnlock()
	if cancel == nil || done == nil {
		return
	}
	cancel()
	<-done
}

// CurrentSnapshot returns an independent clone of the latest accepted snapshot.
func (c *Client) CurrentSnapshot(ctx context.Context) (domain.RouteTargetSnapshot, error) {
	if ctx == nil {
		return domain.RouteTargetSnapshot{}, errors.New("route snapshot context is required")
	}
	if err := ctx.Err(); err != nil {
		return domain.RouteTargetSnapshot{}, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.hasData {
		return domain.RouteTargetSnapshot{}, ErrSnapshotUnavailable
	}
	return c.snapshot.Clone(), nil
}

// SnapshotHealth returns a value copy of the sanitized stream health.
func (c *Client) SnapshotHealth() Health {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.health
}

// Health returns the same immutable health snapshot as SnapshotHealth.
func (c *Client) Health() Health {
	return c.SnapshotHealth()
}

// SetSnapshotAcceptanceObserver installs the edge proxy observer. It is safe
// to call after Start; only snapshots accepted after installation are observed.
func (c *Client) SetSnapshotAcceptanceObserver(observer out.RouteSnapshotAcceptanceObserver) {
	c.mu.Lock()
	c.observer = observer
	c.mu.Unlock()
}

// ReportDrainState sends the narrow opaque drain state over the same
// authenticated gRPC connection that receives snapshots.
// ReportAppliedState reports only a completed, matched edge application. The
// caller supplies the component ID from its immutable container environment;
// control verifies it against the authenticated token identity.
func (c *Client) ReportAppliedState(ctx context.Context, componentID string, routeGeneration, trafficGeneration uint64, healthy bool) error {
	if c == nil || c.client == nil {
		return errors.New("edge snapshot service is required")
	}
	if componentID == "" || routeGeneration == 0 || trafficGeneration == 0 || routeGeneration != trafficGeneration {
		return errors.New("invalid edge applied state")
	}
	if _, err := c.client.ReportAppliedState(ctx, &edgev1.ReportAppliedStateRequest{ComponentId: componentID, RouteGeneration: routeGeneration, TrafficGeneration: trafficGeneration, Healthy: healthy}); err != nil {
		return fmt.Errorf("report edge applied state: %w", err)
	}
	return nil
}

func (c *Client) ReportDrainState(ctx context.Context, state domain.RouteDrainState) error {
	if err := state.Validate(); err != nil {
		return fmt.Errorf("validate edge drain state: %w", err)
	}
	if c.client == nil {
		return errors.New("edge snapshot service is required")
	}
	reason := edgev1.DrainTimeoutReason_DRAIN_TIMEOUT_REASON_UNSPECIFIED
	switch state.TimeoutReason {
	case domain.RouteDrainTimeoutReasonEdge:
		reason = edgev1.DrainTimeoutReason_DRAIN_TIMEOUT_REASON_EDGE
	case domain.RouteDrainTimeoutReasonControl:
		reason = edgev1.DrainTimeoutReason_DRAIN_TIMEOUT_REASON_CONTROL
	}
	request := &edgev1.ReportDrainStateRequest{
		CanonicalDomain:      state.CanonicalDomain,
		TransitionGeneration: uint64(state.TransitionGeneration),
		OldTargetKey:         string(state.OldTargetKey),
		InFlight:             state.InFlight,
		TimeoutReason:        reason,
	}
	if !state.AcknowledgedAt.IsZero() {
		request.AcknowledgedAt = timestamppb.New(state.AcknowledgedAt)
	}
	if _, err := c.client.ReportDrainState(ctx, request); err != nil {
		return fmt.Errorf("report edge drain state: %w", err)
	}
	return nil
}

func (c *Client) watch(ctx context.Context, runID uint64, done chan struct{}) {
	defer func() {
		c.mu.Lock()
		if c.runID == runID {
			c.running = false
			c.cancel = nil
			c.health.Connected = false
			c.health.Healthy = false
		}
		c.mu.Unlock()
		close(done)
	}()

	backoff := c.initialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		stream, err := c.client.WatchRouteSnapshots(ctx, &edgev1.WatchRouteSnapshotsRequest{})
		if err != nil {
			if c.handleWatchError(ctx, err) {
				return
			}
			if !c.retryWait(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff, c.maxBackoff)
			continue
		}

		c.setConnected()
		for {
			message, recvErr := stream.Recv()
			if recvErr != nil {
				if c.handleWatchError(ctx, recvErr) {
					return
				}
				if !c.retryWait(ctx, backoff) {
					return
				}
				backoff = nextBackoff(backoff, c.maxBackoff)
				break
			}
			accepted, err := c.accept(message)
			if err != nil {
				// A malformed routing view is a control-plane contract failure, not
				// a transient transport condition. Do not hot-loop on it.
				c.setError(ErrorInvalid, false)
				return
			}
			if accepted {
				backoff = c.initialBackoff
			}
		}
	}
}

// handleWatchError records sanitized state and reports whether the loop must
// stop. Authentication failures are terminal so a rejected credential never
// causes a hot retry loop.
func (c *Client) handleWatchError(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	if isAuthenticationError(err) {
		c.setError(ErrorAuthentication, false)
		return true
	}
	c.setError(ErrorTransport, false)
	return false
}

func (c *Client) setConnected() {
	c.mu.Lock()
	defer c.mu.Unlock()
	// A transport reconnect only establishes the stream. The edge is healthy
	// again after this stream confirms the existing view or supplies a newer one.
	c.health.Connected = true
	c.health.Healthy = false
	c.health.ErrorCategory = ErrorNone
}

func (c *Client) setError(category ErrorCategory, connected bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.health.Connected = connected
	c.health.Healthy = false
	c.health.ErrorCategory = category
}

func (c *Client) accept(message *edgev1.RouteTargetSnapshot) (bool, error) {
	snapshot, err := routeSnapshotFromProto(message)
	if err != nil {
		return false, err
	}

	var previous *domain.RouteTargetSnapshot
	var observer out.RouteSnapshotAcceptanceObserver
	c.mu.Lock()
	if c.hasData {
		switch {
		case snapshot.Generation < c.snapshot.Generation:
			// A lower generation cannot establish that a reconnected stream is
			// synchronized with our current routing view.
			c.mu.Unlock()
			return false, nil
		case snapshot.Generation == c.snapshot.Generation:
			if !reflect.DeepEqual(snapshot, c.snapshot) {
				c.mu.Unlock()
				return false, fmt.Errorf("conflicting route snapshot generation %d", snapshot.Generation)
			}
			// A byte-for-byte equivalent routing view is reconnect synchronization,
			// not a transition, so it must never duplicate drain reporting.
			c.health.Healthy = true
			c.health.Connected = true
			c.health.ErrorCategory = ErrorNone
			c.mu.Unlock()
			return true, nil
		}
		prior := c.snapshot.Clone()
		previous = &prior
	}
	c.snapshot = snapshot.Clone()
	c.hasData = true
	c.health = Health{
		Healthy:                true,
		Connected:              true,
		LastAcceptedGeneration: snapshot.Generation,
		LastUpdate:             time.Now().UTC(),
	}
	observer = c.observer
	c.mu.Unlock()
	if observer != nil {
		observer.ObserveAcceptedRouteSnapshot(previous, snapshot.Clone())
	}
	return true, nil
}

func routeSnapshotFromProto(message *edgev1.RouteTargetSnapshot) (domain.RouteTargetSnapshot, error) {
	if message == nil {
		return domain.RouteTargetSnapshot{}, errors.New("route snapshot is required")
	}
	snapshot := domain.RouteTargetSnapshot{Generation: domain.RouteTargetGeneration(message.GetGeneration())}
	snapshot.Entries = make([]domain.RouteTargetEntry, 0, len(message.GetEntries()))
	for index, entry := range message.GetEntries() {
		converted, err := routeTargetEntryFromProto(entry)
		if err != nil {
			return domain.RouteTargetSnapshot{}, fmt.Errorf("route snapshot entry %d: %w", index, err)
		}
		snapshot.Entries = append(snapshot.Entries, converted)
	}
	if message.GetRegistryForwardingTarget() != nil {
		entry, err := routeTargetEntryFromProto(message.GetRegistryForwardingTarget())
		if err != nil {
			return domain.RouteTargetSnapshot{}, fmt.Errorf("registry forwarding target: %w", err)
		}
		snapshot.RegistryForwardingTarget = &entry
	}
	if err := snapshot.ValidateSplitReachability(); err != nil {
		return domain.RouteTargetSnapshot{}, fmt.Errorf("validate split route snapshot: %w", err)
	}
	return snapshot, nil
}

func routeTargetEntryFromProto(message *edgev1.RouteTargetEntry) (domain.RouteTargetEntry, error) {
	if message == nil {
		return domain.RouteTargetEntry{}, errors.New("route target entry is required")
	}
	if message.GetTargetPort() < 0 {
		return domain.RouteTargetEntry{}, errors.New("route target port is invalid")
	}
	entry := domain.RouteTargetEntry{
		CanonicalDomain:   message.GetCanonicalDomain(),
		TargetHost:        message.GetTargetHost(),
		TargetPort:        int(message.GetTargetPort()),
		Scheme:            message.GetScheme(),
		Protocol:          domain.RouteTargetProtocol(message.GetProtocol()),
		Status:            domain.RouteTargetStatus(message.GetStatus()),
		UnavailableReason: domain.RouteTargetUnavailableReason(message.GetUnavailableReason()),
		Generation:        domain.RouteTargetGeneration(message.GetGeneration()),
		UpstreamHost:      message.GetUpstreamHost(),
		Attachment:        domain.RouteTargetAttachment(message.GetAttachment()),
		TargetKey:         domain.RouteTargetKey(message.GetTargetKey()),
	}
	if err := entry.Validate(); err != nil {
		return domain.RouteTargetEntry{}, err
	}
	return entry, nil
}

func isAuthenticationError(err error) bool {
	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied:
		return true
	default:
		return false
	}
}

func waitForRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current, maximum time.Duration) time.Duration {
	if current >= maximum {
		return maximum
	}
	if current > maximum/2 {
		return maximum
	}
	return current * 2
}
