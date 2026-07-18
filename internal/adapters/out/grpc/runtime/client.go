package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"

	runtimev1 "github.com/bnema/gordon/api/gordon/runtime/v1"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Client adapts runtime gRPC calls to outbound runtime control boundaries.
type Client struct {
	client runtimev1.RuntimeServiceClient
}

var _ out.RuntimeStandaloneServiceManager = (*Client)(nil)
var _ out.RuntimeEnvironmentProbe = (*Client)(nil)

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

func (c *Client) ApplyStandaloneService(ctx context.Context, command domain.ApplyStandaloneServiceCommand) (domain.RuntimeCommandResult, error) {
	service, err := domainStandaloneService(command.Service)
	if err != nil {
		return domain.RuntimeCommandResult{}, err
	}
	resp, err := c.client.ApplyStandaloneService(ctx, &runtimev1.ApplyStandaloneServiceRequest{Command: &runtimev1.ApplyStandaloneServiceCommand{
		Identity:    domainIdentity(command.RuntimeCommandIdentity),
		Service:     service,
		ResolvedEnv: append([]string(nil), command.ResolvedEnv...),
		ConfigHash:  command.ConfigHash,
	}})
	if err != nil {
		return domain.RuntimeCommandResult{}, err
	}
	if resp == nil || resp.Result == nil {
		return domain.RuntimeCommandResult{}, fmt.Errorf("runtime response missing result")
	}
	return protoResult(resp.Result), nil
}

func (c *Client) RemoveStandaloneService(ctx context.Context, command domain.RemoveStandaloneServiceCommand) (domain.RuntimeCommandResult, error) {
	resp, err := c.client.RemoveStandaloneService(ctx, &runtimev1.RemoveStandaloneServiceRequest{Command: &runtimev1.RemoveStandaloneServiceCommand{
		Identity: domainIdentity(command.RuntimeCommandIdentity),
		Name:     command.Name,
		Reason:   command.Reason,
		Cleanup:  &runtimev1.StandaloneServiceCleanupSpec{PreserveVolumes: command.Cleanup.PreserveVolumes, RemoveContainer: command.Cleanup.RemoveContainer},
	}})
	if err != nil {
		return domain.RuntimeCommandResult{}, err
	}
	if resp == nil || resp.Result == nil {
		return domain.RuntimeCommandResult{}, fmt.Errorf("runtime response missing result")
	}
	return protoResult(resp.Result), nil
}

func (c *Client) ListStandaloneServiceState(ctx context.Context) ([]domain.RuntimeStandaloneServiceState, error) {
	resp, err := c.client.ListStandaloneServiceState(ctx, &runtimev1.ListStandaloneServiceStateRequest{})
	if err != nil {
		return nil, err
	}
	states := make([]domain.RuntimeStandaloneServiceState, 0, len(resp.GetServices()))
	for _, service := range resp.GetServices() {
		if service == nil {
			continue
		}
		states = append(states, protoStandaloneServiceState(service))
	}
	return states, nil
}

