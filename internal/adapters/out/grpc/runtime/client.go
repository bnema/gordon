package runtime

import (
	"context"
	"fmt"

	runtimev1 "github.com/bnema/gordon/api/gordon/runtime/v1"
	"github.com/bnema/gordon/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Client adapts runtime gRPC calls to outbound runtime control boundaries.
type Client struct {
	client runtimev1.RuntimeServiceClient
}

func NewClient(conn grpc.ClientConnInterface) *Client {
	return &Client{client: runtimev1.NewRuntimeServiceClient(conn)}
}

func NewClientWithRuntimeService(client runtimev1.RuntimeServiceClient) *Client {
	return &Client{client: client}
}

func (c *Client) DeployRoute(ctx context.Context, command domain.DeployRouteCommand) (domain.RuntimeCommandResult, error) {
	resp, err := c.client.ApplyCommand(ctx, &runtimev1.ApplyCommandRequest{Command: &runtimev1.ApplyCommandRequest_DeployRoute{DeployRoute: domainDeployRoute(command)}})
	return responseResult(resp, err)
}

func (c *Client) RestartRoute(ctx context.Context, command domain.RestartRouteCommand) (domain.RuntimeCommandResult, error) {
	resp, err := c.client.ApplyCommand(ctx, &runtimev1.ApplyCommandRequest{Command: &runtimev1.ApplyCommandRequest_RestartRoute{RestartRoute: domainRestartRoute(command)}})
	return responseResult(resp, err)
}

func (c *Client) RemoveRoute(ctx context.Context, command domain.RemoveRouteCommand) (domain.RuntimeCommandResult, error) {
	resp, err := c.client.ApplyCommand(ctx, &runtimev1.ApplyCommandRequest{Command: &runtimev1.ApplyCommandRequest_RemoveRoute{RemoveRoute: domainRemoveRoute(command)}})
	return responseResult(resp, err)
}

func (c *Client) Reconcile(ctx context.Context, command domain.ReconcileRuntimeCommand) (domain.RuntimeCommandResult, error) {
	if command.ExpectedRouteCount > maxInt32 || command.ExpectedRouteCount < minInt32 {
		return domain.RuntimeCommandResult{}, fmt.Errorf("expected route count %d overflows int32", command.ExpectedRouteCount)
	}
	expectedRouteCount := int32(command.ExpectedRouteCount)
	resp, err := c.client.ApplyCommand(ctx, &runtimev1.ApplyCommandRequest{Command: &runtimev1.ApplyCommandRequest_Reconcile{Reconcile: domainReconcile(command, expectedRouteCount)}})
	return responseResult(resp, err)
}

func (c *Client) SelfUpdateRuntime(ctx context.Context, command domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error) {
	resp, err := c.client.RuntimeSelfUpdate(ctx, &runtimev1.RuntimeSelfUpdateRequest{Command: domainSelfUpdate(command)})
	return responseResult(resp, err)
}

func (c *Client) PingRuntime(ctx context.Context) error {
	_, err := c.client.GetHealth(ctx, &runtimev1.GetHealthRequest{})
	return err
}

func (c *Client) RuntimeVersion(ctx context.Context) (string, error) {
	resp, err := c.client.GetHealth(ctx, &runtimev1.GetHealthRequest{})
	if err != nil {
		return "", err
	}
	return resp.Message, nil
}

func (c *Client) AcknowledgeRuntimeDrain(ctx context.Context, routeDomain string, generation uint64) error {
	_, err := c.client.ReportEdgeDrain(ctx, &runtimev1.ReportEdgeDrainRequest{RouteDomain: routeDomain, Generation: generation})
	return err
}

func domainIdentity(identity domain.RuntimeCommandIdentity) *runtimev1.RuntimeCommandIdentity {
	return &runtimev1.RuntimeCommandIdentity{Id: string(identity.ID), IdempotencyKey: identity.IdempotencyKey, Generation: identity.Generation, SourceComponentId: identity.SourceComponentID, RequestedAt: timestamppb.New(identity.RequestedAt)}
}

func domainDeployRoute(command domain.DeployRouteCommand) *runtimev1.DeployRouteCommand {
	return &runtimev1.DeployRouteCommand{Identity: domainIdentity(command.RuntimeCommandIdentity), Domain: command.Domain, Image: command.Image, RouteVersion: command.RouteVersion, Env: command.Env}
}

func domainRestartRoute(command domain.RestartRouteCommand) *runtimev1.RestartRouteCommand {
	return &runtimev1.RestartRouteCommand{Identity: domainIdentity(command.RuntimeCommandIdentity), Domain: command.Domain, Reason: command.Reason, WithAttachments: command.WithAttachments}
}

func domainRemoveRoute(command domain.RemoveRouteCommand) *runtimev1.RemoveRouteCommand {
	return &runtimev1.RemoveRouteCommand{Identity: domainIdentity(command.RuntimeCommandIdentity), Domain: command.Domain, Force: command.Force}
}

const (
	maxInt32 = 1<<31 - 1
	minInt32 = -1 << 31
)

func domainReconcile(command domain.ReconcileRuntimeCommand, expectedRouteCount int32) *runtimev1.ReconcileRuntimeCommand {
	routes := make([]*runtimev1.RuntimeRouteDesiredState, 0, len(command.DesiredRoutes))
	for _, route := range command.DesiredRoutes {
		routes = append(routes, &runtimev1.RuntimeRouteDesiredState{Domain: route.Domain, Image: route.Image, Env: route.Env})
	}
	return &runtimev1.ReconcileRuntimeCommand{Identity: domainIdentity(command.RuntimeCommandIdentity), Reason: command.Reason, ExpectedRouteCount: expectedRouteCount, DesiredStateVersion: command.DesiredStateVersion, DesiredRoutes: routes}
}

func domainSelfUpdate(command domain.RuntimeSelfUpdateCommand) *runtimev1.RuntimeSelfUpdateCommand {
	return &runtimev1.RuntimeSelfUpdateCommand{Identity: domainIdentity(command.RuntimeCommandIdentity), TargetComponentId: command.TargetComponentID, TargetComponentRole: string(command.TargetComponentRole), CurrentVersion: command.CurrentVersion, TargetVersion: command.TargetVersion, Policy: string(command.Policy), PolicyDecisionId: command.PolicyDecisionID, ApprovedBy: command.ApprovedBy}
}

func responseResult(resp *runtimev1.ApplyCommandResponse, err error) (domain.RuntimeCommandResult, error) {
	if err != nil {
		return domain.RuntimeCommandResult{}, err
	}
	if resp == nil || resp.Result == nil {
		return domain.RuntimeCommandResult{}, nil
	}
	return protoResult(resp.Result), nil
}

func protoResult(result *runtimev1.RuntimeCommandResult) domain.RuntimeCommandResult {
	out := domain.RuntimeCommandResult{CommandID: domain.RuntimeCommandID(result.CommandId), IdempotencyKey: result.IdempotencyKey, Generation: result.Generation, Status: domain.RuntimeCommandStatus(result.Status)}
	if result.StartedAt != nil {
		out.StartedAt = result.StartedAt.AsTime()
	}
	if result.CompletedAt != nil {
		out.CompletedAt = result.CompletedAt.AsTime()
	}
	if result.Error != nil {
		out.Error = &domain.RuntimeCommandError{Code: result.Error.Code, Message: result.Error.Message, Retryable: result.Error.Retryable}
	}
	return out
}
