// Package edgesnapshot adapts control-owned route snapshots to the edge gRPC API.
package edgesnapshot

import (
	"context"
	"errors"
	"fmt"

	edgev1 "github.com/bnema/gordon/api/gordon/edge/v1"
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/edgesnapshot"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const maxProtoInt32 = 1<<31 - 1

// Server exposes a control-owned snapshot Source. It never reads runtime state.
type Server struct {
	edgev1.UnimplementedEdgeServiceServer
	source          edgesnapshot.Source
	trafficSource   edgesnapshot.TrafficGraphSource
	drainReceiver   edgesnapshot.DrainStateReceiver
	appliedReceiver edgesnapshot.AppliedStateReceiver
}

// NewServer constructs an edge snapshot server from a control-owned source.
func NewServer(source edgesnapshot.Source) *Server {
	return &Server{source: source}
}

// NewServerWithTrafficGraphSource exposes both independent sanitized streams.
func NewServerWithTrafficGraphSource(source edgesnapshot.Source, trafficSource edgesnapshot.TrafficGraphSource) *Server {
	return &Server{source: source, trafficSource: trafficSource}
}

// NewServerWithDrainStateReceiver also relays structurally valid drain reports.
func NewServerWithDrainStateReceiver(source edgesnapshot.Source, receiver edgesnapshot.DrainStateReceiver) *Server {
	return &Server{source: source, drainReceiver: receiver}
}

// NewServerWithDrainStateReceiverAndTrafficGraphSource exposes all independent
// edge contracts without allowing transport code to read control configuration.
func NewServerWithDrainStateReceiverAndTrafficGraphSource(source edgesnapshot.Source, receiver edgesnapshot.DrainStateReceiver, trafficSource edgesnapshot.TrafficGraphSource) *Server {
	return &Server{source: source, drainReceiver: receiver, trafficSource: trafficSource}
}

// NewServerWithTrafficGraphDrainAndAppliedStateReceiver exposes the only
// cutover acknowledgement accepted from an authenticated edge.
func NewServerWithTrafficGraphDrainAndAppliedStateReceiver(source edgesnapshot.Source, drainReceiver edgesnapshot.DrainStateReceiver, trafficSource edgesnapshot.TrafficGraphSource, appliedReceiver edgesnapshot.AppliedStateReceiver) *Server {
	return &Server{source: source, drainReceiver: drainReceiver, trafficSource: trafficSource, appliedReceiver: appliedReceiver}
}

// MethodScopes declares the narrow permissions needed by each EdgeService RPC.
func MethodScopes() map[string]domain.ComponentScope {
	return map[string]domain.ComponentScope{
		edgev1.EdgeService_WatchRouteSnapshots_FullMethodName: domain.ComponentScopeRoutesWatch,
		edgev1.EdgeService_WatchTrafficGraphs_FullMethodName:  domain.ComponentScopeTrafficWatch,
		edgev1.EdgeService_ReportDrainState_FullMethodName:    domain.ComponentScopeEdgeDrain,
		edgev1.EdgeService_ReportAppliedState_FullMethodName:  domain.ComponentScopeEdgeAppliedState,
	}
}

// MethodRoles declares that only edge components may call the EdgeService.
func MethodRoles() map[string]domain.ComponentRole {
	return map[string]domain.ComponentRole{
		edgev1.EdgeService_WatchRouteSnapshots_FullMethodName: domain.ComponentRoleEdge,
		edgev1.EdgeService_WatchTrafficGraphs_FullMethodName:  domain.ComponentRoleEdge,
		edgev1.EdgeService_ReportDrainState_FullMethodName:    domain.ComponentRoleEdge,
		edgev1.EdgeService_ReportAppliedState_FullMethodName:  domain.ComponentRoleEdge,
	}
}

// WatchRouteSnapshots sends the current snapshot followed by strictly newer updates.
func (s *Server) WatchRouteSnapshots(_ *edgev1.WatchRouteSnapshotsRequest, stream edgev1.EdgeService_WatchRouteSnapshotsServer) error {
	if s.source == nil {
		return status.Error(codes.FailedPrecondition, "route snapshot source not configured")
	}
	ctx := stream.Context()
	updates, err := s.source.Subscribe(ctx)
	if err != nil {
		return sourceError(err)
	}

	var sent domain.RouteTargetGeneration
	current, err := s.source.Current(ctx)
	if err == nil {
		if err := s.sendSnapshot(stream, current); err != nil {
			return err
		}
		sent = current.Generation
	} else if !errors.Is(err, edgesnapshot.ErrNoSnapshot) {
		return sourceError(err)
	}

	for {
		select {
		case <-ctx.Done():
			return status.Error(codes.Canceled, ctx.Err().Error())
		case snapshot, ok := <-updates:
			if !ok {
				return nil
			}
			if snapshot.Generation <= sent {
				continue
			}
			if err := s.sendSnapshot(stream, snapshot); err != nil {
				return err
			}
			sent = snapshot.Generation
		}
	}
}

// WatchTrafficGraphs sends the current graph followed by strictly newer updates.
func (s *Server) WatchTrafficGraphs(_ *edgev1.WatchTrafficGraphsRequest, stream edgev1.EdgeService_WatchTrafficGraphsServer) error {
	if s.trafficSource == nil {
		return status.Error(codes.FailedPrecondition, "traffic graph source not configured")
	}
	ctx := stream.Context()
	updates, err := s.trafficSource.SubscribeTrafficGraphs(ctx)
	if err != nil {
		return sourceError(err)
	}
	var sent domain.TrafficGraphGeneration
	current, err := s.trafficSource.CurrentTrafficGraph(ctx)
	if err == nil {
		if err := s.sendTrafficGraph(stream, current); err != nil {
			return err
		}
		sent = current.Generation
	} else if !errors.Is(err, edgesnapshot.ErrNoTrafficGraph) {
		return sourceError(err)
	}
	for {
		select {
		case <-ctx.Done():
			return status.Error(codes.Canceled, ctx.Err().Error())
		case snapshot, ok := <-updates:
			if !ok {
				return nil
			}
			if snapshot.Generation <= sent {
				continue
			}
			if err := s.sendTrafficGraph(stream, snapshot); err != nil {
				return err
			}
			sent = snapshot.Generation
		}
	}
}

func (s *Server) sendTrafficGraph(stream edgev1.EdgeService_WatchTrafficGraphsServer, snapshot domain.TrafficGraphSnapshot) error {
	message, err := TrafficGraphSnapshotToProto(snapshot)
	if err != nil {
		return status.Error(codes.Internal, "invalid traffic graph source data")
	}
	if err := stream.Context().Err(); err != nil {
		return status.Error(codes.Canceled, err.Error())
	}
	return stream.Send(message)
}

func (s *Server) sendSnapshot(stream edgev1.EdgeService_WatchRouteSnapshotsServer, snapshot domain.RouteTargetSnapshot) error {
	message, err := RouteSnapshotToProto(snapshot)
	if err != nil {
		return status.Error(codes.Internal, "invalid route snapshot source data")
	}
	if err := stream.Context().Err(); err != nil {
		return status.Error(codes.Canceled, err.Error())
	}
	return stream.Send(message)
}

// ReportDrainState validates and relays only opaque drain data. The receiver
// obtains the authenticated identity exclusively from ctx.
func (s *Server) ReportDrainState(ctx context.Context, request *edgev1.ReportDrainStateRequest) (*edgev1.ReportDrainStateResponse, error) {
	state, err := DrainStateFromProto(request)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid drain state")
	}
	if s.drainReceiver == nil {
		return nil, status.Error(codes.FailedPrecondition, "drain state receiver not configured")
	}
	if receiver, ok := s.drainReceiver.(edgesnapshot.AuthenticatedDrainStateReceiver); ok {
		identity, authenticated := interceptors.ComponentIdentityFromContext(ctx)
		if !authenticated {
			return nil, status.Error(codes.Unauthenticated, "component identity required")
		}
		if err := receiver.ReportAuthenticatedDrainState(ctx, identity.Name, state); err != nil {
			return nil, relayError(err)
		}
	} else if err := s.drainReceiver.ReportDrainState(ctx, state); err != nil {
		return nil, relayError(err)
	}
	return &edgev1.ReportDrainStateResponse{}, nil
}

