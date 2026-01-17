// Package admin implements the HTTP adapter for the admin API.
package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/bnema/zerowrap"

	"gordon/internal/adapters/dto"
	"gordon/internal/boundaries/in"
	"gordon/internal/boundaries/out"
	"gordon/internal/domain"
)

// maxAdminRequestSize is the maximum allowed size for admin API request bodies.
const maxAdminRequestSize = 1 << 20 // 1MB

// maxLogLines is the maximum allowed number of log lines that can be requested.
const maxLogLines = 10000

// Handler implements the HTTP handler for the admin API.
type Handler struct {
	configSvc    in.ConfigService
	authSvc      in.AuthService
	containerSvc in.ContainerService
	healthSvc    in.HealthService
	secretSvc    in.SecretService
	logSvc       in.LogService
	eventBus     out.EventPublisher
	log          zerowrap.Logger
}

// Type aliases for API responses using shared DTO types.
type routeInfoResponse = dto.RouteInfo
type attachmentResponse = dto.Attachment
type routeResponse = dto.Route

// toAttachmentResponse converts a domain.Attachment to a dto.Attachment.
func toAttachmentResponse(a domain.Attachment) dto.Attachment {
	return dto.Attachment{
		Name:        a.Name,
		Image:       a.Image,
		ContainerID: a.ContainerID,
		Status:      a.Status,
		Network:     a.Network,
	}
}

// toRouteInfoResponse converts a domain.RouteInfo to a dto.RouteInfo.
func toRouteInfoResponse(r domain.RouteInfo) dto.RouteInfo {
	attachments := make([]dto.Attachment, 0, len(r.Attachments))
	for _, a := range r.Attachments {
		attachments = append(attachments, toAttachmentResponse(a))
	}
	return dto.RouteInfo{
		Domain:          r.Domain,
		Image:           r.Image,
		ContainerID:     r.ContainerID,
		ContainerStatus: r.ContainerStatus,
		Network:         r.Network,
		Attachments:     attachments,
	}
}

// toRouteResponse converts a domain.Route to a dto.Route.
func toRouteResponse(r domain.Route) dto.Route {
	return dto.Route{
		Domain: r.Domain,
		Image:  r.Image,
		HTTPS:  r.HTTPS,
	}
}

// NewHandler creates a new admin HTTP handler.
func NewHandler(
	configSvc in.ConfigService,
	authSvc in.AuthService,
	containerSvc in.ContainerService,
	healthSvc in.HealthService,
	secretSvc in.SecretService,
	logSvc in.LogService,
	eventBus out.EventPublisher,
	log zerowrap.Logger,
) *Handler {
	return &Handler{
		configSvc:    configSvc,
		authSvc:      authSvc,
		containerSvc: containerSvc,
		healthSvc:    healthSvc,
		secretSvc:    secretSvc,
		logSvc:       logSvc,
		eventBus:     eventBus,
		log:          log,
	}
}

// RegisterRoutes registers the admin routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/admin/", h.handleAdminRoutes)
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handleAdminRoutes(w, r)
}

func (h *Handler) handleAdminRoutes(w http.ResponseWriter, r *http.Request) {
	ctx := zerowrap.CtxWithFields(r.Context(), map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "http",
		zerowrap.FieldHandler: "admin",
		zerowrap.FieldMethod:  r.Method,
		zerowrap.FieldPath:    r.URL.Path,
	})
	r = r.WithContext(ctx)

	path := strings.TrimPrefix(r.URL.Path, "/admin")

	// Route to appropriate handler
	switch {
	case path == "/routes" || strings.HasPrefix(path, "/routes/"):
		h.handleRoutes(w, r, path)
	case path == "/networks":
		h.handleNetworks(w, r)
	case path == "/secrets" || strings.HasPrefix(path, "/secrets/"):
		h.handleSecrets(w, r, path)
	case path == "/deploy" || strings.HasPrefix(path, "/deploy/"):
		h.handleDeploy(w, r, path)
	case path == "/logs" || strings.HasPrefix(path, "/logs/"):
		h.handleLogs(w, r, path)
	case path == "/status":
		h.handleStatus(w, r)
	case path == "/health":
		h.handleHealth(w, r)
	case path == "/reload":
		h.handleReload(w, r)
	case path == "/config":
		h.handleConfig(w, r)
	default:
		h.sendError(w, http.StatusNotFound, "route not found")
	}
}

