package runtime

import (
	"context"
	"strings"

	commonv1 "github.com/bnema/gordon/api/gordon/common/v1"
	runtimev1 "github.com/bnema/gordon/api/gordon/runtime/v1"
	boundaries "github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server adapts runtime gRPC requests to the inbound RuntimeWorker boundary.
type Server struct {
	runtimev1.UnimplementedRuntimeServiceServer
	worker      boundaries.RuntimeWorker
	componentID string
}

func NewServer(worker boundaries.RuntimeWorker, componentID string) *Server {
	return &Server{worker: worker, componentID: componentID}
}

func MethodScopes() map[string]domain.ComponentScope {
	return map[string]domain.ComponentScope{
		runtimev1.RuntimeService_ApplyCommand_FullMethodName:      domain.ComponentScopeRuntimeDeploy,
		runtimev1.RuntimeService_WatchActualState_FullMethodName:  domain.ComponentScopeRuntimeStatus,
		runtimev1.RuntimeService_GetHealth_FullMethodName:         domain.ComponentScopeRuntimeStatus,
		runtimev1.RuntimeService_StreamLogs_FullMethodName:        domain.ComponentScopeRuntimeLogs,
		runtimev1.RuntimeService_RuntimeSelfUpdate_FullMethodName: domain.ComponentScopeRuntimeSelfUpdate,
		runtimev1.RuntimeService_ReportEdgeDrain_FullMethodName:   domain.ComponentScopeRuntimeDrainAck,
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
		result, err = s.worker.Reconcile(ctx, protoReconcile(command.Reconcile))
	default:
		return nil, status.Error(codes.InvalidArgument, "runtime command is required")
	}
	if err != nil {
		return nil, err
	}
	return &runtimev1.ApplyCommandResponse{Result: protoRuntimeCommandResult(result)}, nil
}

func (s *Server) RuntimeSelfUpdate(ctx context.Context, req *runtimev1.RuntimeSelfUpdateRequest) (*runtimev1.ApplyCommandResponse, error) {
	if s.worker == nil {
		return nil, status.Error(codes.FailedPrecondition, "runtime worker not configured")
	}
	if req == nil || req.Command == nil {
		return nil, status.Error(codes.InvalidArgument, "runtime self-update command is required")
	}
	result, err := s.worker.SelfUpdate(ctx, protoSelfUpdate(req.Command))
	if err != nil {
		return nil, err
	}
	return &runtimev1.ApplyCommandResponse{Result: protoRuntimeCommandResult(result)}, nil
}

func (s *Server) GetHealth(context.Context, *runtimev1.GetHealthRequest) (*runtimev1.GetHealthResponse, error) {
	return &runtimev1.GetHealthResponse{Ok: s.worker != nil, ComponentId: s.componentID, Message: "runtime service ready"}, nil
}

func (s *Server) WatchActualState(*runtimev1.WatchActualStateRequest, runtimev1.RuntimeService_WatchActualStateServer) error {
	return status.Error(codes.Unimplemented, "runtime actual-state streaming is not wired yet")
}

func (s *Server) StreamLogs(*runtimev1.StreamLogsRequest, runtimev1.RuntimeService_StreamLogsServer) error {
	return status.Error(codes.Unimplemented, "runtime log streaming is not wired yet")
}

func (s *Server) ReportEdgeDrain(ctx context.Context, req *runtimev1.ReportEdgeDrainRequest) (*commonv1.Ack, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req == nil || strings.TrimSpace(req.RouteDomain) == "" || req.Generation == 0 {
		return nil, status.Error(codes.InvalidArgument, "route domain and generation are required")
	}
	return &commonv1.Ack{Ok: true, Message: "edge drain acknowledgement recorded"}, nil
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
	return domain.DeployRouteCommand{RuntimeCommandIdentity: protoIdentity(command.Identity), Domain: command.Domain, Image: command.Image, RouteVersion: command.RouteVersion, Env: command.Env}
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

func protoSelfUpdate(command *runtimev1.RuntimeSelfUpdateCommand) domain.RuntimeSelfUpdateCommand {
	if command == nil {
		return domain.RuntimeSelfUpdateCommand{}
	}
	return domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: protoIdentity(command.Identity), TargetComponentID: command.TargetComponentId, TargetComponentRole: domain.ComponentRole(command.TargetComponentRole), CurrentVersion: command.CurrentVersion, TargetVersion: command.TargetVersion, Policy: domain.RuntimeSelfUpdatePolicy(command.Policy), PolicyDecisionID: command.PolicyDecisionId, ApprovedBy: command.ApprovedBy}
}

func protoRuntimeCommandResult(result domain.RuntimeCommandResult) *runtimev1.RuntimeCommandResult {
	out := &runtimev1.RuntimeCommandResult{CommandId: string(result.CommandID), IdempotencyKey: result.IdempotencyKey, Generation: result.Generation, Status: string(result.Status), StartedAt: timestamppb.New(result.StartedAt), CompletedAt: timestamppb.New(result.CompletedAt)}
	if result.Error != nil {
		out.Error = &runtimev1.RuntimeCommandError{Code: result.Error.Code, Message: result.Error.Message, Retryable: result.Error.Retryable}
	}
	return out
}
