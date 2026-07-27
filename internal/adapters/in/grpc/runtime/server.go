package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"time"

	runtimev1 "github.com/bnema/gordon/api/gordon/runtime/v1"
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	boundaries "github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	maxInt32 = 1<<31 - 1
	minInt32 = -1 << 31
)

// Server adapts runtime gRPC requests to the inbound RuntimeWorker boundary.
type Server struct {
	runtimev1.UnimplementedRuntimeServiceServer
	worker                   boundaries.RuntimeWorker
	stateSubscriber          out.RuntimeStateSubscriber
	logReader                out.RuntimeLogReader
	volumeManager            out.RuntimeVolumeManager
	imageManager             out.RuntimeImageManager
	drainAckReceiver         out.RuntimeDrainAckReceiver
	routeDrainAckReceiver    out.RouteDrainAckReceiver
	routeDrainRegistrar      out.RouteDrainRegistrar
	standaloneServiceManager out.RuntimeStandaloneServiceManager
	environmentProbe         out.RuntimeEnvironmentProbe
	componentID              string
}

func NewServer(worker boundaries.RuntimeWorker, componentID string) *Server {
	return NewServerWithLogReader(worker, nil, componentID)
}

func NewServerWithLogReader(worker boundaries.RuntimeWorker, logReader out.RuntimeLogReader, componentID string) *Server {
	return NewServerWithRuntimePorts(worker, logReader, nil, componentID)
}

func NewServerWithStateSubscriber(worker boundaries.RuntimeWorker, stateSubscriber out.RuntimeStateSubscriber, componentID string) *Server {
	return &Server{worker: worker, stateSubscriber: stateSubscriber, componentID: componentID}
}

func NewServerWithVolumeManager(worker boundaries.RuntimeWorker, volumeManager out.RuntimeVolumeManager, componentID string) *Server {
	return NewServerWithRuntimePorts(worker, nil, volumeManager, componentID)
}

func NewServerWithImageManager(worker boundaries.RuntimeWorker, imageManager out.RuntimeImageManager, componentID string) *Server {
	return &Server{worker: worker, imageManager: imageManager, componentID: componentID}
}

func NewServerWithRuntimePorts(worker boundaries.RuntimeWorker, logReader out.RuntimeLogReader, volumeManager out.RuntimeVolumeManager, componentID string) *Server {
	return &Server{worker: worker, logReader: logReader, volumeManager: volumeManager, componentID: componentID}
}

func NewServerWithDrainAckReceiver(worker boundaries.RuntimeWorker, drainAckReceiver out.RuntimeDrainAckReceiver, componentID string) *Server {
	return &Server{worker: worker, drainAckReceiver: drainAckReceiver, componentID: componentID}
}

// NewServerWithRouteDrainAckReceiver configures the opaque split-edge drain protocol.
func NewServerWithRouteDrainAckReceiver(worker boundaries.RuntimeWorker, receiver out.RouteDrainAckReceiver, componentID string) *Server {
	server := &Server{worker: worker, routeDrainAckReceiver: receiver, componentID: componentID}
	if registrar, ok := receiver.(out.RouteDrainRegistrar); ok {
		server.routeDrainRegistrar = registrar
	}
	return server
}

// NewServerWithStandaloneServiceManager configures the optional narrow standalone-service runtime port.
func NewServerWithStandaloneServiceManager(worker boundaries.RuntimeWorker, standaloneServiceManager out.RuntimeStandaloneServiceManager, componentID string) *Server {
	return &Server{worker: worker, standaloneServiceManager: standaloneServiceManager, componentID: componentID}
}

func NewServerWithAllRuntimePorts(worker boundaries.RuntimeWorker, logReader out.RuntimeLogReader, volumeManager out.RuntimeVolumeManager, imageManager out.RuntimeImageManager, componentID string) *Server {
	return NewServerWithAllRuntimePortsAndStateSubscriber(worker, logReader, volumeManager, imageManager, nil, componentID)
}

func NewServerWithAllRuntimePortsAndStateSubscriber(worker boundaries.RuntimeWorker, logReader out.RuntimeLogReader, volumeManager out.RuntimeVolumeManager, imageManager out.RuntimeImageManager, stateSubscriber out.RuntimeStateSubscriber, componentID string) *Server {
	return NewServerWithAllRuntimePortsAndDrainAckReceiver(worker, logReader, volumeManager, imageManager, stateSubscriber, nil, componentID)
}

func NewServerWithAllRuntimePortsAndDrainAckReceiver(worker boundaries.RuntimeWorker, logReader out.RuntimeLogReader, volumeManager out.RuntimeVolumeManager, imageManager out.RuntimeImageManager, stateSubscriber out.RuntimeStateSubscriber, drainAckReceiver out.RuntimeDrainAckReceiver, componentID string) *Server {
	return &Server{worker: worker, logReader: logReader, volumeManager: volumeManager, imageManager: imageManager, stateSubscriber: stateSubscriber, drainAckReceiver: drainAckReceiver, componentID: componentID}
}

