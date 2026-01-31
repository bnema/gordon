// This server exposes administrative APIs over gRPC.
package grpcadmin

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/grpc/auth"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	gordon "github.com/bnema/gordon/internal/grpc"
	"github.com/bnema/gordon/pkg/validation"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// maxLogLines is the maximum allowed number of log lines requested.
const maxLogLines = 10000

// Server implements the AdminService gRPC interface.
type Server struct {
	configSvc    in.ConfigService
	authSvc      in.AuthService
	containerSvc in.ContainerService
	healthSvc    in.HealthService
	secretSvc    in.SecretService
	logSvc       in.LogService
	registrySvc  in.RegistryService
	eventBus     out.EventPublisher
	log          zerowrap.Logger
}

// NewServer creates a new admin gRPC server.
func NewServer(
	configSvc in.ConfigService,
	authSvc in.AuthService,
	containerSvc in.ContainerService,
	healthSvc in.HealthService,
	secretSvc in.SecretService,
	logSvc in.LogService,
	registrySvc in.RegistryService,
	eventBus out.EventPublisher,
	log zerowrap.Logger,
) *Server {
	return &Server{
		configSvc:    configSvc,
		authSvc:      authSvc,
		containerSvc: containerSvc,
		healthSvc:    healthSvc,
		secretSvc:    secretSvc,
		logSvc:       logSvc,
		registrySvc:  registrySvc,
		eventBus:     eventBus,
		log:          log,
	}
}

// ListRoutes returns all configured routes.
func (s *Server) ListRoutes(ctx context.Context, req *gordon.ListRoutesRequest) (*gordon.ListRoutesResponse, error) {
	if err := auth.CheckAccess(ctx, domain.AdminResourceRoutes, domain.AdminActionRead); err != nil {
		return nil, err
	}

	routes := s.configSvc.GetRoutes(ctx)
	resp := &gordon.ListRoutesResponse{Routes: make([]*gordon.AdminRoute, 0, len(routes))}

	for _, route := range routes {
		resp.Routes = append(resp.Routes, &gordon.AdminRoute{
			Domain: route.Domain,
			Image:  route.Image,
			Https:  route.HTTPS,
		})
	}

	if req.Detailed {
		infos := s.containerSvc.ListRoutesWithDetails(ctx)
		resp.RouteInfos = make([]*gordon.RouteInfo, 0, len(infos))
		for _, info := range infos {
			resp.RouteInfos = append(resp.RouteInfos, toProtoRouteInfo(info))
		}
	}

	return resp, nil
}

// GetRoute returns a specific route.
func (s *Server) GetRoute(ctx context.Context, req *gordon.GetRouteRequest) (*gordon.AdminRoute, error) {
	if err := auth.CheckAccess(ctx, domain.AdminResourceRoutes, domain.AdminActionRead); err != nil {
		return nil, err
	}

	route, err := s.configSvc.GetRoute(ctx, req.Domain)
	if err != nil {
		if errors.Is(err, domain.ErrRouteNotFound) {
			return nil, status.Error(codes.NotFound, "route not found")
		}
		return nil, status.Error(codes.Internal, "failed to get route")
	}

	return &gordon.AdminRoute{
		Domain: route.Domain,
		Image:  route.Image,
		Https:  route.HTTPS,
	}, nil
}

// AddRoute creates a new route.
func (s *Server) AddRoute(ctx context.Context, req *gordon.AddRouteRequest) (*gordon.AdminRoute, error) {
	if err := auth.CheckAccess(ctx, domain.AdminResourceRoutes, domain.AdminActionWrite); err != nil {
		return nil, err
	}

	if req.Route == nil {
		return nil, status.Error(codes.InvalidArgument, "route is required")
	}

	route := domain.Route{
		Domain: req.Route.Domain,
		Image:  req.Route.Image,
		HTTPS:  req.Route.Https,
	}

	if err := s.validateRouteImage(ctx, route.Image); err != nil {
		return nil, err
	}

	if err := s.configSvc.AddRoute(ctx, route); err != nil {
		switch {
		case errors.Is(err, domain.ErrRouteDomainEmpty), errors.Is(err, domain.ErrRouteImageEmpty):
			return nil, status.Error(codes.InvalidArgument, err.Error())
		case errors.Is(err, domain.ErrRouteExists):
			return nil, status.Error(codes.AlreadyExists, "route already exists")
		default:
			return nil, status.Error(codes.Internal, "failed to add route")
		}
	}

	return req.Route, nil
}