func (c *Client) SelfUpdateRuntime(ctx context.Context, command domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error) {
	if err := command.Validate(); err != nil {
		return domain.RuntimeCommandResult{}, err
	}
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

func (c *Client) ProbeRuntimeEnvironment(ctx context.Context) (out.RuntimeEnvironment, error) {
	resp, err := c.client.ProbeEnvironment(ctx, &runtimev1.ProbeEnvironmentRequest{})
	if err != nil {
		return out.RuntimeEnvironment{}, err
	}
	if resp == nil {
		return out.RuntimeEnvironment{}, fmt.Errorf("runtime response missing environment probe")
	}
	return out.RuntimeEnvironment{
		Engine:          resp.GetEngine(),
		Rootless:        resp.GetRootless(),
		APIReachable:    resp.GetApiReachable(),
		ImageAvailable:  resp.GetImageAvailable(),
		ImagePullable:   resp.GetImagePullable(),
		NetworkFeasible: resp.GetNetworkFeasible(),
		DiskAvailable:   resp.GetDiskAvailableBytes(),
		DiskSufficient:  resp.GetDiskSufficient(),
	}, nil
}

func (c *Client) ProbePublicListeners(ctx context.Context, ports []int) ([]bool, error) {
	_, available, err := c.ProbeRuntimeEnvironmentWithPublicListeners(ctx, ports)
	return available, err
}

// ProbeRuntimeEnvironmentWithPublicListeners obtains the complete read-only
// migration fact set in one authenticated RPC. Keeping environment and
// listener ownership together prevents preflight from observing different
// runtime states between separate calls.
func (c *Client) ProbeRuntimeEnvironmentWithPublicListeners(ctx context.Context, ports []int) (out.RuntimeEnvironment, []bool, error) {
	if len(ports) > 16 {
		return out.RuntimeEnvironment{}, nil, fmt.Errorf("too many public listeners")
	}
	required := make([]int32, len(ports))
	for i, port := range ports {
		if port < 1 || port > 65535 {
			return out.RuntimeEnvironment{}, nil, fmt.Errorf("invalid public listener")
		}
		required[i] = int32(port)
	}
	resp, err := c.client.ProbeEnvironment(ctx, &runtimev1.ProbeEnvironmentRequest{RequiredPublicPorts: required})
	if err != nil {
		return out.RuntimeEnvironment{}, nil, err
	}
	if resp == nil || len(resp.GetPublicListenersAvailable()) != len(ports) {
		return out.RuntimeEnvironment{}, nil, fmt.Errorf("runtime response missing listener availability")
	}
	return out.RuntimeEnvironment{
		Engine:          resp.GetEngine(),
		Rootless:        resp.GetRootless(),
		APIReachable:    resp.GetApiReachable(),
		ImageAvailable:  resp.GetImageAvailable(),
		ImagePullable:   resp.GetImagePullable(),
		NetworkFeasible: resp.GetNetworkFeasible(),
		DiskAvailable:   resp.GetDiskAvailableBytes(),
		DiskSufficient:  resp.GetDiskSufficient(),
	}, append([]bool(nil), resp.GetPublicListenersAvailable()...), nil
}

func (c *Client) SubscribeRuntimeState(ctx context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
	stream, err := c.client.WatchActualState(ctx, &runtimev1.WatchActualStateRequest{})
	if err != nil {
		return nil, err
	}
	// A stream is not a usable subscription until its source has published a
	// snapshot. Receiving it here makes authorization, source startup, and an
	// immediately closed stream visible to the caller instead of silently
	// turning them into a closed channel in the background pump.
	first, err := stream.Recv()
	if err != nil {
		return nil, sanitizedInitialRuntimeStateError(err)
	}
	out := make(chan domain.RuntimeActualStateSnapshot, 1)
	out <- protoActualStateSnapshot(first)
	go pumpActualStateStream(ctx, stream, out)
	return out, nil
}

func sanitizedInitialRuntimeStateError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	code := status.Code(err)
	return &out.RuntimeStateSubscriptionError{
		Retryable: code == codes.Unavailable || code == codes.ResourceExhausted || code == codes.DeadlineExceeded,
		Err:       status.Error(code, "receive initial runtime state"),
	}
}

// AcknowledgeRuntimeDrain is retained for the monolith compatibility boundary.
// Split deployments use AcknowledgeRouteDrain and never transport aliases.
func (c *Client) AcknowledgeRuntimeDrain(context.Context, string, uint64, string, string) error {
	return fmt.Errorf("alias-based runtime drain acknowledgement is not supported by the split protocol")
}

// AcknowledgeRouteDrain relays the validated opaque control acknowledgement.
// PrepareRouteDrain registers the exact control transition before edge reports
// can be relayed. It is idempotent at runtime.
func (c *Client) PrepareRouteDrain(ctx context.Context, canonicalDomain string, generation domain.RouteTargetGeneration, oldTargetKey domain.RouteTargetKey) error {
	_, err := c.client.PrepareEdgeDrain(ctx, &runtimev1.PrepareEdgeDrainRequest{
		CanonicalDomain: canonicalDomain, TransitionGeneration: uint64(generation), OldTargetKey: string(oldTargetKey),
	})
	return err
}

func (c *Client) AcknowledgeRouteDrain(ctx context.Context, acknowledgement domain.RouteDrainAck) error {
	if err := acknowledgement.Validate(); err != nil {
		return fmt.Errorf("validate route drain acknowledgement: %w", err)
	}
	_, err := c.client.ReportEdgeDrain(ctx, &runtimev1.ReportEdgeDrainRequest{
		CanonicalDomain:      acknowledgement.CanonicalDomain,
		TransitionGeneration: uint64(acknowledgement.TransitionGeneration),
		OldTargetKey:         string(acknowledgement.OldTargetKey),
		AcknowledgedAt:       timestamppb.New(acknowledgement.AcknowledgedAt),
		TimeoutReason:        string(acknowledgement.TimeoutReason),
	})
	return err
}