// NewServerWithAllRuntimePortsAndDrainAckReceiverAndStandaloneServiceManager configures every optional legacy runtime port.
func NewServerWithAllRuntimePortsAndDrainAckReceiverAndStandaloneServiceManager(worker boundaries.RuntimeWorker, logReader out.RuntimeLogReader, volumeManager out.RuntimeVolumeManager, imageManager out.RuntimeImageManager, stateSubscriber out.RuntimeStateSubscriber, drainAckReceiver out.RuntimeDrainAckReceiver, standaloneServiceManager out.RuntimeStandaloneServiceManager, componentID string) *Server {
	server := NewServerWithAllRuntimePortsAndDrainAckReceiver(worker, logReader, volumeManager, imageManager, stateSubscriber, drainAckReceiver, componentID)
	server.standaloneServiceManager = standaloneServiceManager
	return server
}

// NewServerWithAllRuntimePortsAndRouteDrainAckReceiverAndStandaloneServiceManager configures the split opaque drain relay.
func NewServerWithAllRuntimePortsAndRouteDrainAckReceiverAndStandaloneServiceManager(worker boundaries.RuntimeWorker, logReader out.RuntimeLogReader, volumeManager out.RuntimeVolumeManager, imageManager out.RuntimeImageManager, stateSubscriber out.RuntimeStateSubscriber, routeDrainAckReceiver out.RouteDrainAckReceiver, standaloneServiceManager out.RuntimeStandaloneServiceManager, componentID string) *Server {
	return NewServerWithEnvironmentProbe(worker, logReader, volumeManager, imageManager, stateSubscriber, routeDrainAckReceiver, standaloneServiceManager, nil, componentID)
}

// NewServerWithEnvironmentProbe adds the read-only migration preflight port.
func NewServerWithEnvironmentProbe(worker boundaries.RuntimeWorker, logReader out.RuntimeLogReader, volumeManager out.RuntimeVolumeManager, imageManager out.RuntimeImageManager, stateSubscriber out.RuntimeStateSubscriber, routeDrainAckReceiver out.RouteDrainAckReceiver, standaloneServiceManager out.RuntimeStandaloneServiceManager, environmentProbe out.RuntimeEnvironmentProbe, componentID string) *Server {
	server := &Server{worker: worker, logReader: logReader, volumeManager: volumeManager, imageManager: imageManager, stateSubscriber: stateSubscriber, routeDrainAckReceiver: routeDrainAckReceiver, standaloneServiceManager: standaloneServiceManager, environmentProbe: environmentProbe, componentID: componentID}
	if registrar, ok := routeDrainAckReceiver.(out.RouteDrainRegistrar); ok {
		server.routeDrainRegistrar = registrar
	}
	return server
}

func MethodScopes() map[string]domain.ComponentScope {
	return map[string]domain.ComponentScope{
		runtimev1.RuntimeService_ApplyCommand_FullMethodName:               domain.ComponentScopeRuntimeDeploy,
		runtimev1.RuntimeService_WatchActualState_FullMethodName:           domain.ComponentScopeRuntimeStatus,
		runtimev1.RuntimeService_GetHealth_FullMethodName:                  domain.ComponentScopeRuntimeStatus,
		runtimev1.RuntimeService_ProbeEnvironment_FullMethodName:           domain.ComponentScopeRuntimeStatus,
		runtimev1.RuntimeService_StreamLogs_FullMethodName:                 domain.ComponentScopeRuntimeLogs,
		runtimev1.RuntimeService_ListVolumes_FullMethodName:                domain.ComponentScopeRuntimeStatus,
		runtimev1.RuntimeService_RemoveVolume_FullMethodName:               domain.ComponentScopeRuntimeDeploy,
		runtimev1.RuntimeService_ListImages_FullMethodName:                 domain.ComponentScopeRuntimeStatus,
		runtimev1.RuntimeService_PruneImages_FullMethodName:                domain.ComponentScopeRuntimeDeploy,
		runtimev1.RuntimeService_RuntimeSelfUpdate_FullMethodName:          domain.ComponentScopeRuntimeSelfUpdate,
		runtimev1.RuntimeService_ApplyStandaloneService_FullMethodName:     domain.ComponentScopeRuntimeDeploy,
		runtimev1.RuntimeService_RemoveStandaloneService_FullMethodName:    domain.ComponentScopeRuntimeDeploy,
		runtimev1.RuntimeService_ListStandaloneServiceState_FullMethodName: domain.ComponentScopeRuntimeStatus,
		runtimev1.RuntimeService_ReportEdgeDrain_FullMethodName:            domain.ComponentScopeRuntimeDrainAck,
		runtimev1.RuntimeService_PrepareEdgeDrain_FullMethodName:           domain.ComponentScopeRuntimeDrainAck,
	}
}