// UpdateRoute modifies an existing route.
func (s *Server) UpdateRoute(ctx context.Context, req *gordon.UpdateRouteRequest) (*gordon.AdminRoute, error) {
	if err := auth.CheckAccess(ctx, domain.AdminResourceRoutes, domain.AdminActionWrite); err != nil {
		return nil, err
	}

	if req.Route == nil {
		return nil, status.Error(codes.InvalidArgument, "route is required")
	}

	route := domain.Route{
		Domain: req.Domain,
		Image:  req.Route.Image,
		HTTPS:  req.Route.Https,
	}

	if err := s.validateRouteImage(ctx, route.Image); err != nil {
		return nil, err
	}

	if err := s.configSvc.UpdateRoute(ctx, route); err != nil {
		switch {
		case errors.Is(err, domain.ErrRouteNotFound):
			return nil, status.Error(codes.NotFound, "route not found")
		case errors.Is(err, domain.ErrRouteDomainEmpty), errors.Is(err, domain.ErrRouteImageEmpty):
			return nil, status.Error(codes.InvalidArgument, err.Error())
		default:
			return nil, status.Error(codes.Internal, "failed to update route")
		}
	}

	return &gordon.AdminRoute{Domain: route.Domain, Image: route.Image, Https: route.HTTPS}, nil
}

// RemoveRoute deletes a route.
func (s *Server) RemoveRoute(ctx context.Context, req *gordon.RemoveRouteRequest) (*gordon.RemoveRouteResponse, error) {
	if err := auth.CheckAccess(ctx, domain.AdminResourceRoutes, domain.AdminActionWrite); err != nil {
		return nil, err
	}

	if err := s.configSvc.RemoveRoute(ctx, req.Domain); err != nil {
		if errors.Is(err, domain.ErrRouteNotFound) {
			return nil, status.Error(codes.NotFound, "route not found")
		}
		return nil, status.Error(codes.Internal, "failed to remove route")
	}

	return &gordon.RemoveRouteResponse{Success: true}, nil
}

// ListSecrets returns all secrets for a domain.
func (s *Server) ListSecrets(ctx context.Context, req *gordon.ListSecretsRequest) (*gordon.ListSecretsResponse, error) {
	if err := auth.CheckAccess(ctx, domain.AdminResourceSecrets, domain.AdminActionRead); err != nil {
		return nil, err
	}

	keys, attachments, err := s.secretSvc.ListKeysWithAttachments(ctx, req.Domain)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid domain")
	}

	attachmentResponses := make([]*gordon.AttachmentSecrets, 0, len(attachments))
	for _, attachment := range attachments {
		attachmentResponses = append(attachmentResponses, &gordon.AttachmentSecrets{
			Service: attachment.Service,
			Keys:    attachment.Keys,
		})
	}

	return &gordon.ListSecretsResponse{
		Domain:      req.Domain,
		Keys:        keys,
		Attachments: attachmentResponses,
	}, nil
}

// SetSecrets sets or updates secrets for a domain.
func (s *Server) SetSecrets(ctx context.Context, req *gordon.SetSecretsRequest) (*gordon.SetSecretsResponse, error) {
	if err := auth.CheckAccess(ctx, domain.AdminResourceSecrets, domain.AdminActionWrite); err != nil {
		return nil, err
	}

	if err := s.secretSvc.Set(ctx, req.Domain, req.Secrets); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid domain")
	}

	return &gordon.SetSecretsResponse{Success: true}, nil
}