func (c *Client) ReadRouteLogs(ctx context.Context, routeDomain string, follow bool) (io.ReadCloser, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := c.client.StreamLogs(streamCtx, &runtimev1.StreamLogsRequest{RouteDomain: routeDomain, Follow: follow})
	if err != nil {
		cancel()
		return nil, err
	}
	if _, err := stream.Header(); err != nil {
		cancel()
		return nil, err
	}
	reader, writer := io.Pipe()
	go pumpLogStream(stream, writer)
	return runtimeLogReadCloser{reader: reader, cancel: cancel}, nil
}

func (c *Client) ListRuntimeVolumes(ctx context.Context) ([]*domain.VolumeInfo, error) {
	resp, err := c.client.ListVolumes(ctx, &runtimev1.ListVolumesRequest{})
	if err != nil {
		return nil, err
	}
	volumes := make([]*domain.VolumeInfo, 0, len(resp.GetVolumes()))
	for _, volume := range resp.GetVolumes() {
		volumes = append(volumes, protoVolumeInfo(volume))
	}
	return volumes, nil
}

func (c *Client) RemoveRuntimeVolume(ctx context.Context, volumeName string, force bool) error {
	_, err := c.client.RemoveVolume(ctx, &runtimev1.RemoveVolumeRequest{Name: volumeName, Force: force})
	return err
}

func (c *Client) ListRuntimeImages(ctx context.Context) ([]domain.RuntimeImageDetail, error) {
	resp, err := c.client.ListImages(ctx, &runtimev1.ListImagesRequest{})
	if err != nil {
		return nil, err
	}
	images := make([]domain.RuntimeImageDetail, 0, len(resp.GetImages()))
	for _, image := range resp.GetImages() {
		images = append(images, protoImageInfo(image))
	}
	return images, nil
}

func (c *Client) PruneRuntimeImages(ctx context.Context, danglingOnly bool) (domain.RuntimePruneResult, error) {
	resp, err := c.client.PruneImages(ctx, &runtimev1.PruneImagesRequest{DanglingOnly: danglingOnly})
	if err != nil {
		return domain.RuntimePruneResult{}, err
	}
	return domain.RuntimePruneResult{DeletedCount: int(resp.GetDeletedCount()), SpaceReclaimed: resp.GetSpaceReclaimed()}, nil
}

func pumpActualStateStream(ctx context.Context, stream runtimev1.RuntimeService_WatchActualStateClient, out chan<- domain.RuntimeActualStateSnapshot) {
	defer close(out)
	for {
		snapshot, recvErr := stream.Recv()
		if recvErr != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case out <- protoActualStateSnapshot(snapshot):
		}
	}
}

func pumpLogStream(stream runtimev1.RuntimeService_StreamLogsClient, writer *io.PipeWriter) {
	for {
		chunk, recvErr := stream.Recv()
		if recvErr != nil {
			if recvErr == io.EOF {
				_ = writer.Close()
			} else {
				_ = writer.CloseWithError(recvErr)
			}
			return
		}
		if len(chunk.GetData()) == 0 {
			continue
		}
		if _, err := writer.Write(chunk.GetData()); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
	}
}

type runtimeLogReadCloser struct {
	reader *io.PipeReader
	cancel context.CancelFunc
}

func (r runtimeLogReadCloser) Read(p []byte) (int, error) { return r.reader.Read(p) }

func (r runtimeLogReadCloser) Close() error {
	r.cancel()
	return r.reader.Close()
}