// sendJSON sends a JSON response.
func (h *Handler) sendJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}

// sendError sends an error response.
func (h *Handler) sendError(w http.ResponseWriter, status int, message string) {
	h.sendJSON(w, status, dto.ErrorResponse{Error: message})
}

// handleRoutes handles /admin/routes endpoints.
func (h *Handler) handleRoutes(w http.ResponseWriter, r *http.Request, path string) {
	// Parse domain from path if present
	routeDomain := strings.TrimPrefix(path, "/routes/")
	if routeDomain == "/routes" {
		routeDomain = ""
	}

	switch r.Method {
	case http.MethodGet:
		h.handleRoutesGet(w, r, routeDomain)
	case http.MethodPost:
		h.handleRoutesPost(w, r)
	case http.MethodPut:
		h.handleRoutesPut(w, r, routeDomain)
	case http.MethodDelete:
		h.handleRoutesDelete(w, r, routeDomain)
	default:
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleRoutesGet(w http.ResponseWriter, r *http.Request, routeDomain string) {
	ctx := r.Context()

	// Check read permission
	if !HasAccess(ctx, domain.AdminResourceRoutes, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for routes:read")
		return
	}

	if routeDomain == "" {
		if r.URL.Query().Get("detailed") == "true" {
			routes := h.containerSvc.ListRoutesWithDetails(ctx)
			response := make([]routeInfoResponse, 0, len(routes))
			for _, route := range routes {
				response = append(response, toRouteInfoResponse(route))
			}
			h.sendJSON(w, http.StatusOK, dto.RoutesDetailResponse{Routes: response})
			return
		}

		routes := h.configSvc.GetRoutes(ctx)
		response := make([]routeResponse, 0, len(routes))
		for _, route := range routes {
			response = append(response, toRouteResponse(route))
		}
		h.sendJSON(w, http.StatusOK, dto.RoutesResponse{Routes: response})
		return
	}

	if strings.HasSuffix(routeDomain, "/attachments") {
		parentDomain := strings.TrimSuffix(routeDomain, "/attachments")
		if parentDomain == "" {
			h.sendError(w, http.StatusBadRequest, "domain required in path")
			return
		}
		attachments := h.containerSvc.ListAttachments(ctx, parentDomain)
		response := make([]attachmentResponse, 0, len(attachments))
		for _, attachment := range attachments {
			response = append(response, toAttachmentResponse(attachment))
		}
		h.sendJSON(w, http.StatusOK, dto.AttachmentsResponse{Attachments: response})
		return
	}

	route, err := h.configSvc.GetRoute(ctx, routeDomain)
	if err != nil {
		h.sendError(w, http.StatusNotFound, "route not found")
		return
	}
	h.sendJSON(w, http.StatusOK, toRouteResponse(*route))
}

func (h *Handler) handleRoutesPost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Check write permission
	if !HasAccess(ctx, domain.AdminResourceRoutes, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for routes:write")
		return
	}

	// Limit request body size
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminRequestSize)

	var route domain.Route
	if err := json.NewDecoder(r.Body).Decode(&route); err != nil {
		log.Warn().Err(err).Msg("invalid route JSON")
		h.sendError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if err := h.configSvc.AddRoute(ctx, route); err != nil {
		log.Error().Err(err).Str("domain", route.Domain).Msg("failed to add route")
		switch {
		case errors.Is(err, domain.ErrRouteDomainEmpty), errors.Is(err, domain.ErrRouteImageEmpty):
			h.sendError(w, http.StatusBadRequest, err.Error())
		default:
			h.sendError(w, http.StatusInternalServerError, "failed to add route")
		}
		return
	}

	log.Info().Str("domain", route.Domain).Str("image", route.Image).Msg("route added")
	h.sendJSON(w, http.StatusCreated, toRouteResponse(route))
}

func (h *Handler) handleRoutesPut(w http.ResponseWriter, r *http.Request, routeDomain string) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Check write permission
	if !HasAccess(ctx, domain.AdminResourceRoutes, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for routes:write")
		return
	}

	if routeDomain == "" {
		h.sendError(w, http.StatusBadRequest, "domain required in path")
		return
	}

	// Limit request body size
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminRequestSize)

	var route domain.Route
	if err := json.NewDecoder(r.Body).Decode(&route); err != nil {
		log.Warn().Err(err).Msg("invalid route JSON")
		h.sendError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	route.Domain = routeDomain

	if err := h.configSvc.UpdateRoute(ctx, route); err != nil {
		log.Error().Err(err).Str("domain", routeDomain).Msg("failed to update route")
		switch {
		case errors.Is(err, domain.ErrRouteNotFound):
			h.sendError(w, http.StatusNotFound, "route not found")
		case errors.Is(err, domain.ErrRouteDomainEmpty), errors.Is(err, domain.ErrRouteImageEmpty):
			h.sendError(w, http.StatusBadRequest, err.Error())
		default:
			h.sendError(w, http.StatusInternalServerError, "failed to update route")
		}
		return
	}

	log.Info().Str("domain", route.Domain).Str("image", route.Image).Msg("route updated")
	h.sendJSON(w, http.StatusOK, toRouteResponse(route))
}