// DeleteSecret removes a secret key from a domain.
func (s *Server) DeleteSecret(ctx context.Context, req *gordon.DeleteSecretRequest) (*gordon.DeleteSecretResponse, error) {
	if err := auth.CheckAccess(ctx, domain.AdminResourceSecrets, domain.AdminActionWrite); err != nil {
		return nil, err
	}

	if err := s.secretSvc.Delete(ctx, req.Domain, req.Key); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid domain")
	}

	return &gordon.DeleteSecretResponse{Success: true}, nil
}

// GetProcessLogs streams process logs.
func (s *Server) GetProcessLogs(req *gordon.GetProcessLogsRequest, stream gordon.AdminService_GetProcessLogsServer) error {
	ctx := stream.Context()
	if err := auth.CheckAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead); err != nil {
		return err
	}

	if s.logSvc == nil {
		return status.Error(codes.Unavailable, "log service not available")
	}

	lines := clampLogLines(int(req.Lines))
	if req.Follow {
		ch, err := s.logSvc.FollowProcessLogs(ctx, lines)
		if err != nil {
			return status.Error(codes.Internal, "failed to follow process logs")
		}
		return streamLogLines(ctx, ch, "process", stream.Send)
	}

	entries, err := s.logSvc.GetProcessLogs(ctx, lines)
	if err != nil {
		return status.Error(codes.Internal, "failed to get process logs")
	}

	for _, line := range entries {
		if err := stream.Send(&gordon.LogEntry{Line: line, Source: "process"}); err != nil {
			return err
		}
	}

	return nil
}

// GetContainerLogs streams container logs.
func (s *Server) GetContainerLogs(req *gordon.GetContainerLogsRequest, stream gordon.AdminService_GetContainerLogsServer) error {
	ctx := stream.Context()
	if err := auth.CheckAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead); err != nil {
		return err
	}

	if s.logSvc == nil {
		return status.Error(codes.Unavailable, "log service not available")
	}

	lines := clampLogLines(int(req.Lines))
	if req.Follow {
		ch, err := s.logSvc.FollowContainerLogs(ctx, req.Domain, lines)
		if err != nil {
			return status.Error(codes.Internal, "failed to follow container logs")
		}
		return streamLogLines(ctx, ch, req.Domain, stream.Send)
	}

	entries, err := s.logSvc.GetContainerLogs(ctx, req.Domain, lines)
	if err != nil {
		return status.Error(codes.Internal, "failed to get container logs")
	}

	for _, line := range entries {
		if err := stream.Send(&gordon.LogEntry{Line: line, Source: req.Domain}); err != nil {
			return err
		}
	}

	return nil
}

// GetStatus returns basic system status.
func (s *Server) GetStatus(ctx context.Context, _ *gordon.GetStatusRequest) (*gordon.StatusResponse, error) {
	if err := auth.CheckAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead); err != nil {
		return nil, err
	}

	routes := s.configSvc.GetRoutes(ctx)
	containers := s.containerSvc.List(ctx)

	containerStatuses := make(map[string]string, len(routes))
	for _, route := range routes {
		statusValue := "unknown"
		container, ok := s.containerSvc.Get(ctx, route.Domain)
		if ok && container != nil {
			statusValue = container.Status
		}
		containerStatuses[route.Domain] = statusValue
	}

	authEnabled := false
	if s.authSvc != nil {
		authEnabled = s.authSvc.IsEnabled()
	}

	return &gordon.StatusResponse{
		RouteCount:        safeInt32(int64(len(routes))),
		ContainerCount:    safeInt32(int64(len(containers))),
		RegistryDomain:    s.configSvc.GetRegistryDomain(),
		AuthEnabled:       authEnabled,
		RegistryPort:      safeInt32(int64(s.configSvc.GetRegistryPort())),
		ServerPort:        safeInt32(int64(s.configSvc.GetServerPort())),
		AutoRoute:         s.configSvc.IsAutoRouteEnabled(),
		NetworkIsolation:  s.configSvc.IsNetworkIsolationEnabled(),
		ContainerStatuses: containerStatuses,
	}, nil
}