// MethodRoles returns the authorized caller role for every RuntimeService RPC.
func MethodRoles() map[string]domain.ComponentRole {
	return map[string]domain.ComponentRole{
		runtimev1.RuntimeService_ApplyCommand_FullMethodName:               domain.ComponentRoleControl,
		runtimev1.RuntimeService_WatchActualState_FullMethodName:           domain.ComponentRoleControl,
		runtimev1.RuntimeService_GetHealth_FullMethodName:                  domain.ComponentRoleControl,
		runtimev1.RuntimeService_ProbeEnvironment_FullMethodName:           domain.ComponentRoleControl,
		runtimev1.RuntimeService_StreamLogs_FullMethodName:                 domain.ComponentRoleControl,
		runtimev1.RuntimeService_ListVolumes_FullMethodName:                domain.ComponentRoleControl,
		runtimev1.RuntimeService_RemoveVolume_FullMethodName:               domain.ComponentRoleControl,
		runtimev1.RuntimeService_ListImages_FullMethodName:                 domain.ComponentRoleControl,
		runtimev1.RuntimeService_PruneImages_FullMethodName:                domain.ComponentRoleControl,
		runtimev1.RuntimeService_RuntimeSelfUpdate_FullMethodName:          domain.ComponentRoleControl,
		runtimev1.RuntimeService_ApplyStandaloneService_FullMethodName:     domain.ComponentRoleControl,
		runtimev1.RuntimeService_RemoveStandaloneService_FullMethodName:    domain.ComponentRoleControl,
		runtimev1.RuntimeService_ListStandaloneServiceState_FullMethodName: domain.ComponentRoleControl,
		runtimev1.RuntimeService_ReportEdgeDrain_FullMethodName:            domain.ComponentRoleControl,
		runtimev1.RuntimeService_PrepareEdgeDrain_FullMethodName:           domain.ComponentRoleControl,
	}
}

func (s *Server) ApplyCommand(ctx context.Context, req *runtimev1.ApplyCommandRequest) (*runtimev1.ApplyCommandResponse, error) {
	if s.worker == nil {
		return nil, status.Error(codes.FailedPrecondition, "runtime worker not configured")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "apply command request is required")
	}
	var (
		result domain.RuntimeCommandResult
		err    error
	)
	switch command := req.Command.(type) {
	case *runtimev1.ApplyCommandRequest_DeployRoute:
		result, err = s.worker.DeployRoute(ctx, protoDeployRoute(command.DeployRoute))
	case *runtimev1.ApplyCommandRequest_RestartRoute:
		result, err = s.worker.RestartRoute(ctx, protoRestartRoute(command.RestartRoute))
	case *runtimev1.ApplyCommandRequest_RemoveRoute:
		result, err = s.worker.RemoveRoute(ctx, protoRemoveRoute(command.RemoveRoute))
	case *runtimev1.ApplyCommandRequest_Reconcile:
		if _, scopeErr := interceptors.RequireScope(ctx, domain.ComponentScopeRuntimeReconcile); scopeErr != nil {
			return nil, scopeErr
		}
		result, err = s.worker.Reconcile(ctx, protoReconcile(command.Reconcile))
	default:
		return nil, status.Error(codes.InvalidArgument, "runtime command is required")
	}
	if err != nil {
		return nil, err
	}
	return &runtimev1.ApplyCommandResponse{Result: protoRuntimeCommandResult(result)}, nil
}

func (s *Server) ApplyStandaloneService(ctx context.Context, req *runtimev1.ApplyStandaloneServiceRequest) (*runtimev1.ApplyStandaloneServiceResponse, error) {
	if s.standaloneServiceManager == nil {
		return nil, status.Error(codes.FailedPrecondition, "runtime standalone service manager not configured")
	}
	if req == nil || req.Command == nil {
		return nil, status.Error(codes.InvalidArgument, "standalone service apply command is required")
	}
	result, err := s.standaloneServiceManager.ApplyStandaloneService(ctx, protoApplyStandaloneService(req.Command))
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to apply standalone service")
	}
	return &runtimev1.ApplyStandaloneServiceResponse{Result: protoRuntimeCommandResult(result)}, nil
}

func (s *Server) RemoveStandaloneService(ctx context.Context, req *runtimev1.RemoveStandaloneServiceRequest) (*runtimev1.RemoveStandaloneServiceResponse, error) {
	if s.standaloneServiceManager == nil {
		return nil, status.Error(codes.FailedPrecondition, "runtime standalone service manager not configured")
	}
	if req == nil || req.Command == nil {
		return nil, status.Error(codes.InvalidArgument, "standalone service remove command is required")
	}
	result, err := s.standaloneServiceManager.RemoveStandaloneService(ctx, protoRemoveStandaloneService(req.Command))
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to remove standalone service")
	}
	return &runtimev1.RemoveStandaloneServiceResponse{Result: protoRuntimeCommandResult(result)}, nil
}