func protoActualStateSnapshot(snapshot *runtimev1.ActualStateSnapshot) domain.RuntimeActualStateSnapshot {
	if snapshot == nil {
		return domain.RuntimeActualStateSnapshot{}
	}
	out := domain.RuntimeActualStateSnapshot{Generation: snapshot.GetGeneration(), StateVersion: snapshot.GetStateVersion(), SourceComponentID: snapshot.GetSourceComponentId()}
	if snapshot.GetObservedAt() != nil {
		out.ObservedAt = snapshot.GetObservedAt().AsTime()
	}
	for _, route := range snapshot.GetRoutes() {
		out.Routes = append(out.Routes, domain.RuntimeRouteState{Domain: route.GetDomain(), Generation: route.GetGeneration(), RouteVersion: route.GetRouteVersion(), ContainerAlias: route.GetContainerAlias(), EdgeTargetAlias: route.GetEdgeTargetAlias(), TargetPort: int(route.GetTargetPort()), Scheme: route.GetScheme(), Protocol: domain.RouteTargetProtocol(route.GetProtocol()), Status: domain.RouteTargetStatus(route.GetStatus()), UnavailableReason: domain.RouteTargetUnavailableReason(route.GetUnavailableReason()), BackingContainerName: route.GetBackingContainerName()})
	}
	for _, container := range snapshot.GetContainers() {
		labels := domain.SanitizeRuntimeStateLabels(container.GetLabels())
		out.Containers = append(out.Containers, domain.RuntimeContainerState{Name: container.GetName(), Alias: container.GetAlias(), Image: container.GetImage(), ImageID: container.GetImageId(), Status: domain.ContainerStatus(container.GetStatus()), Labels: labels, Generation: container.GetGeneration()})
		if container.GetStartedAt() != nil {
			out.Containers[len(out.Containers)-1].StartedAt = container.GetStartedAt().AsTime()
		}
	}
	for _, network := range snapshot.GetNetworks() {
		out.Networks = append(out.Networks, domain.RuntimeNetworkState{Name: network.GetName(), Driver: network.GetDriver(), Internal: network.GetInternal(), Aliases: append([]string(nil), network.GetAliases()...), Generation: network.GetGeneration()})
	}
	for _, volume := range snapshot.GetVolumes() {
		out.Volumes = append(out.Volumes, domain.RuntimeVolumeState{Name: volume.GetName(), AttachedTo: append([]string(nil), volume.GetAttachedTo()...), Generation: volume.GetGeneration()})
	}
	for _, attachment := range snapshot.GetEdgeAttachments() {
		out.EdgeAttachments = append(out.EdgeAttachments, domain.RuntimeEdgeNetworkAttachmentState{RouteDomain: attachment.GetRouteDomain(), NetworkName: attachment.GetNetworkName(), EdgeAlias: attachment.GetEdgeAlias(), RuntimeAlias: attachment.GetRuntimeAlias(), TargetAlias: attachment.GetTargetAlias(), TargetPort: int(attachment.GetTargetPort()), Attached: attachment.GetAttached(), Generation: attachment.GetGeneration(), SourceComponent: attachment.GetSourceComponent()})
	}
	return out
}

func protoImageInfo(image *runtimev1.RuntimeImageInfo) domain.RuntimeImageDetail {
	if image == nil {
		return domain.RuntimeImageDetail{}
	}
	out := domain.RuntimeImageDetail{ID: image.Id, RepoTags: append([]string(nil), image.RepoTags...), Size: image.Size}
	if image.Created != nil {
		out.Created = image.Created.AsTime()
	}
	return out
}

func protoVolumeInfo(volume *runtimev1.RuntimeVolumeInfo) *domain.VolumeInfo {
	if volume == nil {
		return nil
	}
	out := &domain.VolumeInfo{Name: volume.Name, Driver: volume.Driver, MountPoint: volume.MountPoint, Size: volume.Size, InUse: volume.InUse, Containers: append([]string(nil), volume.Containers...), Labels: map[string]string{}}
	if volume.CreatedAt != nil {
		out.CreatedAt = volume.CreatedAt.AsTime()
	}
	maps.Copy(out.Labels, volume.Labels)
	return out
}

func domainIdentity(identity domain.RuntimeCommandIdentity) *runtimev1.RuntimeCommandIdentity {
	return &runtimev1.RuntimeCommandIdentity{Id: string(identity.ID), IdempotencyKey: identity.IdempotencyKey, Generation: identity.Generation, SourceComponentId: identity.SourceComponentID, RequestedAt: timestamppb.New(identity.RequestedAt)}
}