// GetHealth returns route health statuses.
func (s *Server) GetHealth(ctx context.Context, _ *gordon.GetHealthRequest) (*gordon.HealthResponse, error) {
	if err := auth.CheckAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead); err != nil {
		return nil, err
	}

	if s.healthSvc == nil {
		return nil, status.Error(codes.Unavailable, "health service not available")
	}

	health := s.healthSvc.CheckAllRoutes(ctx)
	response := make(map[string]*gordon.RouteHealth, len(health))

	for domainName, healthStatus := range health {
		if healthStatus == nil {
			continue
		}
		response[domainName] = &gordon.RouteHealth{
			Healthy:        healthStatus.Healthy,
			ResponseTimeMs: safeInt32(healthStatus.ResponseTimeMs),
			Error:          healthStatus.Error,
		}
	}

	return &gordon.HealthResponse{Status: "ok", Routes: response}, nil
}

// GetConfig returns the full configuration as JSON bytes.
func (s *Server) GetConfig(ctx context.Context, _ *gordon.GetConfigRequest) (*gordon.ConfigResponse, error) {
	if err := auth.CheckAccess(ctx, domain.AdminResourceConfig, domain.AdminActionRead); err != nil {
		return nil, err
	}

	routes := s.configSvc.GetRoutes(ctx)
	routeResponses := make([]dto.Route, 0, len(routes))
	for _, route := range routes {
		routeResponses = append(routeResponses, dto.Route{Domain: route.Domain, Image: route.Image, HTTPS: route.HTTPS})
	}

	externalRoutes := s.configSvc.GetExternalRoutes()
	externalResponses := make([]dto.ExternalRoute, 0, len(externalRoutes))
	for domainName, target := range externalRoutes {
		externalResponses = append(externalResponses, dto.ExternalRoute{Domain: domainName, Target: target})
	}

	config := dto.ConfigResponse{
		Server: dto.ServerConfig{
			Port:           s.configSvc.GetServerPort(),
			RegistryPort:   s.configSvc.GetRegistryPort(),
			RegistryDomain: s.configSvc.GetRegistryDomain(),
			DataDir:        s.configSvc.GetDataDir(),
		},
		AutoRoute: dto.AutoRouteConfig{Enabled: s.configSvc.IsAutoRouteEnabled()},
		NetworkIsolation: dto.NetworkIsolationConfig{
			Enabled: s.configSvc.IsNetworkIsolationEnabled(),
			Prefix:  s.configSvc.GetNetworkPrefix(),
		},
		Routes:         routeResponses,
		ExternalRoutes: externalResponses,
	}

	configJSON, err := json.Marshal(config)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to marshal config")
	}

	return &gordon.ConfigResponse{ConfigJson: configJSON}, nil
}

// Reload triggers configuration reload.
func (s *Server) Reload(ctx context.Context, _ *gordon.ReloadRequest) (*gordon.ReloadResponse, error) {
	if err := auth.CheckAccess(ctx, domain.AdminResourceConfig, domain.AdminActionWrite); err != nil {
		return nil, err
	}

	if err := s.configSvc.Reload(ctx); err != nil {
		s.log.Error().Err(err).Msg("config reload failed")
		return &gordon.ReloadResponse{Success: false, Message: err.Error()}, nil
	}

	if s.eventBus != nil {
		if err := s.eventBus.Publish(domain.EventManualReload, nil); err != nil {
			s.log.Warn().Err(err).Msg("failed to publish manual reload event")
		}
	}

	return &gordon.ReloadResponse{Success: true, Message: "configuration reloaded"}, nil
}

// Deploy triggers a deployment for a domain.
func (s *Server) Deploy(ctx context.Context, req *gordon.DeployRequest) (*gordon.DeployResponse, error) {
	if err := auth.CheckAccess(ctx, domain.AdminResourceConfig, domain.AdminActionWrite); err != nil {
		return nil, err
	}

	route, err := s.configSvc.GetRoute(ctx, req.Domain)
	if err != nil {
		return nil, status.Error(codes.NotFound, "route not found")
	}

	container, err := s.containerSvc.Deploy(ctx, *route)
	if err != nil {
		s.log.Error().Err(err).Str("domain", req.Domain).Msg("deployment failed")
		return &gordon.DeployResponse{Success: false, Message: "failed to deploy container"}, nil
	}

	return &gordon.DeployResponse{Success: true, ContainerId: container.ID, Message: "deployment triggered"}, nil
}