func (h *Handler) handleRoutesDelete(w http.ResponseWriter, r *http.Request, routeDomain string) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Check write permission
	if !HasAccess(ctx, domain.AdminResourceRoutes, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for routes:write")
		return
	}

	if routeDomain == "" {
		h.sendError(w, http.StatusBadRequest, "domain required in path")
		return
	}

	if err := h.configSvc.RemoveRoute(ctx, routeDomain); err != nil {
		log.Error().Err(err).Str("domain", routeDomain).Msg("failed to remove route")
		h.sendError(w, http.StatusInternalServerError, "failed to remove route")
		return
	}

	log.Info().Str("domain", routeDomain).Msg("route removed")
	h.sendJSON(w, http.StatusOK, dto.RouteDeleteResponse{Status: "removed"})
}

func (h *Handler) handleNetworks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()

	if !HasAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for status:read")
		return
	}

	networks, err := h.containerSvc.ListNetworks(ctx)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "failed to list networks")
		return
	}

	response := make([]dto.Network, 0, len(networks))
	for _, network := range networks {
		if network == nil {
			continue
		}
		var labelsCopy map[string]string
		if network.Labels != nil {
			labelsCopy = make(map[string]string, len(network.Labels))
			for key, value := range network.Labels {
				labelsCopy[key] = value
			}
		}
		response = append(response, dto.Network{
			ID:         network.ID,
			Name:       network.Name,
			Driver:     network.Driver,
			Containers: append([]string{}, network.Containers...),
			Labels:     labelsCopy,
		})
	}

	h.sendJSON(w, http.StatusOK, dto.NetworksResponse{Networks: response})
}