func (s *Server) ListStandaloneServiceState(ctx context.Context, _ *runtimev1.ListStandaloneServiceStateRequest) (*runtimev1.ListStandaloneServiceStateResponse, error) {
	if s.standaloneServiceManager == nil {
		return nil, status.Error(codes.FailedPrecondition, "runtime standalone service manager not configured")
	}
	states, err := s.standaloneServiceManager.ListStandaloneServiceState(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to list standalone service state")
	}
	services := make([]*runtimev1.RuntimeStandaloneServiceState, 0, len(states))
	for _, service := range states {
		services = append(services, protoStandaloneServiceState(service))
	}
	return &runtimev1.ListStandaloneServiceStateResponse{Services: services}, nil
}

func (s *Server) RuntimeSelfUpdate(ctx context.Context, req *runtimev1.RuntimeSelfUpdateRequest) (*runtimev1.RuntimeSelfUpdateResponse, error) {
	if s.worker == nil {
		return nil, status.Error(codes.FailedPrecondition, "runtime worker not configured")
	}
	if req == nil || req.Command == nil {
		return nil, status.Error(codes.InvalidArgument, "runtime self-update command is required")
	}
	command := protoSelfUpdate(req.Command)
	if err := command.Validate(); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid runtime self-update command")
	}
	result, err := s.worker.SelfUpdate(ctx, command)
	if err != nil {
		return nil, err
	}
	return &runtimev1.RuntimeSelfUpdateResponse{Result: protoRuntimeCommandResult(result)}, nil
}

func (s *Server) GetHealth(context.Context, *runtimev1.GetHealthRequest) (*runtimev1.GetHealthResponse, error) {
	return &runtimev1.GetHealthResponse{Ok: s.worker != nil, ComponentId: s.componentID, Message: "runtime service ready"}, nil
}

func (s *Server) ProbeEnvironment(ctx context.Context, req *runtimev1.ProbeEnvironmentRequest) (*runtimev1.ProbeEnvironmentResponse, error) {
	if s.environmentProbe == nil {
		return nil, status.Error(codes.FailedPrecondition, "runtime environment probe not configured")
	}
	report, err := s.environmentProbe.ProbeRuntimeEnvironment(ctx)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "runtime environment unavailable")
	}
	response := &runtimev1.ProbeEnvironmentResponse{Engine: report.Engine, Rootless: report.Rootless, ApiReachable: report.APIReachable, ImageAvailable: report.ImageAvailable, ImagePullable: report.ImagePullable, NetworkFeasible: report.NetworkFeasible, DiskAvailableBytes: report.DiskAvailable, DiskSufficient: report.DiskSufficient}
	if req == nil || len(req.GetRequiredPublicPorts()) == 0 {
		return response, nil
	}
	if len(req.GetRequiredPublicPorts()) > 16 {
		return nil, status.Error(codes.InvalidArgument, "too many public listeners")
	}
	ports := make([]int, len(req.GetRequiredPublicPorts()))
	for i, port := range req.GetRequiredPublicPorts() {
		if port < 1 || port > 65535 {
			return nil, status.Error(codes.InvalidArgument, "invalid public listener")
		}
		ports[i] = int(port)
	}
	probe, ok := s.environmentProbe.(out.RuntimePublicListenerProbe)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "runtime listener probe not configured")
	}
	available, err := probe.ProbePublicListeners(ctx, ports)
	if err != nil || len(available) != len(ports) {
		return nil, status.Error(codes.Unavailable, "runtime listener availability unavailable")
	}
	response.PublicListenersAvailable = available
	return response, nil
}

func (s *Server) WatchActualState(_ *runtimev1.WatchActualStateRequest, stream runtimev1.RuntimeService_WatchActualStateServer) error {
	if s.stateSubscriber == nil {
		return status.Error(codes.FailedPrecondition, "runtime state subscriber not configured")
	}
	snapshots, err := s.stateSubscriber.SubscribeRuntimeState(stream.Context())
	if err != nil {
		return status.Error(codes.Internal, "failed to subscribe runtime state")
	}
	for {
		select {
		case <-stream.Context().Done():
			return status.Error(codes.Canceled, stream.Context().Err().Error())
		case snapshot, ok := <-snapshots:
			if !ok {
				return nil
			}
			if err := stream.Context().Err(); err != nil {
				return status.Error(codes.Canceled, err.Error())
			}
			if err := stream.Send(protoActualStateSnapshot(snapshot)); err != nil {
				return err
			}
		}
	}
}