// ListNetworks returns managed network information.
func (s *Server) ListNetworks(ctx context.Context, _ *gordon.ListNetworksRequest) (*gordon.ListNetworksResponse, error) {
	if err := auth.CheckAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead); err != nil {
		return nil, err
	}

	networks, err := s.containerSvc.ListNetworks(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to list networks")
	}

	resp := &gordon.ListNetworksResponse{Networks: make([]*gordon.Network, 0, len(networks))}
	for _, network := range networks {
		if network == nil {
			continue
		}
		resp.Networks = append(resp.Networks, &gordon.Network{
			Name:           network.Name,
			Driver:         network.Driver,
			Subnet:         "",
			ContainerCount: safeInt32(int64(len(network.Containers))),
		})
	}

	return resp, nil
}

// GetAttachments returns attachment configuration for a target or all.
func (s *Server) GetAttachments(ctx context.Context, req *gordon.GetAttachmentsRequest) (*gordon.AttachmentsResponse, error) {
	if err := auth.CheckAccess(ctx, domain.AdminResourceConfig, domain.AdminActionRead); err != nil {
		return nil, err
	}

	attachments := make([]*gordon.Attachment, 0)
	if req.Target == "" {
		all := s.configSvc.GetAllAttachments(ctx)
		for target, images := range all {
			for _, image := range images {
				attachments = append(attachments, &gordon.Attachment{Name: target, Image: image})
			}
		}
	} else {
		images, err := s.configSvc.GetAttachmentsFor(ctx, req.Target)
		if err != nil {
			if errors.Is(err, domain.ErrAttachmentNotFound) {
				return nil, status.Error(codes.NotFound, "no attachments found for target")
			}
			return nil, status.Error(codes.Internal, "failed to get attachments")
		}
		for _, image := range images {
			attachments = append(attachments, &gordon.Attachment{Name: req.Target, Image: image})
		}
	}

	return &gordon.AttachmentsResponse{Attachments: attachments}, nil
}

// AddAttachment adds an attachment to a target.
func (s *Server) AddAttachment(ctx context.Context, req *gordon.AddAttachmentRequest) (*gordon.Attachment, error) {
	if err := auth.CheckAccess(ctx, domain.AdminResourceConfig, domain.AdminActionWrite); err != nil {
		return nil, err
	}

	if req.Target == "" {
		return nil, status.Error(codes.InvalidArgument, "target is required")
	}

	if err := s.configSvc.AddAttachment(ctx, req.Target, req.Image); err != nil {
		switch {
		case errors.Is(err, domain.ErrAttachmentExists):
			return nil, status.Error(codes.AlreadyExists, "attachment already exists")
		case errors.Is(err, domain.ErrAttachmentImageEmpty), errors.Is(err, domain.ErrAttachmentTargetEmpty):
			return nil, status.Error(codes.InvalidArgument, err.Error())
		default:
			return nil, status.Error(codes.Internal, "failed to add attachment")
		}
	}

	return &gordon.Attachment{Name: req.Target, Image: req.Image}, nil
}

// RemoveAttachment removes an attachment from a target.
func (s *Server) RemoveAttachment(ctx context.Context, req *gordon.RemoveAttachmentRequest) (*gordon.RemoveAttachmentResponse, error) {
	if err := auth.CheckAccess(ctx, domain.AdminResourceConfig, domain.AdminActionWrite); err != nil {
		return nil, err
	}

	if req.Target == "" || req.Image == "" {
		return nil, status.Error(codes.InvalidArgument, "target and image are required")
	}

	if err := s.configSvc.RemoveAttachment(ctx, req.Target, req.Image); err != nil {
		switch {
		case errors.Is(err, domain.ErrAttachmentNotFound):
			return nil, status.Error(codes.NotFound, "attachment not found")
		case errors.Is(err, domain.ErrAttachmentImageEmpty):
			return nil, status.Error(codes.InvalidArgument, err.Error())
		default:
			return nil, status.Error(codes.Internal, "failed to remove attachment")
		}
	}

	return &gordon.RemoveAttachmentResponse{Success: true}, nil
}