// ReportAppliedState accepts only the identity established by the component
// interceptor. The request component_id is checked for equality so a valid edge
// token cannot publish readiness for another edge.
func (s *Server) ReportAppliedState(ctx context.Context, request *edgev1.ReportAppliedStateRequest) (*edgev1.ReportAppliedStateResponse, error) {
	if request == nil || request.ComponentId == "" || request.RouteGeneration == 0 || request.TrafficGeneration == 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid applied state")
	}
	if s.appliedReceiver == nil {
		return nil, status.Error(codes.FailedPrecondition, "applied state receiver not configured")
	}
	identity, authenticated := interceptors.ComponentIdentityFromContext(ctx)
	if !authenticated {
		return nil, status.Error(codes.Unauthenticated, "component identity required")
	}
	state := edgesnapshot.AppliedState{ComponentID: request.ComponentId, RouteGeneration: request.RouteGeneration, TrafficGeneration: request.TrafficGeneration, Healthy: request.Healthy}
	if err := s.appliedReceiver.ReportAuthenticatedAppliedState(ctx, identity.Name, state); err != nil {
		if errors.Is(err, edgesnapshot.ErrAppliedStateUnexpected) {
			return nil, status.Error(codes.PermissionDenied, "edge is not expected to report applied state")
		}
		if errors.Is(err, edgesnapshot.ErrAppliedStateStale) || errors.Is(err, edgesnapshot.ErrAppliedStateInvalid) {
			return nil, status.Error(codes.FailedPrecondition, "applied state is stale")
		}
		return nil, status.Error(codes.Internal, "failed to relay applied state")
	}
	return &edgev1.ReportAppliedStateResponse{}, nil
}