func (s *Server) StreamLogs(req *runtimev1.StreamLogsRequest, stream runtimev1.RuntimeService_StreamLogsServer) error {
	if s.logReader == nil {
		return status.Error(codes.FailedPrecondition, "runtime log reader not configured")
	}
	if req == nil || strings.TrimSpace(req.RouteDomain) == "" {
		return status.Error(codes.InvalidArgument, "route domain is required")
	}
	if err := stream.SendHeader(metadata.MD{}); err != nil {
		return err
	}
	reader, err := s.logReader.ReadRouteLogs(stream.Context(), req.RouteDomain, req.Follow)
	if err != nil {
		return status.Error(codes.Internal, "failed to read runtime logs")
	}
	defer reader.Close()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			if err := stream.Send(&runtimev1.StreamLogsResponse{Data: append([]byte(nil), buf[:n]...)}); err != nil {
				return err
			}
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		return status.Error(codes.Internal, "failed to stream runtime logs")
	}
}

func (s *Server) ListVolumes(ctx context.Context, _ *runtimev1.ListVolumesRequest) (*runtimev1.ListVolumesResponse, error) {
	if s.volumeManager == nil {
		return nil, status.Error(codes.FailedPrecondition, "runtime volume manager not configured")
	}
	volumes, err := s.volumeManager.ListRuntimeVolumes(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to list runtime volumes")
	}
	out := make([]*runtimev1.RuntimeVolumeInfo, 0, len(volumes))
	for _, volume := range volumes {
		out = append(out, protoVolumeInfo(volume))
	}
	return &runtimev1.ListVolumesResponse{Volumes: out}, nil
}

func (s *Server) RemoveVolume(ctx context.Context, req *runtimev1.RemoveVolumeRequest) (*runtimev1.RemoveVolumeResponse, error) {
	if s.volumeManager == nil {
		return nil, status.Error(codes.FailedPrecondition, "runtime volume manager not configured")
	}
	if req == nil || strings.TrimSpace(req.Name) == "" {
		return nil, status.Error(codes.InvalidArgument, "volume name is required")
	}
	if err := s.volumeManager.RemoveRuntimeVolume(ctx, req.Name, req.Force); err != nil {
		return nil, status.Error(codes.Internal, "failed to remove runtime volume")
	}
	return &runtimev1.RemoveVolumeResponse{Ok: true, Message: "runtime volume removed"}, nil
}

func (s *Server) ListImages(ctx context.Context, _ *runtimev1.ListImagesRequest) (*runtimev1.ListImagesResponse, error) {
	if s.imageManager == nil {
		return nil, status.Error(codes.FailedPrecondition, "runtime image manager not configured")
	}
	images, err := s.imageManager.ListRuntimeImages(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to list runtime images")
	}
	out := make([]*runtimev1.RuntimeImageInfo, 0, len(images))
	for _, image := range images {
		out = append(out, protoImageInfo(image))
	}
	return &runtimev1.ListImagesResponse{Images: out}, nil
}

func (s *Server) PruneImages(ctx context.Context, req *runtimev1.PruneImagesRequest) (*runtimev1.PruneImagesResponse, error) {
	if s.imageManager == nil {
		return nil, status.Error(codes.FailedPrecondition, "runtime image manager not configured")
	}
	danglingOnly := false
	if req != nil {
		danglingOnly = req.DanglingOnly
	}
	report, err := s.imageManager.PruneRuntimeImages(ctx, danglingOnly)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to prune runtime images")
	}
	deletedCount, err := safeInt32(report.DeletedCount)
	if err != nil {
		return nil, status.Error(codes.Internal, "runtime image prune deleted count overflows int32")
	}
	return &runtimev1.PruneImagesResponse{DeletedCount: deletedCount, SpaceReclaimed: report.SpaceReclaimed}, nil
}

func (s *Server) PrepareEdgeDrain(ctx context.Context, req *runtimev1.PrepareEdgeDrainRequest) (*runtimev1.PrepareEdgeDrainResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req == nil || req.CanonicalDomain == "" || req.TransitionGeneration == 0 || req.OldTargetKey == "" {
		return nil, status.Error(codes.InvalidArgument, "route drain registration is required")
	}
	if s.routeDrainRegistrar == nil {
		return nil, status.Error(codes.FailedPrecondition, "runtime route drain registrar not configured")
	}
	if err := s.routeDrainRegistrar.PrepareRouteDrain(ctx, req.CanonicalDomain, domain.RouteTargetGeneration(req.TransitionGeneration), domain.RouteTargetKey(req.OldTargetKey)); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "failed to register route drain")
	}
	return &runtimev1.PrepareEdgeDrainResponse{Ok: true, Message: "route drain registered"}, nil
}