// handleSecrets handles /admin/secrets endpoints.
func (h *Handler) handleSecrets(w http.ResponseWriter, r *http.Request, path string) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Parse path: /secrets/{domain} or /secrets/{domain}/{key}
	parts := strings.Split(strings.TrimPrefix(path, "/secrets/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		h.sendError(w, http.StatusBadRequest, "domain required")
		return
	}

	secretDomain := parts[0]
	secretKey := ""
	if len(parts) > 1 {
		secretKey = parts[1]
	}

	switch r.Method {
	case http.MethodGet:
		// Check read permission
		if !HasAccess(ctx, domain.AdminResourceSecrets, domain.AdminActionRead) {
			h.sendError(w, http.StatusForbidden, "insufficient permissions for secrets:read")
			return
		}
		// List secrets for domain (names only, not values)
		keys, err := h.secretSvc.ListKeys(ctx, secretDomain)
		if err != nil {
			log.Error().Err(err).Str("domain", secretDomain).Msg("failed to list secrets")
			h.sendError(w, http.StatusBadRequest, "invalid domain")
			return
		}
		h.sendJSON(w, http.StatusOK, dto.SecretsListResponse{Domain: secretDomain, Keys: keys})

	case http.MethodPost:
		// Check write permission
		if !HasAccess(ctx, domain.AdminResourceSecrets, domain.AdminActionWrite) {
			h.sendError(w, http.StatusForbidden, "insufficient permissions for secrets:write")
			return
		}

		// Limit request body size
		r.Body = http.MaxBytesReader(w, r.Body, maxAdminRequestSize)

		// Set secret(s)
		var data map[string]string
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			log.Warn().Err(err).Msg("invalid secrets JSON")
			h.sendError(w, http.StatusBadRequest, "invalid JSON")
			return
		}

		if err := h.secretSvc.Set(ctx, secretDomain, data); err != nil {
			log.Error().Err(err).Str("domain", secretDomain).Msg("failed to set secrets")
			h.sendError(w, http.StatusBadRequest, "invalid domain")
			return
		}

		log.Info().Str("domain", secretDomain).Int("count", len(data)).Msg("secrets set")
		h.sendJSON(w, http.StatusOK, dto.SecretsStatusResponse{Status: "updated"})

	case http.MethodDelete:
		// Check write permission
		if !HasAccess(ctx, domain.AdminResourceSecrets, domain.AdminActionWrite) {
			h.sendError(w, http.StatusForbidden, "insufficient permissions for secrets:write")
			return
		}
		if secretKey == "" {
			h.sendError(w, http.StatusBadRequest, "key required in path")
			return
		}

		if err := h.secretSvc.Delete(ctx, secretDomain, secretKey); err != nil {
			log.Error().Err(err).Str("domain", secretDomain).Str("key", secretKey).Msg("failed to delete secret")
			h.sendError(w, http.StatusBadRequest, "invalid domain")
			return
		}

		log.Info().Str("domain", secretDomain).Str("key", secretKey).Msg("secret deleted")
		h.sendJSON(w, http.StatusOK, dto.SecretsStatusResponse{Status: "deleted"})

	default:
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleHealth handles /admin/health endpoint.
// Returns detailed health status for all routes including HTTP probe results.
func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()

	// Check read permission
	if !HasAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for status:read")
		return
	}

	health := h.healthSvc.CheckAllRoutes(ctx)

	response := make(map[string]dto.HealthStatus, len(health))
	for domain, healthStatus := range health {
		if healthStatus == nil {
			continue
		}
		response[domain] = dto.HealthStatus{
			ContainerStatus: healthStatus.ContainerStatus,
			HTTPStatus:      healthStatus.HTTPStatus,
			ResponseTimeMs:  healthStatus.ResponseTimeMs,
			Healthy:         healthStatus.Healthy,
			Error:           healthStatus.Error,
		}
	}

	h.sendJSON(w, http.StatusOK, dto.HealthResponse{Health: response})
}

// handleStatus handles /admin/status endpoint.
func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()

	// Check read permission
	if !HasAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for status:read")
		return
	}

	routes := h.configSvc.GetRoutes(ctx)

	// Get container statuses
	statuses := make(map[string]string)
	for _, route := range routes {
		status := "unknown"
		container, ok := h.containerSvc.Get(ctx, route.Domain)
		if ok && container != nil {
			status = container.Status
		}
		statuses[route.Domain] = status
	}

	status := dto.StatusResponse{
		Routes:            len(routes),
		RegistryDomain:    h.configSvc.GetRegistryDomain(),
		RegistryPort:      h.configSvc.GetRegistryPort(),
		ServerPort:        h.configSvc.GetServerPort(),
		AutoRoute:         h.configSvc.IsAutoRouteEnabled(),
		NetworkIsolation:  h.configSvc.IsNetworkIsolationEnabled(),
		ContainerStatuses: statuses,
	}

	h.sendJSON(w, http.StatusOK, status)
}