func sourceError(err error) error {
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "route snapshot subscription canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "route snapshot subscription timed out")
	}
	return status.Error(codes.Internal, "route snapshot source failed")
}

func relayError(err error) error {
	if errors.Is(err, edgesnapshot.ErrDrainUnexpected) {
		return status.Error(codes.PermissionDenied, "edge is not expected to report drains")
	}
	if errors.Is(err, edgesnapshot.ErrDrainUnknown) || errors.Is(err, edgesnapshot.ErrDrainStale) || errors.Is(err, edgesnapshot.ErrDrainConflicting) {
		return status.Error(codes.FailedPrecondition, "drain is not pending")
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "drain state relay canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "drain state relay timed out")
	}
	return status.Error(codes.Internal, "failed to relay drain state")
}

// RouteSnapshotToProto explicitly converts a validated sanitized snapshot.
func RouteSnapshotToProto(snapshot domain.RouteTargetSnapshot) (*edgev1.RouteTargetSnapshot, error) {
	if err := snapshot.ValidateSplitReachability(); err != nil {
		return nil, fmt.Errorf("validate route snapshot: %w", err)
	}
	message := &edgev1.RouteTargetSnapshot{Generation: uint64(snapshot.Generation)}
	message.Entries = make([]*edgev1.RouteTargetEntry, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		converted, err := routeTargetEntryToProto(entry)
		if err != nil {
			return nil, err
		}
		message.Entries = append(message.Entries, converted)
	}
	if snapshot.RegistryForwardingTarget != nil {
		converted, err := routeTargetEntryToProto(*snapshot.RegistryForwardingTarget)
		if err != nil {
			return nil, err
		}
		message.RegistryForwardingTarget = converted
	}
	return message, nil
}

// RouteSnapshotFromProto explicitly validates every transported snapshot field.
func RouteSnapshotFromProto(message *edgev1.RouteTargetSnapshot) (domain.RouteTargetSnapshot, error) {
	if message == nil {
		return domain.RouteTargetSnapshot{}, fmt.Errorf("route snapshot is required")
	}
	snapshot := domain.RouteTargetSnapshot{Generation: domain.RouteTargetGeneration(message.Generation)}
	snapshot.Entries = make([]domain.RouteTargetEntry, 0, len(message.Entries))
	for index, entry := range message.Entries {
		converted, err := routeTargetEntryFromProto(entry)
		if err != nil {
			return domain.RouteTargetSnapshot{}, fmt.Errorf("entry %d: %w", index, err)
		}
		snapshot.Entries = append(snapshot.Entries, converted)
	}
	if message.RegistryForwardingTarget != nil {
		entry, err := routeTargetEntryFromProto(message.RegistryForwardingTarget)
		if err != nil {
			return domain.RouteTargetSnapshot{}, fmt.Errorf("registry forwarding target: %w", err)
		}
		snapshot.RegistryForwardingTarget = &entry
	}
	if err := snapshot.ValidateSplitReachability(); err != nil {
		return domain.RouteTargetSnapshot{}, fmt.Errorf("validate route snapshot: %w", err)
	}
	return snapshot, nil
}

func routeTargetEntryToProto(entry domain.RouteTargetEntry) (*edgev1.RouteTargetEntry, error) {
	if entry.TargetPort < 0 || entry.TargetPort > maxProtoInt32 {
		return nil, fmt.Errorf("target port overflows transport")
	}
	return &edgev1.RouteTargetEntry{
		CanonicalDomain:   entry.CanonicalDomain,
		TargetHost:        entry.TargetHost,
		TargetPort:        int32(entry.TargetPort),
		Scheme:            entry.Scheme,
		Protocol:          string(entry.Protocol),
		Status:            string(entry.Status),
		UnavailableReason: string(entry.UnavailableReason),
		Generation:        uint64(entry.Generation),
		UpstreamHost:      entry.UpstreamHost,
		Attachment:        string(entry.Attachment),
		TargetKey:         string(entry.TargetKey),
	}, nil
}