func (s *Server) ReportEdgeDrain(ctx context.Context, req *runtimev1.ReportEdgeDrainRequest) (*runtimev1.ReportEdgeDrainResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "route drain acknowledgement is required")
	}
	acknowledgedAt := time.Time{}
	if req.AcknowledgedAt != nil {
		if err := req.AcknowledgedAt.CheckValid(); err != nil {
			return nil, status.Error(codes.InvalidArgument, "acknowledgement time is invalid")
		}
		acknowledgedAt = req.AcknowledgedAt.AsTime()
	}
	ack := domain.RouteDrainAck{RouteDrainState: domain.RouteDrainState{
		CanonicalDomain:      req.CanonicalDomain,
		TransitionGeneration: domain.RouteTargetGeneration(req.TransitionGeneration),
		OldTargetKey:         domain.RouteTargetKey(req.OldTargetKey),
		AcknowledgedAt:       acknowledgedAt,
		TimeoutReason:        domain.RouteDrainTimeoutReason(req.TimeoutReason),
	}, Status: domain.RouteDrainStatusAcknowledged}
	if ack.TimeoutReason != domain.RouteDrainTimeoutReasonNone {
		ack.Status = domain.RouteDrainStatusTimedOut
		ack.InFlight = 1
	}
	if err := ack.Validate(); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid route drain acknowledgement")
	}
	if s.routeDrainAckReceiver == nil {
		return nil, status.Error(codes.FailedPrecondition, "runtime route drain ack receiver not configured")
	}
	if err := s.routeDrainAckReceiver.AcknowledgeRouteDrain(ctx, ack); err != nil {
		return nil, status.Error(codes.Internal, "failed to record route drain acknowledgement")
	}
	return &runtimev1.ReportEdgeDrainResponse{Ok: true, Message: "route drain acknowledgement recorded"}, nil
}

func safeInt32(value int) (int32, error) {
	if value < minInt32 || value > maxInt32 {
		return 0, fmt.Errorf("%d overflows int32", value)
	}
	return int32(value), nil
}

func protoActualStateSnapshot(snapshot domain.RuntimeActualStateSnapshot) *runtimev1.WatchActualStateResponse {
	out := &runtimev1.WatchActualStateResponse{Generation: snapshot.Generation, StateVersion: snapshot.StateVersion, SourceComponentId: snapshot.SourceComponentID, ObservedAt: timestamppb.New(snapshot.ObservedAt)}
	for _, route := range snapshot.Routes {
		targetPort, _ := safeInt32(route.TargetPort)
		out.Routes = append(out.Routes, &runtimev1.RuntimeRouteState{Domain: route.Domain, Generation: route.Generation, RouteVersion: route.RouteVersion, ContainerAlias: route.ContainerAlias, EdgeTargetAlias: route.EdgeTargetAlias, TargetPort: targetPort, Scheme: route.Scheme, Protocol: string(route.Protocol), Status: string(route.Status), UnavailableReason: string(route.UnavailableReason), BackingContainerName: route.BackingContainerName})
	}
	for _, container := range snapshot.Containers {
		out.Containers = append(out.Containers, &runtimev1.RuntimeContainerState{Name: container.Name, Alias: container.Alias, Image: container.Image, ImageId: container.ImageID, Status: string(container.Status), StartedAt: timestamppb.New(container.StartedAt), Labels: domain.SanitizeRuntimeStateLabels(container.Labels), Generation: container.Generation})
	}
	for _, network := range snapshot.Networks {
		out.Networks = append(out.Networks, &runtimev1.RuntimeNetworkState{Name: network.Name, Driver: network.Driver, Internal: network.Internal, Aliases: append([]string(nil), network.Aliases...), Generation: network.Generation})
	}
	for _, volume := range snapshot.Volumes {
		out.Volumes = append(out.Volumes, &runtimev1.RuntimeActualVolumeState{Name: volume.Name, AttachedTo: append([]string(nil), volume.AttachedTo...), Generation: volume.Generation})
	}
	for _, attachment := range snapshot.EdgeAttachments {
		targetPort, _ := safeInt32(attachment.TargetPort)
		out.EdgeAttachments = append(out.EdgeAttachments, &runtimev1.RuntimeEdgeNetworkAttachmentState{RouteDomain: attachment.RouteDomain, NetworkName: attachment.NetworkName, EdgeAlias: attachment.EdgeAlias, RuntimeAlias: attachment.RuntimeAlias, TargetAlias: attachment.TargetAlias, TargetPort: targetPort, Attached: attachment.Attached, Generation: attachment.Generation, SourceComponent: attachment.SourceComponent})
	}
	return out
}

func protoImageInfo(image domain.RuntimeImageDetail) *runtimev1.RuntimeImageInfo {
	return &runtimev1.RuntimeImageInfo{Id: image.ID, RepoTags: append([]string(nil), image.RepoTags...), Size: image.Size, Created: timestamppb.New(image.Created)}
}

func protoVolumeInfo(volume *domain.VolumeInfo) *runtimev1.RuntimeVolumeInfo {
	if volume == nil {
		return nil
	}
	return &runtimev1.RuntimeVolumeInfo{Name: volume.Name, Driver: volume.Driver, MountPoint: volume.MountPoint, Size: volume.Size, CreatedAt: timestamppb.New(volume.CreatedAt), InUse: volume.InUse, Containers: append([]string(nil), volume.Containers...), Labels: cloneStringMap(volume.Labels)}
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	maps.Copy(out, values)
	return out
}