func domainDeployRoute(command domain.DeployRouteCommand) *runtimev1.DeployRouteCommand {
	return &runtimev1.DeployRouteCommand{Identity: domainIdentity(command.RuntimeCommandIdentity), Domain: command.Domain, Image: command.Image, RouteVersion: command.RouteVersion, Env: command.Env, InternalDeploy: command.InternalDeploy}
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

func domainStandaloneService(service domain.StandaloneService) (*runtimev1.StandaloneServiceSpec, error) {
	out := &runtimev1.StandaloneServiceSpec{
		Name:    service.Name,
		Image:   service.Image,
		Enabled: service.Enabled,
		Readiness: &runtimev1.StandaloneServiceReadinessSpec{
			Type:       service.Readiness.Type,
			Path:       service.Readiness.Path,
			Contains:   service.Readiness.Contains,
			TimeoutNs:  int64(service.Readiness.Timeout),
			TimeoutSet: service.Readiness.TimeoutSet,
		},
		Cleanup: &runtimev1.StandaloneServiceCleanupSpec{
			PreserveVolumes: service.Cleanup.PreserveVolumes,
			RemoveContainer: service.Cleanup.RemoveContainer,
		},
	}
	for _, port := range service.Ports {
		if port.Container > maxInt32 || port.Container < minInt32 {
			return nil, fmt.Errorf("standalone service port %q container port %d overflows int32", port.Name, port.Container)
		}
		out.Ports = append(out.Ports, &runtimev1.StandaloneServicePortSpec{
			Name:         port.Name,
			Container:    int32(port.Container),
			Protocol:     string(port.Protocol),
			Publish:      port.Publish,
			Private:      port.Private,
			Public:       port.Public,
			TrustedCidrs: append([]string(nil), port.TrustedCIDRs...),
		})
	}
	for _, volume := range service.Volumes {
		out.Volumes = append(out.Volumes, &runtimev1.StandaloneServiceVolumeSpec{
			Source:   volume.Source,
			Target:   volume.Target,
			ReadOnly: volume.ReadOnly,
		})
	}
	return out, nil
}

func protoStandaloneServiceState(service *runtimev1.RuntimeStandaloneServiceState) domain.RuntimeStandaloneServiceState {
	if service == nil {
		return domain.RuntimeStandaloneServiceState{}
	}
	return domain.RuntimeStandaloneServiceState{
		Name:          service.GetName(),
		ContainerID:   service.GetContainerId(),
		ContainerName: service.GetContainerName(),
		Status:        domain.ContainerStatus(service.GetStatus()),
		ConfigHash:    service.GetConfigHash(),
		Cleanup: domain.StandaloneServiceCleanup{
			PreserveVolumes: service.GetCleanup().GetPreserveVolumes(),
			RemoveContainer: service.GetCleanup().GetRemoveContainer(),
		},
	}
}

func domainSelfUpdate(command domain.RuntimeSelfUpdateCommand) *runtimev1.RuntimeSelfUpdateCommand {
	result := &runtimev1.RuntimeSelfUpdateCommand{Identity: domainIdentity(command.RuntimeCommandIdentity), TargetComponentId: command.TargetComponentID, TargetComponentRole: string(command.TargetComponentRole), CurrentVersion: command.CurrentVersion, TargetVersion: command.TargetVersion, Policy: string(command.Policy), PolicyDecisionId: command.PolicyDecisionID, ApprovedBy: command.ApprovedBy, LifecycleAction: string(command.LifecycleAction), DesiredImage: command.DesiredImage, DesiredStateHash: command.DesiredStateHash, InternalNetwork: command.InternalNetwork, EnvironmentFile: command.EnvironmentFile, ConfigFile: command.ConfigFile, OldServingComponentId: command.OldServingComponentID, PreserveVolumes: command.PreserveVolumes}
	for _, port := range command.PortPublishes {
		if !validProtoComponentPort(port) {
			continue
		}
		// #nosec G115 -- validProtoComponentPort bounds both values to int32.
		result.PortPublishes = append(result.PortPublishes, &runtimev1.ComponentPortBinding{HostIp: port.HostIP, HostPort: int32(port.HostPort), ContainerPort: int32(port.ContainerPort), Protocol: string(port.Protocol)})
	}
	for _, port := range command.FinalPortPublishes {
		if !validProtoComponentPort(port) {
			continue
		}
		// #nosec G115 -- validProtoComponentPort bounds both values to int32.
		result.FinalPortPublishes = append(result.FinalPortPublishes, &runtimev1.ComponentPortBinding{HostIp: port.HostIP, HostPort: int32(port.HostPort), ContainerPort: int32(port.ContainerPort), Protocol: string(port.Protocol)})
	}
	// Validation in SelfUpdateRuntime has already bounded and sanitized these
	// names. Copy them so no caller-owned slice crosses the RPC boundary.
	result.EdgeAppNetworks = append([]string(nil), command.EdgeAppNetworks...)
	return result
}

func validProtoComponentPort(port domain.ContainerPortPublish) bool {
	return port.HostPort >= 0 && port.HostPort <= maxInt32 && port.ContainerPort >= 0 && port.ContainerPort <= maxInt32
}

func responseResult(resp *runtimev1.ApplyCommandResponse, err error) (domain.RuntimeCommandResult, error) {
	if err != nil {
		return domain.RuntimeCommandResult{}, err
	}
	if resp == nil || resp.Result == nil {
		return domain.RuntimeCommandResult{}, fmt.Errorf("runtime response missing result")
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
