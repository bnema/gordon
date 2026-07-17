// Package edgesnapshot adapts control-owned route snapshots to the edge gRPC API.
package edgesnapshot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	edgev1 "github.com/bnema/gordon/api/gordon/edge/v1"
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
	source        edgesnapshot.Source
	drainReceiver edgesnapshot.DrainStateReceiver
}

// NewServer constructs an edge snapshot server from a control-owned source.
func NewServer(source edgesnapshot.Source) *Server {
	return &Server{source: source}
}

// NewServerWithDrainStateReceiver also relays structurally valid drain reports.
func NewServerWithDrainStateReceiver(source edgesnapshot.Source, receiver edgesnapshot.DrainStateReceiver) *Server {
	return &Server{source: source, drainReceiver: receiver}
}

// MethodScopes declares the narrow permissions needed by each EdgeService RPC.
func MethodScopes() map[string]domain.ComponentScope {
	return map[string]domain.ComponentScope{
		edgev1.EdgeService_WatchRouteSnapshots_FullMethodName: domain.ComponentScopeRoutesWatch,
		edgev1.EdgeService_ReportDrainState_FullMethodName:    domain.ComponentScopeEdgeDrain,
	}
}

// MethodRoles declares that only edge components may call the EdgeService.
func MethodRoles() map[string]domain.ComponentRole {
	return map[string]domain.ComponentRole{
		edgev1.EdgeService_WatchRouteSnapshots_FullMethodName: domain.ComponentRoleEdge,
		edgev1.EdgeService_ReportDrainState_FullMethodName:    domain.ComponentRoleEdge,
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
	if err := s.drainReceiver.ReportDrainState(ctx, state); err != nil {
		return nil, relayError(err)
	}
	return &edgev1.ReportDrainStateResponse{}, nil
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
	state := edgesnapshot.DrainState{
		Generation:    domain.RouteTargetGeneration(request.Generation),
		TargetKey:     domain.RouteTargetKey(request.TargetKey),
		InFlight:      request.InFlight,
		TimeoutReason: request.TimeoutReason,
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
		Generation:    uint64(state.Generation),
		TargetKey:     string(state.TargetKey),
		InFlight:      state.InFlight,
		TimeoutReason: strings.Clone(state.TimeoutReason),
	}
	if !state.AcknowledgedAt.IsZero() {
		request.AcknowledgedAt = timestamppb.New(state.AcknowledgedAt)
	}
	return request, nil
}