// VerifyAuth validates authentication context.
func (s *Server) VerifyAuth(ctx context.Context, _ *gordon.VerifyAuthRequest) (*gordon.VerifyAuthResponse, error) {
	subject, err := auth.GetSubject(ctx)
	if err != nil {
		return nil, err
	}

	scopes, _ := ctx.Value(domain.ContextKeyScopes).([]string)
	claims := domain.GetTokenClaims(ctx)

	resp := &gordon.VerifyAuthResponse{Valid: true, Subject: subject, Scopes: scopes}
	if claims != nil && claims.ExpiresAt > 0 {
		resp.ExpiresAt = timestamppb.New(time.Unix(claims.ExpiresAt, 0))
	}

	return resp, nil
}

// AuthenticatePassword issues a token using username and password.
func (s *Server) AuthenticatePassword(ctx context.Context, req *gordon.AuthenticatePasswordRequest) (*gordon.AuthenticatePasswordResponse, error) {
	if s.authSvc == nil || !s.authSvc.IsEnabled() {
		return nil, status.Error(codes.FailedPrecondition, "authentication is disabled")
	}

	if s.authSvc.GetAuthType() != domain.AuthTypePassword {
		return nil, status.Error(codes.FailedPrecondition, "password authentication not configured")
	}

	if req.Username == "" || req.Password == "" {
		return nil, status.Error(codes.InvalidArgument, "username and password are required")
	}

	if !s.authSvc.ValidatePassword(ctx, req.Username, req.Password) {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}

	expiry := 7 * 24 * time.Hour
	scopes := []string{"push", "pull", "admin:*:*"}

	token, err := s.authSvc.GenerateToken(ctx, req.Username, scopes, expiry)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to generate token")
	}

	return &gordon.AuthenticatePasswordResponse{
		Token:     token,
		ExpiresIn: int32(expiry.Seconds()),
		IssuedAt:  time.Now().Format(time.RFC3339),
	}, nil
}

func toProtoRouteInfo(info domain.RouteInfo) *gordon.RouteInfo {
	attachments := make([]*gordon.Attachment, 0, len(info.Attachments))
	for _, attachment := range info.Attachments {
		attachments = append(attachments, &gordon.Attachment{
			Name:        attachment.Name,
			Image:       attachment.Image,
			ContainerId: attachment.ContainerID,
			Status:      attachment.Status,
			Network:     attachment.Network,
		})
	}

	return &gordon.RouteInfo{
		Domain:          info.Domain,
		Image:           info.Image,
		ContainerId:     info.ContainerID,
		ContainerStatus: info.ContainerStatus,
		Network:         info.Network,
		Attachments:     attachments,
	}
}

func clampLogLines(lines int) int {
	if lines <= 0 {
		return 50
	}
	if lines > maxLogLines {
		return maxLogLines
	}
	return lines
}

func streamLogLines(ctx context.Context, ch <-chan string, source string, send func(*gordon.LogEntry) error) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-ch:
			if !ok {
				return nil
			}
			entry := &gordon.LogEntry{Line: line, Source: source, Timestamp: timestamppb.Now()}
			if err := send(entry); err != nil {
				return err
			}
		}
	}
}

func safeInt32(value int64) int32 {
	if value < 0 {
		return 0
	}
	if value > 2147483647 {
		return 2147483647
	}
	return int32(value)
}

func (s *Server) validateRouteImage(ctx context.Context, image string) error {
	if s.registrySvc == nil || image == "" {
		return nil
	}

	imageName, imageRef := validation.ParseImageReference(image)
	if _, err := s.registrySvc.GetManifest(ctx, imageName, imageRef); err != nil {
		if errors.Is(err, domain.ErrManifestNotFound) {
			return status.Error(codes.InvalidArgument, "image not found in registry")
		}
		return status.Error(codes.Unavailable, "failed to verify image in registry")
	}

	return nil
}