func protoIdentity(identity *runtimev1.RuntimeCommandIdentity) domain.RuntimeCommandIdentity {
	if identity == nil {
		return domain.RuntimeCommandIdentity{}
	}
	return domain.RuntimeCommandIdentity{ID: domain.RuntimeCommandID(identity.Id), IdempotencyKey: identity.IdempotencyKey, Generation: identity.Generation, SourceComponentID: identity.SourceComponentId, RequestedAt: identity.RequestedAt.AsTime()}
}

func protoDeployRoute(command *runtimev1.DeployRouteCommand) domain.DeployRouteCommand {
	if command == nil {
		return domain.DeployRouteCommand{}
	}
	return domain.DeployRouteCommand{RuntimeCommandIdentity: protoIdentity(command.Identity), Domain: command.Domain, Image: command.Image, RouteVersion: command.RouteVersion, Env: command.Env, InternalDeploy: command.InternalDeploy}
}

func protoRestartRoute(command *runtimev1.RestartRouteCommand) domain.RestartRouteCommand {
	if command == nil {
		return domain.RestartRouteCommand{}
	}
	return domain.RestartRouteCommand{RuntimeCommandIdentity: protoIdentity(command.Identity), Domain: command.Domain, Reason: command.Reason, WithAttachments: command.WithAttachments}
}

func protoRemoveRoute(command *runtimev1.RemoveRouteCommand) domain.RemoveRouteCommand {
	if command == nil {
		return domain.RemoveRouteCommand{}
	}
	return domain.RemoveRouteCommand{RuntimeCommandIdentity: protoIdentity(command.Identity), Domain: command.Domain, Force: command.Force}
}

func protoReconcile(command *runtimev1.ReconcileRuntimeCommand) domain.ReconcileRuntimeCommand {
	if command == nil {
		return domain.ReconcileRuntimeCommand{}
	}
	routes := make([]domain.Route, 0, len(command.DesiredRoutes))
	for _, route := range command.DesiredRoutes {
		if route == nil {
			continue
		}
		routes = append(routes, domain.Route{Domain: route.Domain, Image: route.Image, Env: route.Env})
	}
	return domain.ReconcileRuntimeCommand{RuntimeCommandIdentity: protoIdentity(command.Identity), Reason: command.Reason, ExpectedRouteCount: int(command.ExpectedRouteCount), DesiredStateVersion: command.DesiredStateVersion, DesiredRoutes: routes}
}

func protoApplyStandaloneService(command *runtimev1.ApplyStandaloneServiceCommand) domain.ApplyStandaloneServiceCommand {
	if command == nil {
		return domain.ApplyStandaloneServiceCommand{}
	}
	return domain.ApplyStandaloneServiceCommand{
		RuntimeCommandIdentity: protoIdentity(command.Identity),
		Service:                protoStandaloneService(command.Service),
		ResolvedEnv:            append([]string(nil), command.ResolvedEnv...),
		ConfigHash:             command.ConfigHash,
	}
}

func protoRemoveStandaloneService(command *runtimev1.RemoveStandaloneServiceCommand) domain.RemoveStandaloneServiceCommand {
	if command == nil {
		return domain.RemoveStandaloneServiceCommand{}
	}
	return domain.RemoveStandaloneServiceCommand{
		RuntimeCommandIdentity: protoIdentity(command.Identity),
		Name:                   command.Name,
		Reason:                 command.Reason,
		Cleanup:                domain.StandaloneServiceCleanup{PreserveVolumes: command.GetCleanup().GetPreserveVolumes(), RemoveContainer: command.GetCleanup().GetRemoveContainer()},
	}
}

func protoStandaloneService(service *runtimev1.StandaloneServiceSpec) domain.StandaloneService {
	if service == nil {
		return domain.StandaloneService{}
	}
	out := domain.StandaloneService{
		Name:    service.Name,
		Image:   service.Image,
		Enabled: service.Enabled,
		Readiness: domain.StandaloneServiceReadiness{
			Type:       service.GetReadiness().GetType(),
			Path:       service.GetReadiness().GetPath(),
			Contains:   service.GetReadiness().GetContains(),
			Timeout:    time.Duration(service.GetReadiness().GetTimeoutNs()),
			TimeoutSet: service.GetReadiness().GetTimeoutSet(),
		},
		Cleanup: domain.StandaloneServiceCleanup{
			PreserveVolumes: service.GetCleanup().GetPreserveVolumes(),
			RemoveContainer: service.GetCleanup().GetRemoveContainer(),
		},
	}
	for _, port := range service.Ports {
		if port == nil {
			continue
		}
		out.Ports = append(out.Ports, domain.StandaloneServicePort{Name: port.Name, Container: int(port.Container), Protocol: domain.NetworkProtocol(port.Protocol), Publish: port.Publish, Private: port.Private, Public: port.Public, TrustedCIDRs: append([]string(nil), port.TrustedCidrs...)})
	}
	for _, volume := range service.Volumes {
		if volume == nil {
			continue
		}
		out.Volumes = append(out.Volumes, domain.StandaloneServiceVolume{Source: volume.Source, Target: volume.Target, ReadOnly: volume.ReadOnly})
	}
	return out
}