// handleReload handles /admin/reload endpoint.
// This reloads configuration from file into memory and triggers container sync.
func (h *Handler) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Check write permission (reload modifies state)
	if !HasAccess(ctx, domain.AdminResourceConfig, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for config:write")
		return
	}

	// Reload configuration from file into memory
	if err := h.configSvc.Load(ctx); err != nil {
		log.Error().Err(err).Msg("failed to reload config")
		h.sendError(w, http.StatusInternalServerError, "failed to reload config")
		return
	}

	// Publish manual reload event to sync containers
	// ManualReloadHandler starts missing containers without restarting running ones
	if h.eventBus != nil {
		if err := h.eventBus.Publish(domain.EventManualReload, nil); err != nil {
			log.Warn().Err(err).Msg("failed to publish manual reload event")
		}
	}

	log.Info().Msg("config reloaded via admin API")
	h.sendJSON(w, http.StatusOK, dto.ReloadResponse{Status: "reloaded"})
}

// handleConfig handles /admin/config endpoint.
func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()

	// Check read permission
	if !HasAccess(ctx, domain.AdminResourceConfig, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for config:read")
		return
	}

	routes := h.configSvc.GetRoutes(ctx)
	routeResponses := make([]dto.Route, 0, len(routes))
	for _, route := range routes {
		routeResponses = append(routeResponses, toRouteResponse(route))
	}

	externalRoutes := h.configSvc.GetExternalRoutes()
	externalResponses := make([]dto.ExternalRoute, 0, len(externalRoutes))
	for domain, target := range externalRoutes {
		externalResponses = append(externalResponses, dto.ExternalRoute{
			Domain: domain,
			Target: target,
		})
	}

	config := dto.ConfigResponse{
		Server: dto.ServerConfig{
			Port:           h.configSvc.GetServerPort(),
			RegistryPort:   h.configSvc.GetRegistryPort(),
			RegistryDomain: h.configSvc.GetRegistryDomain(),
			DataDir:        h.configSvc.GetDataDir(),
		},
		AutoRoute: dto.AutoRouteConfig{
			Enabled: h.configSvc.IsAutoRouteEnabled(),
		},
		NetworkIsolation: dto.NetworkIsolationConfig{
			Enabled: h.configSvc.IsNetworkIsolationEnabled(),
			Prefix:  h.configSvc.GetNetworkPrefix(),
		},
		Routes:         routeResponses,
		ExternalRoutes: externalResponses,
	}

	h.sendJSON(w, http.StatusOK, config)
}