func routeTargetEntryFromProto(message *edgev1.RouteTargetEntry) (domain.RouteTargetEntry, error) {
	if message == nil {
		return domain.RouteTargetEntry{}, fmt.Errorf("route target entry is required")
	}
	if message.TargetPort < 0 {
		return domain.RouteTargetEntry{}, fmt.Errorf("target port is invalid")
	}
	entry := domain.RouteTargetEntry{
		CanonicalDomain:   message.CanonicalDomain,
		TargetHost:        message.TargetHost,
		TargetPort:        int(message.TargetPort),
		Scheme:            message.Scheme,
		Protocol:          domain.RouteTargetProtocol(message.Protocol),
		Status:            domain.RouteTargetStatus(message.Status),
		UnavailableReason: domain.RouteTargetUnavailableReason(message.UnavailableReason),
		Generation:        domain.RouteTargetGeneration(message.Generation),
		UpstreamHost:      message.UpstreamHost,
		Attachment:        domain.RouteTargetAttachment(message.Attachment),
		TargetKey:         domain.RouteTargetKey(message.TargetKey),
	}
	if err := entry.Validate(); err != nil {
		return domain.RouteTargetEntry{}, err
	}
	return entry, nil
}

// DrainStateFromProto converts only the narrow drain report shell.
func DrainStateFromProto(request *edgev1.ReportDrainStateRequest) (edgesnapshot.DrainState, error) {
	if request == nil {
		return edgesnapshot.DrainState{}, fmt.Errorf("drain state is required")
	}
	timeoutReason, err := drainTimeoutReasonFromProto(request.TimeoutReason)
	if err != nil {
		return edgesnapshot.DrainState{}, err
	}
	state := edgesnapshot.DrainState{
		CanonicalDomain:      request.CanonicalDomain,
		TransitionGeneration: domain.RouteTargetGeneration(request.TransitionGeneration),
		OldTargetKey:         domain.RouteTargetKey(request.OldTargetKey),
		InFlight:             request.InFlight,
		TimeoutReason:        timeoutReason,
	}
	if request.AcknowledgedAt != nil {
		if err := request.AcknowledgedAt.CheckValid(); err != nil {
			return edgesnapshot.DrainState{}, fmt.Errorf("acknowledged at is invalid")
		}
		state.AcknowledgedAt = request.AcknowledgedAt.AsTime()
	}
	if err := state.Validate(); err != nil {
		return edgesnapshot.DrainState{}, err
	}
	return state, nil
}

// DrainStateToProto converts the narrow drain report shell for round trips.
func DrainStateToProto(state edgesnapshot.DrainState) (*edgev1.ReportDrainStateRequest, error) {
	if err := state.Validate(); err != nil {
		return nil, err
	}
	request := &edgev1.ReportDrainStateRequest{
		CanonicalDomain:      state.CanonicalDomain,
		TransitionGeneration: uint64(state.TransitionGeneration),
		OldTargetKey:         string(state.OldTargetKey),
		InFlight:             state.InFlight,
		TimeoutReason:        drainTimeoutReasonToProto(state.TimeoutReason),
	}
	if !state.AcknowledgedAt.IsZero() {
		request.AcknowledgedAt = timestamppb.New(state.AcknowledgedAt)
	}
	return request, nil
}

func drainTimeoutReasonFromProto(reason edgev1.DrainTimeoutReason) (domain.RouteDrainTimeoutReason, error) {
	switch reason {
	case edgev1.DrainTimeoutReason_DRAIN_TIMEOUT_REASON_UNSPECIFIED:
		return domain.RouteDrainTimeoutReasonNone, nil
	case edgev1.DrainTimeoutReason_DRAIN_TIMEOUT_REASON_EDGE:
		return domain.RouteDrainTimeoutReasonEdge, nil
	case edgev1.DrainTimeoutReason_DRAIN_TIMEOUT_REASON_CONTROL:
		return domain.RouteDrainTimeoutReasonControl, nil
	default:
		return domain.RouteDrainTimeoutReasonNone, fmt.Errorf("timeout reason is invalid")
	}
}

func drainTimeoutReasonToProto(reason domain.RouteDrainTimeoutReason) edgev1.DrainTimeoutReason {
	switch reason {
	case domain.RouteDrainTimeoutReasonEdge:
		return edgev1.DrainTimeoutReason_DRAIN_TIMEOUT_REASON_EDGE
	case domain.RouteDrainTimeoutReasonControl:
		return edgev1.DrainTimeoutReason_DRAIN_TIMEOUT_REASON_CONTROL
	default:
		return edgev1.DrainTimeoutReason_DRAIN_TIMEOUT_REASON_UNSPECIFIED
	}
}