func protoStandaloneServiceState(service domain.RuntimeStandaloneServiceState) *runtimev1.RuntimeStandaloneServiceState {
	return &runtimev1.RuntimeStandaloneServiceState{
		Name:          service.Name,
		ContainerId:   service.ContainerID,
		ContainerName: service.ContainerName,
		Status:        string(service.Status),
		ConfigHash:    service.ConfigHash,
		Cleanup:       &runtimev1.StandaloneServiceCleanupSpec{PreserveVolumes: service.Cleanup.PreserveVolumes, RemoveContainer: service.Cleanup.RemoveContainer},
	}
}

func protoSelfUpdate(command *runtimev1.RuntimeSelfUpdateCommand) domain.RuntimeSelfUpdateCommand {
	if command == nil {
		return domain.RuntimeSelfUpdateCommand{}
	}
	result := domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: protoIdentity(command.Identity), TargetComponentID: command.TargetComponentId, TargetComponentRole: domain.ComponentRole(command.TargetComponentRole), CurrentVersion: command.CurrentVersion, TargetVersion: command.TargetVersion, Policy: domain.RuntimeSelfUpdatePolicy(command.Policy), PolicyDecisionID: command.PolicyDecisionId, ApprovedBy: command.ApprovedBy, LifecycleAction: domain.RuntimeComponentLifecycleAction(command.LifecycleAction), LifecycleProfile: protoLifecycleProfile(command.LifecycleProfile), DesiredImage: command.DesiredImage, DesiredStateHash: command.DesiredStateHash, InternalNetwork: command.InternalNetwork, EnvironmentFile: command.EnvironmentFile, ConfigFile: command.ConfigFile, OldServingComponentID: command.OldServingComponentId, PreserveVolumes: command.PreserveVolumes}
	for _, port := range command.PortPublishes {
		if port != nil {
			result.PortPublishes = append(result.PortPublishes, domain.ContainerPortPublish{HostIP: port.HostIp, HostPort: int(port.HostPort), ContainerPort: int(port.ContainerPort), Protocol: domain.NetworkProtocol(port.Protocol)})
		}
	}
	for _, port := range command.FinalPortPublishes {
		if port != nil {
			result.FinalPortPublishes = append(result.FinalPortPublishes, domain.ContainerPortPublish{HostIP: port.HostIp, HostPort: int(port.HostPort), ContainerPort: int(port.ContainerPort), Protocol: domain.NetworkProtocol(port.Protocol)})
		}
	}
	// RuntimeSelfUpdate validates this bounded, name-only field before passing
	// it to the worker. Copy it to avoid retaining the protobuf request slice.
	result.EdgeAppNetworks = append([]string(nil), command.EdgeAppNetworks...)
	return result
}

func protoLifecycleProfile(profile *runtimev1.RuntimeComponentLifecycleProfile) domain.RuntimeComponentLifecycleProfile {
	if profile == nil || profile.Uid < 0 || profile.Gid < 0 || uint64(profile.Uid) > uint64(^uint(0)>>1) || uint64(profile.Gid) > uint64(^uint(0)>>1) {
		return domain.RuntimeComponentLifecycleProfile{}
	}
	return domain.RuntimeComponentLifecycleProfile{
		ProcessIdentity:         domain.ComponentProcessIdentity{UID: int(profile.Uid), GID: int(profile.Gid), User: profile.User}, // #nosec G115 -- values are bounded to platform int above.
		UsernsMode:              profile.UsernsMode,
		CapDrop:                 append([]string(nil), profile.CapDrop...),
		NoNewPrivileges:         profile.NoNewPrivileges,
		GenerationVolumeOptions: append([]string(nil), profile.GenerationVolumeOptions...),
	}
}

func protoRuntimeCommandResult(result domain.RuntimeCommandResult) *runtimev1.RuntimeCommandResult {
	out := &runtimev1.RuntimeCommandResult{CommandId: string(result.CommandID), IdempotencyKey: result.IdempotencyKey, Generation: result.Generation, Status: string(result.Status), StartedAt: timestamppb.New(result.StartedAt), CompletedAt: timestamppb.New(result.CompletedAt)}
	if result.Error != nil {
		out.Error = &runtimev1.RuntimeCommandError{Code: result.Error.Code, Message: result.Error.Message, Retryable: result.Error.Retryable}
	}
	return out
}