// handleDeploy handles /admin/deploy/:domain endpoint.
// POST triggers a deployment for the specified domain.
func (h *Handler) handleDeploy(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodPost {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Check write permission
	if !HasAccess(ctx, domain.AdminResourceConfig, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for config:write")
		return
	}

	// Parse domain from path
	deployDomain := strings.TrimPrefix(path, "/deploy/")
	if deployDomain == "" || deployDomain == "/deploy" {
		h.sendError(w, http.StatusBadRequest, "domain required in path")
		return
	}

	// Get the route for this domain
	route, err := h.configSvc.GetRoute(ctx, deployDomain)
	if err != nil {
		h.sendError(w, http.StatusNotFound, "route not found")
		return
	}

	// Deploy the container
	container, err := h.containerSvc.Deploy(ctx, *route)
	if err != nil {
		log.Error().Err(err).Str("domain", deployDomain).Msg("failed to deploy container")
		h.sendError(w, http.StatusInternalServerError, "failed to deploy container")
		return
	}

	log.Info().Str("domain", deployDomain).Str("container_id", container.ID).Msg("container deployed via admin API")
	h.sendJSON(w, http.StatusOK, dto.DeployResponse{
		Status:      "deployed",
		ContainerID: container.ID,
		Domain:      deployDomain,
	})
}

// handleLogs handles /admin/logs endpoints.
// GET /admin/logs - Gordon process logs
// GET /admin/logs/:domain - Container logs for a specific domain
func (h *Handler) handleLogs(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Check read permission
	if !HasAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for status:read")
		return
	}

	// Check if LogService is available
	if h.logSvc == nil {
		h.sendError(w, http.StatusServiceUnavailable, "log service not available")
		return
	}

	// Parse query parameters
	lines := 50 // default
	if linesStr := r.URL.Query().Get("lines"); linesStr != "" {
		if n, err := strconv.Atoi(linesStr); err == nil && n > 0 {
			lines = n
		}
	}
	if lines > maxLogLines {
		lines = maxLogLines
	}
	follow := r.URL.Query().Get("follow") == "true"

	// Parse domain from path
	logDomain := strings.TrimPrefix(path, "/logs/")
	if logDomain == "/logs" {
		logDomain = ""
	}

	if logDomain == "" {
		// Gordon process logs
		h.handleProcessLogs(w, r, lines, follow)
	} else {
		// Container logs
		h.handleContainerLogs(w, r, logDomain, lines, follow)
	}

	// Prevent unused variable warning when follow is implemented
	_ = log
}

// handleProcessLogs handles Gordon process logs.
func (h *Handler) handleProcessLogs(w http.ResponseWriter, r *http.Request, lines int, follow bool) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	if follow {
		// SSE streaming
		h.streamProcessLogs(w, r, lines)
		return
	}

	// Return last N lines as JSON
	logLines, err := h.logSvc.GetProcessLogs(ctx, lines)
	if err != nil {
		log.Warn().Err(err).Msg("failed to get process logs")
		h.sendError(w, http.StatusInternalServerError, "failed to get logs")
		return
	}

	h.sendJSON(w, http.StatusOK, dto.ProcessLogsResponse{Lines: logLines})
}

// handleContainerLogs handles container logs for a specific domain.
func (h *Handler) handleContainerLogs(w http.ResponseWriter, r *http.Request, logDomain string, lines int, follow bool) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	if follow {
		// SSE streaming
		h.streamContainerLogs(w, r, logDomain, lines)
		return
	}

	// Return last N lines as JSON
	logLines, err := h.logSvc.GetContainerLogs(ctx, logDomain, lines)
	if err != nil {
		log.Warn().Err(err).Str("domain", logDomain).Msg("failed to get container logs")
		h.sendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get logs: %v", err))
		return
	}

	h.sendJSON(w, http.StatusOK, dto.ContainerLogsResponse{
		Domain: logDomain,
		Lines:  logLines,
	})
}

// streamProcessLogs streams Gordon process logs via SSE.
func (h *Handler) streamProcessLogs(w http.ResponseWriter, r *http.Request, lines int) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Check for flusher support before setting up SSE
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.sendError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Set up SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch, err := h.logSvc.FollowProcessLogs(ctx, lines)
	if err != nil {
		log.Warn().Err(err).Msg("failed to follow process logs")
		_, _ = fmt.Fprintf(w, "event: error\ndata: failed to stream logs\n\n")
		flusher.Flush()
		return
	}

	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}

// streamContainerLogs streams container logs via SSE.
func (h *Handler) streamContainerLogs(w http.ResponseWriter, r *http.Request, logDomain string, lines int) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Check for flusher support before setting up SSE
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.sendError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Set up SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch, err := h.logSvc.FollowContainerLogs(ctx, logDomain, lines)
	if err != nil {
		log.Warn().Err(err).Str("domain", logDomain).Msg("failed to follow container logs")
		_, _ = fmt.Fprintf(w, "event: error\ndata: failed to stream container logs\n\n")
		flusher.Flush()
		return
	}

	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}
